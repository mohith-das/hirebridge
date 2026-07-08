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
	"hirebridge/internal/httpapi/handler"
	"hirebridge/internal/httpapi/middleware"
	"hirebridge/internal/httpapi/render"
	"hirebridge/internal/mcp"
	"hirebridge/internal/service"
)

type ServerConfig struct {
	DB         *sql.DB
	Logger     *slog.Logger
	AuthSvc    *auth.Service
	IngestSvc  *service.IngestService
	SearchSvc  *service.SearchService
	BaseURL    string
	StaleAge   time.Duration
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
	r.With(authMw).Post("/device/approve", authH.ApproveDevice)

	strictAuth := middleware.Auth(s.cfg.DB, s.cfg.Logger, s.cfg.BaseURL)
	r.With(strictAuth).Post("/ingest/snapshot", ingestH.Snapshot)

	r.Get("/", webH.Landing)

	dashAuth := middleware.OptionalAuth(s.cfg.DB, s.cfg.Logger)
	r.With(dashAuth).Get("/dashboard", webH.DashboardRedirect)
	r.With(dashAuth).Get("/dashboard/talent", webH.TalentDashboard)
	r.With(dashAuth).Get("/dashboard/recruiter", webH.RecruiterDashboard)

	nodeAuth := middleware.Auth(s.cfg.DB, s.cfg.Logger, s.cfg.BaseURL)
	r.With(nodeAuth).Post("/api/nodes/{nodeID}/revoke", webH.RevokeNode)

	mcpSrv := mcp.NewMCPServer(s.cfg.SearchSvc, s.cfg.DB, s.cfg.BaseURL)

	r.Get("/.well-known/oauth-protected-resource", mcpSrv.ServeHTTP)

	mcpMw := middleware.Auth(s.cfg.DB, s.cfg.Logger, s.cfg.BaseURL)
	mcpScope := middleware.RequireScope("talent:search")
	r.With(mcpMw, mcpScope).Get("/mcp", mcpSrv.ServeHTTP)
	r.With(mcpMw, mcpScope).Post("/mcp", mcpSrv.ServeHTTP)
	r.With(mcpMw, mcpScope).Delete("/mcp", mcpSrv.ServeHTTP)

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
