// Package service 包含了应用的业务逻辑层。
package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"mime/multipart"
	"pai-smart-go/internal/config"
	"pai-smart-go/internal/model"
	"pai-smart-go/internal/repository"
	"pai-smart-go/pkg/kafka"
	"pai-smart-go/pkg/log"
	"pai-smart-go/pkg/storage"
	"pai-smart-go/pkg/tasks"
	"strings"

	"github.com/minio/minio-go/v7"
	"gorm.io/gorm"
)

const (
	// DefaultChunkSize 定义了用于计算总分片数的默认分片大小 (5MB)，与 Java 版本保持一致。
	DefaultChunkSize = 5 * 1024 * 1024
)

// UploadService 接口定义了文件上传相关的业务操作。
type UploadService interface {
	CheckFile(ctx context.Context, fileMD5 string, userID uint) (bool, []int, error)
	UploadChunk(ctx context.Context, fileMD5, fileName string, totalSize int64, chunkIndex int, file multipart.File, userID uint, orgTag string, isPublic bool) (uploadedChunks []int, totalChunks int, err error)
	MergeChunks(ctx context.Context, fileMD5, fileName string, userID uint) (string, error)
	GetUploadStatus(ctx context.Context, fileMD5 string, userID uint) (fileName string, fileType string, uploadedChunks []int, totalChunks int, err error)
	GetSupportedFileTypes() (map[string]interface{}, error)
	FastUpload(ctx context.Context, fileMD5 string, userID uint) (bool, error)
}

type uploadService struct {
	uploadRepo repository.UploadRepository
	userRepo   repository.UserRepository // We need user repo to get user info
	minioCfg   config.MinIOConfig
}

// NewUploadService 创建一个新的 UploadService 实例。
func NewUploadService(uploadRepo repository.UploadRepository, userRepo repository.UserRepository, minioCfg config.MinIOConfig) UploadService {
	return &uploadService{
		uploadRepo: uploadRepo,
		userRepo:   userRepo,
		minioCfg:   minioCfg,
	}
}

// CheckFile 检查文件是否已上传（秒传逻辑）。
func (s *uploadService) CheckFile(ctx context.Context, fileMD5 string, userID uint) (bool, []int, error) {
	log.Infof("[CheckFile] 开始秒传检查，文件MD5: %s, 用户ID: %d", fileMD5, userID)

	record, err := s.uploadRepo.GetFileUploadRecord(fileMD5, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			log.Infof("[CheckFile] 文件记录不存在，需要进行普通上传。文件MD5: %s", fileMD5)
			return false, nil, nil
		}
		log.Errorf("[CheckFile] 秒传检查失败：查询文件记录时出错, error: %v", err)
		return false, nil, err
	}

	if record.Status == 1 {
		log.Infof("[CheckFile] 文件已存在且状态为已完成，秒传成功。文件MD5: %s", fileMD5)
		return true, nil, nil
	}

	totalChunks := s.calculateTotalChunks(record.TotalSize)
	uploadedIndexes, err := s.uploadRepo.GetUploadedChunksFromRedis(ctx, fileMD5, userID, totalChunks)
	if err != nil {
		log.Errorf("[CheckFile] 秒传检查失败：从Redis获取已上传分片列表时出错, error: %v", err)
		return false, nil, err
	}
	log.Infof("[CheckFile] 文件记录已存在但未完成，返回已上传的分片列表。文件MD5: %s, 已上传分片数: %d", fileMD5, len(uploadedIndexes))
	return false, uploadedIndexes, nil
}

