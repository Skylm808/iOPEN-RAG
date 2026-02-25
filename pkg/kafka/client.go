// Package kafka 提供了与 Kafka 消息队列交互的功能。
// 本文件包含两个核心能力：
//   - 生产者（ProduceFileTask）：上传合并完成后，把"文件处理任务"写入 Kafka Topic
//   - 消费者（StartConsumer）：持续监听 Topic，取到任务后驱动 Processor.Process 完成
//     "下载 → Tika 提取 → 分块 → 入 MySQL → 向量化 → 入 ES"整条入库流水线
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

// TaskProcessor 是"任务处理器"的抽象接口。
// 这里用接口而不是直接依赖 pipeline.Processor，是为了解耦：
//   - 消费者只知道"有人能处理任务"，不关心具体实现
//   - 好处：测试时可传 mock，生产时才注入真正的 Processor
//   - 实现者：internal/pipeline/processor.go 的 *Processor
type TaskProcessor interface {
	Process(ctx context.Context, task tasks.FileProcessingTask) error
}

// producer 是全局 Kafka 写入器（发送端），由 InitProducer 初始化。
// 使用包级变量而非依赖注入，是因为整个进程只需一个生产者实例。
var producer *kafka.Writer

// InitProducer 初始化 Kafka 生产者，应在进程启动（main.go）时调用一次。
// 参数 cfg 包含 Broker 地址和 Topic 名称，来自配置文件。
//
// Balancer 选择 LeastBytes（发往积压字节最少的 partition），
// 适合消息大小差异较大的场景（文件处理任务 JSON 大小不均）。
func InitProducer(cfg config.KafkaConfig) {
	producer = &kafka.Writer{
		Addr:     kafka.TCP(cfg.Brokers),
		Topic:    cfg.Topic,
		Balancer: &kafka.LeastBytes{},
	}
	log.Info("Kafka 生产者初始化成功")
}

// ProduceFileTask 将一个文件处理任务序列化为 JSON，发送到 Kafka Topic。
//
// 调用时机：upload_service.go 的 MergeChunks 完成合并后立即调用。
// 任务结构（tasks.FileProcessingTask）包含：
//   - FileMD5：文件唯一标识
//   - ObjectUrl：MinIO 预签名 URL（Processor 用于下载，但实际 Processor 用 fileName 拼路径下载）
//   - FileName：原始文件名（如 "员工手册.pdf"）
//   - UserID / OrgTag / IsPublic：权限信息，会带入 ES 文档和 MySQL 记录
//
// 发送是同步的（WriteMessages 阻塞直到 Kafka 确认写入），失败会返回 error。
// 调用方目前"记录日志但不中断"，即 Kafka 写失败不影响合并结果返回给前端。
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

