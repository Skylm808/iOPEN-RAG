// Package service 提供了搜索相关的业务逻辑。
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"pai-smart-go/internal/model"
	"pai-smart-go/internal/repository"
	"pai-smart-go/pkg/embedding"
	"pai-smart-go/pkg/log"
	"regexp"
	"strconv"
	"strings"

	"github.com/elastic/go-elasticsearch/v8"
)

// SearchService 接口定义了搜索操作。
type SearchService interface {
	HybridSearch(ctx context.Context, query string, topK int, user *model.User) ([]model.SearchResponseDTO, error)
}

type searchService struct {
	embeddingClient embedding.Client
	esClient        *elasticsearch.Client
	userService     UserService
	uploadRepo      repository.UploadRepository // 新增：UploadRepository 依赖
}

// NewSearchService 创建一个新的 SearchService 实例。
func NewSearchService(embeddingClient embedding.Client, esClient *elasticsearch.Client, userService UserService, uploadRepo repository.UploadRepository) SearchService {
	return &searchService{
		embeddingClient: embeddingClient,
		esClient:        esClient,
		userService:     userService,
		uploadRepo:      uploadRepo, // 新增
	}
}

// HybridSearch 执行与 Java 项目逻辑一致的两阶段混合搜索。
func (s *searchService) HybridSearch(ctx context.Context, query string, topK int, user *model.User) ([]model.SearchResponseDTO, error) {
	log.Infof("[SearchService] 开始执行混合搜索, query: '%s', topK: %d, user: %s", query, topK, user.Username)

	// 1. 获取用户有效的组织标签（包含层级关系）
	log.Info("[SearchService] 步骤1: 获取用户有效组织标签")
	userEffectiveTags, err := s.userService.GetUserEffectiveOrgTags(user)
	if err != nil {
		log.Errorf("[SearchService] 获取用户有效组织标签失败: %v", err)
		// 即使失败也继续，只是组织标签过滤会失效
		userEffectiveTags = []string{}
	}
	log.Infof("[SearchService] 获取到 %d 个有效组织标签: %v", len(userEffectiveTags), userEffectiveTags)

	// 2. 轻量归一化（去噪）以获取核心短语
	normalized, phrase := normalizeQuery(query)
	if normalized != query {
		log.Infof("[SearchService] 规范化查询: '%s' -> '%s' (phrase='%s')", query, normalized, phrase)
	}

	// 3. 向量化查询（用原始用户问句，保持语义检索能力）
	log.Info("[SearchService] 步骤2: 开始向量化查询")
	queryVector, err := s.embeddingClient.CreateEmbedding(ctx, query)
	if err != nil {
		log.Errorf("[SearchService] 向量化查询失败: %v", err)
		return nil, fmt.Errorf("failed to create query embedding: %w", err)
	}
	log.Infof("[SearchService] 步骤2: 向量化查询成功, 向量维度: %d", len(queryVector))

	// 4. 构建 Elasticsearch 的复杂混合搜索查询 (与Java对齐)，并加入短语兜底 should
	log.Info("[SearchService] 步骤3: 开始构建 Elasticsearch 两阶段混合搜索查询")
	var buf bytes.Buffer
	esQuery := map[string]interface{}{
		"knn": map[string]interface{}{
			"field":          "vector",
			"query_vector":   queryVector,
			"k":              topK * 30,
			"num_candidates": topK * 30,
		},
		"query": map[string]interface{}{
			"bool": map[string]interface{}{
				"must": map[string]interface{}{
					"match": map[string]interface{}{
						"text_content": normalized,
					},
				},
				"filter": map[string]interface{}{
					"bool": map[string]interface{}{
						"should": []map[string]interface{}{
							{"term": map[string]interface{}{"user_id": user.ID}},
							{"term": map[string]interface{}{"is_public": true}},
							{"terms": map[string]interface{}{"org_tag": userEffectiveTags}},
						},
						"minimum_should_match": 1,
					},
				},
				// 额外的 should：对核心短语做 match_phrase 以兜底召回
				"should": buildPhraseShould(phrase),
			},
		},
		"rescore": map[string]interface{}{
			"window_size": topK * 30, // 与 Java 的 recallK 对齐
			"query": map[string]interface{}{
				"rescore_query": map[string]interface{}{
					"match": map[string]interface{}{
						"text_content": map[string]interface{}{
							"query":    normalized,
							"operator": "and",
						},
					},
				},
				"query_weight":         0.2, // 保留部分 k-NN 分数
				"rescore_query_weight": 1.0, // BM25 分数权重
			},
		},
		"size": topK,
	}

	if err := json.NewEncoder(&buf).Encode(esQuery); err != nil {
		log.Errorf("[SearchService] 序列化 Elasticsearch 查询失败: %v", err)
		return nil, fmt.Errorf("failed to encode es query: %w", err)
	}
	log.Infof("[SearchService] 构建的 Elasticsearch 查询语句: %s", buf.String())

	// 5. 执行搜索
	log.Info("[SearchService] 步骤4: 开始向 Elasticsearch 发送搜索请求")
	// 与 Java 索引名保持一致（假设为 knowledge_base）
	res, err := s.esClient.Search(
		s.esClient.Search.WithContext(ctx),
		s.esClient.Search.WithIndex("knowledge_base"),
		s.esClient.Search.WithBody(&buf),
		s.esClient.Search.WithTrackTotalHits(true),
	)
	if err != nil {
		log.Errorf("[SearchService] 向 Elasticsearch 发送搜索请求失败: %v", err)
		return nil, fmt.Errorf("elasticsearch search failed: %w", err)
	}
	defer res.Body.Close()

	if res.IsError() {
		bodyBytes, _ := io.ReadAll(res.Body)
		log.Errorf("[SearchService] Elasticsearch 返回错误, status: %s, body: %s", res.Status(), string(bodyBytes))
		return nil, fmt.Errorf("elasticsearch returned an error: %s", res.String())
	}
	log.Info("[SearchService] 成功从 Elasticsearch 获取响应")

	// 6. 解析结果
	log.Info("[SearchService] 步骤5: 开始解析 Elasticsearch 响应")
	var esResponse struct {
		Hits struct {
			Hits []struct {
				Source model.EsDocument `json:"_source"`
				Score  float64          `json:"_score"` // 获取 ES 的 score
			} `json:"hits"`
		} `json:"hits"`
	}

	if err := json.NewDecoder(res.Body).Decode(&esResponse); err != nil {
		log.Errorf("[SearchService] 解析 Elasticsearch 响应失败: %v", err)
		return nil, fmt.Errorf("failed to decode es response: %w", err)
	}

	if len(esResponse.Hits.Hits) == 0 {
		log.Infof("[SearchService] Elasticsearch 返回 0 条命中结果")
		// 兜底：若规范化后核心短语存在且与原问句不同，则用核心短语重试一次（更强关键词信号）
		if phrase != "" && phrase != query {
			log.Infof("[SearchService] 使用核心短语重试查询: '%s'", phrase)
			// rebuild with phrase in must+rescore
			var retryBuf bytes.Buffer
			retryQuery := esQuery
			// update must.match.text_content
			((retryQuery["query"].(map[string]interface{}))["bool"].(map[string]interface{}))["must"] = map[string]interface{}{
				"match": map[string]interface{}{
					"text_content": phrase,
				},
			}
			// update rescore query
			((retryQuery["rescore"].(map[string]interface{}))["query"].(map[string]interface{}))["rescore_query"] = map[string]interface{}{
				"match": map[string]interface{}{
					"text_content": map[string]interface{}{
						"query":    phrase,
						"operator": "and",
					},
				},
			}
			if err := json.NewEncoder(&retryBuf).Encode(retryQuery); err == nil {
				res2, err2 := s.esClient.Search(
					s.esClient.Search.WithContext(ctx),
					s.esClient.Search.WithIndex("knowledge_base"),
					s.esClient.Search.WithBody(&retryBuf),
					s.esClient.Search.WithTrackTotalHits(true),
				)
				if err2 == nil && !res2.IsError() {
					defer res2.Body.Close()
					if err := json.NewDecoder(res2.Body).Decode(&esResponse); err == nil {
						log.Infof("[SearchService] 重试后命中 %d 条", len(esResponse.Hits.Hits))
					}
				}
			}
		}
		if len(esResponse.Hits.Hits) == 0 {
			return []model.SearchResponseDTO{}, nil
		}
	}

	// 7. 批量获取文件名
	log.Info("[SearchService] 步骤6: 开始批量获取文件名")
	fileMD5s := make([]string, 0, len(esResponse.Hits.Hits))
	for _, hit := range esResponse.Hits.Hits {
		fileMD5s = append(fileMD5s, hit.Source.FileMD5)
	}
	// 使用 map 去重
	uniqueMD5s := make(map[string]struct{})
	for _, md5 := range fileMD5s {
		uniqueMD5s[md5] = struct{}{}
	}
	md5List := make([]string, 0, len(uniqueMD5s))
	for md5 := range uniqueMD5s {
		md5List = append(md5List, md5)
	}

	fileInfos, err := s.uploadRepo.FindBatchByMD5s(md5List)
	if err != nil {
		log.Errorf("[SearchService] 批量查询文件信息失败: %v", err)
		return nil, fmt.Errorf("批量查询文件信息失败: %w", err)
	}

	fileNameMap := make(map[string]string)
	for _, info := range fileInfos {
		fileNameMap[info.FileMD5] = info.FileName
	}
	log.Infof("[SearchService] 批量获取文件名成功, 共获取 %d 个文件信息", len(fileNameMap))

	// 8. 组装最终结果
	log.Info("[SearchService] 步骤7: 开始组装最终响应 DTO")
	var results []model.SearchResponseDTO
	for _, hit := range esResponse.Hits.Hits {
		fileName := fileNameMap[hit.Source.FileMD5]
		if fileName == "" {
			log.Warnf("[SearchService] 未找到 FileMD5 '%s' 对应的文件名, 将使用 '未知文件'", hit.Source.FileMD5)
			fileName = "未知文件"
		}
		dto := model.SearchResponseDTO{
			FileMD5:     hit.Source.FileMD5,
			FileName:    fileName,
			ChunkID:     hit.Source.ChunkID,
			TextContent: hit.Source.TextContent,
			Score:       hit.Score,
			UserID:      strconv.FormatUint(uint64(hit.Source.UserID), 10),
			OrgTag:      hit.Source.OrgTag,
			IsPublic:    hit.Source.IsPublic,
		}
		results = append(results, dto)
	}

	log.Infof("[SearchService] 组装最终响应成功, 返回 %d 条结果", len(results))
	log.Infof("[SearchService] 混合搜索执行完毕, query: '%s'", query)
	return results, nil
}

