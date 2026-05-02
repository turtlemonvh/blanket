#!/bin/sh
set -e

# Install blanket — downloads the latest (or pinned) release binary for
# Linux or macOS, creates XDG-compliant config/data directories, and
# downloads example task types.
#
# Usage:
#   curl -sSfL https://raw.githubusercontent.com/turtlemonvh/blanket/master/scripts/install.sh | bash
#
# Environment variables:
#   VERSION      — tag to install (default: latest release, e.g. v0.1.0)
#   INSTALL_DIR  — directory to place the binary (default: ~/.local/bin)

REPO="turtlemonvh/blanket"
RAW_BASE="https://raw.githubusercontent.com/$REPO/master"
EXAMPLE_TYPES="echo_task.toml bash_task.toml python_hello.toml windows_echo.toml"

# Detect OS
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
  linux)  BINARY="blanket-linux-amd64" ;;
  darwin) BINARY="blanket-darwin-amd64" ;;
  *)
    echo "Error: unsupported OS '$OS'. Use Linux or macOS, or download manually from"
    echo "  https://github.com/$REPO/releases"
    exit 1
    ;;
esac

# Detect arch
ARCH=$(uname -m)
case "$ARCH" in
  x86_64|amd64) ;; # supported
  *)
    echo "Error: unsupported architecture '$ARCH'. Only amd64/x86_64 binaries are available."
    echo "  https://github.com/$REPO/releases"
    exit 1
    ;;
esac

# Determine version
if [ -z "$VERSION" ]; then
  VERSION=$(curl -sSf "https://api.github.com/repos/$REPO/releases/latest" \
    | grep '"tag_name"' | head -1 | cut -d'"' -f4)
  if [ -z "$VERSION" ]; then
    echo "Error: could not determine latest release. Set VERSION explicitly:"
    echo "  VERSION=v0.1.0 curl -sSfL ... | bash"
    exit 1
  fi
fi

# Resolve directories
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"

CONFIG_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/blanket"
DATA_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/blanket"

URL="https://github.com/$REPO/releases/download/$VERSION/$BINARY"

echo "Installing blanket $VERSION ($OS/amd64) ..."
echo "  binary:  $INSTALL_DIR/blanket"
echo "  config:  $CONFIG_DIR/"
echo "  data:    $DATA_DIR/"
echo

# Download binary
mkdir -p "$INSTALL_DIR"

HTTP_CODE=$(curl -sSL -w "%{http_code}" -o "$INSTALL_DIR/blanket" "$URL")
if [ "$HTTP_CODE" -ne 200 ]; then
  rm -f "$INSTALL_DIR/blanket"
  echo "Error: download failed (HTTP $HTTP_CODE). Check that release $VERSION exists:"
  echo "  https://github.com/$REPO/releases"
  exit 1
fi

chmod +x "$INSTALL_DIR/blanket"

# Create config and data directories
mkdir -p "$CONFIG_DIR" "$DATA_DIR/types" "$DATA_DIR/results"

# Write default config if not present
if [ ! -f "$CONFIG_DIR/config.json" ]; then
  TYPES_ABS=$(cd "$DATA_DIR/types" && pwd)
  RESULTS_ABS=$(cd "$DATA_DIR/results" && pwd)
  cat > "$CONFIG_DIR/config.json" <<CONF
{
  "port": 8773,
  "tasks": {
    "typesPaths": ["$TYPES_ABS"],
    "resultsPath": "$RESULTS_ABS"
  },
  "logLevel": "info"
}
CONF
  echo "Created default config: $CONFIG_DIR/config.json"
else
  echo "Config already exists, skipping: $CONFIG_DIR/config.json"
fi

# Download example task types (skip existing files)
echo
for TYPE_FILE in $EXAMPLE_TYPES; do
  DEST="$DATA_DIR/types/$TYPE_FILE"
  if [ -f "$DEST" ]; then
    echo "  skip (exists): $TYPE_FILE"
    continue
  fi

  TYPE_URL="$RAW_BASE/examples/types/$TYPE_FILE"
  HTTP_CODE=$(curl -sSL -w "%{http_code}" -o "$DEST" "$TYPE_URL")
  if [ "$HTTP_CODE" -ne 200 ]; then
    rm -f "$DEST"
    echo "  warn: could not download $TYPE_FILE (HTTP $HTTP_CODE)"
    continue
  fi

  # Check if executor is available
  EXECUTOR=$(grep '^executor' "$DEST" | head -1 | sed 's/.*=.*"\(.*\)".*/\1/')
  if [ -z "$EXECUTOR" ]; then
    EXECUTOR="bash"
  fi
  if command -v "$EXECUTOR" >/dev/null 2>&1; then
    echo "  installed: $TYPE_FILE (executor: $EXECUTOR)"
  else
    echo "  installed: $TYPE_FILE (warning: executor '$EXECUTOR' not found on PATH)"
  fi
done

# PATH hint
echo
case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *)
    echo "Note: $INSTALL_DIR is not on your PATH. Add it with:"
    echo "  export PATH=\"$INSTALL_DIR:\$PATH\""
    echo
    ;;
esac

echo "Done! Run 'blanket --help' to get started."
echo "The server will use config from: $CONFIG_DIR/config.json"
