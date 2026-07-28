package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// PasswordHasher 隔离具体密码哈希算法，登录和改密用例只依赖该接口。
type PasswordHasher interface {
	Hash(password string) (string, error)
	Verify(password, encodedHash string) (bool, error)
	NeedsRehash(encodedHash string) (bool, error)
}

// Argon2idParams 使用 KiB 表示内存成本，其他长度使用字节。
type Argon2idParams struct {
	Memory      uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

// DefaultArgon2idParams 返回当前账号密码的默认成本参数。
// 64 MiB、1 次迭代和 4 路并行来自 x/crypto/argon2 的非交互式示例基线。
func DefaultArgon2idParams() Argon2idParams {
	return Argon2idParams{
		Memory:      64 * 1024,
		Iterations:  1,
		Parallelism: 4,
		SaltLength:  16,
		KeyLength:   32,
	}
}

// Argon2idHasher 使用 PHC 字符串保存算法版本、成本参数、盐和派生密钥。
type Argon2idHasher struct {
	params Argon2idParams
	random io.Reader
}

var _ PasswordHasher = (*Argon2idHasher)(nil)

// NewArgon2idHasher 使用安全随机源创建密码哈希器。
func NewArgon2idHasher(params Argon2idParams) (*Argon2idHasher, error) {
	return newArgon2idHasher(params, rand.Reader)
}

func newArgon2idHasher(params Argon2idParams, random io.Reader) (*Argon2idHasher, error) {
	if err := validateArgon2idParams(params); err != nil {
		return nil, err
	}
	if random == nil {
		return nil, errors.New("argon2id random source is nil")
	}
	return &Argon2idHasher{params: params, random: random}, nil
}

// Hash 为每个密码生成独立随机盐，并返回可持久化的 PHC 字符串。
func (h *Argon2idHasher) Hash(password string) (string, error) {
	salt := make([]byte, h.params.SaltLength)
	if _, err := io.ReadFull(h.random, salt); err != nil {
		return "", fmt.Errorf("read argon2id salt: %w", err)
	}
	key := deriveArgon2id(password, salt, h.params)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		h.params.Memory,
		h.params.Iterations,
		h.params.Parallelism,
		b64.EncodeToString(salt),
		b64.EncodeToString(key),
	), nil
}

// Verify 解析已保存参数并使用常量时间比较派生密钥。
func (h *Argon2idHasher) Verify(password, encodedHash string) (bool, error) {
	parsed, err := parseArgon2idHash(encodedHash)
	if err != nil {
		return false, err
	}
	actual := deriveArgon2id(password, parsed.salt, parsed.params)
	return subtle.ConstantTimeCompare(actual, parsed.key) == 1, nil
}

// NeedsRehash 判断已保存哈希是否仍使用当前成本参数。
func (h *Argon2idHasher) NeedsRehash(encodedHash string) (bool, error) {
	parsed, err := parseArgon2idHash(encodedHash)
	if err != nil {
		return false, err
	}
	return parsed.params != h.params, nil
}

type parsedArgon2idHash struct {
	params Argon2idParams
	salt   []byte
	key    []byte
}

func parseArgon2idHash(encoded string) (parsedArgon2idHash, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return parsedArgon2idHash{}, errors.New("invalid argon2id hash format")
	}
	version, err := parseUintField(parts[2], "v", 8)
	if err != nil || int(version) != argon2.Version {
		return parsedArgon2idHash{}, errors.New("unsupported argon2id version")
	}

	params, err := parseArgon2idParams(parts[3])
	if err != nil {
		return parsedArgon2idHash{}, err
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return parsedArgon2idHash{}, errors.New("invalid argon2id salt encoding")
	}
	key, err := b64.DecodeString(parts[5])
	if err != nil {
		return parsedArgon2idHash{}, errors.New("invalid argon2id key encoding")
	}
	params.SaltLength = uint32(len(salt))
	params.KeyLength = uint32(len(key))
	if err := validateArgon2idParams(params); err != nil {
		return parsedArgon2idHash{}, fmt.Errorf("invalid encoded argon2id parameters: %w", err)
	}
	return parsedArgon2idHash{params: params, salt: salt, key: key}, nil
}

func parseArgon2idParams(encoded string) (Argon2idParams, error) {
	fields := strings.Split(encoded, ",")
	if len(fields) != 3 {
		return Argon2idParams{}, errors.New("invalid argon2id parameter format")
	}
	memory, err := parseUintField(fields[0], "m", 32)
	if err != nil {
		return Argon2idParams{}, err
	}
	iterations, err := parseUintField(fields[1], "t", 32)
	if err != nil {
		return Argon2idParams{}, err
	}
	parallelism, err := parseUintField(fields[2], "p", 8)
	if err != nil {
		return Argon2idParams{}, err
	}
	return Argon2idParams{
		Memory:      uint32(memory),
		Iterations:  uint32(iterations),
		Parallelism: uint8(parallelism),
	}, nil
}

func parseUintField(encoded, name string, bitSize int) (uint64, error) {
	prefix := name + "="
	if !strings.HasPrefix(encoded, prefix) {
		return 0, fmt.Errorf("missing argon2id parameter %s", name)
	}
	value, err := strconv.ParseUint(strings.TrimPrefix(encoded, prefix), 10, bitSize)
	if err != nil {
		return 0, fmt.Errorf("parse argon2id parameter %s: %w", name, err)
	}
	return value, nil
}

func validateArgon2idParams(params Argon2idParams) error {
	if params.Memory < 8*1024 || params.Memory > 256*1024 {
		return errors.New("argon2id memory must be between 8 MiB and 256 MiB")
	}
	if params.Iterations < 1 || params.Iterations > 10 {
		return errors.New("argon2id iterations must be between 1 and 10")
	}
	if params.Parallelism < 1 || params.Parallelism > 32 {
		return errors.New("argon2id parallelism must be between 1 and 32")
	}
	if params.SaltLength < 16 || params.SaltLength > 64 {
		return errors.New("argon2id salt length must be between 16 and 64 bytes")
	}
	if params.KeyLength < 16 || params.KeyLength > 64 {
		return errors.New("argon2id key length must be between 16 and 64 bytes")
	}
	return nil
}

func deriveArgon2id(password string, salt []byte, params Argon2idParams) []byte {
	return argon2.IDKey(
		[]byte(password),
		salt,
		params.Iterations,
		params.Memory,
		params.Parallelism,
		params.KeyLength,
	)
}
