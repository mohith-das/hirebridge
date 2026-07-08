#!/usr/bin/env bash
set -euo pipefail

# HireBridge One-Click Install & Update Script
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/mohith-das/hirebridge/main/install.sh | bash
#   bash install.sh [--update] [--version v1.0.0] [--dir /opt/hirebridge] [--no-systemd]

INSTALL_DIR="${INSTALL_DIR:-/opt/hirebridge}"
VERSION="${VERSION:-latest}"
NO_SYSTEMD="${NO_SYSTEMD:-}"
FORCE_UPDATE="${FORCE_UPDATE:-}"
REPO="mohith-das/hirebridge"
VEC0_VERSION="v0.1.9"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log()  { printf "${GREEN}[hirebridge]${NC} %s\n" "$*"; }
warn() { printf "${YELLOW}[hirebridge]${NC} %s\n" "$*"; }
err()  { printf "${RED}[hirebridge]${NC} %s\n" "$*" >&2; }

usage() {
  cat <<EOF
Usage: bash install.sh [OPTIONS]

Options:
  --update           Update an existing installation (preserves data)
  --version <tag>    Install a specific version (default: latest)
  --dir <path>       Install directory (default: /opt/hirebridge)
  --no-systemd       Skip systemd service setup (use for Docker/macOS)

Examples:
  bash install.sh
  bash install.sh --update
  bash install.sh --version v1.0.0
  bash install.sh --dir /home/user/hirebridge --no-systemd
EOF
  exit 0
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --update)      FORCE_UPDATE=1; shift ;;
    --version)     VERSION="$2"; shift 2 ;;
    --dir)         INSTALL_DIR="$2"; shift 2 ;;
    --no-systemd)  NO_SYSTEMD=1; shift ;;
    --help|-h)     usage ;;
    *)             echo "Unknown option: $1"; usage ;;
  esac
done

# --------------- platform detection ---------------
detect_platform() {
  OS=$(uname -s | tr '[:upper:]' '[:lower:]')
  ARCH=$(uname -m)

  case "$ARCH" in
    x86_64|amd64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) err "Unsupported architecture: $ARCH"; exit 1 ;;
  esac

  case "$OS" in
    linux)   PLATFORM="linux-${ARCH}" ;;
    darwin)  PLATFORM="darwin-${ARCH}"; NO_SYSTEMD=1 ;;
    *)       err "Unsupported OS: $OS"; exit 1 ;;
  esac

  VEC0_OS="linux"
  [[ "$OS" == "darwin" ]] && VEC0_OS="macos"

  log "Detected: $PLATFORM"
}

# --------------- version resolution ---------------
resolve_version() {
  if [[ "$VERSION" == "latest" ]]; then
    log "Resolving latest version..."
    VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
      | grep '"tag_name":' | sed -E 's/.*"tag_name": "([^"]+)".*/\1/')
    if [[ -z "$VERSION" ]]; then
      err "Failed to resolve latest version. Specify --version manually."
      exit 1
    fi
  fi
  log "Version: $VERSION"
}

# --------------- download ---------------
download_binary() {
  local url="https://github.com/${REPO}/releases/download/${VERSION}/hirebridge-${PLATFORM}"
  log "Downloading hirebridge ${VERSION} for ${PLATFORM}..."
  curl -fsSL "$url" -o "$INSTALL_DIR/hirebridge.download"
  chmod +x "$INSTALL_DIR/hirebridge.download"
  mv "$INSTALL_DIR/hirebridge.download" "$INSTALL_DIR/hirebridge"
  echo "$VERSION" > "$INSTALL_DIR/VERSION"
  log "Binary installed ($(du -h "$INSTALL_DIR/hirebridge" | cut -f1))"
}

download_vec0() {
  local ext_dir="/app/ext"
  if [[ "$(uname -s)" == "Darwin" ]]; then
    ext_dir="/usr/local/lib"
  fi

  local tarball="sqlite-vec-${VEC0_VERSION#v}-loadable-${VEC0_OS}-${ARCH}.tar.gz"
  local url="https://github.com/asg017/sqlite-vec/releases/download/${VEC0_VERSION}/${tarball}"

  log "Downloading sqlite-vec ${VEC0_VERSION}..."
  mkdir -p "$ext_dir"
  curl -fsSL "$url" -o /tmp/vec0.tar.gz
  tar xzf /tmp/vec0.tar.gz -C "$ext_dir" vec0.so 2>/dev/null || true
  rm -f /tmp/vec0.tar.gz
  log "vec0.so installed to $ext_dir"
}

# --------------- env file ---------------
setup_env() {
  if [[ ! -f "$INSTALL_DIR/.env" ]]; then
    log "Creating default .env..."
    cat > "$INSTALL_DIR/.env" <<ENV
# HireBridge Configuration
HB_BASE_URL=http://localhost:8080
HB_LISTEN=:8080
HB_DB_PATH=${INSTALL_DIR}/data/hirebridge.db
HB_VEC0_PATH=/app/ext/vec0.so
HB_EMBED_DIM=384
HB_TLS_DOMAIN=
HB_RESEND_API_KEY=
HB_SMTP_HOST=
HB_SMTP_PORT=587
HB_SMTP_USER=
HB_SMTP_PASS=
HB_SMTP_FROM=hirebridge@localhost
HB_MAGIC_TTL=15m
HB_NODE_PING_STALE=90s

# Federation (optional)
HB_FEDERATION_ENABLED=false
HB_FEDERATION_PORT=:8400
HB_FEDERATION_INSTANCE_NAME=
HB_FEDERATION_ENDPOINT=
HB_FEDERATION_DISCOVERY_URL=
HB_FEDERATION_SYNC_INTERVAL=5m
ENV
    log ".env created — edit it to configure your instance."
  else
    log ".env exists, preserving."
  fi
}

