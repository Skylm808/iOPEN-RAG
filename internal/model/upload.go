// Package model 定义了与数据库表对应的 Go 结构体。
package model

import "time"

// FileUpload 定义了 file_upload 表的 ORM 模型。
// 它记录了每个上传文件的元数据和状态。
type FileUpload struct {
	ID        uint       `gorm:"primaryKey;autoIncrement" json:"id"`
	FileMD5   string     `gorm:"type:varchar(32);not null" json:"fileMd5"`
	FileName  string     `gorm:"type:varchar(255);not null" json:"fileName"`
	TotalSize int64      `gorm:"not null" json:"totalSize"`
	Status    int        `gorm:"type:tinyint;not null;default:0" json:"status"` // 0: uploading, 1: completed, 2: failed
	UserID    uint       `gorm:"not null" json:"userId"`
	OrgTag    string     `gorm:"type:varchar(50)" json:"orgTag"`
	IsPublic  bool       `gorm:"not null;default:false" json:"isPublic"`
	CreatedAt time.Time  `gorm:"autoCreateTime" json:"createdAt"`
	MergedAt  *time.Time `gorm:"default:null" json:"mergedAt"`
}

// TableName 指定了此模型在数据库中对应的表名。
func (FileUpload) TableName() string {
	return "file_upload"
}

// ChunkInfo 对应于数据库中的 'chunk_info' 表。
// 它记录了每个文件分块的详细信息。
type ChunkInfo struct {
	ID          uint   `gorm:"primaryKey;autoIncrement" json:"id"`
	FileMD5     string `gorm:"type:varchar(32);not null" json:"fileMd5"`
	ChunkIndex  int    `gorm:"not null" json:"chunkIndex"`
	ChunkMD5    string `gorm:"type:varchar(32);not null" json:"chunkMd5"`
	StoragePath string `gorm:"type:varchar(255);not null" json:"storagePath"`
}

// TableName 指定了此模型在数据库中对应的表名。
func (ChunkInfo) TableName() string {
	return "chunk_info"
}
