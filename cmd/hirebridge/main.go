package main

import (
	"context"
	"crypto/tls"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"hirebridge/internal/auth"
	"hirebridge/internal/config"
	"hirebridge/internal/httpapi"
	"hirebridge/internal/logging"
	"hirebridge/internal/service"
	"hirebridge/internal/store"
	"hirebridge/internal/store/schema"
)

func main() {
	cfg := config.Load()
	logger := logging.New()

	logger.Info("starting hirebridge",
		"listen_addr", cfg.ListenAddr,
		"db_path", cfg.DBPath,
		"vec0_path", cfg.Vec0Path,
		"embed_dim", cfg.EmbedDim,
	)

	store.RegisterDriver(cfg.Vec0Path, logger)

	db, err := store.Open(store.DriverName, cfg.DBPath)
	if err != nil {
		logger.Error("failed to open database", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := store.RunMigrations(db, schema.Migrations); err != nil {
		logger.Error("failed to run migrations", "error", err)
		os.Exit(1)
	}
	logger.Info("migrations complete")

	if err := store.CreateVirtualTables(db, cfg.EmbedDim, logger); err != nil {
		logger.Error("failed to create virtual tables", "error", err)
		os.Exit(1)
	}
	logger.Info("virtual tables ready")

	mailer := auth.NewMailer(auth.MailerConfig{
		ResendAPIKey: cfg.ResendAPIKey,
		SMTPHost:     cfg.SMTPHost,
		SMTPPort:     cfg.SMTPPort,
		SMTPUser:     cfg.SMTPUser,
		SMTPPass:     cfg.SMTPPass,
		SMTPFrom:     cfg.SMTPFrom,
	}, logger)

	authSvc := auth.NewService(db, mailer, cfg.BaseURL, cfg.MagicTTL)
	ingestSvc := service.NewIngestService(db, logger)
	searchSvc := service.NewSearchService(db, logger, cfg.EmbedDim)

	handler := httpapi.NewServer(httpapi.ServerConfig{
		DB:        db,
		Logger:    logger,
		AuthSvc:   authSvc,
		IngestSvc: ingestSvc,
		SearchSvc: searchSvc,
		BaseURL:   cfg.BaseURL,
		StaleAge:  cfg.NodeStaleAge,
	})

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if cfg.TLSDomain != "" {
		certManager := &autocert.Manager{
			Cache:      autocert.DirCache("certs"),
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(cfg.TLSDomain),
		}

		tlsSrv := &http.Server{
			Addr:         ":443",
			Handler:      handler,
			ReadTimeout:  10 * time.Second,
			WriteTimeout: 10 * time.Second,
			IdleTimeout:  60 * time.Second,
			TLSConfig:    &tls.Config{GetCertificate: certManager.GetCertificate},
		}

		go func() {
			logger.Info("listening (http)", "addr", ":80")
			if err := http.ListenAndServe(":80", certManager.HTTPHandler(nil)); err != nil {
				logger.Error("http server error", "error", err)
			}
		}()

		go func() {
			logger.Info("listening (https)", "addr", ":443", "domain", cfg.TLSDomain)
			if err := tlsSrv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
				logger.Error("https server error", "error", err)
				os.Exit(1)
			}
		}()

		<-ctx.Done()
		logger.Info("shutting down")

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()

		if err := tlsSrv.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown failed", "error", err)
		}
	} else {
		srv := &http.Server{
			Addr:         cfg.ListenAddr,
			Handler:      handler,
			ReadTimeout:  10 * time.Second,
			WriteTimeout: 10 * time.Second,
			IdleTimeout:  60 * time.Second,
		}

		go func() {
			logger.Info("listening", "addr", srv.Addr)
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Error("server error", "error", err)
				os.Exit(1)
			}
		}()

		<-ctx.Done()
		logger.Info("shutting down")

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()

		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown failed", "error", err)
		}
	}

	logger.Info("stopped")
}
