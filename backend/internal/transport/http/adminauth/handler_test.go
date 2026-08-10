package adminauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adminapp "github.com/chenyme/grok2api/backend/internal/application/adminauth"
	"github.com/chenyme/grok2api/backend/internal/infra/persistence/relational"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
	"github.com/gin-gonic/gin"
)

func TestRefreshUsesHTTPOnlyCookieAndDoesNotExposeToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	database, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}

	service := adminapp.NewService(
		relational.NewAdminRepository(database),
		relational.NewAdminSessionRepository(database),
		security.NewTokenService("12345678901234567890123456789012"),
		15*time.Minute,
		30*24*time.Hour,
	)
	if err := service.Bootstrap(ctx, "admin", "password123"); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	NewHandler(service, true).RegisterPublic(router.Group("/api/admin/v1"))

	login := httptest.NewRecorder()
	loginRequest := httptest.NewRequest(http.MethodPost, "/api/admin/v1/auth/login", strings.NewReader(`{"username":"admin","password":"password123"}`))
	loginRequest.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(login, loginRequest)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", login.Code, login.Body.String())
	}
	if strings.Contains(login.Body.String(), `"refreshToken":`) {
		t.Fatalf("login response exposed refresh token: %s", login.Body.String())
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != refreshCookieName || !cookies[0].HttpOnly || !cookies[0].Secure {
		t.Fatalf("unexpected refresh cookie: %#v", cookies)
	}

	refresh := httptest.NewRecorder()
	refreshRequest := httptest.NewRequest(http.MethodPost, "/api/admin/v1/auth/refresh", strings.NewReader(`{}`))
	refreshRequest.Header.Set("Content-Type", "application/json")
	refreshRequest.AddCookie(cookies[0])
	router.ServeHTTP(refresh, refreshRequest)
	if refresh.Code != http.StatusOK {
		t.Fatalf("refresh status = %d, body = %s", refresh.Code, refresh.Body.String())
	}
	if strings.Contains(refresh.Body.String(), `"refreshToken":`) {
		t.Fatalf("refresh response exposed refresh token: %s", refresh.Body.String())
	}
}

func TestTrustedLoginByRemoteIP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	database, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}

	service := adminapp.NewService(
		relational.NewAdminRepository(database),
		relational.NewAdminSessionRepository(database),
		security.NewTokenService("12345678901234567890123456789012"),
		15*time.Minute,
		30*24*time.Hour,
	)
	service.SetTrustedLoginIPs([]string{"154.16.10.183"})
	if err := service.Bootstrap(ctx, "admin", "password123"); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	_ = router.SetTrustedProxies(nil)
	NewHandler(service, false).RegisterPublic(router.Group("/api/admin/v1"))

	denied := httptest.NewRecorder()
	deniedRequest := httptest.NewRequest(http.MethodPost, "/api/admin/v1/auth/trusted-login", strings.NewReader(`{}`))
	deniedRequest.Header.Set("Content-Type", "application/json")
	deniedRequest.RemoteAddr = "1.2.3.4:12345"
	router.ServeHTTP(denied, deniedRequest)
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("denied status = %d, body = %s", denied.Code, denied.Body.String())
	}

	allowed := httptest.NewRecorder()
	allowedRequest := httptest.NewRequest(http.MethodPost, "/api/admin/v1/auth/trusted-login", strings.NewReader(`{}`))
	allowedRequest.Header.Set("Content-Type", "application/json")
	allowedRequest.RemoteAddr = "154.16.10.183:54321"
	router.ServeHTTP(allowed, allowedRequest)
	if allowed.Code != http.StatusOK {
		t.Fatalf("allowed status = %d, body = %s", allowed.Code, allowed.Body.String())
	}
	cookies := allowed.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != refreshCookieName || !cookies[0].HttpOnly {
		t.Fatalf("unexpected refresh cookie: %#v", cookies)
	}
	if strings.Contains(allowed.Body.String(), `"refreshToken":`) {
		t.Fatalf("trusted login exposed refresh token: %s", allowed.Body.String())
	}
}
