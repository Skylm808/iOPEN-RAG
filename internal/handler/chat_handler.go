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

// ChatHandler 是 WebSocket 聊天功能的 HTTP/WS 入口层。
//
// ── 职责边界 ─────────────────────────────────────────────────────────────────
//
//   - handler 负责：鉴权、连接升级与管理、停止指令的协议解析、错误回包格式
//   - service 负责：RAG 检索、上下文拼接、LLM 流式转发、会话落库
//
// ── 停止流式输出的机制 ────────────────────────────────────────────────────────
//
// 整个停止机制由三个字段协作完成，见下方字段注释。
// 完整流程见 GetWebsocketStopToken 和 Handle。
type ChatHandler struct {
	chatService service.ChatService
	userService service.UserService
	jwtManager  *token.JWTManager

	// stopToken 是进程内全局唯一的停止口令（简化实现，适合单实例部署）。
	// 前端通过 GET /chat/websocket-token 申请该口令，之后通过 WebSocket 消息携带它触发停止。
	// ⚠️ 已知缺陷：多用户场景下，后申请者会覆盖前申请者的口令，导致先申请者无法停止。
	//    生产改进方案：以 sessionID 为 key 将口令存入 Redis，实现 per-connection 独立口令。
	stopToken string

	// stopTokenLock 互斥锁，保护 stopToken 字符串的并发读写安全。
	// 注意：这里使用 sync.Mutex 而非 sync.Map，是因为被保护的是单个 string 变量而非 Map 结构。
	stopTokenLock sync.Mutex

	// stopFlags 存储"某条 WebSocket 连接是否被请求停止"的标志。
	// key：连接对象的内存地址字符串（由 sessionKey(conn) 生成，唯一标识一条连接）
	// value：bool，true 表示应立即停止继续向前端下发流式内容
	//
	// 使用 sync.Map 的原因：每条连接对应一个 key，多个 goroutine 并发读写不同 key，
	// sync.Map 内部自动处理并发，比手动加 Mutex + 普通 Map 更高效。
	stopFlags sync.Map

	// connWriteLocks 存储每条 WebSocket 连接的写锁。
	// key：连接对象的内存地址字符串（由 sessionKey(conn) 生成）
	// value：*sync.Mutex
	//
	// gorilla/websocket 不支持并发写（Connections support one concurrent reader and one concurrent writer）。
	// 读消息的 goroutine（处理停止指令）和流式输出的 goroutine 会同时调用 conn.WriteMessage，
	// 必须用写锁串行化所有写操作，否则会导致帧损坏或 panic。
	connWriteLocks sync.Map
}

// NewChatHandler 创建一个新的 ChatHandler。
func NewChatHandler(chatService service.ChatService, userService service.UserService, jwtManager *token.JWTManager) *ChatHandler {
	return &ChatHandler{
		chatService: chatService,
		userService: userService,
		jwtManager:  jwtManager,
	}
}

// GetWebsocketStopToken 下发"停止流式输出"控制口令。
//
// ── 调用时机 ─────────────────────────────────────────────────────────────────
//
// 前端在建立 WebSocket 连接之前调用此接口，获取口令并存入内存备用。
// 当用户点击"停止生成"时，前端通过已建立的 WebSocket 连接将该口令发回服务端。
//
// ── 口令设计 ─────────────────────────────────────────────────────────────────
//
// 口令格式：WSS_STOP_CMD_{16位随机hex}，每次调用此接口都会生成新口令覆盖旧值。
// 口令的作用是"鉴权"：证明前端有权发出停止指令，防止其他连接被误停。
// 确定"停止哪条连接"由 stopFlags（连接维度）负责，与口令无关。
func (h *ChatHandler) GetWebsocketStopToken(c *gin.Context) {
	h.stopTokenLock.Lock()
	defer h.stopTokenLock.Unlock()
	// 在多副本部署中，口令应放到共享存储（如 Redis）保证跨实例可见。
	// 当前实现使用进程内单口令，适合单实例场景。
	h.stopToken = "WSS_STOP_CMD_" + token.GenerateRandomString(16)
	c.JSON(http.StatusOK, gin.H{"code": http.StatusOK, "message": "success", "data": gin.H{"cmdToken": h.stopToken}})
}

