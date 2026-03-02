// Package middleware 提供了处理 HTTP 请求的中间件。
// 当前包含三个中间件：
//   - AuthMiddleware：JWT 认证（所有需要登录的接口都要经过它）
//   - AdminAuthMiddleware：管理员权限校验（在 AuthMiddleware 之后执行）
//   - RequestLogger：请求日志记录
package middleware

import (
	"net/http"
	"pai-smart-go/internal/service"
	"pai-smart-go/pkg/database"
	"pai-smart-go/pkg/log"
	"pai-smart-go/pkg/token"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// AuthMiddleware 是 JWT 认证中间件，所有需要登录的接口都挂载它。
//
// ┌─────────────────────────────────────────────────────────────────────┐
// │                   一次请求经过此中间件的完整流程                      │
// │                                                                     │
// │  HTTP 请求到达                                                       │
// │       │                                                             │
// │       ▼                                                             │
// │  读取 Authorization 头                                               │
// │       │                                                             │
// │  为空？──是──▶ 401 Unauthorized，AbortWithStatusJSON，请求终止        │
// │       │ 否                                                           │
// │       ▼                                                             │
// │  格式是 "Bearer <token>"？                                           │
// │  不是？──▶ 401，请求终止                                             │
// │       │ 是                                                           │
// │       ▼                                                             │
// │  jwtManager.VerifyToken(tokenString)                                │
// │  验证失败？──▶ 尝试 X-Refresh-Token 头（只是日志提示，不会自动续签）   │
// │            ──▶ 401，请求终止                                         │
// │       │ 验证成功                                                     │
// │       ▼                                                             │
// │  userService.GetProfile(claims.Username)   ← 去 MySQL 查完整用户信息 │
// │  用户不存在？──▶ 401，请求终止                                        │
// │       │ 存在                                                         │
// │       ▼                                                             │
// │  c.Set("user", user)    ← 把 User 对象放进 gin.Context              │
// │  c.Set("claims", claims)                                            │
// │       │                                                             │
// │  c.Next()  ← 放行，进入下一个中间件或 Handler                         │
// └─────────────────────────────────────────────────────────────────────┘
//
// 参数说明：
//   - jwtManager：负责 token 的生成与验证（签名算法、过期判断）
//   - userService：用于查完整 User 对象（包含 OrgTags、Role 等）
//
// 调用方（后续 Handler）如何取用户：
//
//	user, _ := c.Get("user")
//	currentUser := user.(*model.User)
func AuthMiddleware(jwtManager *token.JWTManager, userService service.UserService) gin.HandlerFunc {
	return func(c *gin.Context) {

		// ── 步骤 1：提取 Authorization 请求头 ──────────────────────────────
		// 标准格式：Authorization: Bearer eyJhbGciOiJIUzI1NiIs...
		// 没有这个头 = 没有登录凭证，直接拒绝
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "请求未包含授权头"})
			return
		}

		// ── 步骤 2：校验格式并提取纯 token 字符串 ─────────────────────────
		// "Bearer " 是 OAuth2 规范要求的前缀，必须带上
		// 去掉前缀后 tokenString 就是原始的 JWT（三段 base64 用点分隔）
		const bearerPrefix = "Bearer "
		if !strings.HasPrefix(authHeader, bearerPrefix) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "无效的授权头格式"})
			return
		}
		tokenString := strings.TrimPrefix(authHeader, bearerPrefix)

		// ── 步骤 3：检查 token 是否已被登出（Redis 黑名单） ─────────────
		// Logout 时会将 token 写入 Redis key "blacklist:<token>"，过期时间与 token 剩余有效期一致。
		// 这里必须在签名验证之前检查，否则已登出但未过期的 token 仍能通过认证。
		blacklistKey := "blacklist:" + tokenString
		exists, err := database.RDB.Exists(c.Request.Context(), blacklistKey).Result()
		if err != nil {
			log.Errorf("检查 token 黑名单失败: %v", err)
			// Redis 异常时放行，避免 Redis 故障导致全站不可用（降级策略）
		} else if exists > 0 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "token 已失效，请重新登录"})
			return
		}

		// ── 步骤 4：验证 JWT 签名和过期时间 ──────────────────────────────
		// VerifyToken 会做两件事：
		//   ① 用密钥验证签名（防止伪造 token）
		//   ② 检查 Claims.ExpiresAt 是否过期
		// 返回的 claims 里包含：UserID / Username / Role / ExpiresAt
		claims, err := jwtManager.VerifyToken(tokenString)
		if err != nil {
			// token 验证失败（过期或签名错误）
			// 这里有一个辅助逻辑：
			//   如果请求头还带了 X-Refresh-Token，说明前端可能想静默续签。
			//   当前实现只打日志提示，不自动续签；正式刷新入口是 POST /auth/refreshToken。
			refresh := c.GetHeader("X-Refresh-Token")
			if refresh != "" {
				if rclaims, rerr := jwtManager.VerifyToken(refresh); rerr == nil {
					if time.Until(rclaims.ExpiresAt.Time) > 0 {
						// refresh token 还有效，前端可以去调 /auth/refreshToken 换新 access token
						log.Infof("检测到过期 access，存在仍有效的 refresh，可引导刷新")
					}
				}
			}
			// 无论 refresh 情况如何，当前请求没有有效 access token，一律 401
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "无效或已过期的 token"})
			return
		}

		// ── 步骤 5：从数据库拉取完整用户信息 ─────────────────────────────
		// 为什么不直接用 claims 里的数据？
		//   claims 里只有 UserID / Username / Role，这是签发 token 时写进去的快照。
		//   但用户的 OrgTags、PrimaryOrg 可能在 token 签发后被管理员修改。
		//   查库可以拿到最新数据，确保权限判断基于实时状态。
		//   如果用户已被删除，这里会返回错误，直接拒绝访问。
		user, err := userService.GetProfile(claims.Username)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "用户不存在"})
			return
		}

		// ── 步骤 6：把用户信息注入 gin.Context，供后续 Handler 使用 ───────
		// Key "user"：存储 *model.User，Handler 里直接取完整用户对象
		// Key "claims"：存储 JWT Claims，偶尔需要原始 token 信息时用
		c.Set("user", user)
		c.Set("claims", claims)

		// ── 步骤 7：放行，进入下一个中间件或 Handler ─────────────────────
		c.Next()
	}
}
