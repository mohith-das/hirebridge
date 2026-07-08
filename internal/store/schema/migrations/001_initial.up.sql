PRAGMA foreign_keys = ON;

CREATE TABLE users (
    id         TEXT PRIMARY KEY,
    email      TEXT UNIQUE NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE TABLE nodes (
    id                  TEXT PRIMARY KEY,
    user_id             TEXT REFERENCES users(id),
    node_type           TEXT NOT NULL CHECK (node_type IN ('JobOps','LivingCV')),
    endpoint_url        TEXT NOT NULL,
    last_ping_timestamp INTEGER,
    is_active           INTEGER NOT NULL DEFAULT 1,
    node_token_hash     TEXT,
    public_key          BLOB,
    display_name        TEXT,
    created_at          INTEGER,
    revoked_at          INTEGER
);

CREATE TABLE snapshots (
    id           TEXT PRIMARY KEY,
    node_id      TEXT NOT NULL REFERENCES nodes(id),
    candidate_id TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    signature    BLOB,
    ingested_at  INTEGER NOT NULL,
    UNIQUE(node_id, candidate_id)
);

CREATE INDEX idx_snapshots_node ON snapshots(node_id);
CREATE INDEX idx_snapshots_candidate ON snapshots(candidate_id);

CREATE TABLE magic_tokens (
    token_hash       TEXT PRIMARY KEY,
    user_id          TEXT,
    device_code_hash TEXT,
    expires_at       INTEGER NOT NULL,
    used_at          INTEGER
);

CREATE TABLE api_tokens (
    token_hash   TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id),
    node_id      TEXT REFERENCES nodes(id),
    label        TEXT,
    scope        TEXT DEFAULT 'all',
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER,
    revoked_at   INTEGER
);

CREATE INDEX idx_api_tokens_user ON api_tokens(user_id);

CREATE TABLE device_sessions (
    device_code_hash TEXT PRIMARY KEY,
    user_code        TEXT UNIQUE NOT NULL,
    user_id          TEXT REFERENCES users(id),
    status           TEXT NOT NULL CHECK (status IN ('pending','approved','denied','expired','consumed')),
    node_id          TEXT REFERENCES nodes(id),
    requested_scopes TEXT,
    created_at       INTEGER NOT NULL,
    expires_at       INTEGER NOT NULL,
    approved_at      INTEGER,
    consumed_at      INTEGER,
    poll_interval    INTEGER NOT NULL DEFAULT 5,
    last_poll_at     INTEGER
);

CREATE TABLE introduction_requests (
    id                TEXT PRIMARY KEY,
    candidate_id      TEXT NOT NULL,
    recruiter_user_id TEXT NOT NULL REFERENCES users(id),
    node_id           TEXT NOT NULL REFERENCES nodes(id),
    status            TEXT NOT NULL DEFAULT 'queued',
    created_at        INTEGER NOT NULL,
    delivered_at      INTEGER
);

CREATE TABLE audit_log (
    id            TEXT PRIMARY KEY,
    actor_user_id TEXT REFERENCES users(id),
    action        TEXT NOT NULL,
    target        TEXT,
    ts            INTEGER NOT NULL
);
