#!/bin/sh
set -e

# Install blanket — downloads the latest (or pinned) release binary for
# Linux or macOS.
#
# Usage:
#   curl -sSfL https://raw.githubusercontent.com/turtlemonvh/blanket/master/scripts/install.sh | bash
#
# Environment variables:
#   VERSION      — tag to install (default: latest release, e.g. v0.1.0)
#   INSTALL_DIR  — directory to place the binary (default: current directory)

REPO="turtlemonvh/blanket"

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

INSTALL_DIR="${INSTALL_DIR:-.}"
URL="https://github.com/$REPO/releases/download/$VERSION/$BINARY"

echo "Installing blanket $VERSION ($OS/amd64) to $INSTALL_DIR/blanket ..."

mkdir -p "$INSTALL_DIR"

HTTP_CODE=$(curl -sSL -w "%{http_code}" -o "$INSTALL_DIR/blanket" "$URL")
if [ "$HTTP_CODE" -ne 200 ]; then
  rm -f "$INSTALL_DIR/blanket"
  echo "Error: download failed (HTTP $HTTP_CODE). Check that release $VERSION exists:"
  echo "  https://github.com/$REPO/releases"
  exit 1
fi

chmod +x "$INSTALL_DIR/blanket"

echo "Done. Run '$INSTALL_DIR/blanket --help' to get started."
