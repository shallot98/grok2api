package adminauth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/chenyme/grok2api/backend/internal/domain/admin"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
	"github.com/chenyme/grok2api/backend/internal/repository"
)

var (
	ErrInvalidCredentials  = errors.New("管理员账号或密码错误")
	ErrInvalidSession      = errors.New("管理员会话无效")
	ErrBootstrapRequired   = errors.New("首次启动需要设置管理员账号和密码")
	ErrInvalidPassword     = errors.New("新密码至少需要 8 个字符")
	ErrLoginRateLimited    = errors.New("管理员登录尝试过于频繁")
	ErrRuntimeUnavailable  = errors.New("管理员认证运行态暂不可用")
	ErrTrustedLoginDenied  = errors.New("当前 IP 不允许免密登录")
	ErrTrustedLoginNoAdmin = errors.New("尚未初始化管理员账号")
)

type Tokens struct {
	AccessToken           string
	AccessTokenExpiresAt  time.Time
	RefreshToken          string
	RefreshTokenExpiresAt time.Time
}

// Service 负责编排单管理员登录、JWT 和 refresh session 生命周期。
type Service struct {
	admins            repository.AdminRepository
	sessions          repository.AdminSessionRepository
	tokens            *security.TokenService
	accessTTL         time.Duration
	refreshTTL        time.Duration
	loginLimiter      repository.RateLimiter
	dummyPasswordHash string
	trustedLoginIPs   map[string]struct{}
}

func NewService(admins repository.AdminRepository, sessions repository.AdminSessionRepository, tokens *security.TokenService, accessTTL, refreshTTL time.Duration) *Service {
	dummyHash, _ := security.HashPassword("grok2api-invalid-admin-password")
	return &Service{admins: admins, sessions: sessions, tokens: tokens, accessTTL: accessTTL, refreshTTL: refreshTTL, dummyPasswordHash: dummyHash, trustedLoginIPs: map[string]struct{}{}}
}

func (s *Service) SetLoginRateLimiter(limiter repository.RateLimiter) { s.loginLimiter = limiter }

// SetTrustedLoginIPs 配置允许免密登录的来源 IP；非法项会被忽略（配置层应已校验）。
func (s *Service) SetTrustedLoginIPs(ips []string) {
	trusted := make(map[string]struct{}, len(ips))
	for _, raw := range ips {
		if key := normalizeIP(raw); key != "" {
			trusted[key] = struct{}{}
		}
	}
	s.trustedLoginIPs = trusted
}

// IsTrustedLoginIP 判断来源 IP 是否在免密白名单中。
func (s *Service) IsTrustedLoginIP(remoteAddress string) bool {
	if len(s.trustedLoginIPs) == 0 {
		return false
	}
	key := normalizeIP(remoteAddress)
	if key == "" {
		return false
	}
	_, ok := s.trustedLoginIPs[key]
	return ok
}

// Bootstrap 在数据库没有管理员时创建唯一管理员。
func (s *Service) Bootstrap(ctx context.Context, username, password string) error {
	count, err := s.admins.Count(ctx)
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	if strings.TrimSpace(username) == "" || len(password) < 8 {
		return ErrBootstrapRequired
	}
	hash, err := security.HashPassword(password)
	if err != nil {
		return err
	}
	_, err = s.admins.Create(ctx, admin.Admin{Username: strings.TrimSpace(username), PasswordHash: hash})
	return err
}

// Login 校验密码并创建新的可撤销 refresh session。
func (s *Service) Login(ctx context.Context, username, password, remoteAddress string) (admin.Admin, Tokens, error) {
	username = strings.TrimSpace(username)
	if err := s.checkLoginRate(ctx, username, remoteAddress); err != nil {
		return admin.Admin{}, Tokens{}, err
	}
	value, err := s.admins.GetByUsername(ctx, username)
	if err != nil {
		_ = security.VerifyPassword(s.dummyPasswordHash, password)
		if !errors.Is(err, repository.ErrNotFound) {
			return admin.Admin{}, Tokens{}, fmt.Errorf("%w: %v", ErrRuntimeUnavailable, err)
		}
		return admin.Admin{}, Tokens{}, ErrInvalidCredentials
	}
	if !security.VerifyPassword(value.PasswordHash, password) {
		return admin.Admin{}, Tokens{}, ErrInvalidCredentials
	}
	tokens, _, err := s.createSession(ctx, value.ID)
	return value, tokens, err
}

