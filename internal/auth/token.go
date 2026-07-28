package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

const tokenEntropyBytes = 32

// Token 同时携带只返回给客户端一次的原始值，以及只保存到数据库的摘要。
type Token struct {
	Raw  string
	Hash []byte
}

// TokenIssuer 隔离随机令牌生成实现，认证用例可以在测试中注入可预测结果。
type TokenIssuer interface {
	Generate() (Token, error)
}

// TokenGenerator 生成 Session Cookie 和 CSRF 使用的高熵随机令牌。
type TokenGenerator struct {
	random io.Reader
}

var _ TokenIssuer = (*TokenGenerator)(nil)

// NewTokenGenerator 使用操作系统安全随机源创建令牌生成器。
func NewTokenGenerator() *TokenGenerator {
	return &TokenGenerator{random: rand.Reader}
}

func newTokenGenerator(random io.Reader) (*TokenGenerator, error) {
	if random == nil {
		return nil, errors.New("token random source is nil")
	}
	return &TokenGenerator{random: random}, nil
}

// Generate 生成 URL-safe 原始令牌，并计算用于持久化查询的 SHA-256 摘要。
func (g *TokenGenerator) Generate() (Token, error) {
	rawBytes := make([]byte, tokenEntropyBytes)
	if _, err := io.ReadFull(g.random, rawBytes); err != nil {
		return Token{}, fmt.Errorf("read random token: %w", err)
	}
	raw := base64.RawURLEncoding.EncodeToString(rawBytes)
	return Token{Raw: raw, Hash: HashToken(raw)}, nil
}

// HashToken 把客户端提交的原始令牌转换成固定长度数据库查询键。
func HashToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}
