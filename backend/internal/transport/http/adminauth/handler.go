package adminauth

import (
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	adminapp "github.com/chenyme/grok2api/backend/internal/application/adminauth"
	admindomain "github.com/chenyme/grok2api/backend/internal/domain/admin"
	"github.com/chenyme/grok2api/backend/internal/shared/response"
	"github.com/chenyme/grok2api/backend/internal/transport/http/middleware"
	"github.com/gin-gonic/gin"
)

const refreshCookieName = "grok2api_admin_refresh"

type Handler struct {
	service       *adminapp.Service
	secureCookies bool
	// trustProxyHeaders 为 true 时使用 gin ClientIP（依赖引擎上的 TrustedProxies）。
	trustProxyHeaders bool
}

func NewHandler(service *adminapp.Service, secureCookies bool) *Handler {
	return &Handler{service: service, secureCookies: secureCookies}
}

// SetTrustProxyHeaders 控制是否从受信代理头解析客户端 IP（用于免密登录白名单）。
func (h *Handler) SetTrustProxyHeaders(enabled bool) {
	h.trustProxyHeaders = enabled
}

func (h *Handler) RegisterPublic(router *gin.RouterGroup) {
	router.POST("/auth/login", h.login)
	router.POST("/auth/trusted-login", h.trustedLogin)
	router.POST("/auth/refresh", h.refresh)
	router.POST("/auth/logout", h.logout)
}

func (h *Handler) RegisterAuthenticated(router *gin.RouterGroup) {
	router.GET("/me", h.me)
	router.PUT("/me/password", h.changePassword)
}

type loginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

type refreshRequest struct {
	RefreshToken string `json:"refreshToken"`
}

type changePasswordRequest struct {
	CurrentPassword string `json:"currentPassword" binding:"required"`
	NewPassword     string `json:"newPassword" binding:"required"`
}

type tokenResponse struct {
	AccessToken           string `json:"accessToken"`
	AccessTokenExpiresAt  string `json:"accessTokenExpiresAt"`
	RefreshTokenExpiresAt string `json:"refreshTokenExpiresAt"`
}

type adminResponse struct {
	ID       uint64 `json:"id,string"`
	Username string `json:"username"`
}

func (h *Handler) login(c *gin.Context) {
	var request loginRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		response.Error(c, http.StatusBadRequest, "invalidRequest", "请求参数无效")
		return
	}
	adminValue, tokens, err := h.service.Login(c.Request.Context(), request.Username, request.Password, h.clientIP(c))
	if err != nil {
		if errors.Is(err, adminapp.ErrLoginRateLimited) {
			response.Error(c, http.StatusTooManyRequests, "loginRateLimited", "登录尝试过于频繁，请稍后重试")
			return
		}
		if errors.Is(err, adminapp.ErrRuntimeUnavailable) {
			response.Error(c, http.StatusServiceUnavailable, "authRuntimeUnavailable", "管理员认证服务暂不可用")
			return
		}
		response.Error(c, http.StatusUnauthorized, "invalidCredentials", "管理员账号或密码错误")
		return
	}
	h.setRefreshCookie(c, tokens)
	response.Success(c, http.StatusOK, gin.H{"admin": newAdminResponse(adminValue), "tokens": newTokenResponse(tokens)})
}

func (h *Handler) trustedLogin(c *gin.Context) {
	adminValue, tokens, err := h.service.LoginByTrustedIP(c.Request.Context(), h.clientIP(c))
	if err != nil {
		if errors.Is(err, adminapp.ErrLoginRateLimited) {
			response.Error(c, http.StatusTooManyRequests, "loginRateLimited", "登录尝试过于频繁，请稍后重试")
			return
		}
		if errors.Is(err, adminapp.ErrRuntimeUnavailable) {
			response.Error(c, http.StatusServiceUnavailable, "authRuntimeUnavailable", "管理员认证服务暂不可用")
			return
		}
		if errors.Is(err, adminapp.ErrTrustedLoginNoAdmin) {
			response.Error(c, http.StatusServiceUnavailable, "adminNotReady", "管理员账号尚未初始化")
			return
		}
		response.Error(c, http.StatusUnauthorized, "trustedLoginDenied", "当前 IP 不允许免密登录")
		return
	}
	h.setRefreshCookie(c, tokens)
	response.Success(c, http.StatusOK, gin.H{"admin": newAdminResponse(adminValue), "tokens": newTokenResponse(tokens)})
}

func (h *Handler) clientIP(c *gin.Context) string {
	if h.trustProxyHeaders {
		if ip := strings.TrimSpace(c.ClientIP()); ip != "" {
			return ip
		}
	}
	return remoteAddress(c.Request)
}

