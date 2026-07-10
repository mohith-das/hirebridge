// Package outbox is the introduction-delivery worker.
//
// It drains the introduction_requests queue introduced by the Canonical
// Federation Contract v1 and HMAC-signs each payload with the candidate's
// LivingCV node's per-node intro_secret before POSTing to {endpoint}/api/inbox.
//
// Retry policy: exponential backoff (1m, 5m, 30m) up to MaxAttempts (default
// 5). When a candidate has no resolvable LivingCV target the row is marked
// undeliverable immediately and never retried.
package outbox

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"hirebridge/internal/store/repo"
)

// SignatureHeader is the header name LivingCV verifies with its stored
// intro_secret. Matches the contract in plan.md / canonical federation v1.
const SignatureHeader = "X-HireBridge-Signature"

// DefaultBackoffs is the exponential schedule used when none is supplied.
// Values are wall-clock delays applied after a failed attempt.
var DefaultBackoffs = []time.Duration{
	1 * time.Minute,
	5 * time.Minute,
	30 * time.Minute,
}

// Config bundles runtime knobs. Zero values fall back to defaults.
type Config struct {
	PollInterval time.Duration
	Backoffs     []time.Duration
	MaxAttempts  int
	HTTPTimeout  time.Duration
	BatchSize    int
}

// Worker drains the introduction queue.
type Worker struct {
	DB     *sql.DB
	Logger *slog.Logger
	HTTP   *http.Client
	cfg    Config
	wake   chan struct{}
}

// NewWorker constructs a Worker. Production code uses Run as the entry point;
// tests can drive processOne directly with a custom http.Client.
func NewWorker(db *sql.DB, logger *slog.Logger, cfg Config) *Worker {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 30 * time.Second
	}
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = 10 * time.Second
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 5
	}
	if len(cfg.Backoffs) == 0 {
		cfg.Backoffs = DefaultBackoffs
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 32
	}
	return &Worker{
		DB:     db,
		Logger: logger,
		HTTP:   &http.Client{Timeout: cfg.HTTPTimeout},
		cfg:    cfg,
		wake:   make(chan struct{}, 1),
	}
}

// SetHTTP swaps the HTTP client (tests only).
func (w *Worker) SetHTTP(c *http.Client) {
	w.HTTP = c
}

// Wake nudges the worker to drain immediately. Non-blocking: if a wake is
// already pending the call is coalesced.
func (w *Worker) Wake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// WakeChannel exposes the underlying signal channel so a caller (e.g. the
// HTTP layer or MCP server) can register its own producer without holding a
// reference to the Worker itself.
func (w *Worker) WakeChannel() chan struct{} { return w.wake }

// Run blocks until ctx is cancelled, polling every cfg.PollInterval AND
// draining immediately when Wake() is called.
func (w *Worker) Run(ctx context.Context) {
	w.Logger.Info("outbox worker started",
		"poll_interval", w.cfg.PollInterval,
		"max_attempts", w.cfg.MaxAttempts,
	)
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			w.Logger.Info("outbox worker stopping")
			return
		case <-ticker.C:
			w.drain(ctx)
		case <-w.wake:
			w.drain(ctx)
		}
	}
}

// DrainOnce processes every currently pending row once and returns. Useful
// for tests that want to synchronously drive the worker.
func (w *Worker) DrainOnce(ctx context.Context) {
	w.drain(ctx)
}