// LoginByTrustedIP 在来源 IP 命中白名单时，为系统唯一管理员签发会话，无需密码。
func (s *Service) LoginByTrustedIP(ctx context.Context, remoteAddress string) (admin.Admin, Tokens, error) {
	if !s.IsTrustedLoginIP(remoteAddress) {
		return admin.Admin{}, Tokens{}, ErrTrustedLoginDenied
	}
	if err := s.checkTrustedLoginRate(ctx, remoteAddress); err != nil {
		return admin.Admin{}, Tokens{}, err
	}
	value, err := s.admins.GetFirst(ctx)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return admin.Admin{}, Tokens{}, ErrTrustedLoginNoAdmin
		}
		return admin.Admin{}, Tokens{}, fmt.Errorf("%w: %v", ErrRuntimeUnavailable, err)
	}
	tokens, _, err := s.createSession(ctx, value.ID)
	return value, tokens, err
}

// Refresh 轮换 refresh token，旧 token 立即失效。
func (s *Service) Refresh(ctx context.Context, rawRefreshToken string) (Tokens, error) {
	hash := security.HashToken(rawRefreshToken)
	session, err := s.sessions.GetByTokenHash(ctx, hash)
	if err != nil {
		if !errors.Is(err, repository.ErrNotFound) {
			return Tokens{}, fmt.Errorf("%w: %v", ErrRuntimeUnavailable, err)
		}
		return Tokens{}, ErrInvalidSession
	}
	if !time.Now().UTC().Before(session.ExpiresAt) {
		return Tokens{}, ErrInvalidSession
	}
	adminValue, err := s.admins.GetByID(ctx, session.AdminID)
	if err != nil {
		if !errors.Is(err, repository.ErrNotFound) {
			return Tokens{}, fmt.Errorf("%w: %v", ErrRuntimeUnavailable, err)
		}
		return Tokens{}, ErrInvalidSession
	}
	accessToken, accessExpiresAt, err := s.tokens.CreateAccessToken(adminValue.ID, session.ID, s.accessTTL)
	if err != nil {
		return Tokens{}, err
	}
	refreshToken, err := security.NewOpaqueToken(32)
	if err != nil {
		return Tokens{}, err
	}
	refreshExpiresAt := time.Now().UTC().Add(s.refreshTTL)
	if err := s.sessions.Rotate(ctx, session.ID, hash, security.HashToken(refreshToken), refreshExpiresAt); err != nil {
		if errors.Is(err, repository.ErrConflict) || errors.Is(err, repository.ErrNotFound) {
			return Tokens{}, ErrInvalidSession
		}
		return Tokens{}, err
	}
	return Tokens{AccessToken: accessToken, AccessTokenExpiresAt: accessExpiresAt, RefreshToken: refreshToken, RefreshTokenExpiresAt: refreshExpiresAt}, nil
}

// Logout 撤销当前 refresh session。
func (s *Service) Logout(ctx context.Context, rawRefreshToken string) error {
	session, err := s.sessions.GetByTokenHash(ctx, security.HashToken(rawRefreshToken))
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("%w: %v", ErrRuntimeUnavailable, err)
	}
	if err := s.sessions.Revoke(ctx, session.ID); err != nil && !errors.Is(err, repository.ErrNotFound) {
		return fmt.Errorf("%w: %v", ErrRuntimeUnavailable, err)
	}
	return nil
}

