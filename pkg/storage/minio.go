// Package storage 提供了与对象存储服务（MinIO）交互的功能。
//
// ── 这个文件在整个项目里的角色 ───────────────────────────────────────────────
//
// MinIO 是本 RAG 系统的【原始文件仓库（对象存储）】，角色等同于 AWS S3 或阿里云 OSS。
// 它负责保存用户上传的所有原始文件的二进制内容（如 PDF、Word、PPT 等），
// 而 MySQL 只存储文件的元数据（文件名、MD5、大小、上传状态等）。
//
// ── MinIO 在整个数据流中的位置 ─────────────────────────────────────────────
//
//	用户上传文件时（upload_handler.go → upload_service.go）：
//	  1. 前端将分块文件传到后端
//	  2. 后端将分块内容上传到 MinIO（对象 Key 通常是 "文件MD5/文件名"）
//	  3. 所有分块上传完毕后，MinIO 中的对象被合并成完整文件
//	  4. 随后 Tika 从 MinIO 下载文件，提取文本，走向 ES 向量化流程
//
//	用户下载/预览文件时（document_handler.go）：
//	  1. 后端调用 GetPresignedURL 生成一个有时效性的临时下载链接
//	  2. 这个 URL 返回给前端（不经过后端中转，直接由浏览器访问 MinIO）
//	  3. 好处：减轻后端 I/O 压力，大文件下载不占用 Go 服务器带宽
//
// ── 关键概念：Bucket（存储桶）─────────────────────────────────────────────
//
//	MinIO 的 Bucket 相当于一个顶层目录/文件夹。
//	启动时本代码会自动检查目标 Bucket（由 config.yaml 中的 bucketName 指定）是否存在，
//	如果不存在则自动创建，避免了手动创建的运维步骤。
//
// ── 关键概念：Presigned URL（预签名 URL）─────────────────────────────────
//
//	MinIO 里的对象默认是私有的（不能直接通过 URL 访问）。
//	Presigned URL 是 MinIO 生成的一个临时授权链接，其中内嵌了签名和过期时间。
//	只要在有效期内，任何人（包括未登录的浏览器）都可以用这个链接直接下载文件。
//	适合实现前端直接下载/预览文件的场景。
package storage

import (
	"context"
	"pai-smart-go/internal/config"
	"pai-smart-go/pkg/log"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// MinioClient 是全局共享的 MinIO 客户端实例。
// 在 main.go 里调用 InitMinIO 初始化后，整个应用通过这个全局变量访问 MinIO。
var MinioClient *minio.Client

// InitMinIO 初始化 MinIO 客户端，并确保配置文件中指定的存储桶存在。
// 这个函数在应用启动阶段（main.go）中被调用一次。
//
// 参数 cfg 来自 config.yaml 中的 minio 配置项，包含：
//   - Endpoint：MinIO 服务地址（如 localhost:9000）
//   - AccessKeyID / SecretAccessKey：访问密钥（类似用户名/密码）
//   - UseSSL：是否通过 HTTPS 连接（本地开发通常为 false）
//   - BucketName：要操作的存储桶名（如 "knowledge-base"）
//
// 如果初始化或 Bucket 创建失败，程序直接 Fatal 退出（因为没有存储服务，系统无法运行）。
func InitMinIO(cfg config.MinIOConfig) {
	var err error

	// 1. 初始化 MinIO 客户端
	// NewStaticV4 提供静态 Access Key / Secret Key 认证（MinIO 默认认证方式）
	MinioClient, err = minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		Secure: cfg.UseSSL, // 是否启用 TLS/HTTPS
	})
	if err != nil {
		log.Fatal("初始化 MinIO 客户端失败", err)
	}

	log.Info("MinIO 客户端初始化成功")

	// 2. 检查存储桶 (Bucket) 是否存在，如果不存在则创建
	// 这一步保证了系统无论是第一次启动还是重启，都能找到正确的 Bucket
	ctx := context.Background()
	bucketName := cfg.BucketName
	exists, err := MinioClient.BucketExists(ctx, bucketName)
	if err != nil {
		log.Fatal("检查 MinIO 存储桶失败", err)
	}

	if !exists {
		log.Infof("存储桶 '%s' 不存在，正在创建...", bucketName)
		// MakeBucketOptions{} 使用默认选项（默认地区、非版本控制）
		err = MinioClient.MakeBucket(ctx, bucketName, minio.MakeBucketOptions{})
		if err != nil {
			log.Fatal("创建 MinIO 存储桶失败", err)
		}
		log.Infof("存储桶 '%s' 创建成功", bucketName)
	} else {
		log.Infof("存储桶 '%s' 已存在", bucketName)
	}
}

// GetPresignedURL 为 MinIO 中的指定对象生成一个有时效的预签名访问 URL。
//
// ── 为什么需要 Presigned URL？ ────────────────────────────────────────────
//
//	MinIO 里的对象默认是"私有"的，外界无法直接通过常规 URL 访问。
//	如果用后端中转（先下载到 Go 服务器内存再返回给浏览器），文件越大占的内存越多，容易 OOM。
//	Presigned URL 方案：
//	  - MinIO 在 URL 中内嵌了合法的时效签名
//	  - 前端收到这个 URL 后，直接用浏览器请求 MinIO，完全不经过后端
//	  - 超过 expiry 时间后，URL 自动失效，保证了安全性
//
// 参数说明：
//   - bucketName：存储桶名
//   - objectName：对象在桶内的路径/Key（如 "abc123/文档.pdf"）
//   - expiry：URL 有效期（如 15 * time.Minute，15 分钟后失效）
//
// 返回值：string 类型的 URL，如 "http://localhost:9000/bucket/xxx?X-Amz-Signature=..."
func GetPresignedURL(bucketName, objectName string, expiry time.Duration) (string, error) {
	presignedURL, err := MinioClient.PresignedGetObject(context.Background(), bucketName, objectName, expiry, nil)
	if err != nil {
		log.Errorf("Error generating presigned URL: %s", err)
		return "", err
	}
	return presignedURL.String(), nil
}
