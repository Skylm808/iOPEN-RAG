// Package kafka 提供了与 Kafka 消息队列交互的功能。
package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"pai-smart-go/internal/config"
	"pai-smart-go/pkg/database"
	"pai-smart-go/pkg/log"
	"pai-smart-go/pkg/tasks"
	"time"

	"github.com/segmentio/kafka-go"
)

// TaskProcessor defines the interface for any service that can process a task.
// This decouples the Kafka consumer from the concrete pipeline implementation.
type TaskProcessor interface {
	Process(ctx context.Context, task tasks.FileProcessingTask) error
}

var producer *kafka.Writer

// InitProducer 初始化 Kafka 生产者。
func InitProducer(cfg config.KafkaConfig) {
	producer = &kafka.Writer{
		Addr:     kafka.TCP(cfg.Brokers),
		Topic:    cfg.Topic,
		Balancer: &kafka.LeastBytes{},
	}
	log.Info("Kafka 生产者初始化成功")
}

// ProduceFileTask 发送一个文件处理任务到 Kafka。
func ProduceFileTask(task tasks.FileProcessingTask) error {
	taskBytes, err := json.Marshal(task)
	if err != nil {
		return err
	}

	err = producer.WriteMessages(context.Background(),
		kafka.Message{
			Value: taskBytes,
		},
	)
	return err
}

// StartConsumer 启动一个 Kafka 消费者来处理文件任务。
func StartConsumer(cfg config.KafkaConfig, processor TaskProcessor) {
	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers:  []string{cfg.Brokers},
		Topic:    cfg.Topic,
		GroupID:  "pai-smart-go-consumer",
		MinBytes: 10e3, // 10KB
		MaxBytes: 10e6, // 10MB
	})

	log.Infof("Kafka 消费者已启动，正在监听主题 '%s'", cfg.Topic)

	for {
		m, err := r.FetchMessage(context.Background())
		if err != nil {
			log.Error("从 Kafka 读取消息失败", err)
			break // 退出循环，可能需要重启策略
		}

		log.Infof("收到 Kafka 消息: offset %d", m.Offset)

		var task tasks.FileProcessingTask
		if err := json.Unmarshal(m.Value, &task); err != nil {
			log.Errorf("无法解析 Kafka 消息: %v, value: %s", err, string(m.Value))
			// 消息格式错误，直接提交，避免阻塞队列
			if err := r.CommitMessages(context.Background(), m); err != nil {
				log.Errorf("提交错误消息失败: %v", err)
			}
			continue
		}

		log.Infof("开始处理文件任务: MD5=%s, FileName=%s", task.FileMD5, task.FileName)
		// 同步处理任务
		if err := processor.Process(context.Background(), task); err != nil {
			log.Errorf("处理文件任务失败: MD5=%s, Error: %v", task.FileMD5, err)
			// 使用 Redis 计数失败次数，达到阈值后提交 offset 终止重试
			attemptsKey := fmt.Sprintf("kafka:attempts:%s", task.FileMD5)
			attempts, incErr := database.RDB.Incr(context.Background(), attemptsKey).Result()
			if incErr == nil {
				_ = database.RDB.Expire(context.Background(), attemptsKey, 24*time.Hour).Err()
			}
			if incErr != nil {
				// Redis 异常时保守处理：不提交 offset，让 Kafka 重试
				continue
			}
			if attempts >= 3 {
				log.Errorf("文件任务多次失败(>=3)，提交 offset 终止重试: MD5=%s", task.FileMD5)
				if err := r.CommitMessages(context.Background(), m); err != nil {
					log.Errorf("提交 Kafka 消息 offset 失败: %v", err)
				}
			}
			// attempts < 3 时，不提交 offset 让 Kafka 自动重试
		} else {
			log.Infof("文件任务处理成功: MD5=%s", task.FileMD5)
			// 清理失败计数
			_ = database.RDB.Del(context.Background(), fmt.Sprintf("kafka:attempts:%s", task.FileMD5)).Err()
			// 任务处理成功后，手动提交 offset
			if err := r.CommitMessages(context.Background(), m); err != nil {
				log.Errorf("提交 Kafka 消息 offset 失败: %v", err)
			}
		}
	}

	if err := r.Close(); err != nil {
		log.Fatalf("关闭 Kafka 消费者失败: %v", err)
	}
}