// Handle 处理 WebSocket 会话主循环。
//
// ── 建立连接的流程 ────────────────────────────────────────────────────────────
//
//  1. 校验 URL 中的 token（当前实现直接复用登录 Access Token 放入 URL）
//     ⚠️ 已知设计债：浏览器原生 WebSocket API 不支持自定义 Header，
//     因此凭证只能放 URL，但长期 JWT 出现在 URL 中有日志泄漏风险。
//     改进方案：专设接口颁发短期（60s）WS Token，即用即废。
//  2. 通过 token 中的 username 反查完整用户信息（用于后续权限检索）
//  3. 将 HTTP 连接升级为 WebSocket 协议
//
// ── 会话循环逻辑 ─────────────────────────────────────────────────────────────
//
// 连接建立后进入 for 循环持续接收消息，每条消息有三种处理路径：
//
//	路径 A（JSON 停止指令）：
//	  {"type":"stop","_internal_cmd_token":"..."}
//	  验证口令后设置 stopFlags，不断开连接（允许用户继续提问）
//
//	路径 B（旧协议兼容，直接发口令字符串）：
//	  "WSS_STOP_CMD_xxxx..."
//	  同路径 A 效果，兼容旧版前端
//
//	路径 C（正常问答消息）：
//	  进入完整 RAG 链路：检索 → 拼 prompt → LLM 流式输出 → 落库
//	  shouldStop 闭包在流式发送途中被 service 轮询，用于中断推送
//
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

	// 初始化该连接的写锁，所有对 conn.WriteMessage 的调用都必须持有此锁。
	// gorilla/websocket 不支持并发写，读协程（处理停止指令）和流式输出协程会同时写，必须串行化。
	connKey := sessionKey(conn)
	writeMu := &sync.Mutex{}
	h.connWriteLocks.Store(connKey, writeMu)
	defer func() {
		// 连接关闭时清理写锁和停止标志，防止 sync.Map 无限增长导致内存泄漏
		h.connWriteLocks.Delete(connKey)
		h.stopFlags.Delete(connKey)
	}()

	log.Infof("WebSocket 连接已建立，用户: %s", claims.Username)

	// 4) 会话循环：持续接收消息，直到连接断开或处理出错
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Warnf("从 WebSocket 读取消息失败: %v", err)
			break
		}
		log.Infof("收到 WebSocket 消息: %s", string(message))

		// 路径 A：JSON 停止指令
		// 格式：{"type":"stop","_internal_cmd_token":"..."}
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
							writeMu.Lock()
							_ = conn.WriteMessage(websocket.TextMessage, b)
							writeMu.Unlock()
							continue
						}
					}
				}
			}
		}
		// 路径 B：旧协议兼容，消息体等于 stopToken 时触发停止
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

		// 路径 C：正常问答消息，进入完整 RAG 链路（检索 -> LLM -> 流式回传）
		//
		// shouldStop 是一个闭包，在 StreamResponse 流式发送每个 token 前被轮询。
		// 若返回 true，service 层会静默丢弃后续 chunk，不再向前端推送，但不断开连接。
		//
		// 每轮新请求前必须清理旧的停止标志，否则上一次停止的 true 会污染本次请求：
		// 例如用户点了停止后再发新问题，如果不清理，新问题会在第一个 token 就停掉。
		shouldStop := func() bool {
			key := sessionKey(conn)
			v, ok := h.stopFlags.Load(key)
			return ok && v.(bool)
		}
		// 清理旧停止标志，保证本轮请求从"未停止"状态开始
		h.stopFlags.Delete(sessionKey(conn))
		err = h.chatService.StreamResponse(c.Request.Context(), string(message), user, conn, shouldStop, writeMu)
		if err != nil {
			log.Errorf("处理流式响应失败: %v", err)
			// 统一错误包，前端按 error 字段展示提示
			errResp := map[string]string{"error": "AI服务暂时不可用，请稍后重试"}
			b, _ := json.Marshal(errResp)
			writeMu.Lock()
			conn.WriteMessage(websocket.TextMessage, b)
			// 与 Java 对齐：错误场景也发 completion，方便前端结束"生成中"状态
			resp := map[string]interface{}{
				"type":      "completion",
				"status":    "finished",
				"message":   "响应已完成",
				"timestamp": time.Now().UnixMilli(),
				"date":      time.Now().Format("2006-01-02T15:04:05"),
			}
			cb, _ := json.Marshal(resp)
			_ = conn.WriteMessage(websocket.TextMessage, cb)
			writeMu.Unlock()
			break
		}
	}
}

func sessionKey(conn *websocket.Conn) string {
	// 用连接对象地址作为会话键，避免额外会话 ID 管理。
	return fmt.Sprintf("%p", conn)
}
