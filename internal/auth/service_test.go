package auth_test

import (
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"hirebridge/internal/auth"
)

func authTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:?_foreign_keys=1")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)

	for _, mig := range []string{
		"../../internal/store/schema/migrations/001_initial.up.sql",
		"../../internal/store/schema/migrations/002_federation.up.sql",
		"../../internal/store/schema/migrations/003_intro_secret.up.sql",
		"../../internal/store/schema/migrations/004_intro_recruiter.up.sql",
	} {
		schema, err := os.ReadFile(mig)
		if err != nil {
			t.Fatalf("read %s: %v", mig, err)
		}
		if _, err := db.Exec(string(schema)); err != nil {
			t.Fatalf("apply %s: %v", mig, err)
		}
	}

	t.Cleanup(func() { db.Close() })
	return db
}

// fakeMailer captures the link sent in the magic-link email so the test can
// drive /auth/callback without standing up a real mailer.
type fakeMailer struct {
	links []string
}

func (f *fakeMailer) SendMagicLink(email, link string) error {
	f.links = append(f.links, link)
	return nil
}

func newAuthSvc(t *testing.T, db *sql.DB) *auth.Service {
	t.Helper()
	m := &fakeMailer{}
	return auth.NewService(db, m, "http://localhost:8080", 0) // 0 → use default TTL via dummy; we'll re-init below
}

// TestPollToken_IssuesIntroSecret_OnNodeApproval verifies that when a node
// session reaches the approved state, the polling client receives a fresh
// intro_secret AND it has been persisted on the nodes row.
func TestPollToken_IssuesIntroSecret_OnNodeApproval(t *testing.T) {
	db := authTestDB(t)
	m := &fakeMailer{}
	svc := auth.NewService(db, m, "http://localhost:8080", 0)

	// Initiate device flow with node_type=LivingCV (the receiver in the
	// canonical contract).
	init, err := svc.InitiateDeviceFlow("LivingCV", "https://livingcv.example.com", nil)
	if err != nil {
		t.Fatalf("initiate: %v", err)
	}

	// Look up the node row that was created and the pending device session.
	var nodeID string
	if err := db.QueryRow(`SELECT id FROM nodes WHERE endpoint_url = ?`, "https://livingcv.example.com").Scan(&nodeID); err != nil {
		t.Fatalf("lookup node: %v", err)
	}

	// Pre-approval: the node row has no intro_secret.
	var preSecret sql.NullString
	db.QueryRow(`SELECT intro_secret FROM nodes WHERE id = ?`, nodeID).Scan(&preSecret)
	if preSecret.Valid {
		t.Fatalf("intro_secret should be NULL before approval, got %q", preSecret.String)
	}

	// Drive the email-link magic flow: POST /auth/request → mailer captures
	// the link → server-side approval via /auth/callback (not exercised here;
	// we approve the device session directly so we can drive PollToken).
	if err := svc.RequestMagicLink("[email protected]", init.UserCode); err != nil {
		t.Fatalf("request magic link: %v", err)
	}
	if len(m.links) != 1 {
		t.Fatalf("expected one magic link, got %d", len(m.links))
	}
	link := m.links[0]
	token := link[strings.Index(link, "token=")+len("token="):]
	if _, err := svc.VerifyMagicCallback(token); err != nil {
		t.Fatalf("verify magic callback: %v", err)
	}

	// Now poll. The device session is approved → consume + create node token.
	resp, err := svc.PollToken(init.DeviceCode)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("poll returned error %q", resp.Error)
	}
	if resp.AccessToken == "" || resp.NodeID != nodeID || resp.Scope != "node:push" {
		t.Fatalf("unexpected poll response: %+v", resp)
	}
	if resp.IntroSecret == "" {
		t.Fatal("expected intro_secret on successful node token poll response")
	}
	if len(resp.IntroSecret) != 64 {
		t.Errorf("intro_secret should be 64 hex chars, got %d", len(resp.IntroSecret))
	}

	// Persisted on the node row.
	var persisted string
	if err := db.QueryRow(`SELECT intro_secret FROM nodes WHERE id = ?`, nodeID).Scan(&persisted); err != nil {
		t.Fatalf("select node: %v", err)
	}
	if persisted != resp.IntroSecret {
		t.Errorf("persisted intro_secret %q != response intro_secret %q", persisted, resp.IntroSecret)
	}

	// TokenPollResponse must marshal intro_secret so the device-flow client
	// (jobops/LivingCV) sees it.
	encoded, _ := json.Marshal(resp)
	if !strings.Contains(string(encoded), `"intro_secret":"`) {
		t.Errorf("intro_secret not in JSON response: %s", encoded)
	}
}

