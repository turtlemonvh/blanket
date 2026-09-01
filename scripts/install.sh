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
#   VERSION        — tag to install (default: latest release, e.g. v0.1.0)
#   INSTALL_DIR    — directory to place the binary (default: ~/.local/bin)
#   BINARY_PATH    — path to an already-downloaded binary; skips OS/arch
#                    detection and the release download entirely (for
#                    offline installs, see docs/offline_install.md)
#   TYPES_SRC      — local directory of *.toml task types to copy instead
#                    of downloading examples/types/ from GitHub
#   INSTALL_SKILLS — 1 to install the blanket-task-type Claude Code skill
#                    without asking, 0 to skip without asking. Unset means
#                    "ask, but only if there's a real terminal to ask on" —
#                    see the "AI agent skill" section below.
#   SKILLS_SRC     — local directory containing a blanket-task-type/
#                    subdirectory with SKILL.md, copied instead of
#                    downloading it from GitHub (offline installs)

REPO="turtlemonvh/blanket"
RAW_BASE="https://raw.githubusercontent.com/$REPO/master"
EXAMPLE_TYPES="echo_task.toml bash_task.toml python_hello.toml windows_echo.toml"

# Agent harnesses this script knows how to install the skill for, and
# where each one looks for skills. Extend this as other harnesses'
# skill-directory conventions are confirmed — codex and others weren't
# wired up here because their layout isn't verified yet.
detect_skill_dest() {
  if command -v claude >/dev/null 2>&1; then
    echo "$HOME/.claude/skills"
    return 0
  fi
  return 1
}

if [ -z "$BINARY_PATH" ]; then
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
else
  OS=$(uname -s | tr '[:upper:]' '[:lower:]')
  VERSION="local"
  if [ ! -f "$BINARY_PATH" ]; then
    echo "Error: BINARY_PATH '$BINARY_PATH' does not exist."
    exit 1
  fi
fi

# Resolve directories
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"

CONFIG_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/blanket"
DATA_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/blanket"

if [ -z "$BINARY_PATH" ]; then
  URL="https://github.com/$REPO/releases/download/$VERSION/$BINARY"
fi

echo "Installing blanket $VERSION ($OS/amd64) ..."
echo "  binary:  $INSTALL_DIR/blanket"
echo "  config:  $CONFIG_DIR/"
echo "  data:    $DATA_DIR/"
echo

# Install binary
mkdir -p "$INSTALL_DIR"

if [ -n "$BINARY_PATH" ]; then
  cp "$BINARY_PATH" "$INSTALL_DIR/blanket"
else
  HTTP_CODE=$(curl -sSL -w "%{http_code}" -o "$INSTALL_DIR/blanket" "$URL") || HTTP_CODE="000"
  if [ "$HTTP_CODE" -ne 200 ]; then
    rm -f "$INSTALL_DIR/blanket"
    echo "Error: download failed (HTTP $HTTP_CODE). Check that release $VERSION exists:"
    echo "  https://github.com/$REPO/releases"
    exit 1
  fi
fi

chmod +x "$INSTALL_DIR/blanket"

# Create config and data directories
mkdir -p "$CONFIG_DIR" "$DATA_DIR/types" "$DATA_DIR/results"

# Write default config if not present
if [ ! -f "$CONFIG_DIR/config.json" ]; then
  TYPES_ABS=$(cd "$DATA_DIR/types" && pwd)
  RESULTS_ABS=$(cd "$DATA_DIR/results" && pwd)
  DATA_ABS=$(cd "$DATA_DIR" && pwd)
  cat > "$CONFIG_DIR/config.json" <<CONF
{
  "port": 8773,
  "database": "$DATA_ABS/blanket.db",
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

# Install example task types (skip existing files)
echo
if [ -n "$TYPES_SRC" ]; then
  if [ ! -d "$TYPES_SRC" ]; then
    echo "Error: TYPES_SRC '$TYPES_SRC' is not a directory."
    exit 1
  fi
  TYPE_FILES=$(cd "$TYPES_SRC" && ls *.toml 2>/dev/null || true)
else
  TYPE_FILES="$EXAMPLE_TYPES"
fi

for TYPE_FILE in $TYPE_FILES; do
  DEST="$DATA_DIR/types/$TYPE_FILE"
  if [ -f "$DEST" ]; then
    echo "  skip (exists): $TYPE_FILE"
    continue
  fi

  if [ -n "$TYPES_SRC" ]; then
    cp "$TYPES_SRC/$TYPE_FILE" "$DEST"
  else
    TYPE_URL="$RAW_BASE/examples/types/$TYPE_FILE"
    HTTP_CODE=$(curl -sSL -w "%{http_code}" -o "$DEST" "$TYPE_URL") || HTTP_CODE="000"
    if [ "$HTTP_CODE" -ne 200 ]; then
      rm -f "$DEST"
      echo "  warn: could not download $TYPE_FILE (HTTP $HTTP_CODE)"
      continue
    fi
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

# AI agent skill — only offered if a supported agent harness (currently
# just Claude Code) is on $PATH. This script is commonly run piped
# (`curl ... | bash`), which makes stdin the pipe rather than a terminal,
# so INSTALL_SKILLS lets a non-interactive install opt in or out
# explicitly; otherwise we only prompt if /dev/tty is actually usable,
# and default to skipping if it isn't.
SKILL_DEST_ROOT=$(detect_skill_dest) || SKILL_DEST_ROOT=""
if [ -n "$SKILL_DEST_ROOT" ]; then
  echo
  DO_INSTALL_SKILL=""
  case "$INSTALL_SKILLS" in
    1) DO_INSTALL_SKILL="yes" ;;
    0) DO_INSTALL_SKILL="no" ;;
    *)
      if [ -r /dev/tty ] && [ -w /dev/tty ]; then
        printf "Install the blanket-task-type Claude Code skill (helps author task types)? [y/N] " > /dev/tty
        REPLY=""
        read -r REPLY < /dev/tty || REPLY=""
        case "$REPLY" in
          y|Y|yes|YES) DO_INSTALL_SKILL="yes" ;;
          *) DO_INSTALL_SKILL="no" ;;
        esac
      else
        DO_INSTALL_SKILL="no"
      fi
      ;;
  esac

  if [ "$DO_INSTALL_SKILL" = "yes" ]; then
    SKILL_DEST="$SKILL_DEST_ROOT/blanket-task-type"
    if [ -d "$SKILL_DEST" ]; then
      echo "  skip (exists): blanket-task-type skill already at $SKILL_DEST"
    else
      mkdir -p "$SKILL_DEST"
      if [ -n "$SKILLS_SRC" ]; then
        cp "$SKILLS_SRC/blanket-task-type/SKILL.md" "$SKILL_DEST/SKILL.md"
        echo "  installed: blanket-task-type skill -> $SKILL_DEST/SKILL.md"
      else
        SKILL_URL="$RAW_BASE/.claude/skills/blanket-task-type/SKILL.md"
        HTTP_CODE=$(curl -sSL -w "%{http_code}" -o "$SKILL_DEST/SKILL.md" "$SKILL_URL") || HTTP_CODE="000"
        if [ "$HTTP_CODE" -ne 200 ]; then
          rm -rf "$SKILL_DEST"
          echo "  warn: could not download blanket-task-type skill (HTTP $HTTP_CODE)"
        else
          echo "  installed: blanket-task-type skill -> $SKILL_DEST/SKILL.md"
        fi
      fi
    fi
  fi
fi

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
