package httpapi

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"hirebridge/internal/auth"
	"hirebridge/internal/httpapi/api"
	"hirebridge/internal/httpapi/handler"
	"hirebridge/internal/httpapi/middleware"
	"hirebridge/internal/httpapi/render"
	"hirebridge/internal/mcp"
	"hirebridge/internal/service"
)

type ServerConfig struct {
	DB            *sql.DB
	Logger        *slog.Logger
	AuthSvc       *auth.Service
	IngestSvc     *service.IngestService
	SearchSvc     *service.SearchService
	BaseURL       string
	StaleAge      time.Duration
	MCPEndpoint   string
	AdminEmail    string
	MagicTTL      time.Duration
	AdminSessions *middleware.AdminSessions
	AdminPending  *middleware.AdminPendingLinks
	SendMagicLink func(email, link string) error
}

func NewServer(cfg ServerConfig) http.Handler {
	return (&Server{cfg: cfg}).build()
}

type Server struct {
	cfg ServerConfig
}

func (s *Server) build() http.Handler {
	r := chi.NewRouter()
	limiter := middleware.NewRateLimiter()

	r.Use(chimw.RequestID)
	r.Use(chimw.Recoverer)
	r.Use(s.loggingMiddleware)

	authH := &handler.AuthHandler{
		Svc:     s.cfg.AuthSvc,
		DB:      s.cfg.DB,
		Logger:  s.cfg.Logger,
		Limiter: limiter,
	}

	ingestH := &handler.IngestHandler{
		Svc:    s.cfg.IngestSvc,
		Logger: s.cfg.Logger,
	}

	webH := &handler.WebUIHandler{
		DB:       s.cfg.DB,
		BaseURL:  s.cfg.BaseURL,
		Logger:   s.cfg.Logger,
		StaleAge: s.cfg.StaleAge,
	}

	adminH := &handler.AdminHandler{
		DB:         s.cfg.DB,
		Logger:     s.cfg.Logger,
		Sessions:   s.cfg.AdminSessions,
		Pending:    s.cfg.AdminPending,
		AdminEmail: s.cfg.AdminEmail,
		LinkTTL:    s.cfg.MagicTTL,
		SendLink:   s.cfg.SendMagicLink,
		Limiter:    limiter,
	}

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})

	r.Get("/static/*", func(w http.ResponseWriter, r *http.Request) {
		render.Static().ServeHTTP(w, r)
	})

	r.Post("/auth/request", authH.RequestMagicLink)
	r.Get("/auth/callback", authH.Callback)
	r.Post("/auth/logout", authH.Logout)
	r.Post("/auth/device", authH.InitDevice)
	r.Post("/auth/token", authH.TokenEndpoint)
	r.Get("/device", authH.DevicePage)

	authMw := middleware.OptionalAuth(s.cfg.DB, s.cfg.Logger)
	csrfMw := middleware.RequireSameOrigin(s.cfg.BaseURL)
	r.With(authMw, csrfMw).Post("/device/approve", authH.ApproveDevice)

	strictAuth := middleware.Auth(s.cfg.DB, s.cfg.Logger, s.cfg.BaseURL)
	r.With(strictAuth).Post("/ingest/snapshot", ingestH.Snapshot)

	r.Get("/", webH.Landing)
	r.Get("/docs", webH.Docs)
	r.Get("/instructions/talent", webH.InstructionsTalent)
	r.Get("/instructions/recruiter", webH.InstructionsRecruiter)

	r.Get("/api/openapi.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(api.Spec())
	})

	dashAuth := middleware.OptionalAuth(s.cfg.DB, s.cfg.Logger)
	dashCSRF := middleware.RequireSameOrigin(s.cfg.BaseURL)
	r.With(dashAuth).Get("/dashboard", webH.DashboardRedirect)
	r.With(dashAuth).Get("/dashboard/talent", webH.TalentDashboard)
	r.With(dashAuth).Get("/dashboard/recruiter", webH.RecruiterDashboard)
	r.With(dashAuth, dashCSRF).Post("/dashboard/recruiter/apikey", webH.GenerateAPIKey)

	r.With(authMw, csrfMw).Post("/api/nodes/{nodeID}/revoke", webH.RevokeNode)

	if adminH.Enabled() {
		adminMw := middleware.RequireAdmin(s.cfg.AdminSessions, true)
		// Login and callback are exempt from RequireAdmin: /admin/login
		// is the entrance, and /admin/callback authenticates via a
		// single-use token in the query string.
		r.Get("/admin/login", adminH.LoginForm)
		r.With(csrfMw).Post("/admin/login", adminH.LoginSubmit)
		r.Get("/admin/callback", adminH.Callback)
		r.With(csrfMw, adminMw).Post("/admin/logout", adminH.Logout)
		r.With(adminMw).Get("/admin", adminH.Panel)
		r.With(csrfMw, adminMw).Post("/admin/peers/{id}/approve", adminH.ApprovePeer)
		r.With(csrfMw, adminMw).Post("/admin/peers/{id}/revoke", adminH.RevokePeer)
	}

	mcpSrv := mcp.NewMCPServer(s.cfg.SearchSvc, s.cfg.DB, s.cfg.BaseURL, s.cfg.MCPEndpoint)

	r.Get("/.well-known/oauth-protected-resource", mcpSrv.ServeHTTP)

	mcpMw := middleware.Auth(s.cfg.DB, s.cfg.Logger, s.cfg.BaseURL)
	mcpScope := middleware.RequireScope("talent:search")
	mcpPath := s.cfg.MCPEndpoint
	r.With(mcpMw, mcpScope).Get(mcpPath, mcpSrv.ServeHTTP)
	r.With(mcpMw, mcpScope).Post(mcpPath, mcpSrv.ServeHTTP)
	r.With(mcpMw, mcpScope).Delete(mcpPath, mcpSrv.ServeHTTP)

	return r
}

func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		s.cfg.Logger.InfoContext(r.Context(), "request",
			"method", r.Method,
			"path", r.URL.Path,
			"remote_addr", r.RemoteAddr,
			"request_id", chimw.GetReqID(r.Context()),
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}
