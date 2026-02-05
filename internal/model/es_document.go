// Package model 定义了与数据库表对应的 Go 结构体。
package model

// SearchResponseDTO 定义了返回给前端的搜索结果结构。
type SearchResponseDTO struct {
	FileMD5     string  `json:"fileMd5"`
	FileName    string  `json:"fileName"` // 新增：原始文件名
	ChunkID     int     `json:"chunkId"`
	TextContent string  `json:"textContent"`
	Score       float64 `json:"score"` // 新增：搜索得分
	UserID      string  `json:"userId"`
	OrgTag      string  `json:"orgTag"`
	IsPublic    bool    `json:"isPublic"`
}

// EsDocument 代表存储在 Elasticsearch 中的文档结构。
// EsDocument 定义了存储在 Elasticsearch 中的文档结构。
type EsDocument struct {
	VectorID     string    `json:"vector_id"` // 唯一标识，例如 fileMd5 + chunkId
	FileMD5      string    `json:"file_md5"`
	ChunkID      int       `json:"chunk_id"`
	TextContent  string    `json:"text_content"`
	Vector       []float32 `json:"vector"` // 文本内容的向量表示
	ModelVersion string    `json:"model_version"`
	UserID       uint      `json:"user_id"`
	OrgTag       string    `json:"org_tag"`
	IsPublic     bool      `json:"is_public"`
}
