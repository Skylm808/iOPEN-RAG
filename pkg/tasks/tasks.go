// Package tasks defines the structure for tasks that are sent to Kafka.
package tasks

// FileProcessingTask represents the data structure for a file processing job.
type FileProcessingTask struct {
	FileMD5   string `json:"file_md5"`
	ObjectUrl string `json:"object_url"`
	FileName  string `json:"file_name"`
	UserID    uint   `json:"user_id"`
	OrgTag    string `json:"org_tag"`
	IsPublic  bool   `json:"is_public"`
}
