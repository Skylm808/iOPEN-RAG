// Package service 包含了应用的业务逻辑层。
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"pai-smart-go/internal/config"
	"pai-smart-go/internal/model"
	"pai-smart-go/internal/repository"
	"pai-smart-go/pkg/llm"
	"pai-smart-go/pkg/log"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// ChatService 定义了聊天操作的接口。
type ChatService interface {
	// writeMu 是该连接的写锁，由 handler 层创建并传入，确保所有对 conn 的写操作串行化。
	StreamResponse(ctx context.Context, query string, user *model.User, ws *websocket.Conn, shouldStop func() bool, writeMu *sync.Mutex) error
}

type chatService struct {
	searchService    SearchService
	llmClient        llm.Client
	conversationRepo repository.ConversationRepository
}

// NewChatService 创建一个新的 ChatService 实例。
func NewChatService(searchService SearchService, llmClient llm.Client, conversationRepo repository.ConversationRepository) ChatService {
	return &chatService{
		searchService:    searchService,
		llmClient:        llmClient,
		conversationRepo: conversationRepo,
	}
}

// StreamResponse 协调“检索增强问答（RAG）+ 流式输出”的主流程。
// 按阶段可理解为：
// 1) 检索阶段：调用 HybridSearch 取 topK 上下文
// 2) 组包阶段：构造 system/context + 历史 + 当前 user 问句
// 3) 生成阶段：调用 LLM 流式接口（上游 SSE），并桥接为前端 WebSocket chunk
// 4) 收尾阶段：发送 completion，并把完整问答写入 Redis 会话历史
func (s *chatService) StreamResponse(ctx context.Context, query string, user *model.User, ws *websocket.Conn, shouldStop func() bool, writeMu *sync.Mutex) error {
	// 1. 检索阶段：使用 SearchService 检索上下文（topK=10）
	// 返回的是结构化命中结果（文件、分块、文本、分数等），不是直接给 LLM 的 message 格式。
	results, err := s.searchService.HybridSearch(ctx, query, 10, user)
	if err != nil {
		return fmt.Errorf("failed to retrieve context: %w", err)
	}

	// 2. 组包阶段：把检索结果转成 LLM 可消费的 messages
	// - buildContextText: 将 topK 命中拼为 [序号](文件名) 片段文本
	// - buildSystemMessage: 注入规则 + 参考包裹（如 <<REF>>...<<END>>）
	// - loadHistory/composeMessages: 组装成 [system + history + user]
	contextText := s.buildContextText(results)
	systemMsg := s.buildSystemMessage(contextText)
	history, err := s.loadHistory(ctx, user.ID)
	if err != nil {
		log.Errorf("Failed to load conversation history: %v", err)
		history = []model.ChatMessage{}
	}
	messages := s.composeMessages(systemMsg, history, query)

	// 3. 提前落库用户消息（在 LLM 生成开始前）。
	// 目的：即使用户在 AI 回答过程中切走页面，切回来时也能从 API 拉到本轮问题。
	// 若落库失败只打日志，不影响主流程继续。
	if err := s.persistUserMessage(context.Background(), user.ID, user.Username, query); err != nil {
		log.Errorf("Failed to persist user message early: %v", err)
	}

	// 拦截 writer：同一份分片做两件事
	// - 写入 answerBuilder，便于流结束后持久化完整回答
	// - 包装为 {"chunk":"..."} 并通过 WebSocket 推给前端
	answerBuilder := &strings.Builder{}
	interceptor := &wsWriterInterceptor{conn: ws, writer: answerBuilder, shouldStop: shouldStop, writeMu: writeMu}

	// 4. 生成阶段：调用 LLM 客户端流式生成
	// 注意协议方向：
	// - 后端 -> LLM：HTTP JSON 请求（stream=true）
	// - LLM -> 后端：SSE 流式响应
	// - 后端 -> 前端：WebSocket JSON chunk（由 interceptor 桥接）
	gen := s.buildGenerationParams()
	var llmMsgs []llm.Message
	for _, m := range messages {
		llmMsgs = append(llmMsgs, llm.Message{Role: m.Role, Content: m.Content})
	}
	err = s.llmClient.StreamChatMessages(ctx, llmMsgs, gen, interceptor)
	if err != nil {
		return err
	}

	// 5. 收尾阶段：通知完成 + 落库 assistant 回答
	sendCompletion(ws, writeMu)
	fullAnswer := answerBuilder.String()
	if len(fullAnswer) > 0 {
		// 使用后台上下文，即使原始请求被取消也保存已生成的答案
		if err = s.persistAssistantMessage(context.Background(), user.ID, fullAnswer); err != nil {
			log.Errorf("Failed to persist assistant message: %v", err)
		}
	}

	return nil
}

