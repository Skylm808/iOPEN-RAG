// Package llm provides a client for interacting with Large Language Models.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"pai-smart-go/internal/config"
	"strings"

	"github.com/gorilla/websocket"
)

// MessageWriter defines an interface for writing WebSocket messages.
// This allows both a standard websocket.Conn and our interceptor to be used.
type MessageWriter interface {
	WriteMessage(messageType int, data []byte) error
}

// Client defines the interface for an LLM client.
type Client interface {
	// StreamChatMessages 以 role-based 消息与可选生成参数调用聊天接口，并将流式分块写入 writer。
	StreamChatMessages(ctx context.Context, messages []Message, gen *GenerationParams, writer MessageWriter) error
	// 为兼容旧调用，保留 StreamChat：由内部包装为 messages 调用。
	StreamChat(ctx context.Context, prompt string, writer MessageWriter) error
}

type deepseekClient struct {
	cfg    config.LLMConfig
	client *http.Client
}

// NewClient creates a new LLM client based on the provider in the config.
func NewClient(cfg config.LLMConfig) Client {
	return &deepseekClient{
		cfg:    cfg,
		client: &http.Client{},
	}
}

// Message 表示一条角色消息
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Stream      bool      `json:"stream"`
	Temperature *float64  `json:"temperature,omitempty"`
	TopP        *float64  `json:"top_p,omitempty"`
	MaxTokens   *int      `json:"max_tokens,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
}

// GenerationParams 控制生成行为
type GenerationParams struct {
	Temperature *float64
	TopP        *float64
	MaxTokens   *int
}

// StreamChat calls the DeepSeek API for chat completions and streams the response.
func (c *deepseekClient) StreamChat(ctx context.Context, prompt string, writer MessageWriter) error {
	// 兼容旧接口：仅发送一条 user 消息，不带生成参数
	return c.StreamChatMessages(ctx, []Message{{Role: "user", Content: prompt}}, nil, writer)
}

func (c *deepseekClient) StreamChatMessages(ctx context.Context, messages []Message, gen *GenerationParams, writer MessageWriter) error {
	reqBody := chatRequest{
		Model:    c.cfg.Model,
		Messages: messages,
		Stream:   true,
	}
	// 从配置或传参注入生成参数（传参优先生效）
	if gen != nil {
		reqBody.Temperature = gen.Temperature
		reqBody.TopP = gen.TopP
		reqBody.MaxTokens = gen.MaxTokens
	} else {
		// 从全局配置注入（若非零值）
		if c.cfg.Generation.Temperature != 0 {
			t := c.cfg.Generation.Temperature
			reqBody.Temperature = &t
		}
		if c.cfg.Generation.TopP != 0 {
			p := c.cfg.Generation.TopP
			reqBody.TopP = &p
		}
		if c.cfg.Generation.MaxTokens != 0 {
			m := c.cfg.Generation.MaxTokens
			reqBody.MaxTokens = &m
		}
	}

	reqBytes, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal chat request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.cfg.BaseURL+"/chat/completions", bytes.NewReader(reqBytes))
	if err != nil {
		return fmt.Errorf("failed to create chat request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to call chat api: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("chat api returned non-200 status: %s, body: %s", resp.Status, string(bodyBytes))
	}

	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("failed to read from stream: %w", err)
		}

		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if strings.TrimSpace(data) == "[DONE]" {
				break
			}

			var chunk chatResponse
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue
			}

			if len(chunk.Choices) > 0 {
				content := chunk.Choices[0].Delta.Content
				if err := writer.WriteMessage(websocket.TextMessage, []byte(content)); err != nil {
					return fmt.Errorf("failed to write message to websocket: %w", err)
				}
			}
		}
	}
	return nil
}
