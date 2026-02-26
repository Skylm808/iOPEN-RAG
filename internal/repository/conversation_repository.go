// Package repository 提供了数据访问层的实现。
// conversation_repository.go 专门负责"对话历史"的读写，底层存储是 Redis。
//
// ── 为什么用 Redis 而不是 MySQL？ ─────────────────────────────────────
// 对话历史是"临时性"数据：7 天不活跃就自动过期，不需要永久保存。
// 每次问答都要读写，Redis 的内存读写比 MySQL 快得多，延迟低。
// MySQL 更适合存"永久性"数据，如用户信息、文件元数据。
//
// ── Redis 里存了什么？两个 Key ──────────────────────────────────────
//
//	Key 1: user:{userID}:current_conversation
//	  存的是：当前会话 ID（字符串，如 "1740488888888-1001"）
//	  TTL：7 天
//	  作用：记录"这个用户当前用的是哪个会话"
//
//	Key 2: conversation:{conversationID}
//	  存的是：对话历史数组（JSON 序列化），最多 20 条
//	  TTL：7 天（每次更新自动重置）
//	  作用：存实际的消息内容（user 问了什么、assistant 答了什么）
//
// ── 两个 Key 的关系 ──────────────────────────────────────────────────
//
//	通过 userID 查 Key1 → 拿到 conversationID → 通过 conversationID 查 Key2 → 拿到历史
//
//	userID=1001    →   Key1 → "1740488888888-1001"
//	                                 ↓
//	                          conversationID
//	                                 ↓
//	                           Key2 → [{role:user, content:"..."}, ...]
package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"pai-smart-go/internal/model"
	"time"

	"github.com/go-redis/redis/v8"
)

// ConversationRepository 定义了对话历史记录的操作接口。
type ConversationRepository interface {
	GetOrCreateConversationID(ctx context.Context, userID uint) (string, error)
	GetConversationHistory(ctx context.Context, conversationID string) ([]model.ChatMessage, error)
	UpdateConversationHistory(ctx context.Context, conversationID string, messages []model.ChatMessage) error
	GetAllUserConversationMappings(ctx context.Context) (map[uint]string, error)
}

type redisConversationRepository struct {
	redisClient *redis.Client
}

// NewConversationRepository 创建一个新的 ConversationRepository 实例。
func NewConversationRepository(redisClient *redis.Client) ConversationRepository {
	return &redisConversationRepository{redisClient: redisClient}
}

// GetOrCreateConversationID 获取用户当前的会话 ID；不存在则创建一个新的。
//
// 读取 Redis Key：user:{userID}:current_conversation
//
// 两种情况：
//   - Key 存在 → 直接返回已有的 conversationID（用户还在同一个会话里）
//   - Key 不存在（首次对话 / 7 天过期后）→ 生成一个新 conversationID，写入 Redis，TTL 7 天
//
// conversationID 的格式：{时间戳纳秒}-{userID}
// 例如："1740488888123456789-1001"
// 这样生成简单轻量（不引入 UUID 库），且天然唯一（纳秒时间戳 + 用户 ID 组合）
func (r *redisConversationRepository) GetOrCreateConversationID(ctx context.Context, userID uint) (string, error) {
	userKey := fmt.Sprintf("user:%d:current_conversation", userID)

	// 尝试从 Redis 读取已有的会话 ID
	convID, err := r.redisClient.Get(ctx, userKey).Result()

	if err == redis.Nil {
		// Key 不存在：用户第一次对话，或者上次会话已经过期（7 天）
		// 生成新的会话 ID：时间戳（纳秒）+ 用户 ID，保证唯一性
		convID = fmt.Sprintf("%d-%d", time.Now().UnixNano(), userID)

		// 写入 Redis，TTL 7 天。7 天内如果用户再次问问题，会继续用这个会话 ID
		if err := r.redisClient.Set(ctx, userKey, convID, 7*24*time.Hour).Err(); err != nil {
			return "", fmt.Errorf("failed to set conversation id: %w", err)
		}
		return convID, nil
	}

	if err != nil {
		// Redis 自身出错（连接断等）
		return "", fmt.Errorf("failed to get conversation id: %w", err)
	}

	// Key 存在，返回已有的会话 ID
	return convID, nil
}