// TestPollToken_RotatesIntroSecret_OnSecondDeviceFlow verifies that running
// the device flow a second time for the same node produces a different
// intro_secret — invalidating any previously-captured outbox payloads.
func TestPollToken_RotatesIntroSecret_OnSecondDeviceFlow(t *testing.T) {
	db := authTestDB(t)
	m := &fakeMailer{}
	svc := auth.NewService(db, m, "http://localhost:8080", 0)

	endpoint := "https://livingcv.example.com"

	// First device flow.
	firstInit, err := svc.InitiateDeviceFlow("LivingCV", endpoint, nil)
	if err != nil {
		t.Fatalf("first initiate: %v", err)
	}
	var firstNodeID string
	db.QueryRow(`SELECT id FROM nodes WHERE endpoint_url = ?`, endpoint).Scan(&firstNodeID)

	if err := svc.RequestMagicLink("[email protected]", firstInit.UserCode); err != nil {
		t.Fatalf("request magic link 1: %v", err)
	}
	tok1 := m.links[0][strings.Index(m.links[0], "token=")+len("token="):]
	if _, err := svc.VerifyMagicCallback(tok1); err != nil {
		t.Fatalf("verify callback 1: %v", err)
	}
	first, err := svc.PollToken(firstInit.DeviceCode)
	if err != nil {
		t.Fatalf("first poll: %v", err)
	}

	// Second device flow — same endpoint, same email (so the device flow
	// ends up re-using the node row; the canonical contract calls this
	// "rotate on re-flow").
	m2 := &fakeMailer{}
	svc2 := auth.NewService(db, m2, "http://localhost:8080", 0)
	secondInit, err := svc2.InitiateDeviceFlow("LivingCV", endpoint, nil)
	if err != nil {
		t.Fatalf("second initiate: %v", err)
	}
	if err := svc2.RequestMagicLink("[email protected]", secondInit.UserCode); err != nil {
		t.Fatalf("request magic link 2: %v", err)
	}
	tok2 := m2.links[0][strings.Index(m2.links[0], "token=")+len("token="):]
	if _, err := svc2.VerifyMagicCallback(tok2); err != nil {
		t.Fatalf("verify callback 2: %v", err)
	}
	second, err := svc2.PollToken(secondInit.DeviceCode)
	if err != nil {
		t.Fatalf("second poll: %v", err)
	}

	if first.IntroSecret == "" || second.IntroSecret == "" {
		t.Fatalf("expected non-empty secrets on both polls: first=%q second=%q", first.IntroSecret, second.IntroSecret)
	}
	if first.IntroSecret == second.IntroSecret {
		t.Fatal("intro_secret must rotate on re-flow (was identical)")
	}
}

// TestPollToken_OmitsIntroSecret_ForNonNodeFlow ensures the field stays
// empty for sessions that have no attached node (browser-only login).
func TestPollToken_OmitsIntroSecret_ForNonNodeFlow(t *testing.T) {
	db := authTestDB(t)
	m := &fakeMailer{}
	svc := auth.NewService(db, m, "http://localhost:8080", 0)

	// Empty node_type / endpoint_url → no node is created.
	init, err := svc.InitiateDeviceFlow("", "", nil)
	if err != nil {
		t.Fatalf("initiate: %v", err)
	}
	if err := svc.RequestMagicLink("[email protected]", init.UserCode); err != nil {
		t.Fatalf("request magic link: %v", err)
	}
	tok := m.links[0][strings.Index(m.links[0], "token=")+len("token="):]
	if _, err := svc.VerifyMagicCallback(tok); err != nil {
		t.Fatalf("verify callback: %v", err)
	}
	resp, err := svc.PollToken(init.DeviceCode)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("poll error: %s", resp.Error)
	}
	if resp.IntroSecret != "" {
		t.Errorf("intro_secret must be empty when no node is attached, got %q", resp.IntroSecret)
	}
	if resp.Scope != "all" {
		t.Errorf("expected scope=all for browser login, got %q", resp.Scope)
	}

	// And: JSON omits the field entirely (omitempty).
	encoded, _ := json.Marshal(resp)
	if strings.Contains(string(encoded), "intro_secret") {
		t.Errorf("intro_secret should be omitted from JSON when empty: %s", encoded)
	}
}

// Smoke: the device-init handler returns a usable JSON body. (The HTTP layer
// is exercised in handler/admin_test.go and similar; here we just guard the
// Service against JSON breakage.)
func TestInitiateDeviceFlow_StructuredResponse(t *testing.T) {
	db := authTestDB(t)
	m := &fakeMailer{}
	svc := auth.NewService(db, m, "http://localhost:8080", 0)

	init, err := svc.InitiateDeviceFlow("JobOps", "https://jobops.example.com", nil)
	if err != nil {
		t.Fatalf("initiate: %v", err)
	}
	if init.DeviceCode == "" || init.UserCode == "" {
		t.Fatal("expected device_code and user_code")
	}
	if init.VerificationURI == "" || init.VerificationURIComplete == "" {
		t.Fatal("expected verification URIs")
	}
}

// Pin TestPollToken_* helper to ensure we don't accidentally regress the
// JSON wire shape of TokenPollResponse. The canonical contract requires the
// field name `intro_secret` so clients can read it on a successful poll.
func TestAuthTokenEndpoint_JSONShape(t *testing.T) {
	resp := auth.TokenPollResponse{
		AccessToken: "tok",
		NodeID:      "n",
		TokenType:   "Bearer",
		Scope:       "node:push",
		IntroSecret: "abcd",
	}
	encoded, _ := json.Marshal(resp)
	if !strings.Contains(string(encoded), `"intro_secret":"abcd"`) {
		t.Fatalf("TokenPollResponse JSON missing intro_secret: %s", encoded)
	}

	empty := auth.TokenPollResponse{Scope: "all"}
	encoded2, _ := json.Marshal(empty)
	if strings.Contains(string(encoded2), "intro_secret") {
		t.Fatalf("intro_secret must be omitted when empty: %s", encoded2)
	}
}