// UploadChunk 处理单个分片的上传。
func (s *uploadService) UploadChunk(ctx context.Context, fileMD5, fileName string, totalSize int64, chunkIndex int, file multipart.File, userID uint, orgTag string, isPublic bool) ([]int, int, error) {
	log.Infof("[UploadChunk] 开始上传分片，文件MD5: %s, 分片序号: %d, 用户ID: %d", fileMD5, chunkIndex, userID)

	// 增强逻辑: 文件类型验证 (简化版)
	if chunkIndex == 0 {
		supportedTypes, _ := s.GetSupportedFileTypes()
		extensions, ok := supportedTypes["supportedExtensions"].([]string)
		if !ok {
			return nil, 0, errors.New("invalid supported types configuration")
		}
		isValid := false
		for _, ext := range extensions {
			if strings.HasSuffix(strings.ToLower(fileName), ext) { // ext now includes "."
				isValid = true
				break
			}
		}
		if !isValid {
			return nil, 0, fmt.Errorf("unsupported file type for %s", fileName)
		}
	}

	// 1. 检查或创建 FileUpload 记录
	record, err := s.uploadRepo.GetFileUploadRecord(fileMD5, userID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		log.Infof("[UploadChunk] 文件上传记录不存在，为文件MD5: %s 创建新记录", fileMD5)
		// 增强逻辑2: 自动关联主组织
		if orgTag == "" {
			user, userErr := s.userRepo.FindByID(userID)
			if userErr != nil {
				return nil, 0, userErr
			}
			orgTag = user.PrimaryOrg
		}

		newRecord := &model.FileUpload{
			FileMD5:   fileMD5,
			FileName:  fileName,
			TotalSize: totalSize,
			Status:    0, // 上传中
			UserID:    userID,
			OrgTag:    orgTag,
			IsPublic:  isPublic, // 保存 isPublic 状态
		}
		if err := s.uploadRepo.CreateFileUploadRecord(newRecord); err != nil {
			log.Errorf("[UploadChunk] 创建文件上传记录失败, error: %v", err)
			return nil, 0, err
		}
		record = newRecord // use the new record for subsequent logic
	} else if err != nil {
		log.Errorf("[UploadChunk] 查询文件上传记录失败, error: %v", err)
		return nil, 0, err
	}

	// 2. 检查分片是否已上传 (Redis)
	isUploaded, err := s.uploadRepo.IsChunkUploaded(ctx, fileMD5, userID, chunkIndex)
	if err != nil {
		log.Errorf("[UploadChunk] 从Redis检查分片上传状态失败, error: %v", err)
		return nil, 0, fmt.Errorf("failed to check chunk status from redis: %w", err)
	}
	if isUploaded {
		log.Infof("[UploadChunk] 分片 %d 已上传过，跳过本次上传。文件MD5: %s", chunkIndex, fileMD5)
		totalChunks := s.calculateTotalChunks(record.TotalSize)
		uploadedIndexes, err := s.uploadRepo.GetUploadedChunksFromRedis(ctx, fileMD5, userID, totalChunks)
		if err != nil {
			return nil, 0, err
		}
		return uploadedIndexes, totalChunks, nil
	}

	// 3. 将分片上传到 MinIO
	objectName := fmt.Sprintf("chunks/%s/%d", fileMD5, chunkIndex) // 与 Java 一致的路径
	_, err = storage.MinioClient.PutObject(ctx, s.minioCfg.BucketName, objectName, file, -1, minio.PutObjectOptions{})
	if err != nil {
		log.Errorf("[UploadChunk] 上传分片到MinIO失败, objectName: %s, error: %v", objectName, err)
		return nil, 0, err
	}

	// 4. 在数据库中记录分片信息
	chunkRecord := &model.ChunkInfo{
		FileMD5:     fileMD5,
		ChunkIndex:  chunkIndex,
		ChunkMD5:    "",         // Go version doesn't calculate chunk md5 for now
		StoragePath: objectName, // 保存存储路径
	}
	if err := s.uploadRepo.CreateChunkInfoRecord(chunkRecord); err != nil {
		log.Errorf("[UploadChunk] 在数据库中创建分片记录失败, error: %v", err)
		return nil, 0, err
	}

	// 5. 在 Redis 中标记分片为已上传
	if err := s.uploadRepo.MarkChunkUploaded(ctx, fileMD5, userID, chunkIndex); err != nil {
		log.Errorf("[UploadChunk] 严重错误：在Redis中标记分片已上传失败, error: %v", err)
		return nil, 0, err
	}

	// 6. 获取最新的已上传分片列表并计算总分片数
	totalChunks := s.calculateTotalChunks(record.TotalSize)
	uploadedIndexes, err := s.uploadRepo.GetUploadedChunksFromRedis(ctx, fileMD5, userID, totalChunks)
	if err != nil {
		log.Errorf("[UploadChunk] 上传成功后从Redis获取最新分片列表失败, error: %v", err)
		return nil, 0, err
	}

	log.Infof("[UploadChunk] 分片上传成功。文件MD5: %s, 分片序号: %d, 总进度: %d/%d", fileMD5, chunkIndex, len(uploadedIndexes), totalChunks)
	return uploadedIndexes, totalChunks, nil
}

