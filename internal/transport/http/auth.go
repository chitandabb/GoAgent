package httptransport

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/chitandabb/GoAgent/internal/apperror"
	"github.com/chitandabb/GoAgent/internal/auth"

	"github.com/gin-gonic/gin"
)

const (
	sessionCookieName = "mesguard_session"
	csrfCookieName    = "mesguard_csrf"
	csrfHeaderName    = "X-CSRF-Token"
	identityKey       = "authIdentity"
)

type loginUseCase interface {
	Login(ctx context.Context, input auth.LoginInput) (auth.LoginResult, error)
}

type sessionUseCase interface {
	Authenticate(ctx context.Context, rawToken string) (auth.Identity, error)
	ValidateCSRF(identity auth.Identity, rawToken string) error
	Logout(ctx context.Context, identity auth.Identity) error
}

// CookieSettings 集中定义认证 Cookie 的部署属性。
type CookieSettings struct {
	Domain string
	Secure bool
}

// AuthRoutes 装配认证 Handler 和安全中间件。
type AuthRoutes struct {
	login          loginUseCase
	sessions       sessionUseCase
	cookies        CookieSettings
	allowedOrigins map[string]struct{}
}

// NewAuthRoutes 创建认证 HTTP 边界。
func NewAuthRoutes(
	login loginUseCase,
	sessions sessionUseCase,
	cookies CookieSettings,
	allowedOrigins []string,
) (*AuthRoutes, error) {
	if login == nil {
		return nil, errors.New("login use case is nil")
	}
	if sessions == nil {
		return nil, errors.New("session use case is nil")
	}
	if len(allowedOrigins) == 0 {
		return nil, errors.New("allowed origins are empty")
	}
	origins := make(map[string]struct{}, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		origin = strings.TrimSpace(origin)
		if origin == "" {
			return nil, errors.New("allowed origin is empty")
		}
		origins[origin] = struct{}{}
	}
	return &AuthRoutes{login: login, sessions: sessions, cookies: cookies, allowedOrigins: origins}, nil
}

// Register 在 /api/v1/auth 下注册认证接口。
func (r *AuthRoutes) Register(api *gin.RouterGroup) {
	routes := api.Group("/auth")
	routes.POST("/login", r.requireTrustedOrigin(), r.loginHandler)

	protected := routes.Group("")
	protected.Use(r.requireAuthentication())
	protected.GET("/me", r.meHandler)
	routes.POST("/logout", r.requireTrustedOrigin(), r.logoutHandler)
}

type loginRequest struct {
	Username string `json:"username" binding:"required,max=64"`
	Password string `json:"password" binding:"required,max=256"`
}

type authUserResponse struct {
	ID                 string    `json:"id"`
	Username           string    `json:"username"`
	DisplayName        string    `json:"displayName"`
	Role               auth.Role `json:"role"`
	MustChangePassword bool      `json:"mustChangePassword"`
}

type authResponse struct {
	User              authUserResponse `json:"user"`
	CSRFToken         string           `json:"csrfToken"`
	IdleExpiresAt     time.Time        `json:"idleExpiresAt"`
	AbsoluteExpiresAt time.Time        `json:"absoluteExpiresAt"`
}

func (r *AuthRoutes) loginHandler(c *gin.Context) {
	request, ok := BindJSON[loginRequest](c)
	if !ok {
		return
	}
	result, err := r.login.Login(c.Request.Context(), auth.LoginInput{
		Username: request.Username,
		Password: request.Password,
	})
	if err != nil {
		AbortWithError(c, err)
		return
	}
	r.setAuthCookies(c, result.SessionToken, result.CSRFToken, result.AbsoluteExpiresAt)
	WriteSuccess(c, authResponseFrom(result.User, result.CSRFToken, result.IdleExpiresAt, result.AbsoluteExpiresAt))
}

func (r *AuthRoutes) meHandler(c *gin.Context) {
	identity, ok := identityFromContext(c)
	if !ok {
		AbortWithError(c, apperror.New(apperror.CodeUnauthorized))
		return
	}
	csrfToken, err := c.Cookie(csrfCookieName)
	if err != nil || r.sessions.ValidateCSRF(identity, csrfToken) != nil {
		r.clearAuthCookies(c)
		AbortWithError(c, apperror.New(apperror.CodeUnauthorized))
		return
	}
	WriteSuccess(c, authResponseFrom(
		identity.User,
		csrfToken,
		identity.Session.IdleExpiresAt,
		identity.Session.AbsoluteExpiresAt,
	))
}

