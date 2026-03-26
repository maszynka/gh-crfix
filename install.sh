#!/usr/bin/env bash
# Install gh-crfix as a gh extension (symlink)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
EXT_DIR="$(gh extension dir 2>/dev/null || echo "$HOME/.local/share/gh/extensions")/gh-crfix"

if [ -d "$EXT_DIR" ] && [ ! -L "$EXT_DIR" ]; then
  echo "ERROR: $EXT_DIR exists and is not a symlink. Remove it first."
  exit 1
fi

rm -f "$EXT_DIR"
ln -sf "$SCRIPT_DIR" "$EXT_DIR"
echo "Installed: gh crfix -> $SCRIPT_DIR"
echo "Run with: gh crfix <PR>"