// buildContextText 将检索结果拼接成“参考上下文文本”。
// 该文本会被放进 system message 的参考区块中，供 LLM 回答时引用。
func (s *chatService) buildContextText(searchResults []model.SearchResponseDTO) string {
	if len(searchResults) == 0 {
		return ""
	}
	// 单条片段最多保留 1000 字符，避免 system message 过长。
	// 这是一层“上下文长度保护”，不是 ES 分块尺寸本身。
	const maxSnippetLen = 1000
	var contextBuilder strings.Builder
	for i, r := range searchResults {
		snippet := r.TextContent
		if len(snippet) > maxSnippetLen {
			snippet = snippet[:maxSnippetLen] + "…"
		}
		fileLabel := r.FileName
		if fileLabel == "" {
			fileLabel = "unknown"
		}
		contextBuilder.WriteString(fmt.Sprintf("[%d] (%s) %s\n", i+1, fileLabel, snippet))
	}
	return contextBuilder.String()
}

func (s *chatService) buildSystemMessage(contextText string) string {
	// 从配置读取 system 规则与参考包裹符。
	// 优先使用 ai.prompt，缺失时回退到 llm.prompt。
	rules := config.Conf.AI.Prompt.Rules
	if rules == "" {
		rules = config.Conf.LLM.Prompt.Rules
	}
	refStart := config.Conf.AI.Prompt.RefStart
	if refStart == "" {
		refStart = config.Conf.LLM.Prompt.RefStart
	}
	if refStart == "" {
		refStart = "<<REF>>"
	}
	refEnd := config.Conf.AI.Prompt.RefEnd
	if refEnd == "" {
		refEnd = config.Conf.LLM.Prompt.RefEnd
	}
	if refEnd == "" {
		refEnd = "<<END>>"
	}
	var sys strings.Builder
	if rules != "" {
		sys.WriteString(rules)
		sys.WriteString("\n\n")
	}
	sys.WriteString(refStart)
	sys.WriteString("\n")
	if contextText != "" {
		sys.WriteString(contextText)
	} else {
		// 检索空结果时注入“无检索结果提示”，避免模型误以为有引用上下文。
		noRes := config.Conf.AI.Prompt.NoResultText
		if noRes == "" {
			noRes = config.Conf.LLM.Prompt.NoResultText
		}
		if noRes == "" {
			noRes = "（本轮无检索结果）"
		}
		sys.WriteString(noRes)
		sys.WriteString("\n")
	}
	sys.WriteString(refEnd)
	return sys.String()
}

func (s *chatService) loadHistory(ctx context.Context, userID uint) ([]model.ChatMessage, error) {
	convID, err := s.conversationRepo.GetOrCreateConversationID(ctx, userID)
	if err != nil {
		return nil, err
	}
	return s.conversationRepo.GetConversationHistory(ctx, convID)
}

func (s *chatService) composeMessages(systemMsg string, history []model.ChatMessage, userInput string) []model.ChatMessage {
	// 标准 role 顺序：system -> history -> 本轮 user
	// 这样既能注入规则/参考，又能保留多轮对话语境。
	msgs := make([]model.ChatMessage, 0, len(history)+2)
	msgs = append(msgs, model.ChatMessage{Role: "system", Content: systemMsg})
	msgs = append(msgs, history...)
	msgs = append(msgs, model.ChatMessage{Role: "user", Content: userInput})
	return msgs
}

// persistUserMessage 在 LLM 开始生成前，将用户本轮问题先行落库。
// 这样即使用户在 AI 回答过程中导航离开，再回来时也能从 API 拉到本轮问题记录。
func (s *chatService) persistUserMessage(ctx context.Context, userID uint, username, question string) error {
	conversationID, err := s.conversationRepo.GetOrCreateConversationID(ctx, userID)
	if err != nil {
		log.Errorf("[persistUserMessage] userID=%d 获取 conversationID 失败: %v", userID, err)
		return fmt.Errorf("failed to get or create conversation ID: %w", err)
	}
	log.Infof("[persistUserMessage] userID=%d conversationID=%s 准备写入 user 消息", userID, conversationID)
	history, err := s.conversationRepo.GetConversationHistory(ctx, conversationID)
	if err != nil {
		log.Errorf("[persistUserMessage] 获取历史失败: %v", err)
		return fmt.Errorf("failed to get conversation history: %w", err)
	}
	history = append(history, model.ChatMessage{
		Role:      "user",
		Content:   question,
		Timestamp: time.Now(),
		Username:  username,
	})
	if err = s.conversationRepo.UpdateConversationHistory(ctx, conversationID, history); err != nil {
		log.Errorf("[persistUserMessage] 写入 Redis 失败: %v", err)
		return err
	}
	log.Infof("[persistUserMessage] userID=%d user 消息写入 Redis 成功，当前历史 %d 条", userID, len(history))
	return nil
}

