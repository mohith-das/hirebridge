package middleware

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"hirebridge/internal/store/repo"
)

type contextKey string

const (
	UserIDKey      contextKey = "user_id"
	NodeIDKey      contextKey = "node_id"
	BearerTokenKey contextKey = "bearer"
	ScopeKey       contextKey = "scope"
)

func UserIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(UserIDKey).(string)
	return v
}

func NodeIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(NodeIDKey).(string)
	return v
}

func ScopeFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ScopeKey).(string)
	return v
}

func writeUnauthorized(w http.ResponseWriter, baseURL, code string) {
	w.Header().Set("WWW-Authenticate",
		fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource"`, baseURL))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	w.Write([]byte(fmt.Sprintf(`{"error":"%s"}`, code)))
}

func Auth(db *sql.DB, logger *slog.Logger, baseURL string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractBearer(r)
			if token == "" {
				writeUnauthorized(w, baseURL, "missing_token")
				return
			}

			at, err := repo.APITokenByHash(db, token)
			if err != nil {
				logger.ErrorContext(r.Context(), "auth lookup error", "error", err)
				http.Error(w, `{"error":"internal"}`, http.StatusInternalServerError)
				return
			}
			if at == nil {
				writeUnauthorized(w, baseURL, "invalid_token")
				return
			}

			_ = repo.TouchAPIToken(db, token)

			ctx := context.WithValue(r.Context(), UserIDKey, at.UserID)
			if at.NodeID.Valid {
				ctx = context.WithValue(ctx, NodeIDKey, at.NodeID.String)
			}
			if at.Scope.Valid {
				ctx = context.WithValue(ctx, ScopeKey, at.Scope.String)
			}
			ctx = context.WithValue(ctx, BearerTokenKey, token)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func OptionalAuth(db *sql.DB, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractBearer(r)
			if token == "" {
				next.ServeHTTP(w, r)
				return
			}

			at, err := repo.APITokenByHash(db, token)
			if err != nil || at == nil {
				next.ServeHTTP(w, r)
				return
			}

			_ = repo.TouchAPIToken(db, token)

			ctx := context.WithValue(r.Context(), UserIDKey, at.UserID)
			if at.NodeID.Valid {
				ctx = context.WithValue(ctx, NodeIDKey, at.NodeID.String)
			}
			if at.Scope.Valid {
				ctx = context.WithValue(ctx, ScopeKey, at.Scope.String)
			}
			ctx = context.WithValue(ctx, BearerTokenKey, token)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func RequireScope(required string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scope := ScopeFromContext(r.Context())
			if scope != "all" && scope != required {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				w.Write([]byte(`{"error":"insufficient_scope"}`))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func BearerFromRequest(r *http.Request) string {
	return extractBearer(r)
}

func extractBearer(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		cookie, err := r.Cookie("hb_token")
		if err == nil && cookie.Value != "" {
			return cookie.Value
		}
		return ""
	}
	if !strings.HasPrefix(auth, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(auth, "Bearer ")
}
