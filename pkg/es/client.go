// Package es 提供了与 Elasticsearch 交互的客户端功能。
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

var ESClient *elasticsearch.Client

// InitES 初始化 Elasticsearch 客户端
func InitES(esCfg config.ElasticsearchConfig) error {
	cfg := elasticsearch.Config{
		Addresses: []string{esCfg.Addresses},
		Username:  esCfg.Username,
		Password:  esCfg.Password,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	client, err := elasticsearch.NewClient(cfg)
	if err != nil {
		return err
	}
	ESClient = client
	return createIndexIfNotExists(esCfg.IndexName)
}

// createIndexIfNotExists 检查索引是否存在，如果不存在则创建它
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

	// 完全对齐 Java 项目的 knowledge_base.json 结构
	// 使用 ik 中文分词器，并指定向量维度为 2048 和 cosine 相似度
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

// IndexDocument 将单个文档向量索引到 Elasticsearch。
func IndexDocument(ctx context.Context, indexName string, doc model.EsDocument) error {
	docBytes, err := json.Marshal(doc)
	if err != nil {
		return err
	}

	req := esapi.IndexRequest{
		Index:      indexName,
		DocumentID: doc.VectorID,
		Body:       bytes.NewReader(docBytes),
		Refresh:    "true",
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
