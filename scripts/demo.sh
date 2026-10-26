#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
export OPENNOW_API_KEY="${OPENNOW_API_KEY:-demo-key}"
export OPENNOW_DATA_DIR="${OPENNOW_DATA_DIR:-/tmp/opennow-demo-data}"
export OPENNOW_PUBLIC_URL="${OPENNOW_PUBLIC_URL:-http://127.0.0.1:8080}"
BIN="${BIN:-./opennow}"

CGO_ENABLED=0 go build -o "$BIN" ./cmd/opennow

rm -rf "$OPENNOW_DATA_DIR"
mkdir -p /tmp/opennow-sample
echo '<!doctype html><title>demo</title><link rel="stylesheet" href="/style.css"><p>ok</p>' > /tmp/opennow-sample/index.html
echo 'p{color:teal}' > /tmp/opennow-sample/style.css

"$BIN" serve &
PID=$!
trap 'kill $PID' EXIT
for i in $(seq 1 30); do
  curl -sf "$OPENNOW_PUBLIC_URL/healthz" >/dev/null && break
  sleep 0.2
done

"$BIN" publish --slug sample /tmp/opennow-sample
curl -sf "$OPENNOW_PUBLIC_URL/sample/" | grep -q ok
curl -sf -H "Referer: $OPENNOW_PUBLIC_URL/sample/" "$OPENNOW_PUBLIC_URL/style.css" | grep -q teal
echo "demo ok"
