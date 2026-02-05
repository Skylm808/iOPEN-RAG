// Package repository 定义了与数据库进行数据交换的接口和实现。
package repository

import (
	"context"
	"github.com/go-redis/redis/v8"
	"gorm.io/gorm"
	"pai-smart-go/internal/model"
	"strconv"
)

// UploadRepository 接口定义了文件上传相关的数据持久化操作。
type UploadRepository interface {
	// FileUpload operations
	CreateFileUploadRecord(record *model.FileUpload) error
	GetFileUploadRecord(fileMD5 string, userID uint) (*model.FileUpload, error)
	UpdateFileUploadStatus(recordID uint, status int) error
	FindFilesByUserID(userID uint) ([]model.FileUpload, error)
	FindAccessibleFiles(userID uint, orgTags []string) ([]model.FileUpload, error)
	DeleteFileUploadRecord(fileMD5 string, userID uint) error
	UpdateFileUploadRecord(record *model.FileUpload) error
	FindBatchByMD5s(md5s []string) ([]*model.FileUpload, error)

	// ChunkInfo operations (GORM)
	CreateChunkInfoRecord(record *model.ChunkInfo) error
	GetChunkInfoRecords(fileMD5 string) ([]model.ChunkInfo, error)

	// Chunk status operations (Redis)
	IsChunkUploaded(ctx context.Context, fileMD5 string, userID uint, chunkIndex int) (bool, error)
	MarkChunkUploaded(ctx context.Context, fileMD5 string, userID uint, chunkIndex int) error
	GetUploadedChunksFromRedis(ctx context.Context, fileMD5 string, userID uint, totalChunks int) ([]int, error)
	DeleteUploadMark(ctx context.Context, fileMD5 string, userID uint) error
}

// uploadRepository 是 UploadRepository 接口的 GORM+Redis 实现。
type uploadRepository struct {
	db          *gorm.DB
	redisClient *redis.Client
}

// NewUploadRepository 创建一个新的 UploadRepository 实例。
func NewUploadRepository(db *gorm.DB, redisClient *redis.Client) UploadRepository {
	return &uploadRepository{db: db, redisClient: redisClient}
}

// getRedisUploadKey generates the redis key for upload status.
func (r *uploadRepository) getRedisUploadKey(fileMD5 string, userID uint) string {
	return "upload:" + strconv.FormatUint(uint64(userID), 10) + ":" + fileMD5
}

// CreateFileUploadRecord 在数据库中创建一个新的文件上传总记录。
func (r *uploadRepository) CreateFileUploadRecord(record *model.FileUpload) error {
	return r.db.Create(record).Error
}

// GetFileUploadRecord 根据文件 MD5 和用户 ID 检索文件上传记录。
func (r *uploadRepository) GetFileUploadRecord(fileMD5 string, userID uint) (*model.FileUpload, error) {
	var record model.FileUpload
	err := r.db.Where("file_md5 = ? AND user_id = ?", fileMD5, userID).First(&record).Error
	if err != nil {
		return nil, err
	}
	return &record, nil
}

// FindBatchByMD5s finds file upload records by a slice of MD5s.
func (r *uploadRepository) FindBatchByMD5s(md5s []string) ([]*model.FileUpload, error) {
	var records []*model.FileUpload
	if len(md5s) == 0 {
		return records, nil
	}
	err := r.db.Where("file_md5 IN ?", md5s).Find(&records).Error
	return records, err
}

// UpdateFileUploadStatus 更新指定文件上传记录的状态。
func (r *uploadRepository) UpdateFileUploadStatus(recordID uint, status int) error {
	return r.db.Model(&model.FileUpload{}).Where("id = ?", recordID).Update("status", status).Error
}

// GetChunkInfoRecords 获取指定文件已上传的所有分块信息 (from DB, used for merge)。
func (r *uploadRepository) GetChunkInfoRecords(fileMD5 string) ([]model.ChunkInfo, error) {
	var chunks []model.ChunkInfo
	err := r.db.Where("file_md5 = ?", fileMD5).Order("chunk_index asc").Find(&chunks).Error
	return chunks, err
}