// GetConversationHistory 从 Redis 读取对话历史消息列表。
//
// 读取 Redis Key：conversation:{conversationID}
// 值是 []model.ChatMessage 的 JSON 序列化结果。
//
// 返回值：
//   - 有历史记录 → 反序列化后返回（最多 20 条，由 UpdateConversationHistory 控制上限）
//   - 没有历史记录（Key 不存在）→ 返回空数组（首次对话没有历史是正常的）
//   - Redis 出错 → 返回 error
func (r *redisConversationRepository) GetConversationHistory(ctx context.Context, conversationID string) ([]model.ChatMessage, error) {
	key := fmt.Sprintf("conversation:%s", conversationID)

	jsonData, err := r.redisClient.Get(ctx, key).Result()
	if err == redis.Nil {
		// 该会话还没有历史记录（新会话），返回空数组，不报错
		return []model.ChatMessage{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get conversation history: %w", err)
	}

	// 把 JSON 字符串 → []ChatMessage 结构体列表
	var messages []model.ChatMessage
	err = json.Unmarshal([]byte(jsonData), &messages)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal conversation history: %w", err)
	}
	return messages, nil
}

// UpdateConversationHistory 把更新后的对话历史写入 Redis（同时做截断和 TTL 刷新）。
//
// 写入 Redis Key：conversation:{conversationID}
//
// ── 截断逻辑（核心）────────────────────────────────────────────────
// 上限是 20 条消息。每轮问答追加 2 条（一条 user、一条 assistant），
// 当总数超过 20 时，保留最新的 20 条，删掉最早的那些。
//
// 例如：第 11 轮问答后，历史变成 22 条 → 截断为后 20 条
//
//	messages[22条] → messages[22-20 : ] → messages[2:]（丢弃最早的 2 条）
//
// 这样做是为了控制发给 LLM 的 Prompt 长度（历史太长会超出 token 限制）。
//
// ── TTL 刷新 ──────────────────────────────────────────────────────
// 每次更新都会重置 TTL 为 7 天（用 Set 覆写，TTL 自动刷新）。
// 意味着：只要用户 7 天内有过任何问答，会话就不会过期。
// 超过 7 天不活跃，Key 自动删除，下次问答会创建新会话。
func (r *redisConversationRepository) UpdateConversationHistory(ctx context.Context, conversationID string, messages []model.ChatMessage) error {
	key := fmt.Sprintf("conversation:%s", conversationID)

	// 超过 20 条时截断，只保留最新的 20 条
	// messages[len(messages)-20:] 表示"从倒数第 20 个开始，取到末尾"
	if len(messages) > 20 {
		messages = messages[len(messages)-20:]
	}

	// 把 []ChatMessage → JSON 字符串，存入 Redis
	jsonData, err := json.Marshal(messages)
	if err != nil {
		return fmt.Errorf("failed to marshal conversation history: %w", err)
	}

	// Set 覆写（不是追加），同时重置 TTL 为 7 天
	err = r.redisClient.Set(ctx, key, jsonData, 7*24*time.Hour).Err()
	if err != nil {
		return fmt.Errorf("failed to set conversation history: %w", err)
	}
	return nil
}

// GetAllUserConversationMappings 返回所有用户的 userID → conversationID 映射。
// 主要供管理员后台查看所有用户的当前会话状态。
//
// 实现方式：
//  1. 用 Redis KEYS 命令匹配所有 "user:*:current_conversation" 格式的 Key
//  2. 从每个 Key 里解析出 userID（用 Sscanf 按格式提取）
//  3. 读取每个 Key 的值（conversationID）
//  4. 组装成 map[userID]conversationID 返回
//
// ⚠️ 注意：Redis KEYS 命令会全量扫描所有 Key，在 Key 数量非常多时会阻塞 Redis。
// 生产环境建议改用 SCAN 命令分批迭代，避免阻塞。
func (r *redisConversationRepository) GetAllUserConversationMappings(ctx context.Context) (map[uint]string, error) {
	// 匹配所有符合格式的 Key，例如：["user:1001:current_conversation", "user:1002:current_conversation"]
	keys, err := r.redisClient.Keys(ctx, "user:*:current_conversation").Result()
	if err != nil {
		return nil, fmt.Errorf("failed to scan user conversation keys: %w", err)
	}

	result := make(map[uint]string)
	for _, k := range keys {
		// 从 Key 字符串里解析出 userID，格式：user:{uid}:current_conversation
		var uid uint
		_, scanErr := fmt.Sscanf(k, "user:%d:current_conversation", &uid)
		if scanErr != nil {
			continue // 格式不对的 Key 跳过（可能是其他模块写入的）
		}

		// 读取该 Key 的值（conversationID）
		convID, getErr := r.redisClient.Get(ctx, k).Result()
		if getErr != nil {
			continue // Key 刚好过期或读取失败，跳过
		}

		result[uid] = convID
	}
	return result, nil
}
