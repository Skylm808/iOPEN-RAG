// Package handler 包含了处理 HTTP 请求的控制器逻辑。
package handler

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"pai-smart-go/internal/service"
	"pai-smart-go/pkg/token"
)

// ConversationHandler 处理与对话相关的 API 请求。
type ConversationHandler struct {
	service service.ConversationService
}

// NewConversationHandler 创建一个新的 ConversationHandler。
func NewConversationHandler(service service.ConversationService) *ConversationHandler {
	return &ConversationHandler{service: service}
}

// GetConversations 处理获取用户对话历史的请求。
func (h *ConversationHandler) GetConversations(c *gin.Context) {
	claims := c.MustGet("claims").(*token.CustomClaims)

	history, err := h.service.GetConversationHistory(c.Request.Context(), claims.UserID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    http.StatusInternalServerError,
			"message": "Failed to retrieve conversation history",
			"data":    nil,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    http.StatusOK,
		"message": "success",
		"data":    history,
	})
}