func (w *Worker) drain(ctx context.Context) {
	now := time.Now().Unix()
	rows, err := repo.PendingIntroductionRequests(w.DB, now, w.cfg.BatchSize)
	if err != nil {
		w.Logger.Warn("outbox: failed to load pending", "error", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	for _, r := range rows {
		if ctx.Err() != nil {
			return
		}
		if err := w.processOne(ctx, r); err != nil {
			w.Logger.Warn("outbox: process failed", "request_id", r.ID, "error", err)
		}
	}
}

// processOne performs a single delivery attempt for an introduction request.
// The row may transition to delivered, retrying (with bumped attempts +
// scheduled next_attempt_at), failed (max attempts reached), or undeliverable
// (no LivingCV target).
func (w *Worker) processOne(ctx context.Context, r repo.IntroductionRequest) error {
	target, err := repo.ResolveDeliveryTarget(w.DB, r.NodeID)
	if err != nil {
		return fmt.Errorf("resolve target: %w", err)
	}
	if target == nil {
		if err := repo.MarkIntroUndeliverable(w.DB, r.ID, "no active LivingCV node with endpoint_url and intro_secret"); err != nil && err != repo.ErrNotFound {
			return fmt.Errorf("mark undeliverable: %w", err)
		}
		w.Logger.Info("outbox: marked undeliverable", "request_id", r.ID, "reason", "no target")
		return nil
	}

	body, err := buildPayload(r)
	if err != nil {
		return fmt.Errorf("build payload: %w", err)
	}
	sig := SignIntro(target.IntroSecret, body)

	url := target.EndpointURL + "/api/inbox"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(SignatureHeader, sig)

	resp, err := w.HTTP.Do(req)
	if err != nil {
		w.scheduleOrFail(r, fmt.Sprintf("http error: %v", err))
		return nil
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if err := repo.MarkIntroDelivered(w.DB, r.ID); err != nil && err != repo.ErrNotFound {
			return fmt.Errorf("mark delivered: %w", err)
		}
		w.Logger.Info("outbox: delivered",
			"request_id", r.ID,
			"target_node_id", target.NodeID,
			"target_endpoint", target.EndpointURL,
			"attempt", r.Attempts+1,
		)
		return nil
	}

	w.scheduleOrFail(r, fmt.Sprintf("non-2xx: %d", resp.StatusCode))
	return nil
}

// scheduleOrFail decides whether a failed attempt should be retried or
// terminally failed based on MaxAttempts.
func (w *Worker) scheduleOrFail(r repo.IntroductionRequest, lastErr string) {
	attemptNumber := r.Attempts + 1 // the attempt we just made is attempt #attemptNumber
	if attemptNumber >= w.cfg.MaxAttempts {
		if err := repo.MarkIntroFailed(w.DB, r.ID, lastErr); err != nil && err != repo.ErrNotFound {
			w.Logger.Warn("outbox: mark failed failed", "error", err, "request_id", r.ID)
		}
		w.Logger.Warn("outbox: giving up", "request_id", r.ID, "attempts", attemptNumber, "error", lastErr)
		return
	}

	delay := w.cfg.Backoffs[len(w.cfg.Backoffs)-1]
	if attemptNumber-1 < len(w.cfg.Backoffs) {
		delay = w.cfg.Backoffs[attemptNumber-1]
	}
	nextAt := time.Now().Add(delay).Unix()
	if err := repo.MarkIntroRetrying(w.DB, r.ID, lastErr, nextAt); err != nil && err != repo.ErrNotFound {
		w.Logger.Warn("outbox: mark retrying failed", "error", err, "request_id", r.ID)
	}
	w.Logger.Info("outbox: scheduled retry",
		"request_id", r.ID,
		"attempt", attemptNumber,
		"next_in", delay,
		"error", lastErr,
	)
}

// buildPayload serializes the exact JSON body that will be signed. Field
// ordering matches the Canonical Federation Contract v1. Callers must pass
// the returned bytes BOTH to SignIntro and into http.Request.Body — the
// contract requires signing the EXACT bytes sent over the wire.
func buildPayload(r repo.IntroductionRequest) ([]byte, error) {
	identity := map[string]string{
		"name":  r.RecruiterName,
		"email": r.RecruiterEmail,
	}
	if r.RecruiterCompany.Valid {
		identity["company"] = r.RecruiterCompany.String
	}
	body := map[string]any{
		"request_id":         r.ID,
		"candidate_id":       r.CandidateID,
		"recruiter_identity": identity,
		"ts":                 time.Unix(r.CreatedAt, 0).UTC().Format(time.RFC3339),
	}
	return json.Marshal(body)
}

// SignIntro returns hex(hmac_sha256(secret, body)). The contract requires
// signing the EXACT bytes sent over the wire — callers must pass the same
// body that goes into http.Request.Body.
func SignIntro(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}