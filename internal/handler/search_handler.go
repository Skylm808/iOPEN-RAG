package handler

import (
	"net/http"
	"pai-smart-go/internal/model"
	"pai-smart-go/internal/service"
	"pai-smart-go/pkg/log"
	"strconv"

	"github.com/gin-gonic/gin"
)

// SearchHandler 结构体定义了搜索相关的处理器。
type SearchHandler struct {
	searchService service.SearchService
}

// NewSearchHandler 创建一个新的 SearchHandler 实例。
func NewSearchHandler(searchService service.SearchService) *SearchHandler {
	return &SearchHandler{
		searchService: searchService,
	}
}

// HybridSearch 是处理混合搜索请求的 Gin 处理函数。
func (h *SearchHandler) HybridSearch(c *gin.Context) {
	query := c.Query("query")
	log.Infof("[SearchHandler] 收到混合搜索请求, query: %s", query)

	if query == "" {
		log.Warnf("[SearchHandler] 搜索请求失败: query 参数为空")
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的查询参数"})
		return
	}
	topKStr := c.DefaultQuery("topK", "10")
	topK, err := strconv.Atoi(topKStr)
	if err != nil || topK <= 0 {
		topK = 10
	}
	log.Infof("[SearchHandler] 解析参数, topK: %d", topK)

	user, exists := c.Get("user")
	if !exists {
		log.Errorf("[SearchHandler] 无法从 Gin 上下文中获取用户信息")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "无法获取用户信息"})
		return
	}

	results, err := h.searchService.HybridSearch(c.Request.Context(), query, topK, user.(*model.User))
	if err != nil {
		log.Errorf("[SearchHandler] 混合搜索服务返回错误, error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "搜索失败"})
		return
	}

	log.Infof("[SearchHandler] 混合搜索成功, query: '%s', 返回 %d 条结果", query, len(results))
	c.JSON(http.StatusOK, gin.H{"code": 200, "data": results, "message": "success"})
}
