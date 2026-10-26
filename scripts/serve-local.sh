#!/usr/bin/env bash
# 本地单二进制启动示例（无 Docker / 无 Cloudflare 依赖）
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
export OPENNOW_API_KEY="${OPENNOW_API_KEY:-dev-key}"
export OPENNOW_DATA_DIR="${OPENNOW_DATA_DIR:-./data}"
export OPENNOW_PUBLIC_URL="${OPENNOW_PUBLIC_URL:-http://127.0.0.1:8080}"
export OPENNOW_ADDR="${OPENNOW_ADDR:-:8080}"
BIN="${BIN:-./opennow}"
if [[ ! -x "$BIN" ]]; then
  CGO_ENABLED=0 go build -o "$BIN" ./cmd/opennow
fi
echo "listening ${OPENNOW_PUBLIC_URL}  (admin ${OPENNOW_PUBLIC_URL}/_admin/  healthz ${OPENNOW_PUBLIC_URL}/healthz)"
exec "$BIN" serve
