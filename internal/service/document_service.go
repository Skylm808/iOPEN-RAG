// Package service 包含了应用的业务逻辑层。
package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"pai-smart-go/internal/config"
	"pai-smart-go/internal/model"
	"pai-smart-go/internal/repository"
	"pai-smart-go/pkg/storage"
	"strings"
	"time"

	"pai-smart-go/pkg/tika"

	"github.com/minio/minio-go/v7"
)

// FileUploadDTO 是一个数据传输对象，用于在返回给前端时隐藏一些字段并添加额外信息。
type FileUploadDTO struct {
	model.FileUpload
	OrgTagName string `json:"orgTagName"`
}

// DownloadInfoDTO 封装了文件下载链接所需的信息。
type DownloadInfoDTO struct {
	FileName    string `json:"fileName"`
	DownloadURL string `json:"downloadUrl"`
	FileSize    int64  `json:"fileSize"`
}

// PreviewInfoDTO 封装了文件预览所需的信息。
type PreviewInfoDTO struct {
	FileName string `json:"fileName"`
	Content  string `json:"content"`
	FileSize int64  `json:"fileSize"`
}

// DocumentService 接口定义了文档管理相关的业务操作。
type DocumentService interface {
	ListAccessibleFiles(user *model.User) ([]model.FileUpload, error)
	ListUploadedFiles(userID uint) ([]FileUploadDTO, error)
	DeleteDocument(fileMD5 string, user *model.User) error
	GenerateDownloadURL(fileName string, user *model.User) (*DownloadInfoDTO, error)
	GetFilePreviewContent(fileName string, user *model.User) (*PreviewInfoDTO, error)
}

type documentService struct {
	uploadRepo repository.UploadRepository
	userRepo   repository.UserRepository
	orgTagRepo repository.OrgTagRepository // 新增依赖
	minioCfg   config.MinIOConfig
	tikaClient *tika.Client // 新增依赖
}

// NewDocumentService 创建一个新的 DocumentService 实例。
func NewDocumentService(uploadRepo repository.UploadRepository, userRepo repository.UserRepository, orgTagRepo repository.OrgTagRepository, minioCfg config.MinIOConfig, tikaClient *tika.Client) DocumentService {
	return &documentService{
		uploadRepo: uploadRepo,
		userRepo:   userRepo,
		orgTagRepo: orgTagRepo,
		minioCfg:   minioCfg,
		tikaClient: tikaClient,
	}
}

// ListAccessibleFiles 获取用户可访问的文件列表。
func (s *documentService) ListAccessibleFiles(user *model.User) ([]model.FileUpload, error) {
	orgTags := strings.Split(user.OrgTags, ",")
	return s.uploadRepo.FindAccessibleFiles(user.ID, orgTags)
}

// ListUploadedFiles 获取用户自己上传的文件列表，并附加组织标签名称。
func (s *documentService) ListUploadedFiles(userID uint) ([]FileUploadDTO, error) {
	files, err := s.uploadRepo.FindFilesByUserID(userID)
	if err != nil {
		return nil, err
	}

	dtos, err := s.mapFileUploadsToDTOs(files)
	if err != nil {
		return nil, err
	}

	return dtos, nil
}

// DeleteDocument 删除一个文档。
//
// ── 权限控制说明 ──────────────────────────────────────────────────────────
//
// 本方法支持两种删除场景：
//  1. 普通用户删除自己的文件
//  2. 管理员删除任意用户的文件
//
// ❌ 原实现的 Bug（管理员删除失效）：
//
//	record, err := s.uploadRepo.GetFileUploadRecord(fileMD5, user.ID)
//	// 问题：强制使用当前用户 ID 查询
//	// 结果：管理员永远查不到别人的文件，导致删除失败
//
//	if record.UserID != user.ID && user.Role != "ADMIN" {
//	    // 这个判断永远不会生效，因为上面已经限制了 user.ID
//	}
//
// ✅ 修复方案：
//
//	根据用户角色选择不同的查询策略：
//	- 普通用户：使用 GetFileUploadRecord（限制 userID）
//	- 管理员：使用 GetFileUploadRecordByMD5（不限制 userID）
//
// 示例：
//
//	场景1：用户 alice 删除自己的文件 → 只能查到自己的文件 → 成功
//	场景2：用户 alice 删除 bob 的文件 → 查不到（userID 不匹配）→ 失败
//	场景3：管理员删除 bob 的文件 → 能查到任意文件 → 成功
func (s *documentService) DeleteDocument(fileMD5 string, user *model.User) error {
	var record *model.FileUpload
	var err error

	// 根据用户角色选择查询策略
	if user.Role == "ADMIN" {
		// 管理员：可以查询任意用户的文件
		record, err = s.uploadRepo.GetFileUploadRecordByMD5(fileMD5)
		if err != nil {
			return errors.New("文件不存在")
		}
		// 管理员无需额外权限检查，直接允许删除
	} else {
		// 普通用户：只能查询自己的文件
		record, err = s.uploadRepo.GetFileUploadRecord(fileMD5, user.ID)
		if err != nil {
			return errors.New("文件不存在或不属于该用户")
		}
		// 双重验证：确保文件确实属于当前用户
		if record.UserID != user.ID {
			return errors.New("没有权限删除此文件")
		}
	}

	// 从 MinIO 删除文件对象
	objectName := fmt.Sprintf("merged/%s", record.FileName)
	err = storage.MinioClient.RemoveObject(context.Background(), s.minioCfg.BucketName, objectName, minio.RemoveObjectOptions{})
	if err != nil {
		// 即使 MinIO 删除失败，也继续删除数据库记录
		// 避免数据库和对象存储不一致（可以通过定时任务清理孤立对象）
	}

	// 从数据库删除记录
	return s.uploadRepo.DeleteFileUploadRecord(fileMD5, record.UserID)
}