func (r *AuthRoutes) logoutHandler(c *gin.Context) {
	rawSessionToken, err := c.Cookie(sessionCookieName)
	if err != nil {
		r.clearAuthCookies(c)
		WriteSuccess(c, gin.H{"loggedOut": true})
		return
	}
	identity, err := r.sessions.Authenticate(c.Request.Context(), rawSessionToken)
	if err != nil {
		if apperror.Normalize(err).Code == apperror.CodeUnauthorized {
			r.clearAuthCookies(c)
			WriteSuccess(c, gin.H{"loggedOut": true})
			return
		}
		AbortWithError(c, err)
		return
	}
	csrfCookie, cookieErr := c.Cookie(csrfCookieName)
	csrfHeader := c.GetHeader(csrfHeaderName)
	if cookieErr != nil || !constantTimeStringEqual(csrfCookie, csrfHeader) {
		AbortWithError(c, apperror.New(apperror.CodeForbidden))
		return
	}
	if err := r.sessions.ValidateCSRF(identity, csrfHeader); err != nil {
		AbortWithError(c, err)
		return
	}
	if err := r.sessions.Logout(c.Request.Context(), identity); err != nil {
		AbortWithError(c, err)
		return
	}
	r.clearAuthCookies(c)
	WriteSuccess(c, gin.H{"loggedOut": true})
}

func (r *AuthRoutes) requireAuthentication() gin.HandlerFunc {
	return func(c *gin.Context) {
		rawToken, err := c.Cookie(sessionCookieName)
		if err != nil {
			r.clearAuthCookies(c)
			AbortWithError(c, apperror.New(apperror.CodeUnauthorized))
			return
		}
		identity, err := r.sessions.Authenticate(c.Request.Context(), rawToken)
		if err != nil {
			if apperror.Normalize(err).Code == apperror.CodeUnauthorized {
				r.clearAuthCookies(c)
			}
			AbortWithError(c, err)
			return
		}
		c.Set(identityKey, identity)
		c.Next()
	}
}

func (r *AuthRoutes) requireTrustedOrigin() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := strings.TrimSpace(c.GetHeader("Origin"))
		if _, ok := r.allowedOrigins[origin]; !ok {
			AbortWithError(c, apperror.New(apperror.CodeForbidden))
			return
		}
		c.Next()
	}
}

func (r *AuthRoutes) requireCSRF() gin.HandlerFunc {
	return func(c *gin.Context) {
		identity, ok := identityFromContext(c)
		if !ok {
			AbortWithError(c, apperror.New(apperror.CodeUnauthorized))
			return
		}
		cookieToken, err := c.Cookie(csrfCookieName)
		headerToken := c.GetHeader(csrfHeaderName)
		if err != nil || !constantTimeStringEqual(cookieToken, headerToken) {
			AbortWithError(c, apperror.New(apperror.CodeForbidden))
			return
		}
		if err := r.sessions.ValidateCSRF(identity, headerToken); err != nil {
			AbortWithError(c, err)
			return
		}
		c.Next()
	}
}

func (r *AuthRoutes) setAuthCookies(
	c *gin.Context,
	sessionToken, csrfToken string,
	absoluteExpiresAt time.Time,
) {
	r.setCookie(c, sessionCookieName, sessionToken, absoluteExpiresAt, true, 0)
	r.setCookie(c, csrfCookieName, csrfToken, absoluteExpiresAt, false, 0)
}

func (r *AuthRoutes) clearAuthCookies(c *gin.Context) {
	expired := time.Unix(1, 0).UTC()
	r.setCookie(c, sessionCookieName, "", expired, true, -1)
	r.setCookie(c, csrfCookieName, "", expired, false, -1)
}

func (r *AuthRoutes) setCookie(
	c *gin.Context,
	name, value string,
	expires time.Time,
	httpOnly bool,
	maxAge int,
) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		Domain:   r.cookies.Domain,
		Expires:  expires.UTC(),
		MaxAge:   maxAge,
		Secure:   r.cookies.Secure,
		HttpOnly: httpOnly,
		SameSite: http.SameSiteLaxMode,
	})
}

func authResponseFrom(user auth.User, csrfToken string, idleExpiresAt, absoluteExpiresAt time.Time) authResponse {
	return authResponse{
		User: authUserResponse{
			ID:                 user.ID.String(),
			Username:           user.Username,
			DisplayName:        user.DisplayName,
			Role:               user.Role,
			MustChangePassword: user.MustChangePassword,
		},
		CSRFToken:         csrfToken,
		IdleExpiresAt:     idleExpiresAt,
		AbsoluteExpiresAt: absoluteExpiresAt,
	}
}

func identityFromContext(c *gin.Context) (auth.Identity, bool) {
	value, ok := c.Get(identityKey)
	if !ok {
		return auth.Identity{}, false
	}
	identity, ok := value.(auth.Identity)
	return identity, ok
}

func constantTimeStringEqual(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}
