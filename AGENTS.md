# AGENTS.md

This file provides guidance to Qoder (qoder.com) when working with code in this repository.

## Project Overview

PaiSmart (派聪明) is an enterprise-grade AI knowledge base management system built with RAG (Retrieval Augmented Generation) technology. It provides intelligent document processing, indexing, and retrieval capabilities with multi-tenant support.

**Technology Stack:**
- **Backend**: Go 1.23+ with Gin framework
- **Frontend**: Vue 3 + TypeScript + Vite + Naive UI
- **Database**: MySQL 8.0, Redis 7.0
- **Search**: Elasticsearch 8.10.4
- **Message Queue**: Apache Kafka
- **Storage**: MinIO
- **Document Parsing**: Apache Tika
- **AI Integration**: DeepSeek API / Ollama + 豆包 Embedding

## Common Commands

### Backend (Go)

**Start Backend Server:**
```bash
go run cmd/server/main.go
```

**Build Backend:**
```bash
go build -o bin/server cmd/server/main.go
```

**Install Dependencies:**
```bash
go mod download
go mod tidy
```

**Configuration:**
- Config file: `configs/config.yaml`
- Update database credentials, API keys, and service endpoints before running

### Frontend (Vue)

**Development Server:**
```bash
cd frontend
pnpm install
pnpm run dev        # Development mode (test environment)
pnpm run dev:prod   # Development mode (prod environment)
```

**Build:**
```bash
cd frontend
pnpm run build      # Production build
pnpm run build:test # Test environment build
```

**Linting and Type Checking:**
```bash
cd frontend
pnpm run lint       # Run ESLint with auto-fix
pnpm run typecheck  # Run Vue TypeScript type checking
```

### Infrastructure

**Start All Services (Docker Compose):**
```bash
docker compose -f deployments/docker-compose.yaml up -d
```

**Pull Images Separately (if network issues):**
```bash
docker compose -f deployments/docker-compose.yaml pull mysql
docker compose -f deployments/docker-compose.yaml pull redis
docker compose -f deployments/docker-compose.yaml pull minio
docker compose -f deployments/docker-compose.yaml pull tika
docker compose -f deployments/docker-compose.yaml pull zookeeper
docker compose -f deployments/docker-compose.yaml pull kafka
docker compose -f deployments/docker-compose.yaml pull es
```

**Stop All Services:**
```bash
docker compose -f deployments/docker-compose.yaml down
```

**Services Included:**
- MySQL (port 3306)
- Redis (port 6379)
- MinIO (ports 9000, 9001)
- Elasticsearch (port 9200)
- Kafka (port 9092)
- Zookeeper (port 2181)
- Apache Tika (port 9998)

## Architecture

### Backend Architecture (Go)

The backend follows a clean, layered architecture:

```
cmd/server/          - Application entry point (main.go)
internal/
  ├── config/        - Configuration management (Viper)
  ├── handler/       - HTTP handlers (Gin controllers)
  ├── middleware/    - Authentication, logging, admin authorization
  ├── model/         - Domain models and DTOs
  ├── pipeline/      - Document processing pipeline (Kafka consumer logic)
  ├── repository/    - Data access layer (GORM)
  └── service/       - Business logic layer
pkg/
  ├── database/      - MySQL and Redis initialization
  ├── embedding/     - Embedding API client (豆包)
  ├── es/            - Elasticsearch client and operations
  ├── kafka/         - Kafka producer and consumer
  ├── llm/           - LLM API client (DeepSeek)
  ├── log/           - Structured logging (Zap)
  ├── storage/       - MinIO object storage client
  ├── tika/          - Apache Tika document parser client
  └── token/         - JWT token management
```

**Key Design Patterns:**
- **Dependency Injection**: Services receive dependencies via constructors in `main.go`
- **Repository Pattern**: Data access is abstracted through repository interfaces
- **Pipeline Processing**: Kafka-based async document processing (upload → parse → chunk → embed → index)
- **Middleware Chain**: Authentication (JWT), role-based authorization, request logging

