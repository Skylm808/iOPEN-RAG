// Package repository 包含了所有与数据库交互的逻辑。
package repository

import (
	"gorm.io/gorm"
	"pai-smart-go/internal/model"
)

// OrgTagRepository 接口定义了组织标签的数据操作方法。
type OrgTagRepository interface {
	Create(tag *model.OrganizationTag) error
	FindByID(id string) (*model.OrganizationTag, error)
	FindAll() ([]model.OrganizationTag, error)
	FindBatchByIDs(ids []string) ([]model.OrganizationTag, error)
	Update(tag *model.OrganizationTag) error
	Delete(id string) error
}

type orgTagRepository struct {
	db *gorm.DB
}

// NewOrgTagRepository 创建一个新的 OrgTagRepository 实例。
func NewOrgTagRepository(db *gorm.DB) OrgTagRepository {
	return &orgTagRepository{db: db}
}

// Create 在数据库中插入一个新的组织标签记录。
func (r *orgTagRepository) Create(tag *model.OrganizationTag) error {
	return r.db.Create(tag).Error
}

// FindAll 从数据库中检索所有的组织标签记录。
func (r *orgTagRepository) FindAll() ([]model.OrganizationTag, error) {
	var tags []model.OrganizationTag
	err := r.db.Find(&tags).Error
	return tags, err
}

// FindBatchByIDs finds organization tags by a slice of IDs.
func (r *orgTagRepository) FindBatchByIDs(ids []string) ([]model.OrganizationTag, error) {
	var tags []model.OrganizationTag
	if len(ids) == 0 {
		return tags, nil
	}
	err := r.db.Where("tag_id IN ?", ids).Find(&tags).Error
	return tags, err
}

// FindByID 根据给定的 tagID 从数据库中查找一个组织标签。
func (r *orgTagRepository) FindByID(tagID string) (*model.OrganizationTag, error) {
	var tag model.OrganizationTag
	err := r.db.Where("tag_id = ?", tagID).First(&tag).Error
	if err != nil {
		return nil, err
	}
	return &tag, nil
}

// Update 更新数据库中一个已存在的组织标签记录。
func (r *orgTagRepository) Update(tag *model.OrganizationTag) error {
	return r.db.Save(tag).Error
}

// Delete 根据给定的 tagID 从数据库中删除一个组织标签记录。
func (r *orgTagRepository) Delete(tagID string) error {
	return r.db.Delete(&model.OrganizationTag{}, "tag_id = ?", tagID).Error
}