// MergeChunks 合并所有分片。
func (s *uploadService) MergeChunks(ctx context.Context, fileMD5, fileName string, userID uint) (string, error) {
	log.Infof("[MergeChunks] 开始合并文件分片，文件MD5: %s, 用户ID: %d", fileMD5, userID)
	record, err := s.uploadRepo.GetFileUploadRecord(fileMD5, userID)
	if err != nil {
		log.Errorf("[MergeChunks] 合并分片失败：获取文件记录时出错, error: %v", err)
		return "", err
	}

	// 1. 检查分片是否已全部上传 (Redis)，这是快速检查
	totalChunks := s.calculateTotalChunks(record.TotalSize)
	uploadedIndexes, err := s.uploadRepo.GetUploadedChunksFromRedis(ctx, fileMD5, userID, totalChunks)
	if err != nil {
		log.Errorf("[MergeChunks] 合并分片失败：从Redis检查分片完整性时出错, error: %v", err)
		return "", fmt.Errorf("failed to get uploaded chunks from redis: %w", err)
	}
	if len(uploadedIndexes) < totalChunks {
		log.Warnf("[MergeChunks] 拒绝合并请求：分片未完全上传。文件MD5: %s, 期望分片数: %d, 实际分片数: %d", fileMD5, totalChunks, len(uploadedIndexes))
		return "", fmt.Errorf("分片未全部上传，无法合并 (期望: %d, 实际: %d)", totalChunks, len(uploadedIndexes))
	}

	// 2. 根据分片数量选择合并策略
	destObjectName := fmt.Sprintf("merged/%s", fileName) // 与 Java 一致的路径

	if totalChunks == 1 {
		// 对于单分片文件，使用 CopyObject
		src := minio.CopySrcOptions{
			Bucket: s.minioCfg.BucketName,
			Object: fmt.Sprintf("chunks/%s/0", fileMD5),
		}
		dst := minio.CopyDestOptions{
			Bucket: s.minioCfg.BucketName,
			Object: destObjectName,
		}
		_, err = storage.MinioClient.CopyObject(context.Background(), dst, src)
		if err != nil {
			log.Errorf("[MergeChunks] 单分片文件复制失败, error: %v", err)
			return "", fmt.Errorf("failed to copy single chunk object: %w", err)
		}
		log.Infof("[MergeChunks] 单分片文件复制成功。")
	} else {
		// 对于多分片文件，使用 ComposeObject
		// 通过代码直接构建源对象路径，而不是从数据库读取
		var srcs []minio.CopySrcOptions
		for i := 0; i < totalChunks; i++ {
			srcs = append(srcs, minio.CopySrcOptions{
				Bucket: s.minioCfg.BucketName,
				Object: fmt.Sprintf("chunks/%s/%d", fileMD5, i),
			})
		}

		dst := minio.CopyDestOptions{
			Bucket: s.minioCfg.BucketName,
			Object: destObjectName,
		}
		_, err = storage.MinioClient.ComposeObject(context.Background(), dst, srcs...)
		if err != nil {
			log.Errorf("[MergeChunks] 多分片文件合并失败, error: %v", err)
			return "", err
		}
		log.Infof("[MergeChunks] 多分片文件合并成功。")
	}

	// 3. 更新数据库记录状态
	if err := s.uploadRepo.UpdateFileUploadStatus(record.ID, 1); err != nil {
		log.Errorf("[MergeChunks] 更新数据库文件状态为“已完成”失败, error: %v", err)
		return "", err
	}
	log.Infof("[MergeChunks] 数据库文件状态已更新为“已完成”。文件ID: %d", record.ID)

	// 4. 触发 Kafka 消息
	objectURL, _ := storage.GetPresignedURL(s.minioCfg.BucketName, destObjectName, 60*60)
	task := tasks.FileProcessingTask{
		FileMD5:   fileMD5,
		ObjectUrl: objectURL,
		FileName:  fileName,
		UserID:    userID,
		OrgTag:    record.OrgTag,
		IsPublic:  record.IsPublic,
	}
	if err := kafka.ProduceFileTask(task); err != nil {
		log.Errorf("[MergeChunks] 发送文件处理任务到Kafka失败, error: %v", err)
	} else {
		log.Infof("[MergeChunks] 文件处理任务已成功发送到Kafka。")
	}

	// 5. 清理 Redis 和 MinIO 中的分片
	go func() {
		bgCtx := context.Background()
		log.Infof("[MergeChunks] 启动后台清理任务。文件MD5: %s", fileMD5)
		if err := s.uploadRepo.DeleteUploadMark(bgCtx, fileMD5, userID); err != nil {
			log.Warnf("[MergeChunks] 后台清理任务：删除Redis上传标记失败, fileMD5: %s, error: %v", fileMD5, err)
		}

		objectsCh := make(chan minio.ObjectInfo)
		go func() {
			defer close(objectsCh)
			for i := 0; i < totalChunks; i++ {
				objectsCh <- minio.ObjectInfo{Key: fmt.Sprintf("chunks/%s/%d", fileMD5, i)}
			}
		}()
		// Note: This is a fire-and-forget cleanup. In a production system,
		// you might want a more robust mechanism to handle cleanup failures.
		for range storage.MinioClient.RemoveObjects(bgCtx, s.minioCfg.BucketName, objectsCh, minio.RemoveObjectsOptions{}) {
			// We can log errors here if needed, but we don't block the main flow.
		}
		log.Infof("[MergeChunks] 后台清理任务完成。文件MD5: %s", fileMD5)
	}()

	return objectURL, nil
}

