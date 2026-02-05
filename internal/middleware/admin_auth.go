// Package middleware 提供了处理 HTTP 请求的中间件。
package middleware

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"pai-smart-go/internal/model"
)

// AdminAuthMiddleware 检查用户是否具有管理员权限。
// 此中间件必须在 AuthMiddleware 之后使用。
func AdminAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 从 AuthMiddleware 设置的上下文中获取 user 对象
		user, exists := c.Get("user")
		if !exists {
			// 如果 user 对象不存在，说明 AuthMiddleware 未能成功解析，这是一个服务器内部错误
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "无法获取用户信息"})
			return
		}

		// 类型断言，将 user 转换为 *model.User
		currentUser, ok := user.(*model.User)
		if !ok {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "用户数据类型错误"})
			return
		}

		// 检查用户角色是否为 "ADMIN"
		if currentUser.Role != "ADMIN" {
			// 如果不是管理员，则返回 Forbidden 状态
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "权限不足，需要管理员权限"})
			return
		}

		// 用户是管理员，继续处理请求
		c.Next()
	}
}
