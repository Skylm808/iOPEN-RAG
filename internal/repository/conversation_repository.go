// Package repository 提供了数据访问层的实现。
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

// GetOrCreateConversationID 获取或创建一个新的对话ID。
func (r *redisConversationRepository) GetOrCreateConversationID(ctx context.Context, userID uint) (string, error) {
	userKey := fmt.Sprintf("user:%d:current_conversation", userID)
	convID, err := r.redisClient.Get(ctx, userKey).Result()
	if err == redis.Nil {
		// generate uuid-like using timestamp+userID (avoid heavy deps)
		convID = fmt.Sprintf("%d-%d", time.Now().UnixNano(), userID)
		if err := r.redisClient.Set(ctx, userKey, convID, 7*24*time.Hour).Err(); err != nil {
			return "", fmt.Errorf("failed to set conversation id: %w", err)
		}
		return convID, nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to get conversation id: %w", err)
	}
	return convID, nil
}

// GetConversationHistory 从 Redis 获取对话历史记录。
func (r *redisConversationRepository) GetConversationHistory(ctx context.Context, conversationID string) ([]model.ChatMessage, error) {
	key := fmt.Sprintf("conversation:%s", conversationID)
	jsonData, err := r.redisClient.Get(ctx, key).Result()
	if err == redis.Nil {
		return []model.ChatMessage{}, nil // No history yet
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get conversation history: %w", err)
	}
	var messages []model.ChatMessage
	err = json.Unmarshal([]byte(jsonData), &messages)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal conversation history: %w", err)
	}
	return messages, nil
}

// UpdateConversationHistory 在 Redis 中更新对话历史记录。
func (r *redisConversationRepository) UpdateConversationHistory(ctx context.Context, conversationID string, messages []model.ChatMessage) error {
	key := fmt.Sprintf("conversation:%s", conversationID)
	// 保留最近 20 条
	if len(messages) > 20 {
		messages = messages[len(messages)-20:]
	}
	jsonData, err := json.Marshal(messages)
	if err != nil {
		return fmt.Errorf("failed to marshal conversation history: %w", err)
	}
	err = r.redisClient.Set(ctx, key, jsonData, 7*24*time.Hour).Err()
	if err != nil {
		return fmt.Errorf("failed to set conversation history: %w", err)
	}
	return nil
}

// GetAllUserConversationMappings returns map[userID]conversationID by scanning user:*:current_conversation
func (r *redisConversationRepository) GetAllUserConversationMappings(ctx context.Context) (map[uint]string, error) {
	keys, err := r.redisClient.Keys(ctx, "user:*:current_conversation").Result()
	if err != nil {
		return nil, fmt.Errorf("failed to scan user conversation keys: %w", err)
	}
	result := make(map[uint]string)
	for _, k := range keys {
		// k format: user:{uid}:current_conversation
		var uid uint
		_, scanErr := fmt.Sscanf(k, "user:%d:current_conversation", &uid)
		if scanErr != nil {
			continue
		}
		convID, getErr := r.redisClient.Get(ctx, k).Result()
		if getErr != nil {
			continue
		}
		result[uid] = convID
	}
	return result, nil
}
