# HireBridge — The Decentralized AI Talent Bridge

**The open-core routing layer that connects recruiter AI agents to candidate-owned data, without centralizing anyone's information.**

---

## The Open-Core Philosophy

The recruiting industry is being reshaped by AI—but in the dominant model, both candidates and recruiters are losing control. Black-box platforms aggregate and resell candidate data, while recruiters are increasingly locked into opaque matching algorithms they can't inspect, tune, or trust.

HireBridge takes the opposite approach.

- **Candidates own their data.** Self-hosted edge nodes (LivingCV, JobOps) hold career data and embeddings locally. Nothing is uploaded to a central database beyond what the candidate explicitly pushes.
- **HireBridge is a stateless proxy and discovery index.** It stores only signed, cached snapshots for fast retrieval. It does not hold the "master copy" of anyone's career packet.
- **Transparency by default.** Every cached snapshot carries a cryptographic signature (ed25519). A recruiter who retrieves a profile can independently verify it hasn't been tampered with.
- **Zero vendor lock-in.** HireBridge speaks open protocols (MCP, OAuth 2.1, REST). Any MCP-compatible client—Claude Desktop, a custom agent, a CLI tool—can search and retrieve talent profiles.

---

## Architecture

HireBridge is designed to run on **the smallest possible machine**—1 vCPU, 1 GB RAM, zero external services.

```
                         ┌──────────────┐
  Claude Desktop ──MCP──▶│              │
                         │  HireBridge  │──── REST ────▶ LivingCV (candidate edge)
  Custom Agent  ──MCP──▶│  (Go binary)  │
                         │              │──── REST ────▶ JobOps  (candidate edge)
  Recruiter UI  ──HTTP──▶│   SQLite     │
                         └──────────────┘
```

### Stack

