package repository

import (
	"gorm.io/gorm"
	"pai-smart-go/internal/model"
)

// DocumentVectorRepository 定义了对 document_vectors 表的数据操作接口。
type DocumentVectorRepository interface {
	BatchCreate(vectors []*model.DocumentVector) error
	FindByFileMD5(fileMD5 string) ([]*model.DocumentVector, error)
	DeleteByFileMD5(fileMD5 string) error
}

type documentVectorRepository struct {
	db *gorm.DB
}

// NewDocumentVectorRepository 创建一个新的 DocumentVectorRepository 实例。
func NewDocumentVectorRepository(db *gorm.DB) DocumentVectorRepository {
	return &documentVectorRepository{db: db}
}

// BatchCreate 批量创建文档向量记录。
func (r *documentVectorRepository) BatchCreate(vectors []*model.DocumentVector) error {
	if len(vectors) == 0 {
		return nil
	}
	return r.db.CreateInBatches(vectors, 100).Error // 每100条记录一批
}

// FindByFileMD5 根据文件MD5查找所有相关的文档向量记录。
func (r *documentVectorRepository) FindByFileMD5(fileMD5 string) ([]*model.DocumentVector, error) {
	var vectors []*model.DocumentVector
	err := r.db.Where("file_md5 = ?", fileMD5).Find(&vectors).Error
	return vectors, err
}

// DeleteByFileMD5 根据文件MD5删除所有相关的文档向量记录。
func (r *documentVectorRepository) DeleteByFileMD5(fileMD5 string) error {
	return r.db.Where("file_md5 = ?", fileMD5).Delete(&model.DocumentVector{}).Error
}