# --------------- systemd ---------------
setup_systemd() {
  [[ "$NO_SYSTEMD" == "1" ]] && { log "Skipping systemd setup."; return; }

  log "Setting up systemd service..."
  cat > /etc/systemd/system/hirebridge.service <<UNIT
[Unit]
Description=HireBridge — Decentralized AI Talent Bridge
After=network.target

[Service]
Type=simple
User=hirebridge
Group=hirebridge
WorkingDirectory=${INSTALL_DIR}
EnvironmentFile=${INSTALL_DIR}/.env
ExecStart=${INSTALL_DIR}/hirebridge
ExecReload=/bin/kill -HUP \$MAINPID
Restart=always
RestartSec=5
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=${INSTALL_DIR}/data ${INSTALL_DIR}/certs
ReadOnlyPaths=${INSTALL_DIR}/hirebridge ${INSTALL_DIR}/.env
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
UNIT

  systemctl daemon-reload
  systemctl enable hirebridge
  systemctl restart hirebridge
  log "Systemd service installed and started."
}

# --------------- user ---------------
setup_user() {
  [[ "$NO_SYSTEMD" == "1" ]] && return
  if ! id hirebridge &>/dev/null; then
    useradd -r -s /bin/false -d "$INSTALL_DIR" hirebridge
    log "Created system user: hirebridge"
  fi
}

# --------------- paths ---------------
setup_paths() {
  mkdir -p "$INSTALL_DIR"/{data,certs}
  chown -R hirebridge:hirebridge "$INSTALL_DIR" 2>/dev/null || true
}

# --------------- federation identity ---------------
setup_federation_identity() {
  local keyfile="$INSTALL_DIR/federation_key.json"
  if [[ -f "$keyfile" ]]; then
    log "Federation identity exists: $keyfile"
    return
  fi

  log "Generating federation ed25519 identity..."
  if command -v openssl &>/dev/null; then
    openssl genpkey -algorithm ed25519 -out "$INSTALL_DIR/fed_private.pem" 2>/dev/null
    openssl pkey -in "$INSTALL_DIR/fed_private.pem" -pubout -out "$INSTALL_DIR/fed_public.pem" 2>/dev/null
    local pub_hex=$(openssl pkey -in "$INSTALL_DIR/fed_public.pem" -pubout -outform DER 2>/dev/null | tail -c 32 | xxd -p | tr -d '\n')
    cat > "$keyfile" <<JSON
{
  "public_key": "${pub_hex}",
  "private_key_path": "${INSTALL_DIR}/fed_private.pem",
  "generated_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}
JSON
    chmod 600 "$INSTALL_DIR/fed_private.pem" "$keyfile"
    chown hirebridge:hirebridge "$INSTALL_DIR/fed_private.pem" "$keyfile" 2>/dev/null || true
    log "Federation identity generated: $keyfile"
    log "Public key: ${pub_hex:0:16}..."
  else
    warn "openssl not found — skipping federation identity generation."
    log "To set up federation later, run: hirebridge gen-federation-key"
  fi
}

# --------------- update flow ---------------
do_update() {
  if [[ ! -f "$INSTALL_DIR/VERSION" ]]; then
    err "No existing installation found at $INSTALL_DIR."
    err "Run without --update to do a fresh install."
    exit 1
  fi
  local current=$(cat "$INSTALL_DIR/VERSION")
  resolve_version
  if [[ "$current" == "$VERSION" ]] && [[ -z "$FORCE_UPDATE" ]]; then
    log "Already at $VERSION. Use --force to reinstall."
    exit 0
  fi
  log "Updating from $current to $VERSION..."

  [[ "$NO_SYSTEMD" != "1" ]] && systemctl stop hirebridge 2>/dev/null || true
  download_binary
  [[ "$NO_SYSTEMD" != "1" ]] && systemctl start hirebridge

  log "Updated to $VERSION."
}

# --------------- install flow ---------------
do_install() {
  log "Installing HireBridge to $INSTALL_DIR..."

  detect_platform
  resolve_version
  setup_user
  setup_paths
  download_binary
  download_vec0
  setup_env
  setup_federation_identity
  setup_systemd

  echo ""
  log "=============================="
  log "HireBridge $VERSION installed!"
  log "=============================="
  log ""
  log "Next steps:"
  log "  1. Edit ${INSTALL_DIR}/.env"
  log "  2. Set HB_BASE_URL and HB_TLS_DOMAIN"
  log "  3. Configure email (HB_RESEND_API_KEY or HB_SMTP_*)"
  log ""
  if [[ "$NO_SYSTEMD" != "1" ]]; then
    log "  Service: systemctl status hirebridge"
    log "  Logs:    journalctl -u hirebridge -f"
  else
    log "  Start:   ${INSTALL_DIR}/hirebridge"
  fi

  if [[ "$NO_SYSTEMD" != "1" ]]; then
    sleep 2
    if systemctl is-active --quiet hirebridge; then
      log "  Status:  $(systemctl is-active hirebridge)"
    else
      warn "  Status:  $(systemctl is-active hirebridge) — check logs"
    fi
  fi
}

# --------------- main ---------------
if [[ "$FORCE_UPDATE" == "1" ]]; then
  do_update
else
  do_install
fi
