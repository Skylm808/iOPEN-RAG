// Package reranker 提供了与 Cross-Encoder Reranker 模型交互的客户端功能。
//
// ── 这个文件在整个项目里的角色 ───────────────────────────────────────────────
//
// Reranker（精排器）是两阶段 RAG 检索的第二阶段核心组件。
//
// 第一阶段（ES 召回）：KNN 向量 + BM25 快速从全量文档里拉出候选集（Top-20）
//
//	→ 召回优先"不漏"，但精度有限（Bi-Encoder 独立编码 query 和 doc）
//
// 第二阶段（Reranker 精排）：Cross-Encoder 对每个 (query, doc) 联合建模重打分
//
//	→ 精度极高（query 和 doc 一起过 Transformer），从候选集里选出真正相关的 Top-5
//
// ── 兼容性设计 ─────────────────────────────────────────────────────────────
//
// 本客户端实现了 Cohere Rerank 兼容接口，以下服务均可对接：
//   - Xinference 本地部署 bge-reranker-v2-m3（推荐，私有化）
//   - 阿里云 DashScope gte-rerank
//   - Cohere 官方 API
//
// 只需修改 config.yaml 里的 base_url、model、api_key 即可切换，无需改代码。
package reranker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"pai-smart-go/internal/config"
	"pai-smart-go/pkg/log"
	"sort"
)

// Client 定义了与 Reranker 服务交互的最小接口契约。
// 接受一个 query 和若干候选文档，返回按相关性得分降序排列的结果。
type Client interface {
	// Rerank 对候选文档进行重排序。
	// documents：候选文档的原始文本切片（来自 ES 召回结果）
	// topN：最终返回的文档数量
	// 返回：按 relevance_score 降序排列的 RerankResult 列表
	Rerank(ctx context.Context, query string, documents []string, topN int) ([]RerankResult, error)
}

// RerankResult 是 Reranker 返回的单条结果。
// Index 是原始 documents 切片中的下标，用于回查对应的文档对象。
type RerankResult struct {
	Index          int     // 对应 documents[Index] 的原始下标
	RelevanceScore float64 // Cross-Encoder 给出的相关性得分（越高越相关）
}

// cohereCompatibleClient 是 Client 接口的具体实现。
// 通过 HTTP POST 请求调用任何遵循 Cohere Rerank API 格式的服务。
type cohereCompatibleClient struct {
	cfg    config.RerankConfig
	client *http.Client
}

// NewClient 是工厂函数，返回一个 cohereCompatibleClient 实例。
func NewClient(cfg config.RerankConfig) Client {
	return &cohereCompatibleClient{
		cfg:    cfg,
		client: &http.Client{},
	}
}

// ── 请求/响应结构体 ──────────────────────────────────────────────────────────

// rerankRequest 是发送给 Reranker 服务的请求体。
type rerankRequest struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TopN      int      `json:"top_n"`
}

// rerankResponse 是 Reranker 服务返回的响应体。
// Xinference/DashScope/Cohere 均使用此格式。
type rerankResponse struct {
	Results []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
	} `json:"results"`
}

// Rerank 调用 Reranker API，对文档列表按与 query 的相关性进行重排序。
//
// 核心流程：
//  1. 构造请求体（query、文档列表、模型名、topN）
//  2. 发起 HTTP POST 请求到 base_url + "/rerank"
//  3. 解析响应，提取每条结果的 index 和 relevance_score
//  4. 按 relevance_score 降序排序后返回
func (c *cohereCompatibleClient) Rerank(ctx context.Context, query string, documents []string, topN int) ([]RerankResult, error) {
	log.Infof("[RerankerClient] 开始调用 Rerank API, model: %s, doc数: %d, topN: %d",
		c.cfg.Model, len(documents), topN)

	if len(documents) == 0 {
		return []RerankResult{}, nil
	}

	// 构造请求体
	reqBody := rerankRequest{
		Model:     c.cfg.Model,
		Query:     query,
		Documents: documents,
		TopN:      topN,
	}

	reqBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal rerank request: %w", err)
	}

	// 构造 HTTP 请求，目标地址 = base_url + "/rerank"
	req, err := http.NewRequestWithContext(ctx, "POST", c.cfg.BaseURL+"/rerank", bytes.NewReader(reqBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create rerank request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	// 部分服务需要鉴权（如 Cohere/DashScope），Xinference 本地部署填 "EMPTY" 占位即可
	if c.cfg.APIKey != "" && c.cfg.APIKey != "EMPTY" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		log.Errorf("[RerankerClient] 调用 Rerank API 失败, error: %v", err)
		return nil, fmt.Errorf("failed to call rerank api: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Errorf("[RerankerClient] Rerank API 返回非 200 状态码: %s", resp.Status)
		return nil, fmt.Errorf("rerank api returned non-200 status: %s", resp.Status)
	}

	var rerankResp rerankResponse
	if err := json.NewDecoder(resp.Body).Decode(&rerankResp); err != nil {
		return nil, fmt.Errorf("failed to decode rerank response: %w", err)
	}

	if len(rerankResp.Results) == 0 {
		log.Warnf("[RerankerClient] Rerank API 返回了空结果")
		return []RerankResult{}, nil
	}

	// 转换为内部结构体，并按 relevance_score 降序排序
	results := make([]RerankResult, 0, len(rerankResp.Results))
	for _, r := range rerankResp.Results {
		results = append(results, RerankResult{
			Index:          r.Index,
			RelevanceScore: r.RelevanceScore,
		})
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].RelevanceScore > results[j].RelevanceScore
	})

	log.Infof("[RerankerClient] Rerank 完成, 返回 %d 条结果, 最高分: %.4f",
		len(results), results[0].RelevanceScore)
	return results, nil
}
