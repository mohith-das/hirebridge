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
| `request_introduction(candidate_id, recruiter_identity)` | Dispatches an introduction request to the candidate's edge node inbox |

---

## Security & Privacy

**No token passthrough.** A recruiter's MCP bearer token is accepted *only* at HireBridge's `/mcp` boundary. Outbound calls to candidate edge nodes (e.g., introduction requests) are made with HireBridge's own credentials—the recruiter's token is never forwarded. This prevents a compromised or malicious client from impersonating a recruiter on a candidate's self-hosted node.

**Data provenance via ed25519.** Every snapshot pushed to HireBridge carries an ed25519 signature over the exact transmitted payload bytes (not subject to server-side re-encoding). The `embedding` field is intentionally not covered by the signature. HireBridge verifies the signature against the node's public key before caching. A recruiter retrieving `get_talent_profile` receives both the payload and the signature, enabling independent verification.

**Passwordless authentication.** No passwords are stored—ever. The web UI uses single-use magic links sent via email (Resend or SMTP). The CLI uses the OAuth 2.1 Device Authorization Grant: a short user code displayed in the terminal is approved via the browser. Long-lived tokens are opaque (SHA-256 hashed at rest) and revocable from the dashboard.

**Data minimization.** HireBridge caches only what edge nodes explicitly push. It does not crawl, scrape, or aggregate from external sources. Nodes can revoke access at any time, removing their data from the index.

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
