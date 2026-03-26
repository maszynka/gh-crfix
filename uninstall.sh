#!/usr/bin/env bash
# Remove gh-fix gh extension symlink
set -euo pipefail

EXT_DIR="$(gh extension dir 2>/dev/null || echo "$HOME/.local/share/gh/extensions")/gh-fix"

if [ -L "$EXT_DIR" ]; then
  rm "$EXT_DIR"
  echo "Removed: $EXT_DIR"
else
  echo "Not installed as symlink: $EXT_DIR"
fi
