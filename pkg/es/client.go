// Package es 提供了与 Elasticsearch 交互的客户端功能。
//
// ── 这个文件在整个项目里的角色 ───────────────────────────────────────────────
//
// Elasticsearch（以下简称 ES）是本 RAG 系统的【知识库搜索引擎】。
// 它承担了两件核心的事情：
//
//  1. 【存储向量和文档元数据】：
//     当用户上传一个 PDF/Word 文件后，文件的每一个文本分块（Chunk）会在
//     upload_service.go 中被调用 Embedding 模型转化成一个高维向量（float32 数组）。
//     转化后的向量，连同原文本和文件元数据（文件 MD5、用户 ID、组织标签等），
//     会通过这里的 IndexDocument 函数，被写入（索引）到 ES 的 knowledge_base 索引中。
//
//  2. 【混合检索：KNN 向量搜索 + BM25 关键词搜索】：
//     当用户在聊天界面提问时，search_service.go 会将问题向量化，
//     然后向本 ES 集群发送一个同时包含 KNN（向量相似度）和 BM25（关键词匹配）的
//     复合 DSL 查询，检索出最相关的知识文本片段，作为大模型（LLM）的上下文参考。
//
// ── 关键设计决策 ─────────────────────────────────────────────────────────────
//
//   - InsecureSkipVerify: true：
//     我们跳过了 ES 的 TLS 证书校验。这在本地开发或内网部署时很常见，
//     但在生产环境中应当配置正确的 CA 证书。
//
//   - Index Mapping（索引结构，类比 MySQL 的建表语句）：
//     text_content 字段使用 ik 中文分词器（ik_max_word/ik_smart），
//     这是专为中文语料优化的分词插件，能正确把中文句子切分为词语。
//     vector 字段类型为 dense_vector，维度 2048，使用 cosine 余弦相似度计算，
//     这是向量检索（KNN）的基础存储单元。
//
//   - Refresh: "true"：
//     在索引一个文档后立即强制刷新，使得该文档可以被立即搜索到。
//     这会牺牲少量写入性能，但保证了数据的可见性，适合小批量写入场景。
package es

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"pai-smart-go/internal/config"
	"pai-smart-go/internal/model"
	"pai-smart-go/pkg/log"
	"strings"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/esapi"
)

// ESClient 是全局共享的 Elasticsearch 客户端实例。
// 在 main.go 里调用 InitES 初始化后，整个应用通过这个全局变量访问 ES。
var ESClient *elasticsearch.Client

