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
	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true // 允许所有来源
		},
	}
)

// ChatHandler 负责处理 WebSocket 聊天连接。
type ChatHandler struct {
	chatService   service.ChatService
	userService   service.UserService
	jwtManager    *token.JWTManager
	stopToken     string
	stopTokenLock sync.Mutex
	// 每连接停止标志
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

// GetWebsocketStopToken 返回一个可用于停止流的令牌。
func (h *ChatHandler) GetWebsocketStopToken(c *gin.Context) {
	h.stopTokenLock.Lock()
	defer h.stopTokenLock.Unlock()
	// 在真实的多服务器设置中，这应该在 Redis 中生成和存储
	// 为简单起见，我们在这里使用一个单一的、轮换的令牌。
	h.stopToken = "WSS_STOP_CMD_" + token.GenerateRandomString(16)
	c.JSON(http.StatusOK, gin.H{"code": http.StatusOK, "message": "success", "data": gin.H{"cmdToken": h.stopToken}})
}

// Handle 处理一个传入的 WebSocket 连接。
func (h *ChatHandler) Handle(c *gin.Context) {
	tokenString := c.Param("token")
	claims, err := h.jwtManager.VerifyToken(tokenString)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"code": http.StatusUnauthorized, "message": "无效的 token", "data": nil})
		return
	}

	// 获取用户模型
	user, err := h.userService.GetProfile(claims.Username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": http.StatusInternalServerError, "message": "无法获取用户信息", "data": nil})
		return
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Error("WebSocket 升级失败", err)
		return
	}
	defer conn.Close()

	log.Infof("WebSocket 连接已建立，用户: %s", claims.Username)

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Warnf("从 WebSocket 读取消息失败: %v", err)
			break
		}
		log.Infof("收到 WebSocket 消息: %s", string(message))

		// 1) JSON 停止指令: {"type":"stop","_internal_cmd_token":"..."}
		var ctrl map[string]interface{}
		if len(message) > 0 && message[0] == '{' {
			if err := json.Unmarshal(message, &ctrl); err == nil {
				if t, ok := ctrl["type"].(string); ok && t == "stop" {
					if tok, ok := ctrl["_internal_cmd_token"].(string); ok {
						h.stopTokenLock.Lock()
						valid := (tok == h.stopToken)
						h.stopTokenLock.Unlock()
						if valid {
							// 设置停止标志
							key := sessionKey(conn)
							h.stopFlags.Store(key, true)
							// 回发停止确认
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
		// 2) 旧停止令牌：整条消息等于 stopToken（保留兼容）
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

		// 调用 ChatService 处理完整的 RAG 和流式逻辑
		shouldStop := func() bool {
			key := sessionKey(conn)
			v, ok := h.stopFlags.Load(key)
			return ok && v.(bool)
		}
		// 清除旧标志
		h.stopFlags.Delete(sessionKey(conn))
		err = h.chatService.StreamResponse(c.Request.Context(), string(message), user, conn, shouldStop)
		if err != nil {
			log.Errorf("处理流式响应失败: %v", err)
			// 统一 JSON 错误
			errResp := map[string]string{"error": "AI服务暂时不可用，请稍后重试"}
			b, _ := json.Marshal(errResp)
			conn.WriteMessage(websocket.TextMessage, b)
			// 与 Java 对齐：错误时也发送 completion 通知
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
	return fmt.Sprintf("%p", conn)
}
