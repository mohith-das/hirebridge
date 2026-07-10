package repo_test

import (
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"hirebridge/internal/store/repo"
)

// nodesTestDB constructs an in-memory SQLite with all migrations applied so
// the test exercises the real schema.
func nodesTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:?_foreign_keys=1")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)

	for _, mig := range []string{
		"../schema/migrations/001_initial.up.sql",
		"../schema/migrations/002_federation.up.sql",
		"../schema/migrations/003_intro_secret.up.sql",
		"../schema/migrations/004_intro_recruiter.up.sql",
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

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func TestRotateNodeIntroSecret_RotatesAndPersists(t *testing.T) {
	db := nodesTestDB(t)
	userID := "u-1"
	mustExec(t, db, `INSERT INTO users (id, email, created_at) VALUES (?, ?, ?)`,
		userID, "[email protected]", time.Now().Unix())
	nodeID := repo.NewID()
	mustExec(t, db,
		`INSERT INTO nodes (id, user_id, node_type, endpoint_url, is_active, created_at)
		 VALUES (?, ?, 'LivingCV', 'https://livingcv.example.com', 1, ?)`,
		nodeID, userID, time.Now().Unix(),
	)

	first, err := repo.RotateNodeIntroSecret(db, nodeID)
	if err != nil {
		t.Fatalf("rotate1: %v", err)
	}
	if len(first) != 64 {
		t.Errorf("intro_secret should be 64 hex chars, got %d (%q)", len(first), first)
	}
	persisted, err := repo.NodeByID(db, nodeID)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if !persisted.IntroSecret.Valid || persisted.IntroSecret.String != first {
		t.Errorf("persisted = %v, want %q", persisted.IntroSecret, first)
	}

	second, err := repo.RotateNodeIntroSecret(db, nodeID)
	if err != nil {
		t.Fatalf("rotate2: %v", err)
	}
	if second == first {
		t.Fatal("second rotation must produce a different secret")
	}
}

func TestResolveDeliveryTarget_WalksSnapshotNodeToUserToLivingCV(t *testing.T) {
	db := nodesTestDB(t)
	now := time.Now().Unix()
	userID := "u-target"
	mustExec(t, db, `INSERT INTO users (id, email, created_at) VALUES (?, ?, ?)`,
		userID, "[email protected]", now)

	jobops := repo.NewID()
	mustExec(t, db,
		`INSERT INTO nodes (id, user_id, node_type, endpoint_url, is_active, created_at)
		 VALUES (?, ?, 'JobOps', 'http://jobops.local', 1, ?)`,
		jobops, userID, now,
	)

	livingcv := repo.NewID()
	mustExec(t, db,
		`INSERT INTO nodes (id, user_id, node_type, endpoint_url, is_active, created_at, intro_secret)
		 VALUES (?, ?, 'LivingCV', 'https://livingcv.example.com', 1, ?, 'deadbeefcafebabe0123456789abcdef0123456789abcdef0123456789abcdef')`,
		livingcv, userID, now,
	)

	target, err := repo.ResolveDeliveryTarget(db, jobops)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if target == nil {
		t.Fatal("expected a delivery target, got nil")
	}
	if target.NodeID != livingcv {
		t.Errorf("target node_id: got %s, want %s", target.NodeID, livingcv)
	}
	if target.EndpointURL != "https://livingcv.example.com" {
		t.Errorf("target endpoint: got %s", target.EndpointURL)
	}
	if target.IntroSecret == "" {
		t.Error("target intro_secret missing")
	}
}

// TestResolveDeliveryTarget_NilWhenUserOrphan covers the case where the
// snapshot's owner has no user_id (i.e. snapshot pushed before device
// approval completed) — there is no LivingCV to resolve to.
func TestResolveDeliveryTarget_NilWhenUserOrphan(t *testing.T) {
	db := nodesTestDB(t)
	jobops := repo.NewID()
	mustExec(t, db,
		`INSERT INTO nodes (id, user_id, node_type, endpoint_url, is_active, created_at)
		 VALUES (?, NULL, 'JobOps', 'http://jobops.local', 1, ?)`,
		jobops, time.Now().Unix(),
	)
	target, err := repo.ResolveDeliveryTarget(db, jobops)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if target != nil {
		t.Errorf("expected nil target for orphan node, got %+v", target)
	}
}

// TestResolveDeliveryTarget_NilWhenLivingCVMissingIntroSecret: even if a
// LivingCV node is registered, an empty intro_secret means we cannot HMAC
// the payload — return nil so the outbox marks the row undeliverable.
func TestResolveDeliveryTarget_NilWhenLivingCVMissingIntroSecret(t *testing.T) {
	db := nodesTestDB(t)
	now := time.Now().Unix()
	userID := "u-empty"
	mustExec(t, db, `INSERT INTO users (id, email, created_at) VALUES (?, ?, ?)`,
		userID, "[email protected]", now)

	jobops := repo.NewID()
	mustExec(t, db,
		`INSERT INTO nodes (id, user_id, node_type, endpoint_url, is_active, created_at)
		 VALUES (?, ?, 'JobOps', 'http://jobops.local', 1, ?)`,
		jobops, userID, now,
	)
	// LivingCV node with empty intro_secret.
	mustExec(t, db,
		`INSERT INTO nodes (id, user_id, node_type, endpoint_url, is_active, created_at, intro_secret)
		 VALUES (?, ?, 'LivingCV', 'https://livingcv.example.com', 1, ?, '')`,
		repo.NewID(), userID, now,
	)

	target, err := repo.ResolveDeliveryTarget(db, jobops)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if target != nil {
		t.Errorf("expected nil target (LivingCV has no intro_secret), got %+v", target)
	}
}

// TestResolveDeliveryTarget_PrefersLatestLivingCV: when multiple LivingCV
// nodes exist for a user, the most recently created one wins. This matters
// during re-onboarding when a new LivingCV rotates the old one.
func TestResolveDeliveryTarget_PrefersLatestLivingCV(t *testing.T) {
	db := nodesTestDB(t)
	now := time.Now().Unix()
	userID := "u-multi"
	mustExec(t, db, `INSERT INTO users (id, email, created_at) VALUES (?, ?, ?)`,
		userID, "[email protected]", now)

	jobops := repo.NewID()
	mustExec(t, db,
		`INSERT INTO nodes (id, user_id, node_type, endpoint_url, is_active, created_at)
		 VALUES (?, ?, 'JobOps', 'http://jobops.local', 1, ?)`,
		jobops, userID, now,
	)

	// Older LivingCV.
	older := repo.NewID()
	mustExec(t, db,
		`INSERT INTO nodes (id, user_id, node_type, endpoint_url, is_active, created_at, intro_secret)
		 VALUES (?, ?, 'LivingCV', 'https://livingcv-old.example.com', 1, ?, 'oldsecret')`,
		older, userID, now-100,
	)
	// Newer LivingCV.
	newer := repo.NewID()
	mustExec(t, db,
		`INSERT INTO nodes (id, user_id, node_type, endpoint_url, is_active, created_at, intro_secret)
		 VALUES (?, ?, 'LivingCV', 'https://livingcv-new.example.com', 1, ?, 'newsecret')`,
		newer, userID, now,
	)

	target, err := repo.ResolveDeliveryTarget(db, jobops)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if target == nil {
		t.Fatal("expected a target")
	}
	if target.NodeID != newer {
		t.Errorf("expected newer node %s, got %s", newer, target.NodeID)
	}
	if target.EndpointURL != "https://livingcv-new.example.com" {
		t.Errorf("expected newer endpoint, got %s", target.EndpointURL)
	}
}

// TestSetNodeIntroSecret_UsedByAuth verifies the test-helper path: the auth
// service uses Rotate, tests may seed with Set.
func TestSetNodeIntroSecret_Roundtrip(t *testing.T) {
	db := nodesTestDB(t)
	nodeID := repo.NewID()
	mustExec(t, db,
		`INSERT INTO nodes (id, user_id, node_type, endpoint_url, is_active, created_at)
		 VALUES (?, NULL, 'LivingCV', 'https://x', 1, ?)`,
		nodeID, time.Now().Unix(),
	)
	if err := repo.SetNodeIntroSecret(db, nodeID, "known-secret"); err != nil {
		t.Fatalf("set: %v", err)
	}
	n, err := repo.NodeByID(db, nodeID)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if !n.IntroSecret.Valid || n.IntroSecret.String != "known-secret" {
		t.Errorf("expected intro_secret=known-secret, got %v", n.IntroSecret)
	}
}