| Layer | Choice | Why |
|-------|--------|-----|
| Runtime | **Go 1.23**, single binary | ~13 MB stripped, ~40 MB RSS; goroutine concurrency for fan-out |
| Database | **SQLite (WAL mode)** | In-process, zero RAM overhead for a separate process |
| Keyword search | **FTS5** (BM25, Porter stemmer) | Built into SQLite; no external index service |
| Semantic search | **sqlite-vec** `vec0` | Loadable extension; vectors pushed by edge nodes; no embedding model on the server |
| Search fusion | **Reciprocal Rank Fusion** (BM25 + vec0) | Pure BM25 when no query vector; KNN re-rank when caller supplies one |
| Auth | **Magic link** (web) + **OAuth 2.1 Device Authorization Grant** (CLI, RFC 8628) | Passwordless; long-lived opaque bearer tokens validated by SHA-256 hash lookup |
| MCP transport | **Streamable HTTP**, stateless POST | `mark3labs/mcp-go`; `/.well-known/oauth-protected-resource` (RFC 9728) |
| UI | **Go `html/template`** + Brutalist Glassmorphism | Server-side rendered; no SPA, no JS framework, includes built-in dark/light theme switcher |
| TLS | **Go `autocert`** (Let's Encrypt) | No reverse proxy needed—saves ~30 MB RAM |
| Deploy | **Docker** multi-stage (CGO + `vec0.so`) | `distroless-static` final image; hardened `systemd` unit for OCI Free Tier |

### MCP Tools

| Tool | Description |
|------|-------------|
| `search_talent(query, limit, query_vector?)` | BM25 FTS5 recall + optional vec0 KNN re-rank → ranked candidate pointers |
| `get_talent_profile(candidate_id)` | Returns the full cached career packet with its ed25519 signature |
| `request_introduction(candidate_id, recruiter_name, recruiter_email, recruiter_company?)` | Queues a structured introduction request to the candidate's edge node inbox. The HireBridge outbox HMAC-signs the payload with the candidate's LivingCV `intro_secret` and POSTs it to that node's `/api/inbox` with exponential-backoff retries. |

---

## Security & Privacy

**No token passthrough.** A recruiter's MCP bearer token is accepted *only* at HireBridge's `/mcp` boundary. Outbound calls to candidate edge nodes (e.g., introduction requests) are made with HireBridge's own credentials—the recruiter's token is never forwarded. This prevents a compromised or malicious client from impersonating a recruiter on a candidate's self-hosted node.

**Data provenance via ed25519.** Every snapshot pushed to HireBridge carries an ed25519 signature over the exact transmitted payload bytes (not subject to server-side re-encoding). The `embedding` field is intentionally not covered by the signature. HireBridge verifies the signature against the node's public key before caching. A recruiter retrieving `get_talent_profile` receives both the payload and the signature, enabling independent verification.

**Passwordless authentication.** No passwords are stored—ever. The web UI uses single-use magic links sent via email (Resend or SMTP). The CLI uses the OAuth 2.1 Device Authorization Grant: a short user code displayed in the terminal is approved via the browser. Long-lived tokens are opaque (SHA-256 hashed at rest) and revocable from the dashboard.

**Data minimization.** HireBridge caches only what edge nodes explicitly push. It does not crawl, scrape, or aggregate from external sources. Nodes can revoke access at any time, removing their data from the index.

**Federation peer approval.** When federation is enabled, every peer must be trusted before it can read snapshots or push intros. The trust bootstrap is one of two paths:
1. **Shared join secret (low-touch):** the operator sets `HB_FEDERATION_JOIN_SECRET` to a high-entropy value known to the operator and the peer. The peer sends it as `X-Fed-Join-Secret` on the `register` or `handshake` request, and the row lands as `is_active=1` immediately. The constant-time comparison prevents remote timing leaks. With the secret unset, no fast path exists and every registration is `pending`.
2. **Admin panel (manual):** the operator seeds a single `HB_ADMIN_EMAIL` and authenticates to `/admin` via magic link (request a sign-in link → click it → session cookie for ~2 h). The admin identity is provisioned at deploy time only — it is **not** a row in the `users` table and cannot be created or authenticated via the normal magic-link or device flows. The link tokens live in process memory under a `sync.Mutex` (decoupled from the `magic_tokens` DB table used by regular users), expire in `HB_MAGIC_TTL` (default 15m, shared with the user-flow knob), and are single-use. The login response is uniform so it doesn't leak whether the submitted email matched. Sessions are stored in process memory under a separate `sync.Mutex`, scoped to the `/admin/` cookie path, and expire after ~2 hours. If `HB_ADMIN_EMAIL` is unset (or whitespace), every `/admin/*` route returns 404.

In short: **trust is established by an out-of-band artifact (the join secret) or by an out-of-band human (the admin panel), never by the request itself.**

---

## Edge-node onboarding (jobops + LivingCV)

Both edge nodes (jobops and LivingCV) register themselves with HireBridge through the same canonical device-authorization flow used by CLI clients. The flow is OAuth 2.1 Device Authorization Grant (RFC 8628) — there is no separate admin path for nodes.

### The three calls

1. **`POST /auth/device`** (JSON body)

   ```json
   { "node_type": "jobops", "endpoint_url": "https://jobops.example.com", "public_key": "<64-hex ed25519>" }
   ```

   `public_key` is **required for jobops** (signatures on `/ingest/snapshot` are verified against it) and **omitted by LivingCV** (LivingCV signs with its own per-deployment key; HireBridge only needs its public key for snapshot ingest). `endpoint_url` is the node's public base URL — for LivingCV that is the portfolio site itself.

   Response: `{device_code, user_code, verification_uri, verification_uri_complete, expires_in, interval}`.

2. **`POST /auth/request`** (form-encoded)

   The node triggers the email itself: `email=<operator email>&uc=<user_code>`. Clicking the emailed link approves the device session.

3. **`POST /auth/token`** (form-encoded)

   `grant_type=urn:ietf:params:oauth:grant-type:device_code&device_code=<code>`.

   While pending: HTTP 400 + `{"error":"authorization_pending"}`. On success: `{access_token, node_id, token_type:"Bearer", scope:"node:push", intro_secret}`.

### `intro_secret`

`intro_secret` is a 64-char hex string returned alongside the node's bearer token. It is **not** a credential that grants access to HireBridge — it is an outbound-delivery signing key. HireBridge uses it to HMAC-SHA256 introduction requests before POSTing them to the LivingCV node's `/api/inbox`.

A fresh `intro_secret` is generated **every time** a node completes the device flow. LivingCV persists the latest value alongside its token and uses it to verify incoming `X-HireBridge-Signature` headers. Re-running the device flow for the same node **rotates** the secret, invalidating any previously-captured outbox payloads.

## Introduction delivery contract

When `request_introduction` is called, HireBridge:

1. Validates that `candidate_id` exists and the recruiter supplied `recruiter_name` + `recruiter_email` (and optionally `recruiter_company`).
2. Resolves the **delivery target**: `snapshot.node → that node's user → the user's active LivingCV node with non-empty endpoint_url AND intro_secret`.
3. Queues a row in `introduction_requests` (`status='queued'`, `next_attempt_at=NULL`) and returns immediately with `status: "queued"` plus a `deliverable: true/false` flag.
4. Nudges the background outbox worker (started by `cmd/hirebridge`).

The outbox worker then signs and delivers:

- **Endpoint:** `POST {livingcv_endpoint_url}/api/inbox`
- **Body (raw bytes signed exactly as sent):**

  ```json
  {
    "request_id":        "<intro row id>",
    "candidate_id":      "<32-hex canonical id>",
    "recruiter_identity": { "name": "…", "email": "…", "company": "…" },
    "ts":                "<RFC3339 timestamp>"
  }
  ```

- **Header:** `X-HireBridge-Signature: hex(hmac_sha256(intro_secret, raw_body_bytes))`

- **Timeout:** 10s per request. 2xx ⇒ row marked `delivered`. Other statuses ⇒ retry.
- **Retry policy:** exponential backoff `1m / 5m / 30m`, max 5 attempts. After the last failure the row is `status='failed'` with `last_error` populated.
- **No target:** if the user has no active LivingCV node with `endpoint_url` + `intro_secret`, the row is `status='undeliverable'` immediately and is never retried.

The canonical row states are `queued → retrying → delivered | failed | undeliverable`. Only `queued` and `retrying` rows are eligible for delivery.

### Walkthrough (matches the integration test)

```bash
# 1. Register a fake LivingCV — receives intro_secret in the token response.
curl -sX POST localhost:8080/auth/device \
  -H 'Content-Type: application/json' \
  -d '{"node_type":"LivingCV","endpoint_url":"http://localhost:9000"}'

# 2. Approve + poll until success; grab intro_secret from the response.

# 3. Register a fake jobops the same way; push a signed snapshot to /ingest/snapshot.

# 4. Call request_introduction via MCP — tool result includes status:"queued",
#    deliverable:true, delivery_path:"http://localhost:9000/api/inbox".

# 5. The outbox worker signs the body and POSTs to that URL with header
#    X-HireBridge-Signature: hex(hmac_sha256(intro_secret, body)).
```

---

## Getting Started

### Prerequisites

- **Go 1.23+** (the `mattn/go-sqlite3` driver requires CGO; a C compiler must be present)
- **SQLite 3.35+** (for `RETURNING` clause support)
- **sqlite-vec** `vec0.so` (optional for semantic search; the server runs fine without it)

### Quick start

```bash
# Clone
git clone https://github.com/mohith-das/hirebridge.git
cd hirebridge

# Build (macOS / Linux with CGO)
make build

# Run
make dev
# → http://localhost:8080
```

Set environment variables (or a `.env` file—never committed):

```bash
HB_BASE_URL=http://localhost:8080
HB_DB_PATH=data/hirebridge.db
HB_VEC0_PATH=/app/ext/vec0.so   # optional; skip to run without semantic search
HB_EMBED_DIM=384
HB_RESEND_API_KEY=re_...        # or configure SMTP
HB_TLS_DOMAIN=                   # set for production autocert TLS

# Federation (default: disabled)
# HB_FEDERATION_ENABLED=true
# HB_FEDERATION_PORT=:8400
# HB_FEDERATION_JOIN_SECRET=...   # high-entropy shared secret; empty disables the fast path
# HB_ADMIN_EMAIL=...              # single admin address; unset = admin routes 404
```

### Docker

```bash
docker compose -f deploy/docker-compose.yml up
```

The multi-stage Dockerfile:
1. Builds the Go binary with CGO (`sqlite_fts5`, `sqlite_load_extension` tags)
2. Downloads the official `vec0.so` from [sqlite-vec releases](https://github.com/asg017/sqlite-vec/releases)
3. Produces a `distroless-static` image with the binary and extension

### Automated CI/CD Deployment

HireBridge includes a pre-configured GitHub Actions workflow (`.github/workflows/deploy.yml`) for seamless deployments to production (e.g., OCI Free Tier). 

When you push to the `main` branch, the workflow will automatically:
1. Check out the code.
2. Build the optimized Go binary with `sqlite_fts5` and `sqlite_load_extension`.
3. Securely SSH into your server (using your GitHub Repository Secrets: `SERVER_HOST`, `SERVER_USERNAME`, `SERVER_SSH_KEY`).
4. Replace the old binary and restart the `hirebridge` systemd service with zero downtime.

If you prefer manual deployment, you can simply use `scp` to copy the binary and `systemd` service over to your server.

The hardened systemd unit runs with `NoNewPrivileges=yes`, `ProtectSystem=strict`, and `PrivateTmp=yes`.

### Verifying the build

```bash
make build     # compile
make vet       # static analysis
make test      # test suite
```

---

## Documentation

- **[plan.md](plan.md)** — Full technical blueprint, phased build log, schema definitions, and decision records.

---

## License

[MIT](LICENSE)
