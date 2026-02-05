# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

PaiSmart (派聪明) is an enterprise-level AI knowledge base management system using RAG (Retrieval Augmented Generation) technology. This is the **Go backend implementation** of the system, which also includes a Vue 3 frontend and a homepage.

**Core Architecture:**
- Backend: Go (this repository)
- Frontend: Vue 3 + TypeScript (in `frontend/` directory)
- Homepage: Static site (in `homepage/` directory)

**Technology Stack:**
- **Framework:** Gin (HTTP), WebSocket (real-time communication)
- **Databases:** MySQL (metadata), Redis (caching), Elasticsearch 8.10.0 (vector search)
- **Message Queue:** Kafka (async document processing)
- **Object Storage:** MinIO (file storage)
- **Document Processing:** Apache Tika (text extraction)
- **AI Services:** DeepSeek/Ollama (LLM), Alibaba DashScope (embeddings)
- **Security:** JWT authentication, role-based authorization

## Common Commands

### Backend (Go)

```bash
# Build the application
go build -o bin/server cmd/server/main.go

# Run the server directly
go run cmd/server/main.go

# Run tests (if tests exist)
go test ./...

# Run tests for a specific package
go test ./internal/service/...

# Update dependencies
go mod tidy
go mod download

# Format code
go fmt ./...

# Lint (requires golangci-lint)
golangci-lint run
```

### Frontend (Vue 3)

```bash
cd frontend

# Install dependencies
pnpm install

# Development server (test mode)
pnpm run dev

# Development server (prod mode)
pnpm run dev:prod

# Build for production
pnpm run build

# Build for test environment
pnpm run build:test

# Type checking
pnpm run typecheck

# Lint and fix
pnpm run lint

# Preview production build
pnpm run preview
```

### Docker & Infrastructure

```bash
# Start all services (MySQL, Redis, Kafka, Elasticsearch, MinIO, Tika)
cd deployments
docker-compose up -d

# Stop all services
docker-compose down

# View logs
docker-compose logs -f [service_name]

# Rebuild and restart a specific service
docker-compose up -d --build [service_name]
```

## Project Structure

### Backend Structure (Go)

```
pai-smart-go/
├── cmd/server/main.go          # Application entry point, dependency injection
├── configs/config.yaml         # Configuration file (NOT committed with secrets)
├── internal/                   # Internal application code
│   ├── config/                 # Config loading (Viper)
│   ├── handler/                # HTTP/WebSocket handlers (Gin controllers)
│   ├── middleware/             # Auth, logging, admin authorization
│   ├── model/                  # Domain models and database entities
│   ├── pipeline/               # Document processing pipeline (Tika → chunking → embedding → ES)
│   ├── repository/             # Data access layer (GORM, Redis)
│   └── service/                # Business logic layer
├── pkg/                        # Reusable packages
│   ├── database/               # MySQL, Redis initialization
│   ├── embedding/              # Embedding client (DashScope)
│   ├── es/                     # Elasticsearch client
│   ├── hash/                   # Password hashing (bcrypt)
│   ├── kafka/                  # Kafka producer/consumer
│   ├── llm/                    # LLM client (DeepSeek/Ollama)
│   ├── log/                    # Zap logger
│   ├── storage/                # MinIO client
│   ├── tika/                   # Tika document extraction
│   └── token/                  # JWT token management
└── docs/ddl.sql                # Database schema
```

### Frontend Structure (Vue 3)

```
frontend/
├── src/
│   ├── assets/                 # Static assets
│   ├── components/             # Vue components
│   ├── layouts/                # Page layouts
│   ├── router/                 # Vue Router configuration
│   ├── service/                # API integration
│   ├── store/                  # Pinia state management
│   └── views/                  # Page components
└── packages/                   # Monorepo workspace packages
```

## Architecture Patterns

### Dependency Injection & Initialization Flow

The application follows a layered dependency injection pattern initialized in `cmd/server/main.go`:

1. **Config** → Load from `configs/config.yaml`
2. **Infrastructure** → Initialize MySQL, Redis, MinIO, Elasticsearch, Kafka
3. **Repositories** → Create data access layer instances
4. **Services** → Inject repositories and clients
5. **Handlers** → Inject services
6. **Routes** → Register handlers with Gin router
7. **Background Workers** → Start Kafka consumer for async processing

### RAG Implementation Flow

**Document Upload & Processing:**
1. Client uploads file chunks → `UploadHandler`
2. Chunks stored in MinIO, metadata in MySQL
3. After merge, Kafka message published to `file-processing` topic
4. `Processor` consumes message:
   - Downloads file from MinIO
   - Extracts text via Tika
   - Splits text into chunks (500 chars, 50 overlap)
   - Saves chunks to MySQL (`document_vector` table)
   - Generates embeddings via DashScope
   - Indexes to Elasticsearch with vectors

