package httptransport

import (
	"context"
	"errors"
	"strings"

	"github.com/chitandabb/GoAgent/internal/apperror"
	"github.com/chitandabb/GoAgent/internal/auth"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type adminUsersUseCase interface {
	List(ctx context.Context, input auth.ListUsersInput) (auth.UserPage, error)
	Create(ctx context.Context, input auth.CreateUserInput) (auth.User, error)
	SetStatus(ctx context.Context, actorID, userID uuid.UUID, status auth.UserStatus) error
	SetRole(ctx context.Context, actorID, userID uuid.UUID, role auth.Role) error
	ResetPassword(ctx context.Context, actorID, userID uuid.UUID, temporaryPassword string) error
}

// AdminUsersRoutes 提供管理员用户管理接口。
type AdminUsersRoutes struct {
	useCase adminUsersUseCase
	auth    gin.HandlerFunc
	csrf    gin.HandlerFunc
}

// NewAdminUsersRoutes 创建管理员用户路由。
func NewAdminUsersRoutes(
	useCase adminUsersUseCase,
	authMiddleware gin.HandlerFunc,
	csrfMiddleware gin.HandlerFunc,
) (*AdminUsersRoutes, error) {
	if useCase == nil || authMiddleware == nil || csrfMiddleware == nil {
		return nil, errors.New("admin users route dependencies are nil")
	}
	return &AdminUsersRoutes{useCase: useCase, auth: authMiddleware, csrf: csrfMiddleware}, nil
}

// Register 在 /api/v1/admin/users 下注册用户管理接口。
func (r *AdminUsersRoutes) Register(api *gin.RouterGroup) {
	group := api.Group("/admin/users")
	group.Use(r.auth, r.requireAdmin())
	group.GET("", r.list)

	commands := group.Group("")
	commands.Use(r.csrf)
	commands.POST("", r.create)
	commands.PATCH("/:userId", r.patch)
	commands.POST("/:userId/reset-password", r.resetPassword)
}

func (r *AdminUsersRoutes) requireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		identity, ok := identityFromContext(c)
		if !ok {
			AbortWithError(c, apperror.New(apperror.CodeUnauthorized))
			return
		}
		if !identity.User.IsAdmin() {
			AbortWithError(c, apperror.New(apperror.CodeForbidden))
			return
		}
		c.Next()
	}
}

type adminUserListQuery struct {
	PageQuery
	Status string `form:"status"`
	Role   string `form:"role"`
}

type adminUserCreateRequest struct {
	Username           string   `json:"username" binding:"required,max=64"`
	DisplayName        string   `json:"displayName" binding:"required,max=128"`
	Role               auth.Role `json:"role" binding:"required"`
	TemporaryPassword  string   `json:"temporaryPassword" binding:"required,max=256"`
}

type adminUserPatchRequest struct {
	Status *auth.UserStatus `json:"status"`
	Role   *auth.Role       `json:"role"`
}

type adminUserResetPasswordRequest struct {
	TemporaryPassword string `json:"temporaryPassword" binding:"required,max=256"`
}

type adminUserResponse struct {
	ID                 string           `json:"id"`
	Username           string           `json:"username"`
	DisplayName        string           `json:"displayName"`
	Role               auth.Role        `json:"role"`
	Status             auth.UserStatus  `json:"status"`
	MustChangePassword bool             `json:"mustChangePassword"`
	LastLoginAt        *string          `json:"lastLoginAt,omitempty"`
	CreatedAt          string           `json:"createdAt"`
	UpdatedAt          string           `json:"updatedAt"`
}

type adminUserListResponse struct {
	Items    []adminUserResponse `json:"items"`
	Page     int                 `json:"page"`
	PageSize int                 `json:"pageSize"`
	Total    int64               `json:"total"`
}

