package auth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

const (
	// SessionCookieName 是管理员会话 Cookie 的键名
	SessionCookieName = "homeagent_session"
)

var (
	// ErrDeviceNotFound 表示设备不存在
	ErrDeviceNotFound = errors.New("device not found")
	// ErrDeviceMismatch 表示 Device Token 不属于请求路径中的设备（防 IDOR 越权）
	ErrDeviceMismatch = errors.New("device token does not match requested device id")
	// ErrDeviceRevoked 表示该设备的 Token 已被吊销
	ErrDeviceRevoked = errors.New("device authorization revoked")
)

type contextKey string

const (
	sessionContextKey contextKey = "homeagent_admin_session"
	deviceContextKey  contextKey = "homeagent_authenticated_device_id"
)

// DeviceAuthorizer 抽象设备 Token 授权比对能力
type DeviceAuthorizer interface {
	AuthorizeDevice(rawToken, deviceID string) error
}

// SetSessionCookie 在 HTTP 响应中安全写入管理员会话 Cookie
func SetSessionCookie(w http.ResponseWriter, rawToken string, rememberMe bool, secure bool) {
	cookie := &http.Cookie{
		Name:     SessionCookieName,
		Value:    rawToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   secure,
	}
	if rememberMe {
		cookie.MaxAge = int(SessionDurationRememberMe / time.Second)
		cookie.Expires = time.Now().Add(SessionDurationRememberMe)
	}
	http.SetCookie(w, cookie)
}

// ClearSessionCookie 清除浏览器端会话 Cookie
func ClearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   secure,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	})
}

// ExtractSessionToken 从 HTTP 请求中提取会话凭据（优先 Cookie）
func ExtractSessionToken(r *http.Request) string {
	if c, err := r.Cookie(SessionCookieName); err == nil && strings.TrimSpace(c.Value) != "" {
		return strings.TrimSpace(c.Value)
	}
	// 允许通过 Header 传递以支持自动化脚本/测试
	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer agt_sess_") {
		return strings.TrimPrefix(authHeader, "Bearer ")
	}
	return ""
}

// ExtractBearerToken 提取 Authorization: Bearer <token>
func ExtractBearerToken(r *http.Request) string {
	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
	}
	// 查询参数备选
	if q := r.URL.Query().Get("token"); q != "" {
		return strings.TrimSpace(q)
	}
	return ""
}

// RequireAdmin 返回仅允许有效管理员 Session 访问的中间件
func RequireAdmin(sm *SessionManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := ExtractSessionToken(r)
			if token == "" {
				writeAuthError(w, http.StatusUnauthorized, "unauthorized", "Valid admin session required")
				return
			}

			session, err := sm.ValidateSession(token)
			if err != nil {
				writeAuthError(w, http.StatusUnauthorized, "unauthorized", "Session invalid or expired")
				return
			}

			ctx := context.WithValue(r.Context(), sessionContextKey, session)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireDevice 返回强制校验 Device Token 且防范跨设备越权（IDOR）的中间件
func RequireDevice(authorizer DeviceAuthorizer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := ExtractBearerToken(r)
			if token == "" {
				writeAuthError(w, http.StatusUnauthorized, "unauthorized", "Device token required")
				return
			}

			deviceID := r.PathValue("id")
			if deviceID == "" {
				// 兼容非 1.22 模式的路径解析
				parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
				for i, p := range parts {
					if p == "devices" && i+1 < len(parts) {
						deviceID = parts[i+1]
						break
					}
				}
			}

			if err := authorizer.AuthorizeDevice(token, deviceID); err != nil {
				if errors.Is(err, ErrDeviceMismatch) {
					writeAuthError(w, http.StatusForbidden, "forbidden", "Device token mismatch (IDOR protection)")
					return
				}
				writeAuthError(w, http.StatusUnauthorized, "unauthorized", "Invalid or revoked device token")
				return
			}

			ctx := context.WithValue(r.Context(), deviceContextKey, deviceID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireAdminOrDevice 允许管理员 Session 或持有匹配该设备的 Device Token 访问
func RequireAdminOrDevice(sm *SessionManager, authorizer DeviceAuthorizer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 先尝试管理员会话鉴权
			sessToken := ExtractSessionToken(r)
			if sessToken != "" {
				if session, err := sm.ValidateSession(sessToken); err == nil {
					ctx := context.WithValue(r.Context(), sessionContextKey, session)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}

			// 尝试 Device Token 鉴权
			devToken := ExtractBearerToken(r)
			if devToken != "" {
				deviceID := r.PathValue("id")
				if deviceID == "" {
					parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
					for i, p := range parts {
						if p == "devices" && i+1 < len(parts) {
							deviceID = parts[i+1]
							break
						}
					}
				}

				if err := authorizer.AuthorizeDevice(devToken, deviceID); err != nil {
					if errors.Is(err, ErrDeviceMismatch) {
						writeAuthError(w, http.StatusForbidden, "forbidden", "Device token mismatch")
						return
					}
					writeAuthError(w, http.StatusUnauthorized, "unauthorized", "Invalid or revoked device token")
					return
				}

				ctx := context.WithValue(r.Context(), deviceContextKey, deviceID)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			writeAuthError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		})
	}
}

// Bearer 保留旧版恒定时间比对中间件用于向下兼容与单元测试
func Bearer(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		value := ExtractBearerToken(r)
		if token == "" || len(value) != len(token) || subtle.ConstantTimeCompare([]byte(value), []byte(token)) != 1 {
			writeAuthError(w, http.StatusUnauthorized, "unauthorized", "Unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// GetSessionFromContext 从 Context 中提取已鉴权的 Admin Session
func GetSessionFromContext(ctx context.Context) *Session {
	if v, ok := ctx.Value(sessionContextKey).(*Session); ok {
		return v
	}
	return nil
}

func writeAuthError(w http.ResponseWriter, statusCode int, errCode, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":   errCode,
		"message": message,
	})
}
