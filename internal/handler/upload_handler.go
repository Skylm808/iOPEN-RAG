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

// calculateProgress 计算上传百分比（0~100）。
// uploadedChunks 是“已上传分片索引列表”，totalChunks 是文件总分片数。
func calculateProgress(uploadedChunks []int, totalChunks int) float64 {
	if totalChunks == 0 {
		return 0.0
	}
	return (float64(len(uploadedChunks)) / float64(totalChunks)) * 100
}

// UploadHandler 是文件上传相关 HTTP 接口的入口层。
// handler 仅负责参数解析/鉴权上下文提取/响应封装，具体业务在 UploadService 中。
type UploadHandler struct {
	uploadService service.UploadService
}

// NewUploadHandler 创建一个新的 UploadHandler 实例。
func NewUploadHandler(uploadService service.UploadService) *UploadHandler {
	return &UploadHandler{uploadService: uploadService}
}

// CheckFileRequest 对应秒传检查接口请求体。
type CheckFileRequest struct {
	MD5 string `json:"md5" binding:"required"`
}

// CheckFile 处理“秒传/断点续传预检查”。
// 返回：
// - completed=true：文件已完成上传，可直接秒传
// - uploadedChunks：若未完成，返回已上传分片索引供前端续传
func (h *UploadHandler) CheckFile(c *gin.Context) {
	var req CheckFileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求负载"})
		return
	}

	// claims 由 AuthMiddleware 注入，包含当前登录用户身份
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

// UploadChunk 处理单个上传分片。
// 请求来自 multipart/form-data，核心字段：
// - fileMd5/fileName/totalSize/chunkIndex
// - file（二进制分片）
// - orgTag/isPublic（文档权限元信息）
func (h *UploadHandler) UploadChunk(c *gin.Context) {
	// 1) 读取表单参数
	fileMD5 := c.PostForm("fileMd5")
	fileName := c.PostForm("fileName")
	totalSizeStr := c.PostForm("totalSize")
	chunkIndexStr := c.PostForm("chunkIndex")
	orgTag := c.PostForm("orgTag")
	isPublicStr := c.PostForm("isPublic") // "true" or "false"

	// 2) 参数完整性和类型校验
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
	// ParseBool 失败时返回 false，等价“默认私有”
	isPublic, _ := strconv.ParseBool(isPublicStr)

	// 3) 读取分片文件流
	file, _, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "未能获取上传的分片"})
		return
	}
	defer file.Close()

	// 4) 取当前用户并交给 service 执行业务逻辑（幂等检查、落 MinIO、写分片记录、Redis 标记）
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

	// 5) 计算并返回当前上传进度
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

// MergeChunksRequest 对应“请求合并分片”接口的请求体。
type MergeChunksRequest struct {
	MD5      string `json:"fileMd5" binding:"required"`
	FileName string `json:"fileName" binding:"required"`
}

// MergeChunks 触发服务端合并流程。
// 核心动作（在 service 层）：
// - 校验分片完整性
// - 在 MinIO 合并对象
// - 更新数据库状态
// - 投递 Kafka 异步处理任务（文本提取/切块/向量化）
func (h *UploadHandler) MergeChunks(c *gin.Context) {
	var req MergeChunksRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求负载"})
		return
	}

	// 从 claims 获取当前用户，确保按“用户+文件”维度执行合并
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

// GetUploadStatus 查询某个文件当前上传状态（用于前端轮询/断点续传恢复）。
// 返回文件名、文件类型、已上传分片、总分片、进度百分比。
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
		// 这里通过字符串判断“记录不存在”，后续可改为 errors.Is 提升健壮性。
		if err.Error() == "record not found" {
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

// GetSupportedFileTypes 返回系统支持上传并可解析的文件类型。
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

// FastUpload 处理“快速上传”检查（轻量接口）。
// 与 CheckFile 的差异：该接口仅返回是否已上传完成，不返回 uploadedChunks 细节。
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
