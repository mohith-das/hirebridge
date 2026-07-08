# HireBridge — Technical Implementation Blueprint

> **Constraint:** 1 vCPU / 1 GB RAM Oracle Free Tier AMD Micro. No Postgres. No Redis. No embedding/LLM model on the server. No heavy frontend frameworks. Single binary deploy.

---

## Locked Stack

| Layer | Choice |
|---|---|
| Runtime | Go 1.22+, single binary |
| HTTP/routing | `chi` v5 |
| DB | SQLite (WAL) via `mattn/go-sqlite3` (CGO), build tags `sqlite_fts5 sqlite_load_extension` |
| Keyword search | FTS5 (BM25), `porter unicode61` tokenizer |
| Semantic search | sqlite-vec `vec0`, loadable `vec0.so` (official `loadable-linux-x86_64`), loaded at runtime |
| Data access | `database/sql` + hand-written repo packages (no sqlc, no ORM) |
| Migrations | `golang-migrate` (embedded `iofs`) |
| MCP server | `mark3labs/mcp-go`, Streamable HTTP on `/mcp`, stateless POST |
| OAuth 2.1 | HireBridge = Resource Server; `/.well-known/oauth-protected-resource` (RFC 9728); opaque static bearer |
| Auth | Magic-link (web) + Device Authorization Grant (CLI, RFC 8628) → long-lived bearer |
| UI | Go `html/template` brutalist SSR (no SPA, no Vite, no Node build stage) |
| Email | `resend/resend-go` behind `Mailer` interface; `net/smtp` fallback |
| Crypto | `golang.org/x/crypto` (ed25519), `crypto/rand`, `crypto/sha256` |
| Logging | stdlib `log/slog` (JSON) |
| TLS | Go `autocert` (Let's Encrypt) direct — no nginx/Caddy |
| Deploy | Docker multi-stage (CGO Go build + fetch `vec0.so`) → distroless final |

---

## Config Surface

```
HB_BASE_URL=https://hirebridge.example.app
HB_DB_PATH=/data/hirebridge.db
HB_VEC0_PATH=/app/ext/vec0.so
HB_EMBED_DIM=384
HB_TLS_DOMAIN=hirebridge.example.app
HB_RESEND_API_KEY=...
HB_SMTP_HOST/PORT/USER/PASS/FROM
HB_MAGIC_TTL=15m
HB_NODE_PING_STALE=90s
HB_MCP_ENDPOINT=/mcp
```

---

## 1. Database & Identity

### Core Tables

```sql
users (
  id         TEXT PRIMARY KEY,
  email      TEXT UNIQUE NOT NULL,
  created_at INTEGER NOT NULL
);

nodes (
  id                  TEXT PRIMARY KEY,
  user_id             TEXT NOT NULL REFERENCES users(id),
  node_type           TEXT NOT NULL CHECK (node_type IN ('JobOps','LivingCV')),
  endpoint_url        TEXT NOT NULL,
  last_ping_timestamp INTEGER,
  is_active           INTEGER NOT NULL DEFAULT 1,
  -- server-managed:
  node_token_hash     TEXT,
  public_key          BLOB,           -- ed25519 public key for snapshot signing
  display_name        TEXT,
  created_at          INTEGER,
  revoked_at          INTEGER
);
```

### Supporting Tables

```sql
snapshots (
  id           TEXT PRIMARY KEY,
  node_id      TEXT NOT NULL REFERENCES nodes(id),
  candidate_id TEXT NOT NULL,
  payload_json TEXT NOT NULL,          -- career-packet.json
  signature    BLOB,                   -- ed25519 over canonical JSON
  ingested_at  INTEGER NOT NULL,
  UNIQUE(node_id, candidate_id)
);

-- FTS5 external-content table over snapshots payload text
snapshots_fts (candidate_id UNINDEXED, content)  -- tokenize='porter unicode61', content=''

-- vec0 virtual table for semantic search
candidate_vec (candidate_id TEXT, embedding float[384])

magic_tokens (
  token_hash         TEXT PRIMARY KEY,  -- sha256(plain_token)
  user_id            TEXT,
  device_code_hash   TEXT,              -- links to device session (optional)
  expires_at         INTEGER NOT NULL,
  used_at            INTEGER
);

api_tokens (
  token_hash    TEXT PRIMARY KEY,       -- sha256(bearer)
  user_id       TEXT NOT NULL REFERENCES users(id),
  node_id       TEXT,                   -- if this is a node bearer
  label         TEXT,
  scope         TEXT DEFAULT 'all',
  created_at    INTEGER NOT NULL,
  last_used_at  INTEGER,
  revoked_at    INTEGER
);

device_sessions (
  device_code_hash TEXT PRIMARY KEY,    -- sha256(device_code)
  user_code        TEXT UNIQUE NOT NULL,-- 8 chars, unambiguous charset
  user_id          TEXT REFERENCES users(id),
  status           TEXT NOT NULL CHECK (status IN ('pending','approved','denied','expired','consumed')),
  node_id          TEXT REFERENCES nodes(id),
  requested_scopes TEXT,
  created_at       INTEGER NOT NULL,
  expires_at       INTEGER NOT NULL,    -- 15 min
  approved_at      INTEGER,
  consumed_at      INTEGER,
  poll_interval    INTEGER NOT NULL DEFAULT 5,
  last_poll_at     INTEGER
);

introduction_requests (
  id                  TEXT PRIMARY KEY,
  candidate_id        TEXT NOT NULL,
  recruiter_user_id   TEXT NOT NULL REFERENCES users(id),
  node_id             TEXT NOT NULL REFERENCES nodes(id),
  status              TEXT NOT NULL DEFAULT 'queued',
  created_at          INTEGER NOT NULL,
  delivered_at        INTEGER
);

audit_log (
  id             TEXT PRIMARY KEY,
  actor_user_id  TEXT REFERENCES users(id),
  action         TEXT NOT NULL,
  target         TEXT,
  ts             INTEGER NOT NULL
);
```

### Search Pipeline

1. **Recall (BM25):** FTS5 keyword search over `snapshots_fts`
2. **Re-rank (vec0):** KNN using optional caller-supplied `query_vector` (384-dim float32 array)
3. **Fusion:** Reciprocal Rank Fusion of BM25 + vec0 scores → final ranked pointers
4. **Fallback:** Pure BM25 when `query_vector` is absent

---

## 2. Auth Flows

### Magic Link (Web)

| Step | Route | Action |
|---|---|---|
| Request | `POST /auth/request` `{email}` | Rate-limited; generate 32-byte token; store sha256 with 15-min TTL; email via Mailer |
| Callback | `GET /auth/callback?token=…` | Constant-time verify; mark used; issue long-lived bearer; set HttpOnly cookie; render dashboard or device-approved page |
| Logout | `POST /auth/logout` | Revoke cookie-bound token |
| Rotate | `POST /api/me/token/rotate` | Invalidate old, mint new |

### Device Authorization Grant (CLI — RFC 8628)

| Step | Route | Action |
|---|---|---|
| Initiate | `POST /auth/device` `{node_type?, endpoint_url?}` | Create `device_sessions` (pending, 15-min TTL). Return `{device_code, user_code, verification_uri, verification_uri_complete, expires_in:900, interval:5}`. |
| Verify page | `GET /device?uc=USERCODE` | SSR: confirm user_code, show email input (or approve button if already logged in). Email submit → `POST /auth/request` with `user_code`. |
| Poll | `POST /auth/token` `grant_type=urn:ietf:params:oauth:grant-type:device_code&device_code=…` | RFC 8628 responses: `authorization_pending`, `slow_down`, `expired_token`, `access_denied`, or success with `{access_token, token_type, node_id, scope}`. |
| Approval | Magic-link callback with `device_code_hash` set | Mark device session approved, mint node bearer, render "Device approved." |

**Node provisioning:** On approval, the server upserts the `nodes` row (node_type, endpoint_url, is_active=1, fresh node_token_hash). The CLI stores `node_id` + `access_token` and uses it for `/ingest/snapshot`. Revocable from talent dashboard.

### Shared Middleware

Cookie OR `Authorization: Bearer` accepted by one middleware. UI uses cookie; API + `/mcp` + `/ingest` use bearer. Token validated by SHA-256 hash lookup against `api_tokens`.

---

## 3. Web Interface — Brutalist SSR

`html/template` + `//go:embed` templates & static. Brutalist CSS (system fonts, monochrome, ~3 KB). ~30 lines vanilla JS (copy API key, poll node status).

| Route | Page | Auth |
|---|---|---|
| `GET /` | Landing: "The Decentralized AI Talent Bridge" + email input | Public |
| `GET /dashboard/talent` | Talent Dashboard: table of nodes, online/offline, last sync, Revoke button | Session cookie |
| `GET /dashboard/recruiter` | Recruiter Dashboard: MCP API key (copy), node count, usage metrics | Session cookie |
| `GET /device` | Device verification page (user_code + email input) | Public (optional session) |
| `GET /healthz` | Liveness | Public |

---

## 4. Cached Snapshots Ingestion

`POST /ingest/snapshot` — authenticated by node bearer (`Authorization: Bearer <node_token>`).

Request body:
```json
{
  "candidate_id": "…",
  "payload": { /* career-packet.json */ },
  "embedding": [float, …],
  "signature": "<ed25519 hex>"
}
```

Pipeline:
1. Verify node bearer → resolve `nodes` row; reject if inactive/revoked.
2. ed25519 verify `signature` over canonical JSON of `payload` using `nodes.public_key`.
3. Upsert `snapshots`; FTS5 sync (trigger-driven); upsert `candidate_vec` embedding; update `nodes.last_ping_timestamp`.
4. Idempotent on `(node_id, candidate_id)`.

---

## 5. MCP Transport & Authorization

- **Transport:** `mcp-go` `NewStreamableHTTPServer` on `/mcp`, stateless POST.
- **OAuth 2.1 RS:** `WithProtectedResourceMetadata` auto-mounts `/.well-known/oauth-protected-resource` (RFC 9728). Invalid bearer → `401` + `WWW-Authenticate`.
- **Authorization middleware:** validate recruiter bearer → inject `recruiter_user_id` into context.
- **No token passthrough (strict):** recruiter bearer accepted only at HireBridge's `/mcp` boundary. Outbound calls to candidate edge nodes use HireBridge's own per-node credential / server-signed ed25519 request.

---

## 6. Recruiter AI Tool Surface (JSON-RPC schemas)

### `search_talent(query, limit, query_vector?)`

```jsonc
// request
{ "method":"tools/call","params":{ "name":"search_talent",
  "arguments":{ "query":"distributed Go React","limit":10,
    "query_vector":[0.012,-0.33,…] /* optional, 384 float32 */ } } }

// result (ranked pointers, not full packets)
{ "candidates":[
  { "candidate_id":"c_…","node_id":"n_…","endpoint_url":"…",
    "display_name":"…","snippet":"…FTS highlight…",
    "bm25_score":-2.13,"vec_score":0.81,"rank":1,
    "last_sync":1719936000,"is_active":true } ] }
```

### `get_talent_profile(candidate_id)`

```jsonc
{ "method":"tools/call","params":{ "name":"get_talent_profile",
  "arguments":{ "candidate_id":"c_…" } } }

// result: cached signed snapshot + provenance
{ "candidate_id":"c_…","node_id":"n_…","endpoint_url":"…",
  "ingested_at":1719936000,"signature":"<ed25519 hex>","public_key":"<hex>",
  "packet":{ /* full career-packet.json */ } }
```

### `request_introduction(candidate_id, recruiter_identity)`

```jsonc
{ "method":"tools/call","params":{ "name":"request_introduction",
  "arguments":{ "candidate_id":"c_…","recruiter_identity":"Jane Doe, Acme — jane@acme.com" } } }

// result
{ "request_id":"req_…","status":"queued","node_id":"n_…","delivered":false }
```

---

## 7. File Structure

```
hirebridge/
├── cmd/hirebridge/main.go
├── internal/
│   ├── config/                 # env + yaml
│   ├── store/
│   │   ├── store.go            # open, PRAGMAs, conn pool
│   │   ├── migrate.go          # golang-migrate runner
│   │   ├── vec0.go             # load vec0.so, create vec0/fts5 vtables
│   │   ├── schema/             # embedded migrations (iofs)
│   │   └── repo/               # users, nodes, snapshots, tokens, intros, audit (hand-written SQL)
│   ├── auth/                   # magic link, device flow, token hash, bearer validation
│   ├── crypto/                 # ed25519 verify, canonical JSON, constant-time
│   ├── service/                # search, snapshot ingest, intro dispatch
│   ├── outbox/                 # optional: intro retry worker
│   ├── httpapi/
│   │   ├── server.go           # chi mux + middleware wiring
│   │   ├── middleware/
│   │   ├── handler/            # auth, ingest, node, webui
│   │   └── render/             # html/template engine + embedded templates/static
│   ├── mcp/                    # mcp-go streamable HTTP, tool defs, authz
│   └── logging/                # slog setup
├── migrations/                 # golang-migrate SQL (embedded)
├── deploy/
│   ├── Dockerfile
│   ├── docker-compose.yml
│   └── oci/systemd.service
├── .github/workflows/ci.yml
├── go.mod  Makefile  plan.md  README.md
```

---

## Phased Build Plan

### Phase 1 — Skeleton + Store ✅ COMPLETE (2026-07-08)
- [x] `go.mod`, `cmd/hirebridge/main.go`
- [x] `internal/config` — env-var config loading
- [x] `internal/logging` — slog (JSON) setup
- [x] `internal/store/store.go` — SQLite WAL open, PRAGMAs, custom driver (`sqlite3_vec`) with `ConnectHook` loading `vec0.so`
- [x] `internal/store/vec0.go` — FTS5 + vec0 virtual table creation (dynamic dim)
- [x] `internal/store/schema/` — embedded migration files (first migration: all core + supporting tables)
- [x] `internal/store/migrate.go` — golang-migrate runner (embedded `iofs`)
- [x] `GET /healthz` endpoint + chi server
- [x] `Makefile`
- [x] `.gitignore`
- **Verified:** binary builds, starts, migrates DB (WAL + foreign_keys), 10 standard tables exist, `snapshots_fts` FTS5 virtual table created, vec0 gracefully skipped on macOS (`.so` missing — works on Linux/Docker), `healthz` returns `{"ok":true}`, graceful shutdown via SIGTERM, `go vet` clean.

### Phase 2 — Auth ✅ COMPLETE (2026-07-08)
- [x] `internal/auth/mailer.go` — Mailer interface, NoopMailer (dev), SMTPMailer
- [x] `internal/auth/service.go` — MagicLink request + callback, Device Authorization Grant (RFC 8628) initiate + poll
- [x] `internal/store/repo/` — repos: users, tokens (magic+API), devices, nodes (hand-written SQL)
- [x] `internal/httpapi/middleware/auth.go` — Bearer/cookie extraction, `Auth()` + `OptionalAuth()` middleware, context helpers
- [x] `internal/httpapi/middleware/ratelimit.go` — In-memory rate limiter (IP: 10/15min, email: 5/15min)
- [x] `POST /auth/request` handler — magic link request with rate limiting + user_code binding
- [x] `GET /auth/callback` handler — verify → issue bearer → set cookie → redirect
- [x] `POST /auth/logout` handler — revoke token + clear cookie
- [x] `POST /auth/device` handler — initiate device flow (creates node + device_session)
- [x] `GET /device` SSR page — device verification (user_code display, email form, approve button)
- [x] `POST /device/approve` handler — approve device (for already-logged-in users)
- [x] `POST /auth/token` handler — RFC 8628 token endpoint (form-urlencoded, polling with interval)
- [x] `internal/httpapi/render/` — template engine (embedded html/template) + brutalist CSS + device.html
- **Verified:** magic-link round-trip (email→link→click→cookie→redirect /dashboard), device flow full round-trip (init→magic-link with user_code→callback approves device→poll returns node bearer), rate limiting (email 5/15min, IP 10/15min), unsupported grant rejection, logout clears cookie + revokes token, `go vet` clean.

### Phase 3 — Ingestion ✅ COMPLETE (2026-07-08)
- [x] `internal/crypto/verify.go` — canonical JSON (sorted keys) + ed25519 signature verification
- [x] `internal/store/repo/snapshots.go` — UpsertSnapshot, ReplaceFTS5Row, UpsertVec0Embedding, Float64ToBlob
- [x] `internal/store/repo/nodes.go` — UpdateNodePing, NodeByID
- [x] `internal/service/ingest.go` — Process: lookup node → optional sig verify → upsert snapshot → sync FTS5 → upsert vec0 (graceful skip) → update ping
- [x] `internal/httpapi/handler/ingest.go` — POST /ingest/snapshot handler with node auth (strict `Auth` middleware)
- [x] Middleware updated: `NodeIDKey` context key populated from API token's `node_id`
- **Verified:** 3 unsigned snapshots pushed → FTS5 BM25 returns correct results ("Go"=3, "Python"=0, "distributed Kubernetes"=3). ed25519: valid sig accepted (200), invalid sig rejected (500 "ingest_failed"), missing sig rejected (500). Only valid-signed snapshot persisted. `go vet` clean.
- **Verify:** push signed packet from test node; BM25 + KNN both return it

### Phase 4 — SSR UI ✅ COMPLETE (2026-07-08)
- [x] `internal/httpapi/render/templates/landing.html` — value prop + email input for magic link
- [x] `internal/httpapi/render/templates/talent_dashboard.html` — node table (type, endpoint, status, last sync, revoke button)
- [x] `internal/httpapi/render/templates/recruiter_dashboard.html` — MCP API key (copy), endpoint, candidate count, active node count, recent activity
- [x] `internal/httpapi/handler/webui.go` — Landing, DashboardRedirect, TalentDashboard, RecruiterDashboard, RevokeNode handlers
- [x] Server restructured to `ServerConfig` struct for clean DI
- **Verified:** landing page renders title; recruiter dashboard shows API key, endpoint, 0/0 counts; after device flow + snapshot push: talent dashboard shows "LivingCV online", recruiter shows "2 candidates indexed across 1 active node"; revoke returns 303 and sets `is_active=0`; `go vet` clean.

### Phase 5 — MCP Surface ✅ COMPLETE (2026-07-08)
- [x] `internal/service/search.go` — SearchTalent (BM25 + optional vec0 KNN + RRF fusion), GetTalentProfile
- [x] `internal/store/repo/search.go` — FTS5Search, Vec0Search, SnapshotsByCandidateIDs, GetSnapshotByCandidate
- [x] `internal/mcp/server.go` — mcp-go Streamable HTTP server, stateless (`WithStateLess(true)`), `WithProtectedResourceMetadata` (RFC 9728), 3 tool registrations + handlers
- [x] OAuth metadata: `GET /.well-known/oauth-protected-resource` (public, no auth)
- [x] MCP auth: `Authorization: Bearer` middleware on `/mcp` (GET/POST/DELETE)
- [x] `ServerConfig` extended with `SearchSvc`
- **Verified:** `tools/list` returns 3 tools (search_talent, get_talent_profile, request_introduction); `search_talent` returns FTS5-ranked pointers with scores + endpoint info; `get_talent_profile` returns full cached signed payload; `request_introduction` returns queued request_id; OAuth metadata returns `resource`, `authorization_servers`, `scopes_supported`; `go vet` clean.

### Phase 6 — Deploy & Polish ✅ COMPLETE (2026-07-08)
- [x] `deploy/Dockerfile` — multi-stage: golang:1.23-alpine CGO build (with tags), fetch vec0.so from official release, distroless final
- [x] `deploy/docker-compose.yml` — local dev with persistent volume
- [x] `deploy/oci/systemd.service` — hardened systemd unit (NoNewPrivileges, ProtectSystem=strict, PrivateTmp)
- [x] `.github/workflows/ci.yml` — build + vet + test on push/PR, artifact upload, binary size check
- [x] autocert TLS wired in `main.go` — when `HB_TLS_DOMAIN` is set: :80 (ACME + redirect) + :443 (autocert); when empty: plain HTTP
- **Verified:** binary is 13MB stripped (`-ldflags="-s -w"`), `go vet` clean, healthz 200, OAuth metadata returns correct scopes, magic link returns `{"ok":"check your email"}`

### Phase 7 — API Documentation System ✅ COMPLETE (2026-07-08)
- [x] Embedded Redoc standalone bundle (1.1MB) in static/redoc.js
- [x] OpenAPI 3.0.3 spec covering all 13 endpoints + MCP tools + schemas
- [x] `GET /docs` route — renders Redoc interactive docs page
- [x] `GET /api/openapi.json` route — serves raw OpenAPI spec
- [x] Nav bar added to landing page (Home, Docs) and dashboards (Home, Docs, Log Out)
- [x] CSS polished: navbar, subtitle, improved spacing, ~950B
- **Verified:** landing page renders nav; docs page loads Redoc with dark theme; openapi.json returns valid spec with 13 paths; all tests pass

---

## Decisions Log

| Date | Decision | Rationale |
|---|---|---|
| 2026-07-08 | Go single binary, sqlite3 (CGO) with FTS5 + vec0 | 1GB/1vCPU; no Postgres/Redis; single binary ~40MB RSS |
| 2026-07-08 | vec0 loadable `.so` at runtime | Official sqlite-vec release shippable artifact; no custom C build; simpler than static-link amalgamation |
| 2026-07-08 | 384-dim default for vec0 | Common self-hosted model dim (MiniLM); configurable |
| 2026-07-08 | Hand-written SQL repos (no sqlc) | sqlc SQLite dialect can't parse `vec0`/FTS5 virtual-table DDL |
| 2026-07-08 | Brutalist SSR (Go html/template) | User constraint: no React SPA; fits 1GB; zero Node build step |
| 2026-07-08 | Optional caller-supplied `query_vector` arg for `search_talent` | Server must NOT run embedding model; vector comes from recruiter's edge (JobOps); BM25 fallback when absent |
| 2026-07-08 | Device Authorization Grant (RFC 8628) for JobOps CLI | CLI needs secure, browser-mediated auth without passwords; magic link as approval event |
| 2026-07-08 | Device flow provisions the node + returns node bearer | Single step for CLI (less round-trips); node revocable from talent dashboard |
| 2026-07-08 | Opaque static bearer tokens (hash-lookup) | Resource Server side of OAuth 2.1; interactive code+PKCE deferred |
| 2026-07-08 | `autocert` TLS (no nginx/Caddy) | Saves ~30MB RAM |
| 2026-07-08 | **Phase 1 complete** — go build + vet clean, 10 std tables + FTS5 vtable, WAL, healthz | Migrations embedded via `iofs`; vec0 gracefully skips on macOS; `SetMaxOpenConns(1)` for SQLite serialization; `busy_timeout=5000`; `cache_size=-2000` (2 MB page cache) |
| 2026-07-08 | **Phase 2 complete** — magic-link + Device Authorization Grant (RFC 8628) | NoopMailer for dev; SMTPMailer ready; token hashing via SHA-256; hb_token cookie; rate limiter (IP + email per-window); device-flow provisions node + returns node bearer; `OptionalAuth` middleware on `/device/approve` |
| 2026-07-08 | **Phase 3 complete** — snapshot ingestion + ed25519 + FTS5 | canonical JSON (Go's sorted-key marshal); ed25519 verify against `nodes.public_key`; FTS5 content table (not contentless — DELETE/INSERT work normally); vec0 upsert graceful skip on macOS; `Float64ToBlob` for vec0 insert |
| 2026-07-08 | **Phase 5 complete** — MCP surface with 3 tools | mcp-go v0.55.1; `WithStateLess(true)` for stateless POST; `WithProtectedResourceMetadata` for RFC 9728 `/.well-known/oauth-protected-resource`; BM25 + vec0 RRF fusion in SearchService; tools/list confirms 3 tools; OAuth metadata public (no auth); MCP endpoints auth via bearer middleware |
| 2026-07-08 | **Phase 6 complete** — deploy + polish | Docker multi-stage (golang:1.23-alpine CGO → distroless-static); vec0.so fetched from official v0.1.9 release; 13MB stripped binary; autocert TLS on :80+:443 when `HB_TLS_DOMAIN` set; hardened systemd unit; CI (build+vet+test) on push/PR |
| 2026-07-08 | **Phase 7 complete** — self-contained API docs | Redoc standalone bundle embedded (1.1MB); OpenAPI 3.0.3 spec at /api/openapi.json; interactive docs at /docs; nav bar (Home + Docs + Logout) on all pages; polished brutalist CSS (~950B); 13 endpoints + 3 MCP tools + component schemas documented |
