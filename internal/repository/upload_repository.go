package repository

import (
	"context"
	"pai-smart-go/internal/model"
	"strconv"

	"github.com/go-redis/redis/v8"
	"gorm.io/gorm"
)

type UploadRepository interface {
	CreateFileUploadRecord(record *model.FileUpload) error
	GetFileUploadRecord(fileMD5 string, userID uint) (*model.FileUpload, error)
	GetFileUploadRecordByMD5(fileMD5 string) (*model.FileUpload, error)
	UpdateFileUploadStatus(recordID uint, status int) error
	FindFilesByUserID(userID uint) ([]model.FileUpload, error)
	FindAccessibleFiles(userID uint, orgTags []string) ([]model.FileUpload, error)
	DeleteFileUploadRecord(fileMD5 string, userID uint) error
	UpdateFileUploadRecord(record *model.FileUpload) error
	FindBatchByMD5s(md5s []string) ([]*model.FileUpload, error)

	CreateChunkInfoRecord(record *model.ChunkInfo) error
	GetChunkInfoRecord(fileMD5 string, chunkIndex int) (*model.ChunkInfo, error)
	GetChunkInfoRecords(fileMD5 string) ([]model.ChunkInfo, error)

	IsChunkUploaded(ctx context.Context, fileMD5 string, userID uint, chunkIndex int) (bool, error)
	MarkChunkUploaded(ctx context.Context, fileMD5 string, userID uint, chunkIndex int) error
	GetUploadedChunksFromRedis(ctx context.Context, fileMD5 string, userID uint, totalChunks int) ([]int, error)
	DeleteUploadMark(ctx context.Context, fileMD5 string, userID uint) error
}

type uploadRepository struct {
	db          *gorm.DB
	redisClient *redis.Client
}

func NewUploadRepository(db *gorm.DB, redisClient *redis.Client) UploadRepository {
	return &uploadRepository{db: db, redisClient: redisClient}
}

func (r *uploadRepository) getRedisUploadKey(fileMD5 string, userID uint) string {
	return "upload:" + strconv.FormatUint(uint64(userID), 10) + ":" + fileMD5
}

func (r *uploadRepository) CreateFileUploadRecord(record *model.FileUpload) error {
	return r.db.Create(record).Error
}

func (r *uploadRepository) GetFileUploadRecord(fileMD5 string, userID uint) (*model.FileUpload, error) {
	var record model.FileUpload
	err := r.db.Where("file_md5 = ? AND user_id = ?", fileMD5, userID).First(&record).Error
	if err != nil {
		return nil, err
	}
	return &record, nil
}

func (r *uploadRepository) GetFileUploadRecordByMD5(fileMD5 string) (*model.FileUpload, error) {
	var record model.FileUpload
	err := r.db.Where("file_md5 = ?", fileMD5).First(&record).Error
	if err != nil {
		return nil, err
	}
	return &record, nil
}

func (r *uploadRepository) FindBatchByMD5s(md5s []string) ([]*model.FileUpload, error) {
	var records []*model.FileUpload
	if len(md5s) == 0 {
		return records, nil
	}
	err := r.db.Where("file_md5 IN ?", md5s).Find(&records).Error
	return records, err
}

func (r *uploadRepository) UpdateFileUploadStatus(recordID uint, status int) error {
	return r.db.Model(&model.FileUpload{}).Where("id = ?", recordID).Update("status", status).Error
}

func (r *uploadRepository) GetChunkInfoRecord(fileMD5 string, chunkIndex int) (*model.ChunkInfo, error) {
	var chunk model.ChunkInfo
	err := r.db.Where("file_md5 = ? AND chunk_index = ?", fileMD5, chunkIndex).First(&chunk).Error
	if err != nil {
		return nil, err
	}
	return &chunk, nil
}

func (r *uploadRepository) GetChunkInfoRecords(fileMD5 string) ([]model.ChunkInfo, error) {
	var chunks []model.ChunkInfo
	err := r.db.Where("file_md5 = ?", fileMD5).Order("chunk_index asc").Find(&chunks).Error
	return chunks, err
}

func (r *uploadRepository) FindFilesByUserID(userID uint) ([]model.FileUpload, error) {
	var files []model.FileUpload
	err := r.db.Where("user_id = ?", userID).Find(&files).Error
	return files, err
}

func (r *uploadRepository) FindAccessibleFiles(userID uint, orgTags []string) ([]model.FileUpload, error) {
	var files []model.FileUpload
	err := r.db.Where("status = ?", 1).
		Where(r.db.Where("user_id = ?", userID).
			Or("is_public = ?", true).
			Or("org_tag IN ?", orgTags)).
		Find(&files).Error
	return files, err
}

func (r *uploadRepository) DeleteFileUploadRecord(fileMD5 string, userID uint) error {
	return r.db.Where("file_md5 = ? AND user_id = ?", fileMD5, userID).Delete(&model.FileUpload{}).Error
}

func (r *uploadRepository) UpdateFileUploadRecord(record *model.FileUpload) error {
	return r.db.Save(record).Error
}

func (r *uploadRepository) CreateChunkInfoRecord(record *model.ChunkInfo) error {
	return r.db.Create(record).Error
}

func (r *uploadRepository) IsChunkUploaded(ctx context.Context, fileMD5 string, userID uint, chunkIndex int) (bool, error) {
	key := r.getRedisUploadKey(fileMD5, userID)
	val, err := r.redisClient.GetBit(ctx, key, int64(chunkIndex)).Result()
	if err != nil {
		return false, err
	}
	return val == 1, nil
}

func (r *uploadRepository) MarkChunkUploaded(ctx context.Context, fileMD5 string, userID uint, chunkIndex int) error {
	key := r.getRedisUploadKey(fileMD5, userID)
	return r.redisClient.SetBit(ctx, key, int64(chunkIndex), 1).Err()
}

func (r *uploadRepository) GetUploadedChunksFromRedis(ctx context.Context, fileMD5 string, userID uint, totalChunks int) ([]int, error) {
	if totalChunks == 0 {
		return []int{}, nil
	}

	key := r.getRedisUploadKey(fileMD5, userID)
	bitmap, err := r.redisClient.Get(ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			return []int{}, nil
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

func (r *uploadRepository) DeleteUploadMark(ctx context.Context, fileMD5 string, userID uint) error {
	key := r.getRedisUploadKey(fileMD5, userID)
	return r.redisClient.Del(ctx, key).Err()
}
