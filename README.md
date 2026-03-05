# iOPEN — 智能知识库系统

<p align="center">
  <img src="logo.png" alt="iOPEN" width="180" />
</p>

> 面向实验室/企业的私有化 AI 知识库，基于两阶段 RAG 架构（ES 混合召回 + Cross-Encoder 精排 + Gemini 2.5 流式生成）。

---

## 技术栈

| 层次 | 技术 |
|---|---|
| **后端** | Go · Gin · GORM |
| **存储** | MySQL · Redis · MinIO · Elasticsearch |
| **消息队列** | Kafka |
| **文档解析** | Apache Tika |
| **AI** | Gemini 2.5 · text-embedding-v4 · bge-reranker-v2-m3 (Xinference) |
| **前端** | Vue 3 · TypeScript · Naive UI |
| **通信** | WebSocket |

---

## 核心功能

### 📁 大文件断点续传
- 基于 **MD5 指纹**的用户级断点续传方案
- **5MB 固定分片**上传至 MinIO，Redis 追踪各分片状态
- 断网重连后跳过已传分片，避免整文件重传
- 同一 MD5 已存在时**秒传**，保证幂等

### 🔄 Kafka 异步处理管道
- 文件合并完成后投递 Kafka 消息，**解耦上传与处理**
- 消费端串行执行：**Tika 文本提取 → 滑动窗口分块 → Embedding 向量化 → ES 索引入库**
- 上传接口无需等待解析完成即可返回

### 🔍 两阶段混合检索
**阶段一（ES 内部）：**
- 单次请求融合 **KNN 向量检索**（语义召回）+ **BM25 关键词匹配**
- rescore 窗口内 BM25 AND 二次精排（权重 0.2:1.0）
- 检索阶段注入**用户/团队/公开**三级权限过滤

**阶段二（Cross-Encoder）：**
- 召回结果发往 **bge-reranker-v2-m3**（Xinference 部署），联合建模重打分
- Reranker 不可达时自动**降级**回 ES 排序结果，不影响可用性

### 💬 流式问答
- **WebSocket** 实现 LLM 流式输出
- URL 参数携带 JWT 完成 WS 握手鉴权（WS 协议不支持自定义 Header）
- `sync.Map` 维护连接级停止标志，支持用户随时**中断生成**
- 会话历史存储于 Redis（滚动窗口，自动过期）

### 🔐 三层权限隔离
- **私有**（user_id）/ **组织**（org_tag）/ **公开**（is_public）
- 权限过滤在 ES 检索层 filter 注入，越权数据不进入候选集

---

## 项目结构

```
iopen-go/
├── cmd/server/          # 程序入口 (main.go)
├── configs/             # 配置文件 (config.yaml)
├── internal/
│   ├── config/          # 配置结构体
│   ├── handler/         # HTTP/WebSocket 处理器
│   ├── middleware/       # JWT 鉴权、请求日志
│   ├── model/           # 数据模型
│   ├── pipeline/        # 文档处理管道 (Tika + 分块 + Embedding)
│   ├── repository/      # 数据访问层 (MySQL + Redis)
│   └── service/         # 业务逻辑层
├── pkg/
│   ├── embedding/       # Embedding 客户端
│   ├── es/              # Elasticsearch 客户端
│   ├── kafka/           # Kafka 生产者/消费者
│   ├── llm/             # LLM 客户端（流式输出）
│   ├── reranker/        # Cross-Encoder Reranker 客户端
│   ├── storage/         # MinIO 客户端
│   └── tika/            # Apache Tika 客户端
└── frontend/            # Vue 3 前端
```

---

## 快速启动

### 1. 依赖服务

确保以下服务已运行：
- MySQL / Redis / Elasticsearch / MinIO / Kafka
- Apache Tika Server
- Xinference（用于 Reranker，可选）

### 2. 配置

```bash
cp configs/config.yaml configs/config.yaml
# 编辑 config.yaml，填入各服务地址和 API Key
```

关键配置项：
```yaml
reranker:
  enabled: true            # 设为 false 可禁用 Reranker
  base_url: "http://YOUR_SERVER_IP:9997/v1"
  model: "bge-reranker-v2-m3"
```

### 3. 启动后端

```bash
go run ./cmd/server/main.go
```

### 4. 启动前端

```bash
cd frontend
npm install
npm run dev
```

---

## Reranker 部署（可选）

使用 **Xinference** 本地部署 `bge-reranker-v2-m3`：

```bash
# 安装
pip install "xinference[transformers]"

# 启动服务
xinference-local --host 0.0.0.0 --port 9997

# 通过 UI 加载模型：http://YOUR_IP:9997
# 选择 Model Type: rerank，Engine: sentence_transformers，Model: bge-reranker-v2-m3
```

---

## License

MIT
