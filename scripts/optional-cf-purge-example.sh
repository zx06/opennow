#!/usr/bin/env bash
# 【可选附录】Cloudflare Cache Purge 示例 —— 非 opennow 运行时依赖。
# 仅在你把站点挂在 CF CDN 且需要主动清缓存时使用。
set -euo pipefail
: "${CF_ZONE_ID:?set CF_ZONE_ID}"
: "${CF_API_TOKEN:?set CF_API_TOKEN}"
URLS=("$@")
if [[ ${#URLS[@]} -eq 0 ]]; then
  echo "usage: $0 https://example.com/slug/ [more urls...]" >&2
  exit 1
fi
# shellcheck disable=SC2016
payload=$(printf '%s\n' "${URLS[@]}" | jq -R . | jq -s '{files: .}')
curl -fsS -X POST "https://api.cloudflare.com/client/v4/zones/${CF_ZONE_ID}/purge_cache" \
  -H "Authorization: Bearer ${CF_API_TOKEN}" \
  -H "Content-Type: application/json" \
  --data "$payload"
echo
