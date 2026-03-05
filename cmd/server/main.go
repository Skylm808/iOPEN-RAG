// Package main 是应用程序的入口点。
package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/signal"
	"pai-smart-go/internal/config"
	"pai-smart-go/internal/handler"
	"pai-smart-go/internal/middleware"
	"pai-smart-go/internal/pipeline"
	"pai-smart-go/internal/repository"
	"pai-smart-go/internal/service"
	"pai-smart-go/pkg/database"
	"pai-smart-go/pkg/embedding"
	"pai-smart-go/pkg/es"
	"pai-smart-go/pkg/kafka"
	"pai-smart-go/pkg/llm"
	"pai-smart-go/pkg/log"
	"pai-smart-go/pkg/reranker"
	"pai-smart-go/pkg/storage"
	"pai-smart-go/pkg/tika"
	"pai-smart-go/pkg/token"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
)

func main() {
	// 1. 初始化配置
	config.Init("./configs/config.yaml")
	cfg := config.Conf

	// 2. 初始化日志记录器
	log.Init(cfg.Log.Level, cfg.Log.Format, cfg.Log.OutputPath)
	defer log.Sync() // 确保在程序退出时刷新所有缓冲的日志条目
	log.Info("日志记录器初始化成功")

	// 3. 初始化数据库和 Redis
	database.InitMySQL(cfg.Database.MySQL.DSN)
	database.InitRedis(cfg.Database.Redis.Addr, cfg.Database.Redis.Password, cfg.Database.Redis.DB)
	storage.InitMinIO(cfg.MinIO)
	err := es.InitES(cfg.Elasticsearch)
	if err != nil {
		log.Errorf("es 初始化失败 %s", err)
		return
	}
	kafka.InitProducer(cfg.Kafka)

	// 4. 初始化 Repository
	userRepository := repository.NewUserRepository(database.DB)
	orgTagRepo := repository.NewOrgTagRepository(database.DB)
	uploadRepo := repository.NewUploadRepository(database.DB, database.RDB)
	conversationRepo := repository.NewConversationRepository(database.RDB)
	docVectorRepo := repository.NewDocumentVectorRepository(database.DB)

	// 5. 初始化 Service (依赖注入)
	jwtManager := token.NewJWTManager(cfg.JWT.Secret, cfg.JWT.AccessTokenExpireHours, cfg.JWT.RefreshTokenExpireDays)
	tikaClient := tika.NewClient(cfg.Tika)
	embeddingClient := embedding.NewClient(cfg.Embedding)
	llmClient := llm.NewClient(cfg.LLM)
	userService := service.NewUserService(userRepository, orgTagRepo, jwtManager)
	adminService := service.NewAdminService(orgTagRepo, userRepository, conversationRepo)
	uploadService := service.NewUploadService(uploadRepo, userRepository, cfg.MinIO)
	documentService := service.NewDocumentService(uploadRepo, userRepository, orgTagRepo, cfg.MinIO, tikaClient)

	// 初始化 Reranker 客户端（可选）：enabled=false 时传 nil，HybridSearch 自动降级到 ES rescore 逻辑
	var rerankerClient reranker.Client
	if cfg.Reranker.Enabled {
		rerankerClient = reranker.NewClient(cfg.Reranker)
		log.Infof("Reranker 已启用, model: %s, base_url: %s", cfg.Reranker.Model, cfg.Reranker.BaseURL)
	} else {
		log.Info("Reranker 未启用，HybridSearch 将使用 ES rescore 模式")
	}

	searchService := service.NewSearchService(embeddingClient, es.ESClient, userService, uploadRepo, rerankerClient)
	conversationService := service.NewConversationService(conversationRepo)
	chatService := service.NewChatService(searchService, llmClient, conversationRepo)

	// 6. 初始化文件处理管道 (Processor)
	processor := pipeline.NewProcessor(
		tikaClient,
		embeddingClient,
		cfg.Elasticsearch,
		cfg.MinIO,
		cfg.Embedding,
		uploadRepo,
		docVectorRepo,
	)

	// 7. 启动后台 Kafka 消费者
	go kafka.StartConsumer(cfg.Kafka, processor)

	// 7.1 初始化导入 initfile 目录：模拟真实上传 + 合并（全员可见，归属 admin），已导入则跳过
	initCtx, cancelInit := context.WithCancel(context.Background())
	defer cancelInit()
	go initSeedFiles(initCtx, "initfile", userRepository, uploadService)

	// 8. 设置 Gin 模式并创建路由引擎
	gin.SetMode(cfg.Server.Mode)
	r := gin.New() // 使用 New() 创建一个不带默认中间件的引擎
	// 添加我们自定义的日志中间件和 Gin 的 Recovery 中间件
	r.Use(middleware.RequestLogger(), gin.Recovery())

	// 9. 注册路由
	apiV1 := r.Group("/api/v1")
	{
		// Auth 路由组
		auth := apiV1.Group("/auth")
		{
			auth.POST("/refreshToken", handler.NewAuthHandler(userService).RefreshToken)
		}

		users := apiV1.Group("/users")
		{
			// 无需认证的路由 (公开访问)
			users.POST("/register", handler.NewUserHandler(userService).Register)
			users.POST("/login", handler.NewUserHandler(userService).Login)

			// 需要认证的路由 (仅限登录用户访问)
			authed := users.Group("/")
			authed.Use(middleware.AuthMiddleware(jwtManager, userService))
			{
				authed.GET("/me", handler.NewUserHandler(userService).GetProfile)
				authed.POST("/logout", handler.NewUserHandler(userService).Logout)
				authed.PUT("/primary-org", handler.NewUserHandler(userService).SetPrimaryOrg)
				authed.GET("/org-tags", handler.NewUserHandler(userService).GetUserOrgTags)
			}
		}

		// Upload 路由组，需要认证
		upload := apiV1.Group("/upload")
		upload.Use(middleware.AuthMiddleware(jwtManager, userService))
		{
			upload.POST("/check", handler.NewUploadHandler(uploadService).CheckFile)
			upload.POST("/chunk", handler.NewUploadHandler(uploadService).UploadChunk)
			upload.POST("/merge", handler.NewUploadHandler(uploadService).MergeChunks)
			upload.GET("/status", handler.NewUploadHandler(uploadService).GetUploadStatus)
			upload.GET("/supported-types", handler.NewUploadHandler(uploadService).GetSupportedFileTypes)
			upload.POST("/fast-upload", handler.NewUploadHandler(uploadService).FastUpload)
		}

		// Document 路由组，需要认证
		documents := apiV1.Group("/documents")
		documents.Use(middleware.AuthMiddleware(jwtManager, userService))
		{
			documents.GET("/accessible", handler.NewDocumentHandler(documentService, userService).ListAccessibleFiles)
			documents.GET("/uploads", handler.NewDocumentHandler(documentService, userService).ListUploadedFiles)
			documents.DELETE("/:fileMd5", handler.NewDocumentHandler(documentService, userService).DeleteDocument)
			documents.GET("/download", handler.NewDocumentHandler(documentService, userService).GenerateDownloadURL) // Path param -> Query param
			documents.GET("/preview", handler.NewDocumentHandler(documentService, userService).PreviewFile)
		}

		// Search 路由组
		search := apiV1.Group("/search")
		search.Use(middleware.AuthMiddleware(jwtManager, userService))
		{
			search.GET("/hybrid", handler.NewSearchHandler(searchService).HybridSearch)
		}

		// Conversation 路由组
		conversation := apiV1.Group("/users/conversation")
		conversation.Use(middleware.AuthMiddleware(jwtManager, userService))
		{
			conversation.GET("", handler.NewConversationHandler(conversationService).GetConversations)
		}

		// Chat 路由 (WebSocket)
		chatGroup := apiV1.Group("/chat")
		{
			chatGroup.GET("/websocket-token", handler.NewChatHandler(chatService, userService, jwtManager).GetWebsocketStopToken)
		}
		r.GET("/chat/:token", handler.NewChatHandler(chatService, userService, jwtManager).Handle)

		admin := apiV1.Group("/admin")
		// 管理员路由组，需要同时通过认证和管理员授权两个中间件
		admin.Use(middleware.AuthMiddleware(jwtManager, userService), middleware.AdminAuthMiddleware())
		{
			// 管理员用户管理相关路由
			admin.GET("/users/list", handler.NewAdminHandler(adminService, userService).ListUsers)
			admin.PUT("/users/:userId/org-tags", handler.NewAdminHandler(adminService, userService).AssignOrgTagsToUser)
			admin.GET("/conversation", handler.NewAdminHandler(adminService, userService).GetAllConversations)

			// 管理员组织标签管理相关路由
			orgTags := admin.Group("/org-tags")
			{
				orgTags.POST("", handler.NewAdminHandler(adminService, userService).CreateOrganizationTag)
				orgTags.GET("", handler.NewAdminHandler(adminService, userService).ListOrganizationTags)
				orgTags.GET("/tree", handler.NewAdminHandler(adminService, userService).GetOrganizationTagTree)
				orgTags.PUT("/:id", handler.NewAdminHandler(adminService, userService).UpdateOrganizationTag)
				orgTags.DELETE("/:id", handler.NewAdminHandler(adminService, userService).DeleteOrganizationTag)
			}
		}
	}

	// 启动 HTTP 服务器并实现优雅停机
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%s", cfg.Server.Port),
		Handler: r,
	}

	go func() {
		log.Infof("服务启动于 %s", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP 服务监听失败: %s\n", err)
		}
	}()

	// 等待中断信号以实现优雅停机
	// 1. 造一个用来接收信号的通道（Channel）。
	//    缓冲区为 1 是为了防止信号丢失（虽然概率极小）。
	quit := make(chan os.Signal, 1)
	// 2. 告诉操作系统：如果有 SIGINT (Ctrl+C) 或 SIGTERM (Kill命令) 发生，
	//    别直接弄死我，请把那个信号发到 quit 这个通道里
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Info("接收到停机信号，正在关闭服务...")

	// 设置一个5秒的超时上下文
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 关闭 HTTP 服务器
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("HTTP 服务器关闭失败: %v", err)
	}
	// 在优雅停机逻辑中，我们不需要手动关闭 Kafka 消费者，
	// 因为 StartConsumer 是一个循环，会在程序退出时自然结束。
	// 如果需要更精细的控制，可以在 StartConsumer 中实现一个关闭通道。
	log.Info("服务已优雅关闭")
}

