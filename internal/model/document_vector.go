package model

// DocumentVector 对应于数据库中的 document_vectors 表。
// 它的结构与 Java 项目中的 DocumentVector 实体完全一致。
type DocumentVector struct {
	VectorID     uint   `gorm:"primaryKey;autoIncrement;column:vector_id"`
	FileMD5      string `gorm:"type:varchar(32);not null;index;column:file_md5"`
	ChunkID      int    `gorm:"not null;column:chunk_id"`
	TextContent  string `gorm:"type:text;column:text_content"`
	ModelVersion string `gorm:"type:varchar(50);column:model_version"`
	UserID       uint   `gorm:"not null;column:user_id"`
	OrgTag       string `gorm:"type:varchar(50);column:org_tag"`
	IsPublic     bool   `gorm:"not null;default:false;column:is_public"`
}

func (DocumentVector) TableName() string {
	return "document_vectors"
}
