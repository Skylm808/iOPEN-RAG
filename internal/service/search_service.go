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

// HybridSearch 执行混合检索主流程（与 archDocs/search_service_workflow.md 对齐）。
// 流程总览：
// 1) 权限与预处理：获取用户有效组织标签，清洗查询词得到 normalized/phrase
// 2) 向量化 Query：使用原始 query 生成 embedding（保留语气与上下文语义）
// 3) 构建混合查询：单次 ES 请求内组合 knn + bool（must/filter/should）
// 4) 执行搜索：在候选窗口内通过 rescore 进行二次排序
// 5) 解析响应：提取命中块与分数
// 6) 0 命中兜底：保留查询骨架，仅将 must/rescore 的 query 改为 phrase 重试一次
// 7) 元数据补全：按 file_md5 回查 MySQL，组装最终 DTO
//
// 关键语义：
// - 不是“先向量查再关键词查”的两次独立请求，而是同一次请求里的融合检索。
// - 权限在检索阶段直接过滤（私有 / 公开 / 组织），避免越权命中进入候选集。
// - rescore 只作用于候选窗口，用更严格关键词匹配提升排序精度。
func (s *searchService) HybridSearch(ctx context.Context, query string, topK int, user *model.User) ([]model.SearchResponseDTO, error) {
	log.Infof("[SearchService] 开始执行混合搜索, query: '%s', topK: %d, user: %s", query, topK, user.Username)

	// 步骤 1：权限前置。获取用户可见的组织标签（包含继承/层级展开后的有效标签）。
	// 这些标签会进入 bool.filter 的 terms 条件，和 user_id / is_public 一起组成权限三选一过滤。
	log.Info("[SearchService] 步骤1: 获取用户有效组织标签")
	userEffectiveTags, err := s.userService.GetUserEffectiveOrgTags(user)
	if err != nil {
		log.Errorf("[SearchService] 获取用户有效组织标签失败: %v", err)
		// 即使失败也继续，只是组织标签过滤会失效
		userEffectiveTags = []string{}
	}
	log.Infof("[SearchService] 获取到 %d 个有效组织标签: %v", len(userEffectiveTags), userEffectiveTags)

	// 步骤 1（续）：查询归一化。
	// - normalized：用于 must.match 与 rescore.match 的关键词通道
	// - phrase：用于 should.match_phrase 提升短语命中，以及 0 命中时的重试词
	normalized, phrase := normalizeQuery(query)
	if normalized != query {
		log.Infof("[SearchService] 规范化查询: '%s' -> '%s' (phrase='%s')", query, normalized, phrase)
	}

	// 步骤 2：向量化（语义通道）。
	// 使用原始 query（而非 normalized），避免去噪后损失嵌入模型可利用的语义线索。
	log.Info("[SearchService] 步骤2: 开始向量化查询")
	queryVector, err := s.embeddingClient.CreateEmbedding(ctx, query)
	if err != nil {
		log.Errorf("[SearchService] 向量化查询失败: %v", err)
		return nil, fmt.Errorf("failed to create query embedding: %w", err)
	}
	log.Infof("[SearchService] 步骤2: 向量化查询成功, 向量维度: %d", len(queryVector))

	// 步骤 3：构建 ES 混合查询（一次请求内融合召回）。
	// - knn：语义召回，负责“尽量不漏”
	// - bool.must(match)：关键词主条件，限制主题方向
	// - bool.filter：权限硬过滤（user_id OR is_public OR org_tag）
	// - bool.should(match_phrase)：短语命中加分，不是必须条件
	log.Info("[SearchService] 步骤3: 开始构建 Elasticsearch 两阶段混合搜索查询")
	var buf bytes.Buffer
	// 用 map[string]interface{} 组装 DSL，便于后续按条件改写（例如 0 命中时替换 must/rescore 查询词）。
	esQuery := map[string]interface{}{
		// 阶段一（召回）：KNN 先拉大候选池，给后续 rescore 预留足够空间。
		// k/num_candidates = topK*30，与 window_size 对齐，形成“扩召回 -> 精重排”的配合。
		"knn": map[string]interface{}{
			"field":          "vector",
			"query_vector":   queryVector,
			"k":              topK * 30,
			"num_candidates": topK * 30,
		},
		"query": map[string]interface{}{
			"bool": map[string]interface{}{
				// must.match（阶段一关键词约束）：
				// - 基于 normalized 做分词匹配（不是字面全等）
				// - 默认 OR 语义（未显式 operator），用于“宽召回”避免过早漏掉候选
				"must": map[string]interface{}{
					"match": map[string]interface{}{
						"text_content": normalized,
					},
				},
				// filter（权限硬约束）：
				// - 不参与打分，仅决定可见性
				// - 三条规则命中任意一条即可：本人 / 公开 / 同组织
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
				// should.match_phrase（短语增强）：
				// - 非必须命中
				// - 命中核心短语时提高分数，帮助“措辞更贴近问题”的片段前移
				"should": buildPhraseShould(phrase),
			},
		},
		// 阶段二（精排）：仅在候选窗口内执行 rescore。
		// 这里显式 operator=and，比阶段一更严格，用于把“关键词完整覆盖度高”的文档排到前面。
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
				"query_weight":         0.2, // 保留第一阶段分数（低权重）
				"rescore_query_weight": 1.0, // 让第二阶段关键词精排主导最终排序
			},
		},
		"size": topK,
	}

	if err := json.NewEncoder(&buf).Encode(esQuery); err != nil {
		log.Errorf("[SearchService] 序列化 Elasticsearch 查询失败: %v", err)
		return nil, fmt.Errorf("failed to encode es query: %w", err)
	}
	log.Infof("[SearchService] 构建的 Elasticsearch 查询语句: %s", buf.String())

	// 步骤 4：执行搜索（一次请求同时包含召回与精排配置）。
	log.Info("[SearchService] 步骤4: 开始向 Elasticsearch 发送搜索请求")
	// WithIndex("knowledge_base") 指定本次只在 knowledge_base 索引里搜索。
	// 如果不指定索引，可能搜索到默认/其他索引，导致结果不符合预期。
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

	// 步骤 5：解析 ES 响应。
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
		// 步骤 6：0 命中兜底重试。
		// 保留原查询骨架（knn/filter/rescore 结构不变），仅把 must/rescore 的 query 改为 phrase。
		if phrase != "" && phrase != query {
			log.Infof("[SearchService] 使用核心短语重试查询: '%s'", phrase)
			// 用核心短语替换关键词查询条件（must + rescore），其余参数保持一致。
			var retryBuf bytes.Buffer
			retryQuery := esQuery
			// 改写 must.match.text_content.query
			((retryQuery["query"].(map[string]interface{}))["bool"].(map[string]interface{}))["must"] = map[string]interface{}{
				"match": map[string]interface{}{
					"text_content": phrase,
				},
			}
			// 改写 rescore_query.match.text_content.query
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

	// 步骤 7：结果补全。根据 file_md5 批量回查文件名，保证展示使用最新元数据。
	log.Info("[SearchService] 步骤6: 开始批量获取文件名")
	fileMD5s := make([]string, 0, len(esResponse.Hits.Hits))
	for _, hit := range esResponse.Hits.Hits {
		fileMD5s = append(fileMD5s, hit.Source.FileMD5)
	}
	// 先去重，避免重复 MD5 触发无效数据库查询。
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

	// 组装最终返回 DTO（含 chunk 内容、分数、权限字段与补全后的文件名）。
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

// normalizeQuery 对查询做轻量归一化，服务于关键词通道。
// 返回：
// - normalized：用于 must.match + rescore.match 的关键词检索词
// - phrase：当前实现与 normalized 相同，用于 should.match_phrase 与 0 命中重试
func normalizeQuery(q string) (string, string) {
	if q == "" {
		return q, ""
	}
	lower := strings.ToLower(q)
	// 去除常见口语/功能词，降低噪声词对关键词匹配的干扰。
	stopPhrases := []string{"是谁", "是什么", "是啥", "请问", "怎么", "如何", "告诉我", "严格", "按照", "不要补充", "的区别", "区别", "吗", "呢", "？", "?"}
	for _, sp := range stopPhrases {
		lower = strings.ReplaceAll(lower, sp, " ")
	}
	// 仅保留中文、英文、数字与空白，去掉标点及其它符号。
	reKeep := regexp.MustCompile(`[^\p{Han}a-z0-9\s]+`)
	kept := reKeep.ReplaceAllString(lower, " ")
	// 归一化连续空白为单空格，避免生成异常查询词。
	reSpace := regexp.MustCompile(`\s+`)
	kept = strings.TrimSpace(reSpace.ReplaceAllString(kept, " "))
	if kept == "" {
		return q, ""
	}
	return kept, kept
}

// buildPhraseShould 构建 should.match_phrase 子句（带 boost）。
// phrase 为空时返回 nil，表示不添加短语增强分支。
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