### Request Flow

1. **Document Upload**:
   - User uploads file via `/api/v1/upload` (chunked upload with MD5 verification)
   - File metadata stored in MySQL, chunks stored in MinIO
   - Kafka message published to `file-processing` topic

2. **Document Processing** (Async via Kafka):
   - `pipeline.Processor` consumes Kafka messages
   - Downloads file from MinIO → Tika extraction → Text chunking (智能分块)
   - Embedding generation (豆包 API) → Elasticsearch indexing

3. **RAG Query** (WebSocket):
   - User query → Hybrid search (Elasticsearch: semantic + keyword)
   - Top-k relevant chunks retrieved → LLM context assembly
   - Streaming response via WebSocket with citation references

### Frontend Architecture (Vue)

```
frontend/
  ├── src/
  │   ├── assets/        - Static assets (SVG, images)
  │   ├── components/    - Reusable Vue components
  │   ├── layouts/       - Page layouts (admin, user)
  │   ├── router/        - Vue Router configuration
  │   ├── service/       - API integration (Axios)
  │   ├── store/         - Pinia state management
  │   └── views/         - Page components
  └── packages/          - Shared modules
```

**Key Features:**
- **UnoCSS**: Atomic CSS framework for styling
- **Iconify**: Icon management
- **WebSocket**: Real-time chat with AI assistant
- **Chunk Upload**: Large file upload with progress tracking and resume capability

## Multi-Tenant Architecture

- **Organization Tags** (`org_tag`): Each document can be associated with an organization
- **Access Control**:
  - Public documents: Accessible to all users
  - Private documents: Only accessible to users in the same `org_tag`
- **Admin Role**: Can manage users, assign org tags, view all conversations

## Database Schema

Key tables (see `docs/ddl.sql`):
- `users`: User accounts with role (USER/ADMIN) and org_tags
- `organization_tags`: Hierarchical organization structure
- `file_upload`: Document metadata with upload status
- `chunk_info`: File chunk information for resumable uploads
- `document_vectors`: Vector embeddings linked to documents

## RAG Implementation

**Document Processing Pipeline:**
1. **Text Extraction**: Apache Tika extracts text from various formats (PDF, DOCX, etc.)
2. **Chunking**: Intelligent text segmentation (using Gojieba for Chinese)
3. **Embedding**: 豆包 Embedding API generates 2048-dim vectors
4. **Indexing**: Vectors stored in Elasticsearch with metadata

**Query Flow:**
1. **Hybrid Search**: Combines semantic similarity (vector) + keyword matching (BM25)
2. **Reranking**: Top-k chunks filtered by user access permissions
3. **Context Assembly**: Relevant chunks formatted with source citations
4. **LLM Generation**: DeepSeek/Ollama generates response with citations

## Configuration Notes

- **API Keys Required**:
  - `embedding.api_key`: 豆包 Embedding API key (阿里云 DashScope)
  - `llm.api_key`: DeepSeek API key (or leave empty for local Ollama)

- **Local Development**:
  - Update `configs/config.yaml` with local service endpoints
  - For local LLM, change `llm.base_url` to `http://localhost:11434/v1` and `llm.model` to `deepseek-r1:7b`

- **Database Initialization**:
  - MySQL schema auto-applied via `docs/ddl.sql` in docker-entrypoint-initdb.d
  - Elasticsearch index auto-created on first document upload

## WebSocket Protocol

**Endpoint**: `GET /chat/:token`
- Token obtained from `GET /api/v1/chat/websocket-token`
- Message format: JSON with `conversationId` and `query`
- Response: Streaming JSON messages with `type` field (`text`, `sources`, `end`, `error`)

## Important Notes

- **No Tests**: This codebase does not include automated tests. Manual testing is required.
- **Frontend Git Hooks**: Pre-commit hooks run `typecheck` and `lint` automatically.
- **Logs**: Backend logs written to `./logs` directory (configured in `config.yaml`).
- **Graceful Shutdown**: Backend implements graceful shutdown on SIGINT/SIGTERM.
