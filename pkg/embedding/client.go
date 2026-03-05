// Package embedding 提供了与向量嵌入（Embedding）模型交互的客户端功能。
//
// ── 这个文件在整个项目里的角色 ───────────────────────────────────────────────
//
// "向量化"是 RAG 系统的灵魂启动器。任何文本（无论是用户的提问，还是知识库里的文档分块）
// 要想进行"语义搜索（KNN）"，都必须先通过 Embedding 模型把它转化成一个高维浮点数数组（向量）。
// 这个向量代表了这段文字的"语义信息"，语义相近的句子，对应的向量在高维空间里距离也会很近。
//
// ── 调用流程 ─────────────────────────────────────────────────────────────────
//
//	本 Embedding 客户端在项目中有两处调用场景：
//
//	场景 A【文档上传时：离线向量化】
//	  upload_service.go
//	  → 文件文本被切成 Chunks
//	  → 逐个 Chunk 调用 CreateEmbedding(ctx, chunk) 获取向量
//	  → 向量 + 原文 + 元数据 写入 ES（pkg/es/client.go 的 IndexDocument）
//
//	场景 B【用户提问时：在线向量化】
//	  search_service.go
//	  → 用户输入的问题（query）
//	  → 调用 CreateEmbedding(ctx, query) 获取查询向量
//	  → 用查询向量构造 KNN 检索请求，去 ES 里找最相近的文档 Chunk
//
// ── 兼容性设计 ─────────────────────────────────────────────────────────────
//
//	这个客户端实现了 OpenAI 兼容接口（OpenAI Compatible API）。
//	这意味着只要后端 Embedding 服务遵循 OpenAI 的 REST API 规范，
//	就可以无缝对接。比如：
//	  - 阿里云 DashScope（通义千问 Embedding 模型）
//	  - 本地部署的 Ollama / vLLM
//	  - 原版 OpenAI API
//	只需修改 config.yaml 里的 baseURL、model、apiKey 即可切换，无需改代码。
package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"pai-smart-go/internal/config"
	"pai-smart-go/pkg/log"
)

// Client 定义了与 Embedding 服务交互的最小接口契约。
// 只暴露一个方法：把一段文本转换成对应的浮点数向量（[]float32）。
// 使用接口而非具体结构体，目的是：
//
//	a) 方便未来切换不同的 Embedding 服务商（如换成 HuggingFace API）
//	b) 方便在单元测试中注入 Mock 实现，模拟 API 响应
type Client interface {
	CreateEmbedding(ctx context.Context, text string) ([]float32, error)
}

// openAICompatibleClient 是 Client 接口的具体实现。
// 它通过 HTTP POST 请求调用任何遵循 OpenAI Embeddings API 格式的服务。
type openAICompatibleClient struct {
	cfg    config.EmbeddingConfig // 配置：BaseURL、APIKey、模型名、向量维度
	client *http.Client           // 内置的标准 HTTP 客户端
}

// NewClient 是工厂函数，返回一个 openAICompatibleClient 实例（隐藏于 Client 接口后）。
// 由 wire 依赖注入框架在 main.go 启动时调用，将 cfg 从 config.yaml 中读取并注入。
func NewClient(cfg config.EmbeddingConfig) Client {
	return &openAICompatibleClient{
		cfg:    cfg,
		client: &http.Client{},
	}
}

// embeddingRequest 定义了向 OpenAI 兼容接口发送 POST 请求时的请求体结构。
// 序列化为 JSON 后发送，字段含义：
//   - Model：使用哪个 Embedding 模型（如 "text-embedding-v3"）
//   - Input：待向量化的文本列表（虽然是数组，我们每次只传一条）
//   - Dimensions：（可选）指定输出向量的维度，需与 ES 索引中的 dims 保持一致
type embeddingRequest struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions,omitempty"`
}

// embeddingResponse 定义了 OpenAI 兼容接口返回结果的 JSON 解析结构。
// 响应格式固定为 data 数组，每个元素包含一个 embedding（即向量）。
type embeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// CreateEmbedding 调用 OpenAI 兼容的 Embedding API，将一段文本转化为向量。
//
// 核心流程：
//  1. 构造请求体（含模型名、输入文本、向量维度）
//  2. 发起 HTTP POST 请求到配置文件中的 BaseURL + "/embeddings"
//  3. 在请求头加上 Authorization: Bearer {APIKey}（Standard OpenAI Auth）
//  4. 解析响应，提取 data[0].embedding 返回
//
// 错误情况：
//   - API 报错（非 200 状态码）
//   - 响应解析失败
//   - API 返回了空向量（通常是模型不支持该文本类型）
func (c *openAICompatibleClient) CreateEmbedding(ctx context.Context, text string) ([]float32, error) {
	log.Infof("[EmbeddingClient] 开始调用 Embedding API, model: %s, input_len: %d", c.cfg.Model, len(text))
	reqBody := embeddingRequest{
		Model:      c.cfg.Model,      // 从 config.yaml 读取模型名（如 text-embedding-v3）
		Input:      []string{text},   // 每次只传一条文本（批量化可在此扩展）
		Dimensions: c.cfg.Dimensions, // 维度必须与 ES Mapping 中的 dims: 2048 保持一致
	}

	reqBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal embedding request: %w", err)
	}

	// 构造 HTTP 请求，目标地址 = BaseURL + "/embeddings"
	req, err := http.NewRequestWithContext(ctx, "POST", c.cfg.BaseURL+"/embeddings", bytes.NewReader(reqBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create embedding request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey) // OpenAI 标准认证头

	resp, err := c.client.Do(req)
	if err != nil {
		log.Errorf("[EmbeddingClient] 调用 Embedding API 失败, error: %v", err)
		return nil, fmt.Errorf("failed to call embedding api: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Errorf("[EmbeddingClient] Embedding API 返回非 200 状态码: %s", resp.Status)
		return nil, fmt.Errorf("embedding api returned non-200 status: %s", resp.Status)
	}

	var embeddingResp embeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&embeddingResp); err != nil {
		log.Errorf("[EmbeddingClient] 解析 Embedding API 响应失败, error: %v", err)
		return nil, fmt.Errorf("failed to decode embedding response: %w", err)
	}

	if len(embeddingResp.Data) == 0 || len(embeddingResp.Data[0].Embedding) == 0 {
		log.Warnf("[EmbeddingClient] Embedding API 返回了空的向量数据")
		return nil, fmt.Errorf("received empty embedding from api")
	}

	log.Infof("[EmbeddingClient] 成功从 Embedding API 获取向量, 维度: %d", len(embeddingResp.Data[0].Embedding))
	return embeddingResp.Data[0].Embedding, nil
}
