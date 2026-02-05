// Package handler 包含了处理 HTTP 请求的控制器逻辑。
package handler

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"pai-smart-go/internal/service"
	"pai-smart-go/pkg/log"
)

// AuthHandler 负责处理认证相关的 API 请求，例如刷新 token。
type AuthHandler struct {
	userService service.UserService
}

// NewAuthHandler 创建一个新的 AuthHandler 实例。
func NewAuthHandler(userService service.UserService) *AuthHandler {
	return &AuthHandler{userService: userService}
}

// RefreshTokenRequest 定义了刷新 token API 的请求体结构。
type RefreshTokenRequest struct {
	RefreshToken string `json:"refreshToken" binding:"required"`
}

// RefreshToken 处理刷新 token 的请求。
func (h *AuthHandler) RefreshToken(c *gin.Context) {
	var req RefreshTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Warnf("RefreshToken: Invalid request payload, error: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求负载：refreshToken 不能为空"})
		return
	}

	newAccessToken, newRefreshToken, err := h.userService.RefreshToken(req.RefreshToken)
	if err != nil {
		log.Warnf("RefreshToken: Failed to refresh token, error: %v", err)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "无效的 refresh token"})
		return
	}

	log.Info("Token refreshed successfully")
	c.JSON(http.StatusOK, gin.H{
		"code":    http.StatusOK,
		"message": "Token refreshed successfully",
		"data": gin.H{
			"token":        newAccessToken,
			"refreshToken": newRefreshToken,
		},
	})
}
