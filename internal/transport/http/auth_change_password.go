package httptransport

import (
	"context"

	"github.com/chitandabb/GoAgent/internal/apperror"
	"github.com/chitandabb/GoAgent/internal/auth"

	"github.com/gin-gonic/gin"
)

type changePasswordUseCase interface {
	ChangePassword(ctx context.Context, input auth.ChangePasswordInput) error
}

type changePasswordRequest struct {
	CurrentPassword string `json:"currentPassword" binding:"required,max=256"`
	NewPassword     string `json:"newPassword" binding:"required,max=256"`
}

// changePasswordHandler 处理改密命令。成功即撤销该用户全部会话，
// 前端应清除本地身份并引导重新登录。
func (r *AuthRoutes) changePasswordHandler(c *gin.Context) {
	request, ok := BindJSON[changePasswordRequest](c)
	if !ok {
		return
	}
	identity, ok := identityFromContext(c)
	if !ok {
		AbortWithError(c, apperror.New(apperror.CodeUnauthorized))
		return
	}
	if r.changePassword == nil {
		AbortWithError(c, apperror.New(apperror.CodeDependencyUnavailable))
		return
	}
	if err := r.changePassword.ChangePassword(c.Request.Context(), auth.ChangePasswordInput{
		UserID:          identity.User.ID,
		CurrentPassword: request.CurrentPassword,
		NewPassword:     request.NewPassword,
	}); err != nil {
		AbortWithError(c, err)
		return
	}
	WriteSuccess(c, gin.H{"changed": true})
}
