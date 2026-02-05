// Package handler 包含了处理 HTTP 请求的控制器逻辑。
package handler

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"pai-smart-go/internal/model"
	"pai-smart-go/internal/service"
	"pai-smart-go/pkg/log"
	"pai-smart-go/pkg/token"
)

// DocumentHandler 负责处理所有与文档管理相关的 API 请求。
type DocumentHandler struct {
	docService  service.DocumentService
	userService service.UserService
}

// NewDocumentHandler 创建一个新的 DocumentHandler 实例。
func NewDocumentHandler(docService service.DocumentService, userService service.UserService) *DocumentHandler {
	return &DocumentHandler{
		docService:  docService,
		userService: userService,
	}
}

// ListAccessibleFiles 处理获取可访问文件列表的请求。
func (h *DocumentHandler) ListAccessibleFiles(c *gin.Context) {
	user, err := h.getUserFromContext(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "无法获取用户信息"})
		return
	}

	files, err := h.docService.ListAccessibleFiles(user)
	if err != nil {
		log.Error("ListAccessibleFiles: failed", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取文件列表失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"code":    http.StatusOK,
		"message": "获取可访问文件列表成功",
		"data":    files,
	})
}

// ListUploadedFiles 处理获取用户已上传文件列表的请求。
func (h *DocumentHandler) ListUploadedFiles(c *gin.Context) {
	user, err := h.getUserFromContext(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "无法获取用户信息"})
		return
	}

	files, err := h.docService.ListUploadedFiles(user.ID)
	if err != nil {
		log.Error("ListUploadedFiles: failed", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取文件列表失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"code":    http.StatusOK,
		"message": "获取用户上传文件列表成功",
		"data":    files,
	})
}

// DeleteDocument 处理删除文档的请求。
func (h *DocumentHandler) DeleteDocument(c *gin.Context) {
	fileMD5 := c.Param("fileMd5")
	if fileMD5 == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少文件 MD5"})
		return
	}

	user, err := h.getUserFromContext(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "无法获取用户信息"})
		return
	}

	err = h.docService.DeleteDocument(fileMD5, user)
	if err != nil {
		log.Warnf("DeleteDocument: failed for user %s, md5 %s, err: %v", user.Username, fileMD5, err)
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"code":    http.StatusOK,
		"message": "文档删除成功",
	})
}

// GenerateDownloadURL 处理生成文件下载链接的请求。
func (h *DocumentHandler) GenerateDownloadURL(c *gin.Context) {
	fileName := c.Query("fileName") // Changed from Param to Query
	if fileName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少文件名"})
		return
	}

	user, err := h.getUserFromContext(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "无法获取用户信息"})
		return
	}

	downloadInfo, err := h.docService.GenerateDownloadURL(fileName, user)
	if err != nil {
		log.Warnf("GenerateDownloadURL: failed for user %s, file %s, err: %v", user.Username, fileName, err)
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"code":    http.StatusOK,
		"message": "文件下载链接生成成功",
		"data":    downloadInfo,
	})
}

// PreviewFile 处理获取文件预览内容的请求。
func (h *DocumentHandler) PreviewFile(c *gin.Context) {
	fileName := c.Query("fileName")
	if fileName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少文件名"})
		return
	}

	user, err := h.getUserFromContext(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "无法获取用户信息"})
		return
	}

	previewInfo, err := h.docService.GetFilePreviewContent(fileName, user)
	if err != nil {
		log.Warnf("PreviewFile: failed for user %s, file %s, err: %v", user.Username, fileName, err)
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"code":    http.StatusOK,
		"message": "文件预览内容获取成功",
		"data":    previewInfo,
	})
}

// getUserFromContext 是一个辅助函数，用于从 Gin 上下文中获取完整的用户模型。
func (h *DocumentHandler) getUserFromContext(c *gin.Context) (*model.User, error) {
	claimsValue, _ := c.Get("claims")
	claims := claimsValue.(*token.CustomClaims)
	return h.userService.GetProfile(claims.Username)
}
