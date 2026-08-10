package repository

import (
	"context"
	"time"

	"github.com/chenyme/grok2api/backend/internal/domain/admin"
)

// AdminRepository 定义管理员持久化能力。
type AdminRepository interface {
	Count(ctx context.Context) (int64, error)
	Create(ctx context.Context, value admin.Admin) (admin.Admin, error)
	GetByUsername(ctx context.Context, username string) (admin.Admin, error)
	GetByID(ctx context.Context, id uint64) (admin.Admin, error)
	// GetFirst 返回创建时间最早的管理员，用于单管理员场景下的免密会话签发。
	GetFirst(ctx context.Context) (admin.Admin, error)
	UpdatePasswordAndRevokeSessions(ctx context.Context, id uint64, passwordHash string) error
}

// AdminSessionRepository 定义管理员刷新会话持久化能力。
type AdminSessionRepository interface {
	Create(ctx context.Context, value admin.Session) (admin.Session, error)
	GetByID(ctx context.Context, id uint64) (admin.Session, error)
	GetByTokenHash(ctx context.Context, tokenHash string) (admin.Session, error)
	Rotate(ctx context.Context, id uint64, expectedTokenHash, newTokenHash string, expiresAt time.Time) error
	Revoke(ctx context.Context, id uint64) error
	RevokeAllByAdmin(ctx context.Context, adminID uint64) error
}
