// Package handler 包含了处理 HTTP 请求的控制器逻辑。
package handler

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"pai-smart-go/internal/service"
	"pai-smart-go/pkg/log"
	"pai-smart-go/pkg/token"
	"strconv"
)

// calculateProgress is a helper function to calculate upload progress.
func calculateProgress(uploadedChunks []int, totalChunks int) float64 {
	if totalChunks == 0 {
		return 0.0
	}
	return (float64(len(uploadedChunks)) / float64(totalChunks)) * 100
}

// UploadHandler 负责处理所有与文件上传相关的 API 请求。
type UploadHandler struct {
	uploadService service.UploadService
}

// NewUploadHandler 创建一个新的 UploadHandler 实例。
func NewUploadHandler(uploadService service.UploadService) *UploadHandler {
	return &UploadHandler{uploadService: uploadService}
}

// CheckFileRequest 定义了文件秒传检查 API 的请求体结构。
type CheckFileRequest struct {
	MD5 string `json:"md5" binding:"required"`
}

// CheckFile 处理文件秒传检查的请求。
func (h *UploadHandler) CheckFile(c *gin.Context) {
	var req CheckFileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求负载"})
		return
	}

	claimsValue, _ := c.Get("claims")
	userClaims := claimsValue.(*token.CustomClaims)
	userID := userClaims.UserID

	completed, uploadedChunks, err := h.uploadService.CheckFile(c.Request.Context(), req.MD5, userID)
	if err != nil {
		log.Error("CheckFile: failed to check file", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"completed":      completed,
		"uploadedChunks": uploadedChunks,
	})
}

// UploadChunk 处理分片上传的请求。
func (h *UploadHandler) UploadChunk(c *gin.Context) {
	// 从表单中获取参数
	fileMD5 := c.PostForm("fileMd5")
	fileName := c.PostForm("fileName")
	totalSizeStr := c.PostForm("totalSize")
	chunkIndexStr := c.PostForm("chunkIndex")
	orgTag := c.PostForm("orgTag")
	isPublicStr := c.PostForm("isPublic") // "true" or "false"

	if fileMD5 == "" || fileName == "" || totalSizeStr == "" || chunkIndexStr == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少必要的参数"})
		return
	}

	totalSize, err := strconv.ParseInt(totalSizeStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的文件大小"})
		return
	}
	chunkIndex, err := strconv.Atoi(chunkIndexStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的分片索引"})
		return
	}
	isPublic, _ := strconv.ParseBool(isPublicStr) // Defaults to false on error

	// 获取上传的分片文件
	file, _, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "未能获取上传的分片"})
		return
	}
	defer file.Close()

	claims, _ := c.Get("claims")
	userClaims := claims.(*token.CustomClaims)
	userID := userClaims.UserID

	uploadedChunks, totalChunks, err := h.uploadService.UploadChunk(c.Request.Context(), fileMD5, fileName, totalSize, chunkIndex, file, userID, orgTag, isPublic)
	if err != nil {
		log.Error("UploadChunk: failed to upload chunk", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    http.StatusInternalServerError,
			"message": "分片上传失败: " + err.Error(),
		})
		return
	}

	progress := calculateProgress(uploadedChunks, totalChunks)

	c.JSON(http.StatusOK, gin.H{
		"code":    http.StatusOK,
		"message": "分片上传成功",
		"data": gin.H{
			"uploaded": uploadedChunks,
			"progress": progress,
		},
	})
}

// MergeChunksRequest 定义了分片合并 API 的请求体结构。
type MergeChunksRequest struct {
	MD5      string `json:"fileMd5" binding:"required"`
	FileName string `json:"fileName" binding:"required"`
}

// MergeChunks 处理分片合并的请求。
func (h *UploadHandler) MergeChunks(c *gin.Context) {
	var req MergeChunksRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求负载"})
		return
	}

	// Placeholder for user ID
	claimsValue, _ := c.Get("claims")
	userClaims := claimsValue.(*token.CustomClaims)
	userID := userClaims.UserID

	objectURL, err := h.uploadService.MergeChunks(c.Request.Context(), req.MD5, req.FileName, userID)
	if err != nil {
		log.Error("MergeChunks: failed to merge chunks", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "文件合并失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    http.StatusOK,
		"message": "文件合并成功，任务已发送到 Kafka",
		"data":    gin.H{"object_url": objectURL},
	})
}

// GetUploadStatus 处理获取文件上传状态的请求。
func (h *UploadHandler) GetUploadStatus(c *gin.Context) {
	fileMD5 := c.Query("file_md5")
	if fileMD5 == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 file_md5 参数"})
		return
	}

	claims, _ := c.Get("claims")
	userClaims := claims.(*token.CustomClaims)
	userID := userClaims.UserID

	fileName, fileType, uploadedChunks, totalChunks, err := h.uploadService.GetUploadStatus(c.Request.Context(), fileMD5, userID)
	if err != nil {
		if err.Error() == "record not found" { // A more specific error check would be better
			c.JSON(http.StatusNotFound, gin.H{
				"code":    http.StatusNotFound,
				"message": "未找到上传记录",
			})
			return
		}
		log.Error("GetUploadStatus: failed", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取上传状态失败"})
		return
	}

	progress := calculateProgress(uploadedChunks, totalChunks)

	c.JSON(http.StatusOK, gin.H{
		"code":    http.StatusOK,
		"message": "获取上传状态成功",
		"data": gin.H{
			"fileName":    fileName,
			"fileType":    fileType,
			"uploaded":    uploadedChunks,
			"progress":    progress,
			"totalChunks": totalChunks, // Corrected field name
		},
	})
}

// GetSupportedFileTypes 处理获取支持文件类型列表的请求。
func (h *UploadHandler) GetSupportedFileTypes(c *gin.Context) {
	types, err := h.uploadService.GetSupportedFileTypes()
	if err != nil {
		log.Error("GetSupportedFileTypes: failed", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取支持的文件类型失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"code":    http.StatusOK,
		"message": "获取支持的文件类型成功",
		"data":    types,
	})
}

// FastUpload handles the fast upload check request.
func (h *UploadHandler) FastUpload(c *gin.Context) {
	var req struct {
		MD5 string `json:"md5"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	claims := c.MustGet("claims").(*token.CustomClaims)

	isUploaded, err := h.uploadService.FastUpload(c.Request.Context(), req.MD5, claims.UserID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check file status"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"uploaded": isUploaded})
}
