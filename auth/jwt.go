// Package auth 提供 JWT 签发 / 校验中间件（PRD §11.2 多店后台鉴权）
package auth

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/golang-jwt/jwt/v5"
)

// Claims 自定义 JWT 声明
type Claims struct {
	AdminID uint64 `json:"aid"`
	ShopID  string `json:"sid"`
	Role    string `json:"role"`
	jwt.RegisteredClaims
}

// Sign 签发 token（默认 7 天有效）
func Sign(adminID uint64, shopID, role string) (string, error) {
	secret := secret()
	claims := Claims{
		AdminID: adminID,
		ShopID:  shopID,
		Role:    role,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(7 * 24 * time.Hour)),
			Issuer:    "chatwitheino",
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return tok.SignedString([]byte(secret))
}

// Verify 校验 token，返回 Claims
func Verify(tokenStr string) (*Claims, error) {
	if tokenStr == "" {
		return nil, errors.New("empty token")
	}
	tok, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return []byte(secret()), nil
	})
	if err != nil {
		return nil, err
	}
	c, ok := tok.Claims.(*Claims)
	if !ok || !tok.Valid {
		return nil, errors.New("invalid token")
	}
	return c, nil
}

func secret() string {
	return os.Getenv("JWT_SECRET")
}

// Middleware 校验请求里的 token，把 Claims 放进 ctx
func Middleware() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		// 取 token：Header X-Admin-Token / Authorization: Bearer xxx / query ?token=
		var tok string
		if h := string(c.GetHeader("X-Admin-Token")); h != "" {
			tok = h
		} else if h := string(c.GetHeader("Authorization")); h != "" {
			if strings.HasPrefix(h, "Bearer ") {
				tok = strings.TrimPrefix(h, "Bearer ")
			}
		} else if q := c.Query("token"); q != "" {
			tok = q
		}
		claims, err := Verify(tok)
		if err != nil {
			c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized: " + err.Error()})
			c.Abort()
			return
		}
		// 把 claims 挂到 ctx，后续 handler 通过 GetClaims(ctx) 取
		c.Set("auth_claims", claims)
		c.Next(ctx)
	}
}

// GetClaims 从 ctx 取 claims（无则返回 nil）
func GetClaims(c *app.RequestContext) *Claims {
	v, ok := c.Get("auth_claims")
	if !ok {
		return nil
	}
	cl, _ := v.(*Claims)
	return cl
}