// InitES 初始化 Elasticsearch 客户端，并在 ES 中创建必要的索引（如果尚不存在）。
// 这个函数在应用启动阶段（main.go 的 wire 依赖注入流程）中被调用一次。
//
// 参数 esCfg 来自 config.yaml 中的 elasticsearch 配置项，包含：
//   - Addresses：ES 服务地址（如 http://localhost:9200）
//   - Username / Password：ES 的 HTTP 身份认证（Basic Auth）凭证
//   - IndexName：要操作的索引名（knowledge_base）
func InitES(esCfg config.ElasticsearchConfig) error {
	cfg := elasticsearch.Config{
		Addresses: []string{esCfg.Addresses},
		Username:  esCfg.Username,
		Password:  esCfg.Password,
		// 跳过 TLS 证书验证，适用于自签名证书的内网 ES 环境
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	client, err := elasticsearch.NewClient(cfg)
	if err != nil {
		return err
	}
	ESClient = client
	// 客户端建立后，立即检查索引是否存在，不存在则按规定的 Mapping 创建
	return createIndexIfNotExists(esCfg.IndexName)
}

// createIndexIfNotExists 检查指定的 ES 索引是否存在，如果不存在则按照预设的 Mapping 创建它。
//
// ── 为什么需要提前定义 Mapping？ ─────────────────────────────────────────────
//
//	ES 默认会根据你第一次写入的数据自动推断字段类型（Dynamic Mapping）。
//	但这对本项目来说存在致命问题：
//	 - vector 字段必须是 dense_vector 类型，才能支持 KNN 向量检索。
//	 - text_content 字段必须指定 ik 中文分词器，否则中文分词会异常。
//	 - org_tag / is_public / user_id 等权限过滤字段需要精确类型（keyword/boolean/long）。
//	所以我们必须在第一次写入数据前，就先手动建好这张"表结构"。
func createIndexIfNotExists(indexName string) error {
	res, err := ESClient.Indices.Exists([]string{indexName})
	if err != nil {
		log.Errorf("检查索引是否存在时出错: %v", err)
		return err
	}
	// 如果 res.StatusCode 是 200，说明索引已存在
	if !res.IsError() && res.StatusCode == http.StatusOK {
		log.Infof("索引 '%s' 已存在", indexName)
		return nil
	}
	// 如果 res.StatusCode 是 404，说明索引不存在，需要创建
	if res.StatusCode != http.StatusNotFound {
		log.Errorf("检查索引 '%s' 是否存在时收到意外的状态码: %d", indexName, res.StatusCode)
		return fmt.Errorf("检查索引是否存在时收到意外的状态码: %d", res.StatusCode)
	}

	// ── 索引 Mapping 详解 ────────────────────────────────────────────────────
	//
	//  这段 JSON 就是 ES 的建表语句（类比 MySQL 的 CREATE TABLE）。
	//  各字段含义如下：
	//
	//   - vector_id：(keyword) 每个文本分块的唯一 ID，用于幂等写入（覆盖重复分块）
	//   - file_md5：(keyword) 文件的 MD5 哈希值，用于溯源文档来自哪个原始文件
	//   - chunk_id：(integer) 文本切片在原文中的顺序编号（第几块）
	//   - text_content：(text) 原始的中文/英文文本内容，使用 IK 分词器支持中文搜索
	//       - analyzer: "ik_max_word"：写入时使用"最细粒度"分词，把词切得尽可能碎
	//       - search_analyzer: "ik_smart"：搜索时用"智能分词"，把意义完整的词保留
	//   - vector：(dense_vector) 文本内容对应的高维向量表示（由 Embedding 模型生成）
	//       - dims: 2048：向量维度，必须与 Embedding 模型的输出维度一致
	//       - index: true：允许对这个字段进行 ANN（近似最近邻/KNN）向量检索
	//       - similarity: "cosine"：使用余弦相似度衡量两个向量的"意思距离"
	//   - user_id：(long) 上传者的用户 ID，用于私有文档的权限过滤
	//   - org_tag：(keyword) 文档所属的组织/部门标签，用于组织内权限过滤
	//   - is_public：(boolean) 是否是全局公开文档，true 则任何用户均可检索到
	mapping := `{
		"mappings": {
			"properties": {
				"vector_id": { "type": "keyword" },
				"file_md5": { "type": "keyword" },
				"chunk_id": { "type": "integer" },
				"text_content": { 
					"type": "text",
					"analyzer": "ik_max_word",
					"search_analyzer": "ik_smart" 
				},
				"vector": {
					"type": "dense_vector",
					"dims": 2048,
					"index": true,
					"similarity": "cosine"
				},
				"model_version": { "type": "keyword" },
				"user_id": { "type": "long" },
				"org_tag": { "type": "keyword" },
				"is_public": { "type": "boolean" }
			}
		}
	}`

	res, err = ESClient.Indices.Create(
		indexName,
		ESClient.Indices.Create.WithBody(strings.NewReader(mapping)),
	)

	if err != nil {
		log.Errorf("创建索引 '%s' 失败: %v", indexName, err)
		return err
	}
	if res.IsError() {
		log.Errorf("创建索引 '%s' 时 Elasticsearch 返回错误: %s", indexName, res.String())
		return errors.New("创建索引时 Elasticsearch 返回错误")
	}

	log.Infof("索引 '%s' 创建成功", indexName)
	return nil
}

// IndexDocument 将单个文档（文本分块 + 向量 + 权限元数据）写入 Elasticsearch 索引。
//
// ── 调用时机 ─────────────────────────────────────────────────────────────────
//
//	这个函数由 upload_service.go 里的文档处理流水线调用。
//	流程大致如下：
//	  1. 用户上传文件 → Tika 提取全文文本
//	  2. 文本按照固定大小切分成若干 Chunk
//	  3. 每个 Chunk 调用 Embedding 模型转化为 float32 向量
//	  4. 调用本函数 IndexDocument，把 (Chunk 原文 + 向量 + 元数据) 写入 ES
//
// 参数说明：
//   - ctx：请求上下文，用于超时取消控制
//   - indexName：目标 ES 索引名（通常是 "knowledge_base"）
//   - doc：model.EsDocument，包含了 vector_id、text_content、vector、user_id、org_tag 等全量字段
//
// 关键行为：
//   - DocumentID 使用 doc.VectorID，这是文档的唯一主键。
//   - 如果同一个 VectorID 被写入两次，ES 会执行"覆盖更新"而非重复插入（幂等性）。
//   - Refresh: "true" 使得写入后可立即被搜索到（牺牲少量写入吞吐量）。
func IndexDocument(ctx context.Context, indexName string, doc model.EsDocument) error {
	docBytes, err := json.Marshal(doc)
	if err != nil {
		return err
	}

	req := esapi.IndexRequest{
		Index:      indexName,
		DocumentID: doc.VectorID, // 使用 VectorID 作为文档主键，保证幂等写入
		Body:       bytes.NewReader(docBytes),
		Refresh:    "true", // 立即刷新，写入后可即时搜索
	}

	res, err := req.Do(ctx, ESClient)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if res.IsError() {
		log.Errorf("索引文档到 Elasticsearch 出错: %s", res.String())
		return errors.New("failed to index document")
	}

	return nil
}