func remoteAddress(request *http.Request) string {
	value := strings.TrimSpace(request.RemoteAddr)
	host, _, err := net.SplitHostPort(value)
	if err == nil && host != "" {
		return host
	}
	return value
}

func (h *Handler) refresh(c *gin.Context) {
	var request refreshRequest
	if err := c.ShouldBindJSON(&request); err != nil && !errors.Is(err, io.EOF) {
		response.Error(c, http.StatusBadRequest, "invalidRequest", "请求参数无效")
		return
	}
	if request.RefreshToken == "" {
		request.RefreshToken, _ = c.Cookie(refreshCookieName)
	}
	if request.RefreshToken == "" {
		response.Error(c, http.StatusUnauthorized, "invalidRefreshToken", "刷新会话无效")
		return
	}
	tokens, err := h.service.Refresh(c.Request.Context(), request.RefreshToken)
	if err != nil {
		if errors.Is(err, adminapp.ErrRuntimeUnavailable) {
			response.Error(c, http.StatusServiceUnavailable, "authRuntimeUnavailable", "管理员认证服务暂不可用")
			return
		}
		response.Error(c, http.StatusUnauthorized, "invalidRefreshToken", "刷新会话无效")
		return
	}
	h.setRefreshCookie(c, tokens)
	response.Success(c, http.StatusOK, newTokenResponse(tokens))
}

func (h *Handler) logout(c *gin.Context) {
	var request refreshRequest
	if err := c.ShouldBindJSON(&request); err != nil && !errors.Is(err, io.EOF) {
		response.Error(c, http.StatusBadRequest, "invalidRequest", "请求参数无效")
		return
	}
	if request.RefreshToken == "" {
		request.RefreshToken, _ = c.Cookie(refreshCookieName)
	}
	if err := h.service.Logout(c.Request.Context(), request.RefreshToken); err != nil {
		response.Error(c, http.StatusServiceUnavailable, "authRuntimeUnavailable", "管理员认证服务暂不可用")
		return
	}
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(refreshCookieName, "", -1, "/api/admin/v1/auth", "", h.secureCookies || c.Request.TLS != nil, true)
	response.Success(c, http.StatusOK, gin.H{"loggedOut": true})
}

func (h *Handler) me(c *gin.Context) {
	value, ok := c.Get(middleware.AdminKey)
	adminValue, valid := value.(admindomain.Admin)
	if !ok || !valid {
		response.Error(c, http.StatusUnauthorized, "adminUnauthorized", "管理员登录已失效")
		return
	}
	response.Success(c, http.StatusOK, newAdminResponse(adminValue))
}

func (h *Handler) changePassword(c *gin.Context) {
	var request changePasswordRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		response.Error(c, http.StatusBadRequest, "invalidRequest", "请求参数无效")
		return
	}
	value, ok := c.Get(middleware.AdminKey)
	adminValue, valid := value.(admindomain.Admin)
	if !ok || !valid {
		response.Error(c, http.StatusUnauthorized, "adminUnauthorized", "管理员登录已失效")
		return
	}
	if err := h.service.ChangePassword(c.Request.Context(), adminValue.ID, request.CurrentPassword, request.NewPassword); err != nil {
		if errors.Is(err, adminapp.ErrInvalidCredentials) || errors.Is(err, adminapp.ErrInvalidPassword) {
			response.Error(c, http.StatusBadRequest, "passwordChangeFailed", err.Error())
			return
		}
		response.Error(c, http.StatusInternalServerError, "passwordChangeFailed", "修改管理员密码失败")
		return
	}
	response.Success(c, http.StatusOK, gin.H{"passwordChanged": true})
}

func newAdminResponse(value admindomain.Admin) adminResponse {
	return adminResponse{ID: value.ID, Username: value.Username}
}

func newTokenResponse(value adminapp.Tokens) tokenResponse {
	return tokenResponse{AccessToken: value.AccessToken, AccessTokenExpiresAt: value.AccessTokenExpiresAt.Format(time.RFC3339), RefreshTokenExpiresAt: value.RefreshTokenExpiresAt.Format(time.RFC3339)}
}

func (h *Handler) setRefreshCookie(c *gin.Context, value adminapp.Tokens) {
	maxAge := int(time.Until(value.RefreshTokenExpiresAt).Seconds())
	if maxAge < 0 {
		maxAge = 0
	}
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(refreshCookieName, value.RefreshToken, maxAge, "/api/admin/v1/auth", "", h.secureCookies || c.Request.TLS != nil, true)
}