// FindFilesByUserID 查找指定用户上传的所有文件。
func (r *uploadRepository) FindFilesByUserID(userID uint) ([]model.FileUpload, error) {
	var files []model.FileUpload
	err := r.db.Where("user_id = ?", userID).Find(&files).Error
	return files, err
}

// FindAccessibleFiles 查找用户可访问的所有文件。
// 包括：用户自己的文件；任意 is_public=true 的文件（全局可见）；以及用户所属组织内的公开文件。
func (r *uploadRepository) FindAccessibleFiles(userID uint, orgTags []string) ([]model.FileUpload, error) {
	var files []model.FileUpload
	// 查询条件：status=1 AND (user_id=? OR is_public=true OR (org_tag IN ? AND is_public=true))
	err := r.db.Where("status = ?", 1).
		Where(r.db.Where("user_id = ?", userID).
			Or("is_public = ?", true).
			Or("org_tag IN ? AND is_public = ?", orgTags, true)).
		Find(&files).Error
	return files, err
}

// DeleteFileUploadRecord 删除一个文件上传记录。
func (r *uploadRepository) DeleteFileUploadRecord(fileMD5 string, userID uint) error {
	return r.db.Where("file_md5 = ? AND user_id = ?", fileMD5, userID).Delete(&model.FileUpload{}).Error
}

// UpdateFileUploadRecord 更新一个文件上传记录。
func (r *uploadRepository) UpdateFileUploadRecord(record *model.FileUpload) error {
	return r.db.Save(record).Error
}

// CreateChunkInfoRecord 在数据库中创建一个新的文件分块记录。
func (r *uploadRepository) CreateChunkInfoRecord(record *model.ChunkInfo) error {
	return r.db.Create(record).Error
}

// IsChunkUploaded checks if a chunk is marked as uploaded in Redis.
func (r *uploadRepository) IsChunkUploaded(ctx context.Context, fileMD5 string, userID uint, chunkIndex int) (bool, error) {
	key := r.getRedisUploadKey(fileMD5, userID)
	val, err := r.redisClient.GetBit(ctx, key, int64(chunkIndex)).Result()
	if err != nil {
		// If the key doesn't exist, Redis doesn't return an error, but a value of 0.
		// So we only need to handle actual errors.
		return false, err
	}
	return val == 1, nil
}

// MarkChunkUploaded marks a chunk as uploaded in Redis.
func (r *uploadRepository) MarkChunkUploaded(ctx context.Context, fileMD5 string, userID uint, chunkIndex int) error {
	key := r.getRedisUploadKey(fileMD5, userID)
	return r.redisClient.SetBit(ctx, key, int64(chunkIndex), 1).Err()
}

// GetUploadedChunksFromRedis retrieves the list of uploaded chunk indexes from Redis bitmap.
func (r *uploadRepository) GetUploadedChunksFromRedis(ctx context.Context, fileMD5 string, userID uint, totalChunks int) ([]int, error) {
	if totalChunks == 0 {
		return []int{}, nil
	}
	key := r.getRedisUploadKey(fileMD5, userID)
	bitmap, err := r.redisClient.Get(ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			return []int{}, nil // Key doesn't exist, no chunks uploaded
		}
		return nil, err
	}

	uploaded := make([]int, 0)
	for i := 0; i < totalChunks; i++ {
		byteIndex := i / 8
		bitIndex := i % 8
		if byteIndex < len(bitmap) && (bitmap[byteIndex]>>(7-bitIndex))&1 == 1 {
			uploaded = append(uploaded, i)
		}
	}
	return uploaded, nil
}

// DeleteUploadMark deletes the upload status key from Redis.
func (r *uploadRepository) DeleteUploadMark(ctx context.Context, fileMD5 string, userID uint) error {
	key := r.getRedisUploadKey(fileMD5, userID)
	return r.redisClient.Del(ctx, key).Err()
}