// StartConsumer 启动 Kafka 消费者，阻塞运行（通常在独立 goroutine 中调用）。
//
// ┌─────────────────────────────────────────────────────────────────┐
// │                      消费者整体流程                              │
// │                                                                 │
// │  Kafka Topic                                                    │
// │  ┌──────────────┐                                               │
// │  │ 文件处理任务  │  FetchMessage（阻塞，直到有新消息）            │
// │  └──────┬───────┘                                               │
// │         │                                                       │
// │         ▼                                                       │
// │   JSON 解析失败？ ──是──▶ CommitMessages（丢弃坏消息），continue  │
// │         │ 否                                                     │
// │         ▼                                                       │
// │   processor.Process()  ←──── 真正干活：下载+解析+入库+入ES       │
// │         │                                                       │
// │    ┌────┴─────┐                                                 │
// │  成功          失败                                              │
// │    │           │                                                │
// │    │     Redis INCR attempts（失败次数 +1，TTL 24h）             │
// │    │           │                                                │
// │    │      attempts >= 3？                                       │
// │    │        ├── 是：CommitMessages（放弃重试，offset 前进）       │
// │    │        └── 否：不提交 offset → Kafka 将重新投递此消息        │
// │    │                                                            │
// │  Redis DEL attempts（清理计数）                                  │
// │  CommitMessages（offset 前进，消息消费完成）                      │
// └─────────────────────────────────────────────────────────────────┘
//
// 关于 Consumer Group（GroupID = "pai-smart-go-consumer"）：
//   - 同一 GroupID 的多个消费者实例会协调分配 partition，实现水平扩展。
//   - 当前单机部署下只有一个消费者实例，GroupID 主要用于 offset 持久化（Kafka 记住消费到哪里）。
//
// 关于 MinBytes / MaxBytes：
//   - MinBytes=10KB：Kafka 积累到至少 10KB 才返回（减少网络往返，提升吞吐）。
//   - MaxBytes=10MB：单次 Fetch 最多拉 10MB，避免内存撑爆。
//   - 文件处理任务 JSON 通常远小于 10KB，因此 MinBytes 主要起"批量拉取"节流作用。
func StartConsumer(cfg config.KafkaConfig, processor TaskProcessor) {
	// 创建 Kafka Reader（消费者），使用 Consumer Group 模式。
	// Consumer Group 模式下，offset 提交由代码手动控制（CommitMessages），
	// 而不是"拉到就自动提交"，这样可以做到"至少处理一次"语义：
	//   - 处理成功 → 再提交 offset（安全推进）
	//   - 处理失败 → 不提交 offset → Kafka 重新投递
	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers:  []string{cfg.Brokers},
		Topic:    cfg.Topic,
		GroupID:  "pai-smart-go-consumer", // Consumer Group ID，用于 offset 持久化与多实例协调
		MinBytes: 10e3,                    // 10KB：Fetch 等待的最小数据量（节流）
		MaxBytes: 10e6,                    // 10MB：Fetch 单次最大数据量（防内存溢出）
	})

	log.Infof("Kafka 消费者已启动，正在监听主题 '%s'", cfg.Topic)

	// 消费主循环：阻塞等待 → 拿到消息 → 处理 → 根据结果决定是否提交 offset
	for {
		// FetchMessage: 阻塞调用，直到 Kafka 有新消息才返回。
		// 注意：FetchMessage 不会自动提交 offset，必须手动调用 CommitMessages。
		m, err := r.FetchMessage(context.Background())
		if err != nil {
			// FetchMessage 失败通常意味着 Kafka 连接中断，直接 break 退出循环。
			// 生产环境应配套进程守护（如 systemd / k8s 重启策略）来重新拉起消费者。
			log.Error("从 Kafka 读取消息失败", err)
			break
		}

		log.Infof("收到 Kafka 消息: offset %d", m.Offset)

		// 尝试把 Kafka 消息体（JSON）反序列化为 FileProcessingTask。
		var task tasks.FileProcessingTask
		if err := json.Unmarshal(m.Value, &task); err != nil {
			// 消息格式错误（如生产者序列化 bug、消息被篡改），无法处理。
			// 策略：直接提交 offset（让消费进度前进），否则这条坏消息会永远阻塞队列。
			// 这是"丢弃死信"的简易处理，生产环境可改为写入死信队列（DLQ）。
			log.Errorf("无法解析 Kafka 消息: %v, value: %s", err, string(m.Value))
			if err := r.CommitMessages(context.Background(), m); err != nil {
				log.Errorf("提交错误消息失败: %v", err)
			}
			continue
		}

		log.Infof("开始处理文件任务: MD5=%s, FileName=%s", task.FileMD5, task.FileName)

		// 同步调用 processor.Process 执行完整入库流水线：
		//   下载 MinIO → Tika 提取文本 → 分块 → 写 MySQL → 向量化 → 写 ES
		// 同步调用的含义：当前消费循环会等待 Process 完成才处理下一条消息。
		// 这保证了"同一时刻只处理一个文件"，避免并发写 ES 的竞争问题，
		// 代价是吞吐量受限于单个文件的处理时间。
		if err := processor.Process(context.Background(), task); err != nil {
			log.Errorf("处理文件任务失败: MD5=%s, Error: %v", task.FileMD5, err)

			// ── 失败重试机制（基于 Redis 计数）──────────────────────────────
			//
			// 为什么不直接用 Kafka 的自动重试？
			//   因为 Consumer Group 模式下，不提交 offset 就等于"告诉 Kafka 我还没消费"，
			//   下次 FetchMessage（或消费者重启后）会重新拿到同一条消息。
			//   但 Kafka 本身不知道"这条已经失败了几次"，所以需要外部计数器（Redis）。
			//
			// 重试计数的 key 格式：kafka:attempts:{fileMD5}
			//   - 每次处理失败，计数 +1（INCR 是原子操作）
			//   - 设置 24h TTL，防止计数永久残留（如任务被手动干预处理后）
			//   - 成功时主动 DEL，确保下次重新上传同一文件时计数归零
			attemptsKey := fmt.Sprintf("kafka:attempts:%s", task.FileMD5)

			// Redis INCR：原子自增，返回自增后的值（即当前失败次数）
			attempts, incErr := database.RDB.Incr(context.Background(), attemptsKey).Result()
			if incErr == nil {
				// 每次自增后重置 TTL（24h），避免长时间失败的任务计数永久堆积
				_ = database.RDB.Expire(context.Background(), attemptsKey, 24*time.Hour).Err()
			}

			if incErr != nil {
				// Redis 本身不可用时，无法做计数判断。
				// 保守策略：不提交 offset，等 Redis 恢复后重试。
				// 风险：如果 Redis 长期不可用，消费者会一直卡在同一消息上。
				continue
			}

			if attempts >= 3 {
				// 已失败 3 次（含本次），视为"不可恢复错误"，放弃重试。
				// 提交 offset 让消费进度前进，否则这条消息会永远阻塞后续任务。
				// 可改进：此处可将任务写入告警系统或死信队列，方便人工介入。
				log.Errorf("文件任务多次失败(>=3)，提交 offset 终止重试: MD5=%s", task.FileMD5)
				if err := r.CommitMessages(context.Background(), m); err != nil {
					log.Errorf("提交 Kafka 消息 offset 失败: %v", err)
				}
			}
			// attempts < 3：不提交 offset。
			// 效果：下次 FetchMessage 或消费者重启后，Kafka 会再次投递此消息，
			// 实现"至少处理一次 + 限次重试"语义。
		} else {
			// 处理成功
			log.Infof("文件任务处理成功: MD5=%s", task.FileMD5)

			// 清理该文件的失败次数计数（为下一次上传同名文件做归零）
			_ = database.RDB.Del(context.Background(), fmt.Sprintf("kafka:attempts:%s", task.FileMD5)).Err()

			// 手动提交 offset：告诉 Kafka "这条消息我已经处理完了，不要再投递"。
			// 必须在处理成功后才提交，这样即使进程崩溃，Kafka 也会重新投递未提交的消息。
			if err := r.CommitMessages(context.Background(), m); err != nil {
				log.Errorf("提交 Kafka 消息 offset 失败: %v", err)
			}
		}
	}

	// 退出循环后关闭 Reader，释放 Kafka 连接资源。
	// 正常情况下（FetchMessage 报错后 break），会走到这里。
	if err := r.Close(); err != nil {
		log.Fatalf("关闭 Kafka 消费者失败: %v", err)
	}
}
