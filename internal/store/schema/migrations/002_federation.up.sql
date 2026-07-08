PRAGMA foreign_keys = ON;

CREATE TABLE federated_instances (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    endpoint_url    TEXT NOT NULL,
    public_key      TEXT NOT NULL,
    instance_key    TEXT UNIQUE NOT NULL,
    last_seen_at    INTEGER,
    is_active       INTEGER DEFAULT 1,
    created_at      INTEGER NOT NULL,
    revoked_at      INTEGER
);

CREATE TABLE federated_snapshots (
    id               TEXT PRIMARY KEY,
    peer_instance_id TEXT NOT NULL REFERENCES federated_instances(id),
    candidate_id     TEXT NOT NULL,
    payload_preview  TEXT NOT NULL,
    origin_node_id   TEXT NOT NULL,
    origin_endpoint  TEXT NOT NULL,
    ingested_at      INTEGER NOT NULL,
    UNIQUE(peer_instance_id, candidate_id)
);