// normalizeQuery 对用户查询进行轻量去噪与短语提取。
// 返回值：规范化后的查询（用于 BM25/rescore）与核心短语（用于 match_phrase 兜底）。
func normalizeQuery(q string) (string, string) {
	if q == "" {
		return q, ""
	}
	lower := strings.ToLower(q)
	// 去除常见口语/功能词
	stopPhrases := []string{"是谁", "是什么", "是啥", "请问", "怎么", "如何", "告诉我", "严格", "按照", "不要补充", "的区别", "区别", "吗", "呢", "？", "?"}
	for _, sp := range stopPhrases {
		lower = strings.ReplaceAll(lower, sp, " ")
	}
	// 仅保留中文、英文、数字与空白
	reKeep := regexp.MustCompile(`[^\p{Han}a-z0-9\s]+`)
	kept := reKeep.ReplaceAllString(lower, " ")
	// 归一空白
	reSpace := regexp.MustCompile(`\s+`)
	kept = strings.TrimSpace(reSpace.ReplaceAllString(kept, " "))
	if kept == "" {
		return q, ""
	}
	return kept, kept
}

// buildPhraseShould 构建 match_phrase should 子句（带 boost），为空则返回 nil
func buildPhraseShould(phrase string) interface{} {
	if phrase == "" {
		return nil
	}
	return []map[string]interface{}{
		{
			"match_phrase": map[string]interface{}{
				"text_content": map[string]interface{}{
					"query": phrase,
					"boost": 3.0,
				},
			},
		},
	}
}
