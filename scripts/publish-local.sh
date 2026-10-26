#!/usr/bin/env bash
# 向本地 serve 发布一个目录：./scripts/publish-local.sh [slug] [path]
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
export OPENNOW_API_KEY="${OPENNOW_API_KEY:-dev-key}"
export OPENNOW_SERVER="${OPENNOW_SERVER:-http://127.0.0.1:8080}"
BIN="${BIN:-./opennow}"
SLUG="${1:-demo}"
SRC="${2:-}"
if [[ -z "$SRC" ]]; then
  SRC="$(mktemp -d)"
  echo "<!doctype html><title>$SLUG</title><h1>$SLUG</h1>" > "$SRC/index.html"
  echo "(generated sample at $SRC)"
fi
[[ -x "$BIN" ]] || CGO_ENABLED=0 go build -o "$BIN" ./cmd/opennow
"$BIN" publish --slug "$SLUG" --server "$OPENNOW_SERVER" "$SRC"
echo "open: $OPENNOW_SERVER/$SLUG/"
