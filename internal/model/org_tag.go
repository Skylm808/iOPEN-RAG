// Package model 定义了与数据库表对应的 Go 结构体。
package model

import "time"

// OrganizationTag 对应于数据库中的 'organization_tags' 表。
// 它用于定义组织结构或为用户和文档分组。
type OrganizationTag struct {
	// TagID 是组织标签的唯一标识符，作为主键。
	TagID string `gorm:"type:varchar(255);primaryKey" json:"tagId"`
	// Name 是组织标签的显示名称。
	Name string `gorm:"type:varchar(100);not null" json:"name"`
	// Description 提供了对该组织标签更详细的描述。
	Description string `gorm:"type:text" json:"description"`
	// ParentTag 指向父级标签的 TagID，用于构建层级结构。使用指针以接受 NULL 值，表示顶级标签。
	ParentTag *string `gorm:"type:varchar(255)" json:"parentTag"`
	// CreatedBy 记录了创建此标签的用户的 ID。
	CreatedBy uint `gorm:"not null" json:"createdBy"`
	// CreatedAt 由 GORM 自动管理，记录创建时间。
	CreatedAt time.Time `gorm:"autoCreateTime" json:"createdAt"`
	// UpdatedAt 由 GORM 自动管理，记录最后更新时间。
	UpdatedAt time.Time `gorm:"autoUpdateTime" json:"updatedAt"`
}

// OrganizationTagNode represents a node in the organization tag tree.
type OrganizationTagNode struct {
	TagID       string                 `json:"tagId"`
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	ParentTag   *string                `json:"parentTag"`
	Children    []*OrganizationTagNode `json:"children"`
}

// TableName 指定了此模型在数据库中对应的表名。
func (OrganizationTag) TableName() string {
	return "organization_tags"
}