// persistAssistantMessage 在 LLM 流式输出完成后，将完整回答追加落库。
// 与 persistUserMessage 配对使用，分两步落库保证即使中途断开用户消息也不丢失。
func (s *chatService) persistAssistantMessage(ctx context.Context, userID uint, answer string) error {
	conversationID, err := s.conversationRepo.GetOrCreateConversationID(ctx, userID)
	if err != nil {
		log.Errorf("[persistAssistantMessage] userID=%d 获取 conversationID 失败: %v", userID, err)
		return fmt.Errorf("failed to get or create conversation ID: %w", err)
	}
	log.Infof("[persistAssistantMessage] userID=%d conversationID=%s 准备写入 assistant 消息", userID, conversationID)
	history, err := s.conversationRepo.GetConversationHistory(ctx, conversationID)
	if err != nil {
		log.Errorf("[persistAssistantMessage] 获取历史失败: %v", err)
		return fmt.Errorf("failed to get conversation history: %w", err)
	}
	history = append(history, model.ChatMessage{
		Role:      "assistant",
		Content:   answer,
		Timestamp: time.Now(),
		Username:  "",
	})
	if err = s.conversationRepo.UpdateConversationHistory(ctx, conversationID, history); err != nil {
		log.Errorf("[persistAssistantMessage] 写入 Redis 失败: %v", err)
		return err
	}
	log.Infof("[persistAssistantMessage] userID=%d assistant 消息写入 Redis 成功，当前历史 %d 条", userID, len(history))
	return nil
}

// wsWriterInterceptor 是对 websocket.Conn 的封装，用于捕获写入的消息。
// writeMu 由 handler 层传入，保证读协程（停止指令回包）和流式输出协程不会并发写同一个连接。
type wsWriterInterceptor struct {
	conn       *websocket.Conn
	writer     *strings.Builder
	shouldStop func() bool
	writeMu    *sync.Mutex
}

// WriteMessage 满足 llm.MessageWriter 接口。
func (w *wsWriterInterceptor) WriteMessage(messageType int, data []byte) error {
	if w.shouldStop != nil && w.shouldStop() {
		// 停止标志生效：跳过下发与累计（仅停止客户端侧输出）。
		// 注意：这不会自动取消上游 LLM 请求；若需彻底中断需结合 ctx cancel。
		return nil
	}
	w.writer.Write(data)
	// 将模型分片包装为前端统一消费格式：{"chunk":"..."}
	payload := map[string]string{"chunk": string(data)}
	b, _ := json.Marshal(payload)
	w.writeMu.Lock()
	err := w.conn.WriteMessage(messageType, b)
	w.writeMu.Unlock()
	return err
}

// sendCompletion 发送完成通知 JSON。
// writeMu 保证与流式 chunk 写入不会并发，避免 gorilla/websocket 帧损坏。
func sendCompletion(ws *websocket.Conn, writeMu *sync.Mutex) {
	notif := map[string]interface{}{
		"type":      "completion",
		"status":    "finished",
		"message":   "响应已完成",
		"timestamp": time.Now().UnixMilli(),
		"date":      time.Now().Format("2006-01-02T15:04:05"),
	}
	b, _ := json.Marshal(notif)
	writeMu.Lock()
	_ = ws.WriteMessage(websocket.TextMessage, b)
	writeMu.Unlock()
}

func (s *chatService) buildGenerationParams() *llm.GenerationParams {
	// 仅在配置有显式值时才透传参数，避免覆盖上游模型默认采样策略。
	var gp llm.GenerationParams
	if config.Conf.LLM.Generation.Temperature != 0 {
		t := config.Conf.LLM.Generation.Temperature
		gp.Temperature = &t
	}
	if config.Conf.LLM.Generation.TopP != 0 {
		p := config.Conf.LLM.Generation.TopP
		gp.TopP = &p
	}
	if config.Conf.LLM.Generation.MaxTokens != 0 {
		m := config.Conf.LLM.Generation.MaxTokens
		gp.MaxTokens = &m
	}
	if gp.Temperature == nil && gp.TopP == nil && gp.MaxTokens == nil {
		return nil
	}
	return &gp
}
