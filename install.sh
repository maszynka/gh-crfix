#!/usr/bin/env bash
# Install gh-fix as a gh extension (symlink)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
EXT_DIR="$(gh extension dir 2>/dev/null || echo "$HOME/.local/share/gh/extensions")/gh-fix"

if [ -d "$EXT_DIR" ] && [ ! -L "$EXT_DIR" ]; then
  echo "ERROR: $EXT_DIR exists and is not a symlink. Remove it first."
  exit 1
fi

rm -f "$EXT_DIR"
ln -sf "$SCRIPT_DIR" "$EXT_DIR"
echo "Installed: gh fix -> $SCRIPT_DIR"
echo "Run with: gh fix <PR>"
