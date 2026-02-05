// Package token 提供了用于生成和验证 JSON Web Tokens (JWT) 的功能。
package token

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/golang-jwt/jwt/v5"
	"time"
)

// JWTManager 负责管理 JWT 的生成和验证。
type JWTManager struct {
	secretKey       []byte        // secretKey 用于签名和验证 token 的密钥
	accessTokenDur  time.Duration // accessTokenDur 定义了 access token 的有效期
	refreshTokenDur time.Duration // refreshTokenDur 定义了 refresh token 的有效期
}

// CustomClaims 定义了我们想要在 JWT 中存储的自定义数据。
// 它嵌入了 jwt.RegisteredClaims 以包含标准的 JWT 声明（如过期时间）。
type CustomClaims struct {
	UserID   uint   `json:"userId"`
	Username string `json:"username"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

// NewJWTManager 创建一个新的 JWTManager 实例。
// secret: 用于签名的密钥字符串。
// accessTokenExpireHours: access token 的过期时间（小时）。
// refreshTokenExpireDays: refresh token 的过期时间（天）。
func NewJWTManager(secret string, accessTokenExpireHours, refreshTokenExpireDays int) *JWTManager {
	return &JWTManager{
		secretKey:       []byte(secret),
		accessTokenDur:  time.Hour * time.Duration(accessTokenExpireHours),
		refreshTokenDur: time.Duration(refreshTokenExpireDays) * 24 * time.Hour,
	}
}

// GenerateToken 根据给定的用户信息生成一个新的 access token。
func (m *JWTManager) GenerateToken(userID uint, username, role string) (string, error) {
	// 创建 claims，包含自定义数据和标准过期时间
	claims := CustomClaims{
		UserID:   userID,
		Username: username,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(m.accessTokenDur)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now()),
		},
	}
	// 使用 HS256 签名方法创建新的 token 对象
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	// 使用密钥签名 token 并返回字符串形式
	return token.SignedString(m.secretKey)
}

// GenerateRefreshToken 根据给定的用户信息生成一个新的 refresh token。
// 它的工作方式与 GenerateToken 类似，但使用更长的过期时间。
func (m *JWTManager) GenerateRefreshToken(userID uint, username, role string) (string, error) {
	claims := CustomClaims{
		UserID:   userID,
		Username: username,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(m.refreshTokenDur)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(m.secretKey)
}

// VerifyToken 验证给定的 token 字符串。
// 如果 token 有效，它会返回 CustomClaims 对象。
// 如果 token 无效（例如，签名不匹配或已过期），则返回错误。
func (m *JWTManager) VerifyToken(tokenString string) (*CustomClaims, error) {
	// 解析 token 字符串
	token, err := jwt.ParseWithClaims(tokenString, &CustomClaims{}, func(token *jwt.Token) (interface{}, error) {
		// 检查签名方法是否为 HMAC
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		// 返回密钥用于验证
		return m.secretKey, nil
	})

	if err != nil {
		return nil, err
	}

	// 从解析后的 token 中提取 claims
	if claims, ok := token.Claims.(*CustomClaims); ok && token.Valid {
		return claims, nil
	}

	return nil, errors.New("invalid token")
}

// GenerateRandomString generates a random hex string of a given length.
func GenerateRandomString(length int) string {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		// Fallback to a less random string on error
		return fmt.Sprintf("fallback%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(bytes)
}
