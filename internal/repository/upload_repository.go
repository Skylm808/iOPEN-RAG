// Package repository 定义了与数据库进行数据交换的接口和实现。
package repository

import (
	"context"
	"pai-smart-go/internal/model"
	"strconv"

	"github.com/go-redis/redis/v8"
	"gorm.io/gorm"
)

// UploadRepository 接口定义了文件上传相关的数据持久化操作。
type UploadRepository interface {
	// FileUpload operations
	CreateFileUploadRecord(record *model.FileUpload) error
	GetFileUploadRecord(fileMD5 string, userID uint) (*model.FileUpload, error)
	GetFileUploadRecordByMD5(fileMD5 string) (*model.FileUpload, error) // 新增：不限制用户ID，用于管理员操作
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
// 用于普通用户操作，限制只能查询自己的文件。
func (r *uploadRepository) GetFileUploadRecord(fileMD5 string, userID uint) (*model.FileUpload, error) {
	var record model.FileUpload
	err := r.db.Where("file_md5 = ? AND user_id = ?", fileMD5, userID).First(&record).Error
	if err != nil {
		return nil, err
	}
	return &record, nil
}

// GetFileUploadRecordByMD5 根据文件 MD5 检索文件上传记录（不限制用户ID）。
//
// ── 使用场景 ──────────────────────────────────────────────────────────────
//
// 本方法专门用于管理员操作，允许查询任意用户的文件记录。
// 普通用户操作应使用 GetFileUploadRecord（带 userID 限制）。
//
// 典型场景：
//   - 管理员删除任意用户的文件
//   - 管理员查看文件详情进行审核
//   - 系统后台任务处理文件
//
// ⚠️ 安全提示：
//   调用此方法前，必须在 Service 层验证操作者是否有管理员权限。
//   否则会导致越权访问漏洞。
func (r *uploadRepository) GetFileUploadRecordByMD5(fileMD5 string) (*model.FileUpload, error) {
	var record model.FileUpload
	err := r.db.Where("file_md5 = ?", fileMD5).First(&record).Error
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

// FindAccessibleFiles 查找用户可访问的所有文件（三层权限模型）。
//
// ── 权限模型说明 ────────────────────────────────────────────────
//
// 本系统采用三层权限模型，通过 org_tag 和 is_public 两个字段共同控制文档可见性：
//
// 1️⃣ 私有文档（Private Documents）
//   - 特征：org_tag = "PRIVATE_username"（用户注册时自动创建）
//   - 可见性：只有上传者本人可见
//   - 实现原理：只有该用户的 OrgTags 字段包含 "PRIVATE_username"，
//     所以通过 "org_tag IN ?" 条件，只有本人能匹配到
//   - 示例：用户 alice 上传私人笔记，org_tag = "PRIVATE_alice"
//
// 2️⃣ 组织文档（Organization Documents）
//   - 特征：org_tag = "org:xxx"（如 "org:tech", "org:tech:backend"）
//   - 可见性：所有 OrgTags 包含该标签或其父标签的用户可见
//   - 实现原理：通过 "org_tag IN ?" 条件，匹配用户的有效组织标签列表
//     （已在 user_service.GetUserEffectiveOrgTags 中展开父标签）
//   - 示例：技术部文档 org_tag = "org:tech"，技术部及其子部门成员都能看到
//
// 3️⃣ 公开文档（Public Documents）
//   - 特征：is_public = true（不管 org_tag 是什么）
//   - 可见性：所有认证用户可见
//   - 实现原理：通过 "is_public = true" 条件，全局匹配
//   - 示例：公司公告、公开知识库文章
//
// ── 查询逻辑 ──────────────────────────────────────────────────────────────
//
// SQL 等价逻辑：
//
//	SELECT * FROM file_upload
//	WHERE status = 1  -- 只查已完成上传的文件
//	AND (
//	    user_id = ?           -- 条件1：自己上传的文件（包括私有文档）
//	    OR is_public = true   -- 条件2：全局公开的文件
//	    OR org_tag IN (?)     -- 条件3：用户所属组织的文件（包括私有标签）
//	)
//
// 三个条件是 OR 关系，满足任意一个即可访问。
//
// ── 为什么条件3不需要 "AND is_public = true"？ ────────────────────────────
//
// ❌ 错误理解：组织文档需要设置 is_public = true 才能被组织成员看到
// ✅ 正确理解：组织文档通过 org_tag 控制可见范围，is_public 只控制全局可见
//
// 如果加上 "AND is_public = true"，会导致：
//   - 用户上传文档到组织（org_tag = "org:tech"），但不勾选公开（is_public = false）
//   - 结果：组织成员无法看到该文档（违反了"组织文档"的定义）
//   - 只有上传者通过条件1（user_id）才能看到，失去了组织共享的意义
//
// ── 实际使用场景示例 ──────────────────────────────────────────────────────
//
// 场景1：用户 alice 上传私人日记
//   - org_tag = "PRIVATE_alice", is_public = false
//   - 结果：只有 alice 能看到（通过条件1或条件3，因为只有她的 OrgTags 包含 "PRIVATE_alice"）
//
// 场景2：用户 bob（技术部）上传部门文档
//   - org_tag = "org:tech", is_public = false
//   - 结果：技术部所有成员能看到（通过条件3），其他部门看不到
//
// 场景3：用户 charlie 上传公司公告
//   - org_tag = "org:company", is_public = true
//   - 结果：所有员工都能看到（通过条件2）
//
// ── 与搜索服务的一致性 ────────────────────────────────────────────────────
//
// 本方法的权限逻辑与 search_service.go 的 Elasticsearch 过滤条件完全一致：
//
//	"filter": {
//	  "bool": {
//	    "should": [
//	      {"term": {"user_id": user.ID}},           // 条件1
//	      {"term": {"is_public": true}},             // 条件2
//	      {"terms": {"org_tag": userEffectiveTags}}  // 条件3
//	    ],
//	    "minimum_should_match": 1
//	  }
//	}
//
// 这确保了"文档列表"和"搜索结果"的权限过滤行为一致，避免用户困惑。
//
// ── 参数说明 ──────────────────────────────────────────────────────────────
//
// @param userID   当前用户的 ID（用于条件1：匹配自己上传的文件）
// @param orgTags  用户的有效组织标签列表（已展开父标签，用于条件3）
//
//	例如：["PRIVATE_alice", "org:tech:backend", "org:tech", "org:company"]
//
// @return []model.FileUpload  用户可访问的文件列表
// @return error               数据库查询错误
func (r *uploadRepository) FindAccessibleFiles(userID uint, orgTags []string) ([]model.FileUpload, error) {
	var files []model.FileUpload

	// 构建三层权限过滤条件（OR 关系）
	err := r.db.Where("status = ?", 1).
		Where(r.db.Where("user_id = ?", userID). // 条件1：自己上传的
								Or("is_public = ?", true).    // 条件2：全局公开的
								Or("org_tag IN ?", orgTags)). // 条件3：同组织的（修复：移除了错误的 is_public 条件）
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