// GetUploadStatus 获取文件的上传状态。
func (s *uploadService) GetUploadStatus(ctx context.Context, fileMD5 string, userID uint) (string, string, []int, int, error) {
	log.Infof("[GetUploadStatus] 开始获取文件上传状态。文件MD5: %s", fileMD5)
	record, err := s.uploadRepo.GetFileUploadRecord(fileMD5, userID)
	if err != nil {
		log.Errorf("[GetUploadStatus] 获取文件上传状态失败：查询文件记录时出错, error: %v", err)
		return "", "", nil, 0, err
	}

	totalChunks := s.calculateTotalChunks(record.TotalSize)
	uploadedIndexes, err := s.uploadRepo.GetUploadedChunksFromRedis(ctx, fileMD5, userID, totalChunks)
	if err != nil {
		log.Errorf("[GetUploadStatus] 获取文件上传状态失败：从Redis获取已上传分片列表时出错, error: %v", err)
		return "", "", nil, 0, err
	}

	fileType := getFileType(record.FileName)
	log.Infof("[GetUploadStatus] 成功获取文件上传状态。文件MD5: %s", fileMD5)
	return record.FileName, fileType, uploadedIndexes, totalChunks, nil
}

// GetSupportedFileTypes 返回系统支持的文件类型。
func (s *uploadService) GetSupportedFileTypes() (map[string]interface{}, error) {
	log.Info("[GetSupportedFileTypes] 开始获取系统支持的文件类型")
	// 在 Go 中，这些通常是硬编码的，因为它们与编译后的代码能力相关。
	typeMapping := map[string]string{
		".pdf":  "PDF文档",
		".doc":  "Word文档",
		".docx": "Word文档",
		".xls":  "Excel表格",
		".xlsx": "Excel表格",
		".ppt":  "PowerPoint演示文稿",
		".pptx": "PowerPoint演示文稿",
		".txt":  "文本文件",
		".md":   "Markdown文档",
	}

	supportedExtensions := make([]string, 0, len(typeMapping))
	supportedTypes := make([]string, 0, len(typeMapping))
	// Use a map to handle unique types like "Word文档"
	uniqueTypes := make(map[string]struct{})

	for ext, t := range typeMapping {
		supportedExtensions = append(supportedExtensions, ext)
		if _, exists := uniqueTypes[t]; !exists {
			uniqueTypes[t] = struct{}{}
			supportedTypes = append(supportedTypes, t)
		}
	}

	description := "系统支持的文档类型文件，这些文件可以被解析并进行向量化处理"

	data := map[string]interface{}{
		"supportedExtensions": supportedExtensions,
		"supportedTypes":      supportedTypes,
		"description":         description,
	}
	log.Info("[GetSupportedFileTypes] 成功获取系统支持的文件类型。")
	return data, nil
}

// FastUpload provides a dedicated check for fast upload.
func (s *uploadService) FastUpload(ctx context.Context, fileMD5 string, userID uint) (bool, error) {
	log.Infof("[FastUpload] 开始秒传（快速上传）检查。文件MD5: %s", fileMD5)
	record, err := s.uploadRepo.GetFileUploadRecord(fileMD5, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			log.Info("[FastUpload] 秒传检查：文件记录不存在，无法秒传。")
			return false, nil
		}
		log.Errorf("[FastUpload] 秒传检查失败：查询数据库时出错, error: %v", err)
		return false, err
	}
	log.Infof("[FastUpload] 秒传检查：文件记录已存在，状态为 %d。", record.Status)
	return record.Status == 1, nil
}

// calculateTotalChunks 根据文件总大小和默认分片大小计算总分片数。
func (s *uploadService) calculateTotalChunks(totalSize int64) int {
	if totalSize == 0 {
		return 0
	}
	return int(math.Ceil(float64(totalSize) / float64(DefaultChunkSize)))
}

// getFileType 根据文件名推断文件类型描述 (private helper)
func getFileType(fileName string) string {
	if fileName == "" {
		return "未知类型"
	}
	parts := strings.Split(fileName, ".")
	if len(parts) < 2 {
		return "未知类型"
	}
	ext := "." + strings.ToLower(parts[len(parts)-1])

	typeMapping := map[string]string{
		".pdf":  "PDF文档",
		".doc":  "Word文档",
		".docx": "Word文档",
		".xls":  "Excel表格",
		".xlsx": "Excel表格",
		".ppt":  "PowerPoint演示文稿",
		".pptx": "PowerPoint演示文稿",
		".txt":  "文本文件",
		".md":   "Markdown文档",
	}
	if t, ok := typeMapping[ext]; ok {
		return t
	}
	return strings.ToUpper(ext[1:]) + "文件"
}