// initSeedFiles 扫描目录下文件并通过标准上传流程导入（幂等）。
func initSeedFiles(ctx context.Context, dir string, userRepo repository.UserRepository, uploadSvc service.UploadService) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		log.Infof("initSeedFiles: 目录 '%s' 不存在或不可用，跳过初始化导入", dir)
		return
	}

	// 选择归属用户：优先 admin，不存在则取第一个
	var ownerUserID uint
	var ownerOrg string
	if admin, err := userRepo.FindByUsername("admin"); err == nil && admin != nil {
		ownerUserID = admin.ID
		ownerOrg = admin.PrimaryOrg
	} else {
		if users, err := userRepo.FindAll(); err == nil && len(users) > 0 {
			ownerUserID = users[0].ID
			ownerOrg = users[0].PrimaryOrg
		} else {
			log.Warnf("initSeedFiles: 未找到可用用户，跳过初始化导入")
			return
		}
	}

	walkErr := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}

		// 计算 MD5
		f, err := os.Open(path)
		if err != nil {
			log.Warnf("initSeedFiles: 打开文件失败: %s, err=%v", path, err)
			return nil
		}
		h := md5.New()
		size, copyErr := io.Copy(h, f)
		_ = f.Close()
		if copyErr != nil {
			log.Warnf("initSeedFiles: 读取文件失败: %s, err=%v", path, copyErr)
			return nil
		}
		fileMD5 := fmt.Sprintf("%x", h.Sum(nil))
		fileName := info.Name()

		// 幂等检查：已完成则跳过
		if uploaded, ferr := uploadSvc.FastUpload(ctx, fileMD5, ownerUserID); ferr == nil && uploaded {
			log.Infof("initSeedFiles: 已存在，跳过: %s (md5=%s)", fileName, fileMD5)
			return nil
		}

		// 分片上传
		const chunkSize int64 = 5 * 1024 * 1024
		totalChunks := int(math.Ceil(float64(size) / float64(chunkSize)))
		if totalChunks == 0 {
			log.Infof("initSeedFiles: 空文件跳过: %s", path)
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			log.Warnf("initSeedFiles: 重新打开文件失败: %s, err=%v", path, err)
			return nil
		}
		defer file.Close()

		for chunkIndex := 0; chunkIndex < totalChunks; chunkIndex++ {
			offset := int64(chunkIndex) * chunkSize
			if _, err := file.Seek(offset, io.SeekStart); err != nil {
				log.Warnf("initSeedFiles: Seek 失败: %s, chunk=%d, err=%v", path, chunkIndex, err)
				return nil
			}
			toRead := chunkSize
			if offset+toRead > size {
				toRead = size - offset
			}
			buf := make([]byte, toRead)
			if _, err := io.ReadFull(file, buf); err != nil {
				log.Warnf("initSeedFiles: 读取分片失败: %s, chunk=%d, err=%v", path, chunkIndex, err)
				return nil
			}
			// 适配 multipart.File
			cf := &chunkFile{Reader: bytes.NewReader(buf)}

			// 标记 is_public=true（全员可见），org 使用所有者主组织
			if _, _, err := uploadSvc.UploadChunk(ctx, fileMD5, fileName, size, chunkIndex, cf, ownerUserID, ownerOrg, true); err != nil {
				log.Warnf("initSeedFiles: 上传分片失败: %s, chunk=%d, err=%v", path, chunkIndex, err)
				return nil
			}
		}

		if _, err := uploadSvc.MergeChunks(ctx, fileMD5, fileName, ownerUserID); err != nil {
			log.Warnf("initSeedFiles: 合并失败: %s, err=%v", path, err)
			return nil
		}
		log.Infof("initSeedFiles: 导入完成并已触发向量化: %s", fileName)
		return nil
	})
	if walkErr != nil {
		log.Warnf("initSeedFiles: 遍历目录发生错误: %v", walkErr)
	}
}

// chunkFile 适配 bytes.Reader 到 multipart.File 所需接口
type chunkFile struct{ Reader *bytes.Reader }

func (c *chunkFile) Read(p []byte) (int, error)              { return c.Reader.Read(p) }
func (c *chunkFile) ReadAt(p []byte, off int64) (int, error) { return c.Reader.ReadAt(p, off) }
func (c *chunkFile) Seek(offset int64, whence int) (int64, error) {
	return c.Reader.Seek(offset, whence)
}
func (c *chunkFile) Close() error { return nil }
