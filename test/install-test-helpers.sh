#!/usr/bin/env bash
# Install bats-support and bats-assert into test/test_helper/
set -euo pipefail

HELPER_DIR="$(cd "$(dirname "$0")/test_helper" && pwd)"

install_helper() {
  local name="$1" url="$2"
  local dest="$HELPER_DIR/$name"
  if [ -d "$dest" ]; then
    echo "$name already present"
    return
  fi
  git clone --depth 1 "$url" "$dest"
  echo "Installed $name"
}

install_helper bats-support https://github.com/bats-core/bats-support
install_helper bats-assert  https://github.com/bats-core/bats-assert

echo "Test helpers ready."