**Query & Chat:**
1. User sends query via WebSocket → `ChatHandler`
2. `SearchService` performs hybrid search:
   - Semantic search (vector similarity in ES)
   - Keyword search (BM25 in ES)
   - Respects user permissions (public/org/private)
3. Top results fed as context to LLM
4. LLM generates response, streamed back via WebSocket

### Multi-Tenancy & Authorization

**Three-tier access control:**
- **Public documents:** Accessible to all authenticated users
- **Organization documents:** Accessible to users in the same `orgTag`
- **Private documents:** Accessible only to owner (by `userId`)

**Middleware chain:**
- `AuthMiddleware`: Validates JWT, extracts `userId` and `role`
- `AdminAuthMiddleware`: Enforces `role == "admin"` for admin routes

### Async Processing with Kafka

The system uses Kafka to decouple file upload from heavy processing:

- **Producer:** `uploadService.MergeChunks()` publishes `FileProcessingTask` to Kafka
- **Consumer:** Background goroutine in `main.go` runs `kafka.StartConsumer()`
- **Processor:** `pipeline.Processor` handles document processing pipeline

This ensures:
- Fast upload response times
- Graceful handling of processing failures
- Horizontal scalability (multiple consumers)

## Configuration

**Config file:** `configs/config.yaml`

**Critical settings:**
- `server.port`: HTTP server port (default: 8081)
- `server.mode`: Gin mode (debug, release, test)
- `database.mysql.dsn`: MySQL connection string
- `database.redis.addr/password`: Redis connection
- `kafka.brokers/topic`: Kafka settings
- `elasticsearch.addresses/index_name`: ES cluster
- `minio.*`: Object storage credentials
- `embedding.*`: Embedding model API (DashScope)
- `llm.*`: LLM API (DeepSeek/Ollama)

**Important:** Never commit real API keys or passwords to the repository. Use environment variables or secure secret management in production.

## Key Implementation Details

### Chinese Text Processing

The system uses `gojieba` for Chinese word segmentation and Elasticsearch with the `analysis-ik` plugin for Chinese text search. Text chunking respects Unicode rune boundaries to avoid splitting multi-byte characters.

### File Upload with Chunking

Supports large file uploads via:
1. **Check:** Client sends MD5 → server checks if file exists (instant upload)
2. **Chunk:** Upload file in chunks (configurable size)
3. **Merge:** Server merges chunks in MinIO, triggers processing

### WebSocket Authentication

WebSocket connections use a two-step auth:
1. Client requests temporary token via `/api/v1/chat/websocket-token` (authenticated)
2. Client connects to `/chat/:token` with the token in URL
3. Token validated and exchanged for user session

### Graceful Shutdown

The server implements graceful shutdown:
- Catches `SIGINT`/`SIGTERM` signals
- 5-second timeout for existing requests
- Proper cleanup of resources

## Frontend Development Notes

The frontend is a Vue 3 + TypeScript application using:
- **Naive UI** for components
- **Pinia** for state management
- **UnoCSS** for styling
- **Vite** for building
- **pnpm** workspace for monorepo structure

Frontend connects to backend via:
- REST API for data operations
- WebSocket for real-time chat

API base URL configured in `.env` files (`.env`, `.env.test`, `.env.prod`).

## Testing

Currently, the project does not have extensive test coverage. When adding tests:

- Use Go's built-in testing framework (`*_test.go` files)
- Mock external dependencies (Kafka, Elasticsearch, LLM APIs)
- Focus on business logic in `service/` layer
- Use table-driven tests for multiple scenarios

## Common Gotchas

1. **Elasticsearch index mapping:** The index must be created with proper vector field mapping before first use. Check `pkg/es/client.go` for index creation logic.

2. **Kafka consumer blocking:** The Kafka consumer runs in a background goroutine. Ensure proper context cancellation for graceful shutdown if implementing custom shutdown logic.

3. **MinIO bucket creation:** The bucket must exist before file upload. Docker Compose includes `minio-init` service to create it automatically.

4. **JWT secret:** Use a strong random secret in production. The example in `configs/config.yaml` is for development only.

5. **Embedding dimensions:** The `embedding.dimensions` config must match the model being used (default: 2048 for text-embedding-v4).

6. **Chinese text handling:** Always use `[]rune` instead of byte slicing when working with Chinese text to avoid breaking multi-byte characters.

## Related Resources

- Original Java/Spring Boot version: See main README.md for architecture comparison
- Tutorial: https://paicoding.com/column/10/1 (Chinese, requires membership)
- DeepSeek API: https://api.deepseek.com
- Alibaba DashScope: https://dashscope.aliyuncs.com
