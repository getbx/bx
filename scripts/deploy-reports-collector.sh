#!/usr/bin/env bash
# 把问题上报的收集端(tools/reports-collector)部署到维护者的 Cloudflare 账号。
#
# 凭据全部从 macOS 钥匙串取,不落文件、不进仓库:
#   cloudflare-account-id                账号 ID
#   cloudflare-bx-reports-deploy-token   只带 Workers Scripts:Edit + KV:Edit + Account Settings:Read 的 token
#   github-bx-reports-token              只对 getbx/bx-reports 有 Issues 写权限的细粒度 PAT(可选:没有就只存 KV)
# 用法:bash scripts/deploy-reports-collector.sh
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dir="$root/tools/reports-collector"
export CLOUDFLARE_ACCOUNT_ID="$(security find-generic-password -s cloudflare-account-id -w)"
export CLOUDFLARE_API_TOKEN="$(security find-generic-password -s cloudflare-bx-reports-deploy-token -w)"
api="https://api.cloudflare.com/client/v4/accounts/$CLOUDFLARE_ACCOUNT_ID"

# KV namespace:有就复用,没有就建(标题固定)。
kv_id="$(curl -sf -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" "$api/storage/kv/namespaces?per_page=100" \
  | python3 -c 'import json,sys; print(next((n["id"] for n in json.load(sys.stdin)["result"] if n["title"]=="bx-reports"), ""))')"
if [ -z "$kv_id" ]; then
  kv_id="$(curl -sf -X POST -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" -H "Content-Type: application/json" \
    "$api/storage/kv/namespaces" -d '{"title":"bx-reports"}' | python3 -c 'import json,sys; print(json.load(sys.stdin)["result"]["id"])')"
  echo "created KV namespace bx-reports ($kv_id)"
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
cp "$dir/worker.js" "$work/"
sed "s/__KV_ID__/$kv_id/" "$dir/wrangler.toml" > "$work/wrangler.toml"
cd "$work"
npx --yes wrangler@4 deploy
if token="$(security find-generic-password -s github-bx-reports-token -w 2>/dev/null)" && [ -n "$token" ]; then
  printf '%s' "$token" | npx --yes wrangler@4 secret put GITHUB_TOKEN
  echo "GITHUB_TOKEN secret set (issues will be filed in getbx/bx-reports)"
else
  echo "no github-bx-reports-token in the keychain: the collector stores reports in KV only"
fi
