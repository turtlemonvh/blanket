#!/usr/bin/env bash
#
# scripts/setup.sh
#
# Install everything needed to build and test blanket on a fresh Ubuntu /
# WSL2 machine. Idempotent: safe to re-run.
#
# What gets installed:
#   - apt packages: build-essential, git, curl, make, ca-certificates
#   - Go (from https://go.dev/doc/install, to /usr/local/go)
#   - nvm + Node.js LTS (from https://nodejs.org/en/download)
#   - Playwright npm deps + Chromium + Chromium system libs (libnspr4 etc.)
#
# Requires: sudo for apt and for extracting Go to /usr/local/go.
# Usage:   bash scripts/setup.sh
#

set -euo pipefail

GO_VERSION="${GO_VERSION:-1.22.4}"        # override: GO_VERSION=1.23.0 bash scripts/setup.sh
NODE_VERSION="${NODE_VERSION:---lts}"     # nvm install arg: --lts, 20, 22, etc.
NVM_VERSION="${NVM_VERSION:-v0.40.1}"     # https://github.com/nvm-sh/nvm/releases

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

log()  { printf '\n\033[1;36m[setup]\033[0m %s\n' "$*"; }
warn() { printf '\n\033[1;33m[setup]\033[0m %s\n' "$*"; }

require_not_root() {
  if [[ "$(id -u)" -eq 0 ]]; then
    echo "Do not run this as root. It will call sudo for the steps that need it." >&2
    exit 1
  fi
}

# ---------------------------------------------------------------------------
# 1. apt packages
# ---------------------------------------------------------------------------
install_apt_packages() {
  log "Installing apt base packages (sudo)"
  sudo apt-get update -qq
  sudo apt-get install -y --no-install-recommends \
    build-essential git curl make ca-certificates
}

# ---------------------------------------------------------------------------
# 2. Go (https://go.dev/doc/install)
# ---------------------------------------------------------------------------
install_go() {
  if command -v go >/dev/null 2>&1; then
    local have_version
    have_version="$(go version | awk '{print $3}' | sed 's/^go//')"
    if [[ "$have_version" == "$GO_VERSION" ]]; then
      log "Go $GO_VERSION already installed at $(command -v go)"
      return
    fi
    warn "Found go $have_version; installing $GO_VERSION alongside it"
  fi

  local arch
  case "$(uname -m)" in
    x86_64)  arch=amd64 ;;
    aarch64) arch=arm64 ;;
    *) echo "Unsupported arch: $(uname -m)" >&2; exit 1 ;;
  esac

  local tarball="go${GO_VERSION}.linux-${arch}.tar.gz"
  log "Downloading $tarball"
  curl -fsSL -o "/tmp/$tarball" "https://go.dev/dl/$tarball"

  log "Extracting to /usr/local/go (sudo)"
  sudo rm -rf /usr/local/go
  sudo tar -C /usr/local -xzf "/tmp/$tarball"
  rm -f "/tmp/$tarball"
}

# ---------------------------------------------------------------------------
# 3. nvm + Node.js (https://nodejs.org/en/download)
# ---------------------------------------------------------------------------
install_node() {
  export NVM_DIR="$HOME/.nvm"

  if [[ ! -s "$NVM_DIR/nvm.sh" ]]; then
    log "Installing nvm $NVM_VERSION"
    curl -fsSL "https://raw.githubusercontent.com/nvm-sh/nvm/$NVM_VERSION/install.sh" | bash
  else
    log "nvm already present at $NVM_DIR"
  fi

  # shellcheck disable=SC1091
  source "$NVM_DIR/nvm.sh"

  log "Installing Node.js ($NODE_VERSION) via nvm"
  nvm install "$NODE_VERSION"
  nvm use "$NODE_VERSION" >/dev/null
  nvm alias default "$NODE_VERSION" >/dev/null || true
}

# ---------------------------------------------------------------------------
# 4. Playwright + Chromium + system libs
# ---------------------------------------------------------------------------
install_playwright() {
  export NVM_DIR="$HOME/.nvm"
  # shellcheck disable=SC1091
  source "$NVM_DIR/nvm.sh"

  log "Installing Playwright npm deps"
  (cd "$REPO_ROOT/tests/e2e" && npm install --no-audit --no-fund)

  log "Installing Chromium system libs via playwright (sudo)"
  # install-deps needs sudo; we invoke it explicitly so the prompt is clear.
  (cd "$REPO_ROOT/tests/e2e" && sudo -E "$(which npx)" playwright install-deps chromium)

  log "Downloading Chromium browser"
  (cd "$REPO_ROOT/tests/e2e" && npx playwright install chromium)
}

# ---------------------------------------------------------------------------
# 5. Shell rc hints
# ---------------------------------------------------------------------------
print_rc_hints() {
  cat <<EOF

\033[1;32m[setup]\033[0m Install complete.

Add these lines to ~/.bashrc (or ~/.zshrc) if not already present:

  # Go (official install location)
  export PATH=/usr/local/go/bin:\$PATH

  # nvm
  export NVM_DIR="\$HOME/.nvm"
  [ -s "\$NVM_DIR/nvm.sh" ] && . "\$NVM_DIR/nvm.sh"

Then open a new shell and run:

  make test            # Go test suite
  make linux           # build binary
  make test-browser    # full Playwright suite (needs the built binary)

EOF
}

# ---------------------------------------------------------------------------
# 6. Smoke-check what's now on PATH
# ---------------------------------------------------------------------------
smoke_check() {
  export PATH=/usr/local/go/bin:$PATH
  export NVM_DIR="$HOME/.nvm"
  # shellcheck disable=SC1091
  [[ -s "$NVM_DIR/nvm.sh" ]] && source "$NVM_DIR/nvm.sh"

  log "Versions installed:"
  command -v go   && go version   || echo "go: NOT FOUND"
  command -v node && node --version || echo "node: NOT FOUND"
  command -v npm  && npm --version  || echo "npm: NOT FOUND"
  command -v make && make --version | head -1 || echo "make: NOT FOUND"
}

main() {
  require_not_root
  install_apt_packages
  install_go
  install_node
  install_playwright
  smoke_check
  print_rc_hints
}

main "$@"