// GenerateDownloadURL 为用户生成文件的临时预签名下载链接。
//
// ── 预签名 URL 原理 ──────────────────────────────────────────────────────────
//
// MinIO（对象存储）中的文件默认私有，外部无法直接访问。
// 本方法使用 MinIO 私钥对下载请求提前签名，生成一个携带鉴权信息的临时 URL。
// 前端拿到该 URL 后，可直接向 MinIO 发起下载请求，文件流不再经过后端，
// 有效降低后端带宽和内存压力。
//
// ── 有效期设计 ───────────────────────────────────────────────────────────────
//
// 有效期设为 1 小时：
//   - 过短（如 10 秒）：前端拿到链接还未触发下载即已过期，体验差
//   - 过长（如 7 天）：URL 一旦泄漏，他人可在有效期内任意下载
//   - 1 小时是"够用且泄漏影响可控"的经验折中值
//
// ── 权限控制 ─────────────────────────────────────────────────────────────────
//
// 通过 ListAccessibleFiles 进行权限过滤，确保用户只能为自己有权访问的文件生成链接。
// 注意：当前实现以文件名作为查找键，假设文件名唯一，生产环境应改为以文件 MD5 或 ID 查找。
func (s *documentService) GenerateDownloadURL(fileName string, user *model.User) (*DownloadInfoDTO, error) {
	files, err := s.ListAccessibleFiles(user)
	if err != nil {
		return nil, err
	}

	var targetFile *model.FileUpload
	for i := range files {
		if files[i].FileName == fileName {
			targetFile = &files[i]
			break
		}
	}

	if targetFile == nil {
		return nil, errors.New("文件不存在或无权访问")
	}

	// 向 MinIO 申请预签名 URL，有效期 1 小时。
	// 前端凭此 URL 可直连 MinIO 下载，文件流不再经过后端服务。
	expiry := time.Hour
	objectName := fmt.Sprintf("uploads/%d/%s", targetFile.UserID, targetFile.FileName)
	presignedURL, err := storage.MinioClient.PresignedGetObject(context.Background(), s.minioCfg.BucketName, objectName, expiry, url.Values{})
	if err != nil {
		return nil, err
	}

	return &DownloadInfoDTO{
		FileName:    targetFile.FileName,
		DownloadURL: presignedURL.String(),
		FileSize:    targetFile.TotalSize,
	}, nil
}

// GetFilePreviewContent 获取文件的纯文本预览内容。
//
// 与 GenerateDownloadURL 不同，预览不生成对外链接，而是由后端直接从 MinIO 拉取文件流，
// 交由 Apache Tika 解析提取纯文本后，以字符串形式返回给前端。
// 适用于 PDF、Word、Excel 等富文本文件的内容快速预览，无需前端具备对应解析能力。
func (s *documentService) GetFilePreviewContent(fileName string, user *model.User) (*PreviewInfoDTO, error) {
	// 权限检查：只允许预览用户有权访问的文件（自有文件或同组织公开文件）
	files, err := s.ListAccessibleFiles(user)
	if err != nil {
		return nil, err
	}

	var targetFile *model.FileUpload
	for i := range files {
		if files[i].FileName == fileName {
			targetFile = &files[i]
			break
		}
	}

	if targetFile == nil {
		return nil, errors.New("文件不存在或无权访问")
	}

	// 从 MinIO 获取文件对象。
	// 注意：文件合并后统一存储在 merged/ 目录下，路径格式与 upload_service.go 和 processor.go 保持一致。
	// 错误写法：uploads/{userID}/{fileName}（该路径为分片暂存路径，合并后不存在）
	objectName := fmt.Sprintf("merged/%s", targetFile.FileName)
	object, err := storage.MinioClient.GetObject(context.Background(), s.minioCfg.BucketName, objectName, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer object.Close()

	// 将文件流发送给 Tika 进行文本提取
	content, err := s.tikaClient.ExtractText(object, fileName)
	if err != nil {
		return nil, err
	}

	return &PreviewInfoDTO{
		FileName: targetFile.FileName,
		Content:  content,
		FileSize: targetFile.TotalSize,
	}, nil
}

func (s *documentService) mapFileUploadsToDTOs(files []model.FileUpload) ([]FileUploadDTO, error) {
	if len(files) == 0 {
		return []FileUploadDTO{}, nil
	}

	// To avoid N+1 queries, get all unique org tag IDs first
	tagIDs := make(map[string]struct{})
	for _, file := range files {
		if file.OrgTag != "" {
			tagIDs[file.OrgTag] = struct{}{}
		}
	}

	tagIDList := make([]string, 0, len(tagIDs))
	for id := range tagIDs {
		tagIDList = append(tagIDList, id)
	}

	tags, err := s.orgTagRepo.FindBatchByIDs(tagIDList)
	if err != nil {
		return nil, err
	}

	tagMap := make(map[string]string)
	for _, tag := range tags {
		tagMap[tag.TagID] = tag.Name
	}

	dtos := make([]FileUploadDTO, len(files))
	for i, file := range files {
		dtos[i] = FileUploadDTO{
			FileUpload: file,
			OrgTagName: tagMap[file.OrgTag], // Will be empty string if not found
		}
	}

	return dtos, nil
}