// AuthenticateAccess 校验 access token 并读取管理员。
func (s *Service) AuthenticateAccess(ctx context.Context, rawAccessToken string) (admin.Admin, error) {
	identity, err := s.tokens.ParseAccessToken(rawAccessToken)
	if err != nil {
		return admin.Admin{}, ErrInvalidSession
	}
	session, err := s.sessions.GetByID(ctx, identity.SessionID)
	if err != nil {
		if !errors.Is(err, repository.ErrNotFound) {
			return admin.Admin{}, fmt.Errorf("%w: %v", ErrRuntimeUnavailable, err)
		}
		return admin.Admin{}, ErrInvalidSession
	}
	if session.AdminID != identity.AdminID || !time.Now().UTC().Before(session.ExpiresAt) {
		return admin.Admin{}, ErrInvalidSession
	}
	value, err := s.admins.GetByID(ctx, identity.AdminID)
	if err != nil {
		if !errors.Is(err, repository.ErrNotFound) {
			return admin.Admin{}, fmt.Errorf("%w: %v", ErrRuntimeUnavailable, err)
		}
		return admin.Admin{}, ErrInvalidSession
	}
	return value, nil
}

// ChangePassword 修改密码并撤销管理员的全部 refresh session。
func (s *Service) ChangePassword(ctx context.Context, adminID uint64, currentPassword, newPassword string) error {
	if len(newPassword) < 8 {
		return ErrInvalidPassword
	}
	value, err := s.admins.GetByID(ctx, adminID)
	if err != nil {
		if !errors.Is(err, repository.ErrNotFound) {
			return fmt.Errorf("%w: %v", ErrRuntimeUnavailable, err)
		}
		return ErrInvalidCredentials
	}
	if !security.VerifyPassword(value.PasswordHash, currentPassword) {
		return ErrInvalidCredentials
	}
	hash, err := security.HashPassword(newPassword)
	if err != nil {
		return err
	}
	return s.admins.UpdatePasswordAndRevokeSessions(ctx, adminID, hash)
}

func (s *Service) createSession(ctx context.Context, adminID uint64) (Tokens, admin.Session, error) {
	refreshToken, err := security.NewOpaqueToken(32)
	if err != nil {
		return Tokens{}, admin.Session{}, err
	}
	refreshExpiresAt := time.Now().UTC().Add(s.refreshTTL)
	session, err := s.sessions.Create(ctx, admin.Session{AdminID: adminID, RefreshTokenHash: security.HashToken(refreshToken), ExpiresAt: refreshExpiresAt})
	if err != nil {
		return Tokens{}, admin.Session{}, err
	}
	accessToken, accessExpiresAt, err := s.tokens.CreateAccessToken(adminID, session.ID, s.accessTTL)
	if err != nil {
		_ = s.sessions.Revoke(ctx, session.ID)
		return Tokens{}, admin.Session{}, err
	}
	return Tokens{AccessToken: accessToken, AccessTokenExpiresAt: accessExpiresAt, RefreshToken: refreshToken, RefreshTokenExpiresAt: refreshExpiresAt}, session, err
}

func (s *Service) checkLoginRate(ctx context.Context, username, remoteAddress string) error {
	if s.loginLimiter == nil {
		return nil
	}
	now := time.Now().UTC()
	keys := []struct {
		key   string
		limit int
	}{
		{key: "admin-login:ip:" + security.HashToken(strings.TrimSpace(remoteAddress)), limit: 30},
		{key: "admin-login:user:" + security.HashToken(strings.ToLower(username)), limit: 12},
	}
	for _, item := range keys {
		allowed, err := s.loginLimiter.Allow(ctx, item.key, item.limit, now)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrRuntimeUnavailable, err)
		}
		if !allowed {
			return ErrLoginRateLimited
		}
	}
	return nil
}

func (s *Service) checkTrustedLoginRate(ctx context.Context, remoteAddress string) error {
	if s.loginLimiter == nil {
		return nil
	}
	now := time.Now().UTC()
	key := "admin-trusted-login:ip:" + security.HashToken(normalizeIP(remoteAddress))
	allowed, err := s.loginLimiter.Allow(ctx, key, 60, now)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRuntimeUnavailable, err)
	}
	if !allowed {
		return ErrLoginRateLimited
	}
	return nil
}

func normalizeIP(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	value = strings.Trim(value, "[]")
	ip := net.ParseIP(value)
	if ip == nil {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.String()
}
