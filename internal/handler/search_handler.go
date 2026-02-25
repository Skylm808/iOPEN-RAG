package handler

import (
	"net/http"
	"pai-smart-go/internal/model"
	"pai-smart-go/internal/service"
	"pai-smart-go/pkg/log"
	"strconv"

	"github.com/gin-gonic/gin"
)

// SearchHandler 是搜索模块的 HTTP 入口层。
// 主要职责：
// - 解析并校验查询参数（query/topK）
// - 从中间件上下文读取当前用户
// - 调用 SearchService 执行混合检索
// - 统一返回 JSON 响应
type SearchHandler struct {
	searchService service.SearchService
}

// NewSearchHandler 创建一个新的 SearchHandler 实例。
func NewSearchHandler(searchService service.SearchService) *SearchHandler {
	return &SearchHandler{
		searchService: searchService,
	}
}

// HybridSearch 处理 GET 搜索请求。
// 典型调用：/api/v1/search/hybrid?query=年假规则&topK=10
// 注意：真正的检索策略（knn + must/filter/should + rescore）在 service 层实现。
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

	// 3) 从 AuthMiddleware 注入的上下文获取当前用户
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

	// 5) 返回统一成功结构
	log.Infof("[SearchHandler] 混合搜索成功, query: '%s', 返回 %d 条结果", query, len(results))
	c.JSON(http.StatusOK, gin.H{"code": 200, "data": results, "message": "success"})
}