func (r *AdminUsersRoutes) list(c *gin.Context) {
	query, ok := BindQuery[adminUserListQuery](c)
	if !ok {
		return
	}
	query.Normalize()
	var status *auth.UserStatus
	if value := strings.TrimSpace(query.Status); value != "" {
		parsed := auth.UserStatus(value)
		status = &parsed
	}
	var role *auth.Role
	if value := strings.TrimSpace(query.Role); value != "" {
		parsed := auth.Role(value)
		role = &parsed
	}
	page, err := r.useCase.List(c.Request.Context(), auth.ListUsersInput{
		Status: status, Role: role, Page: query.Page, PageSize: query.PageSize,
	})
	if err != nil {
		AbortWithError(c, err)
		return
	}
	items := make([]adminUserResponse, 0, len(page.Items))
	for _, user := range page.Items {
		items = append(items, adminUserResponseFrom(user))
	}
	WriteSuccess(c, adminUserListResponse{
		Items: items, Page: page.Page, PageSize: page.PageSize, Total: page.Total,
	})
}

func (r *AdminUsersRoutes) create(c *gin.Context) {
	request, ok := BindJSON[adminUserCreateRequest](c)
	if !ok {
		return
	}
	user, err := r.useCase.Create(c.Request.Context(), auth.CreateUserInput{
		Username:    request.Username,
		DisplayName: request.DisplayName,
		Password:    request.TemporaryPassword,
		Role:        request.Role,
	})
	if err != nil {
		AbortWithError(c, err)
		return
	}
	WriteSuccessWithStatus(c, 201, adminUserResponseFrom(user))
}

func (r *AdminUsersRoutes) patch(c *gin.Context) {
	userID, ok := parseUserIDParam(c)
	if !ok {
		return
	}
	request, ok := BindJSON[adminUserPatchRequest](c)
	if !ok {
		return
	}
	if request.Status == nil && request.Role == nil {
		AbortWithError(c, apperror.NewWithFields(apperror.CodeValidationFailed, []apperror.FieldError{{
			Field: "status", Reason: "status 与 role 至少需要提供一项",
		}}))
		return
	}
	identity, ok := identityFromContext(c)
	if !ok {
		AbortWithError(c, apperror.New(apperror.CodeUnauthorized))
		return
	}
	var statusErr error
	if request.Status != nil {
		statusErr = r.useCase.SetStatus(c.Request.Context(), identity.User.ID, userID, *request.Status)
	}
	if statusErr == nil && request.Role != nil {
		statusErr = r.useCase.SetRole(c.Request.Context(), identity.User.ID, userID, *request.Role)
	}
	if statusErr != nil {
		AbortWithError(c, statusErr)
		return
	}
	WriteSuccess(c, gin.H{"updated": true})
}

func (r *AdminUsersRoutes) resetPassword(c *gin.Context) {
	userID, ok := parseUserIDParam(c)
	if !ok {
		return
	}
	request, ok := BindJSON[adminUserResetPasswordRequest](c)
	if !ok {
		return
	}
	identity, ok := identityFromContext(c)
	if !ok {
		AbortWithError(c, apperror.New(apperror.CodeUnauthorized))
		return
	}
	if err := r.useCase.ResetPassword(
		c.Request.Context(), identity.User.ID, userID, request.TemporaryPassword,
	); err != nil {
		AbortWithError(c, err)
		return
	}
	WriteSuccess(c, gin.H{"reset": true})
}

func parseUserIDParam(c *gin.Context) (uuid.UUID, bool) {
	userID, err := uuid.Parse(c.Param("userId"))
	if err != nil {
		AbortWithError(c, apperror.NewWithFields(apperror.CodeInvalidArgument, []apperror.FieldError{{
			Field: "userId", Reason: "必须是合法的 UUID",
		}}))
		return uuid.Nil, false
	}
	return userID, true
}

func adminUserResponseFrom(user auth.User) adminUserResponse {
	response := adminUserResponse{
		ID:                 user.ID.String(),
		Username:           user.Username,
		DisplayName:        user.DisplayName,
		Role:               user.Role,
		Status:             user.Status,
		MustChangePassword: user.MustChangePassword,
		CreatedAt:          user.CreatedAt.UTC().Format(timeRFC3339Nano),
		UpdatedAt:          user.UpdatedAt.UTC().Format(timeRFC3339Nano),
	}
	if user.LastLoginAt != nil {
		value := user.LastLoginAt.UTC().Format(timeRFC3339Nano)
		response.LastLoginAt = &value
	}
	return response
}
