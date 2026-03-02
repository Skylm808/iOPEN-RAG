package handler

import (
	"net/http"
	"pai-smart-go/internal/model"
	"pai-smart-go/internal/service"
	"pai-smart-go/pkg/log"
	"strconv"

	"github.com/gin-gonic/gin"
)

// SearchHandler 是搜索模块的 HTTP 入口层（handler 层）。
//
// ── handler 层 vs service 层 ──────────────────────────────────────────────────
//
// 项目采用分层架构，SearchHandler 和 SearchService 都有 HybridSearch 方法，
// 但职责完全不同，可以理解为"服务员"和"厨师"的关系：
//
//	SearchHandler.HybridSearch（服务员）：
//	  - 解析 HTTP 请求参数（query、topK）
//	  - 从中间件上下文中取出当前登录用户
//	  - 调用 SearchService 执行检索
//	  - 将结果序列化为 JSON 返回给前端
//	  - 不关心"怎么检索"，只管"收参数、传结果"
//
//	SearchService.HybridSearch（厨师）：
//	  - 向量召回（knn 相似度搜索）
//	  - 关键词重排（BM25）
//	  - 用户权限过滤（只返回用户有权访问的文件分块）
//	  - RRF 分数融合
//	  - 不关心"从哪来、往哪去"，只管"怎么检索"
//
// ── 两种调用场景 ──────────────────────────────────────────────────────────────
//
// SearchHandler.HybridSearch 对应"知识库检索"功能：
//
//	结果直接返回给前端展示（含分数、文件名、文本片段），用于调试检索质量。
//
// ChatService 内部也会调用 SearchService.HybridSearch，
//
//	但结果不返回前端，而是拼入 LLM 的 prompt 作为参考上下文，生成 AI 回答。
//
//	               用户 → GET /api/v1/search/hybrid
//	                          ↓
//	                SearchHandler.HybridSearch   ← 你在看的这个文件
//	                          ↓
//	                SearchService.HybridSearch   ← search_service.go
//	                          ↓
//	                    返回检索结果给前端
//
//	               用户 → WebSocket /chat/{token}
//	                          ↓
//	                  ChatHandler.Handle
//	                          ↓
//	                ChatService.StreamResponse
//	                          ↓
//	                SearchService.HybridSearch   ← 同一个 service，不同调用路径
//	                          ↓
//	                  结果拼入 LLM prompt → 流式输出 AI 回答
type SearchHandler struct {
	searchService service.SearchService
}

// NewSearchHandler 创建一个新的 SearchHandler 实例。
func NewSearchHandler(searchService service.SearchService) *SearchHandler {
	return &SearchHandler{
		searchService: searchService,
	}
}

// HybridSearch 处理"知识库检索"的 GET 请求。
//
// 典型调用：GET /api/v1/search/hybrid?query=年假规则&topK=10
//
// 注意：这里的 HybridSearch 是 handler 层方法，只负责参数解析和结果返回。
// 真正的检索策略（knn + BM25 + RRF 融合）在 SearchService.HybridSearch 里实现。
func (h *SearchHandler) HybridSearch(c *gin.Context) {
	// 1) 读取 query 文本（必填）
	query := c.Query("query")
	log.Infof("[SearchHandler] 收到混合搜索请求, query: %s", query)

	if query == "" {
		log.Warnf("[SearchHandler] 搜索请求失败: query 参数为空")
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的查询参数"})
		return
	}
	// 2) 读取 topK（可选），非法值回退到默认 10
	topKStr := c.DefaultQuery("topK", "10")
	topK, err := strconv.Atoi(topKStr)
	if err != nil || topK <= 0 {
		topK = 10
	}
	log.Infof("[SearchHandler] 解析参数, topK: %d", topK)

	// 3) 从 AuthMiddleware 注入的上下文获取当前用户。
	// AuthMiddleware 在鉴权成功后会把 user 对象写入 gin.Context，
	// 这里直接取用，不需要重新查数据库。
	user, exists := c.Get("user")
	if !exists {
		log.Errorf("[SearchHandler] 无法从 Gin 上下文中获取用户信息")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "无法获取用户信息"})
		return
	}

	// 4) 调用搜索服务。service 会同时处理：权限过滤、向量召回、关键词重排。
	results, err := h.searchService.HybridSearch(c.Request.Context(), query, topK, user.(*model.User))
	if err != nil {
		log.Errorf("[SearchHandler] 混合搜索服务返回错误, error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "搜索失败"})
		return
	}

	// 5) 返回统一成功结构，前端"知识库检索"面板会展示每条结果的 Score 分数和文本片段
	log.Infof("[SearchHandler] 混合搜索成功, query: '%s', 返回 %d 条结果", query, len(results))
	c.JSON(http.StatusOK, gin.H{"code": 200, "data": results, "message": "success"})
}
