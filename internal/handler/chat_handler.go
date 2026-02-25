// Package handler 包含了处理 HTTP 请求的控制器逻辑。
package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"pai-smart-go/internal/service"
	"pai-smart-go/pkg/log"
	"pai-smart-go/pkg/token"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

var (
	// upgrader 用于把 HTTP 请求升级为 WebSocket 连接。
	// 这里允许所有来源，适合内网/开发环境；生产环境建议按域名做白名单校验。
	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true // 允许所有来源
		},
	}
)

// ChatHandler 负责“WebSocket 协议层”的聊天入口。
// 职责边界：
// - handler 负责：鉴权、连接管理、停止指令协议、错误回包格式
// - service 负责：RAG 检索、上下文拼接、LLM 流式转发、会话落库
type ChatHandler struct {
	chatService   service.ChatService
	userService   service.UserService
	jwtManager    *token.JWTManager
	stopToken     string // 当前进程内的停止口令（简化实现）
	stopTokenLock sync.Mutex
	// stopFlags 存储“某个连接是否被请求停止”。
	// key 使用连接指针字符串，value=true 表示应停止继续向前端下发流式内容。
	stopFlags sync.Map // key: session pointer string, value: bool
}

// NewChatHandler 创建一个新的 ChatHandler。
func NewChatHandler(chatService service.ChatService, userService service.UserService, jwtManager *token.JWTManager) *ChatHandler {
	return &ChatHandler{
		chatService: chatService,
		userService: userService,
		jwtManager:  jwtManager,
	}
}

// GetWebsocketStopToken 下发“停止流式输出”控制口令。
// 前端后续可携带该口令发送 stop 指令，服务端据此设置连接级停止标志。
func (h *ChatHandler) GetWebsocketStopToken(c *gin.Context) {
	h.stopTokenLock.Lock()
	defer h.stopTokenLock.Unlock()
	// 在多副本部署中，口令应放到共享存储（如 Redis）保证跨实例可见。
	// 当前实现使用进程内单口令，适合单实例场景。
	h.stopToken = "WSS_STOP_CMD_" + token.GenerateRandomString(16)
	c.JSON(http.StatusOK, gin.H{"code": http.StatusOK, "message": "success", "data": gin.H{"cmdToken": h.stopToken}})
}

// Handle 处理 WebSocket 会话主循环。
// 请求路径携带临时 token（/chat/:token），验证后进入持续收消息 -> 调 service -> 回写前端。
func (h *ChatHandler) Handle(c *gin.Context) {
	// 1) 校验 URL 中的一次性/临时 token
	tokenString := c.Param("token")
	claims, err := h.jwtManager.VerifyToken(tokenString)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"code": http.StatusUnauthorized, "message": "无效的 token", "data": nil})
		return
	}

	// 2) 通过 claims 反查完整用户模型，后续检索权限依赖该用户信息
	user, err := h.userService.GetProfile(claims.Username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": http.StatusInternalServerError, "message": "无法获取用户信息", "data": nil})
		return
	}

	// 3) 升级协议到 WebSocket
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Error("WebSocket 升级失败", err)
		return
	}
	defer conn.Close()

	log.Infof("WebSocket 连接已建立，用户: %s", claims.Username)

	// 4) 会话循环：持续接收消息，直到连接断开或处理出错
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Warnf("从 WebSocket 读取消息失败: %v", err)
			break
		}
		log.Infof("收到 WebSocket 消息: %s", string(message))

		// 4.1) JSON 停止指令：
		// {"type":"stop","_internal_cmd_token":"..."}
		// 命中后只设置 shouldStop 标志，不直接断开连接（允许后续继续提问）。
		var ctrl map[string]interface{}
		if len(message) > 0 && message[0] == '{' {
			if err := json.Unmarshal(message, &ctrl); err == nil {
				if t, ok := ctrl["type"].(string); ok && t == "stop" {
					if tok, ok := ctrl["_internal_cmd_token"].(string); ok {
						h.stopTokenLock.Lock()
						valid := (tok == h.stopToken)
						h.stopTokenLock.Unlock()
						if valid {
							// 设置连接级停止标志，供 shouldStop 回调读取
							key := sessionKey(conn)
							h.stopFlags.Store(key, true)
							// 回发停止确认事件，前端可据此更新 UI 状态
							resp := map[string]interface{}{
								"type":      "stop",
								"message":   "响应已停止",
								"timestamp": time.Now().UnixMilli(),
								"date":      time.Now().Format("2006-01-02T15:04:05"),
							}
							b, _ := json.Marshal(resp)
							_ = conn.WriteMessage(websocket.TextMessage, b)
							continue
						}
					}
				}
			}
		}
		// 4.2) 兼容旧协议：消息体等于 stopToken 时也触发停止
		h.stopTokenLock.Lock()
		stopTokenValue := h.stopToken
		h.stopTokenLock.Unlock()
		if string(message) == stopTokenValue {
			log.Info("收到停止指令，正在中断流式响应...")
			// 同样置位停止标志
			key := sessionKey(conn)
			h.stopFlags.Store(key, true)
			continue
		}

		// 4.3) 正常问答消息：进入 ChatService 完整链路（检索 -> LLM -> 流式回传）
		// shouldStop 会被 service 在流式发送过程中轮询，用于中断继续下发。
		shouldStop := func() bool {
			key := sessionKey(conn)
			v, ok := h.stopFlags.Load(key)
			return ok && v.(bool)
		}
		// 每轮新请求前清理旧停止标志，避免把上一次 stop 误用于本次问答
		h.stopFlags.Delete(sessionKey(conn))
		err = h.chatService.StreamResponse(c.Request.Context(), string(message), user, conn, shouldStop)
		if err != nil {
			log.Errorf("处理流式响应失败: %v", err)
			// 统一错误包，前端按 error 字段展示提示
			errResp := map[string]string{"error": "AI服务暂时不可用，请稍后重试"}
			b, _ := json.Marshal(errResp)
			conn.WriteMessage(websocket.TextMessage, b)
			// 与 Java 对齐：错误场景也发 completion，方便前端结束“生成中”状态
			resp := map[string]interface{}{
				"type":      "completion",
				"status":    "finished",
				"message":   "响应已完成",
				"timestamp": time.Now().UnixMilli(),
				"date":      time.Now().Format("2006-01-02T15:04:05"),
			}
			cb, _ := json.Marshal(resp)
			_ = conn.WriteMessage(websocket.TextMessage, cb)
			break
		}
	}
}

func sessionKey(conn *websocket.Conn) string {
	// 用连接对象地址作为会话键，避免额外会话 ID 管理。
	return fmt.Sprintf("%p", conn)
}
