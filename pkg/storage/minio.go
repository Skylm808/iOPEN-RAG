// Package storage提供了与对象存储服务（如 MinIO）交互的功能。
package storage

import (
	"context"
	"pai-smart-go/internal/config"
	"pai-smart-go/pkg/log"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// MinioClient 是一个全局的 MinIO 客户端实例。
var MinioClient *minio.Client

// InitMinIO 初始化 MinIO 客户端并确保指定的存储桶存在。
func InitMinIO(cfg config.MinIOConfig) {
	var err error

	// 1. 初始化 MinIO 客户端
	MinioClient, err = minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		log.Fatal("初始化 MinIO 客户端失败", err)
	}

	log.Info("MinIO 客户端初始化成功")

	// 2. 检查存储桶 (Bucket) 是否存在，如果不存在则创建
	ctx := context.Background()
	bucketName := cfg.BucketName
	exists, err := MinioClient.BucketExists(ctx, bucketName)
	if err != nil {
		log.Fatal("检查 MinIO 存储桶失败", err)
	}

	if !exists {
		log.Infof("存储桶 '%s' 不存在，正在创建...", bucketName)
		err = MinioClient.MakeBucket(ctx, bucketName, minio.MakeBucketOptions{})
		if err != nil {
			log.Fatal("创建 MinIO 存储桶失败", err)
		}
		log.Infof("存储桶 '%s' 创建成功", bucketName)
	} else {
		log.Infof("存储桶 '%s' 已存在", bucketName)
	}
}

// GetPresignedURL generates a presigned URL for a given object.
func GetPresignedURL(bucketName, objectName string, expiry time.Duration) (string, error) {
	presignedURL, err := MinioClient.PresignedGetObject(context.Background(), bucketName, objectName, expiry, nil)
	if err != nil {
		log.Errorf("Error generating presigned URL: %s", err)
		return "", err
	}
	return presignedURL.String(), nil
}
