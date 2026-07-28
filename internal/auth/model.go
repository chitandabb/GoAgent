package auth

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

type Role string

const (
	RoleAnalyst Role = "analyst"
	RoleAdmin   Role = "admin"
)

func (r Role) Valid() bool {
	switch r {
	case RoleAnalyst, RoleAdmin:
		return true
	}
	return false
}

type UserStatus string

const (
	UserStatusActive   UserStatus = "active"
	UserStatusDisabled UserStatus = "disabled"
)

func (us UserStatus) Valid() bool {
	switch us {
	case UserStatusActive, UserStatusDisabled:
		return true
	}
	return false
}

type User struct {
	ID                 uuid.UUID
	Username           string
	DisplayName        string
	PasswordHash       string `json:"-"`
	MustChangePassword bool
	// 可空的数据类型,就用指针,用nil来判断是否为空
	PasswordChangedAt *time.Time
	Role              Role
	Status            UserStatus
	LastLoginAt       *time.Time
	// 值类型为空时会赋成0值
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NormalizeUsername 规范化username, 去除首位空格并转小写
func NormalizeUsername(username string) string {
	return strings.TrimSpace(strings.ToLower(username))
}

// CanLogin 判断用户是否可以登录
func (u User) CanLogin() bool {
	// 只有active返回true
	if u.Status == UserStatusActive {
		return true
	}

	return false
}

// IsAdmin
//
//	@Description: 判断用户是否是管理员
func (u User) IsAdmin() bool {
	if u.Role == RoleAdmin {
		return true
	}
	return false
}

// RequiresPasswordChange
//
//	@Description: 判断用户是否必需更改密码(用于首次登录后)
func (u User) RequiresPasswordChange() bool {
	return u.MustChangePassword
}
