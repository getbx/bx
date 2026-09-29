#!/usr/bin/env bash
# 跑收集端 Worker(tools/reports-collector/worker.js)纯函数的 node 断言。
# 形状照抄 scripts/test-page-js.sh:没有 node 就明说跳过(exit 2),判据是退出码。
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if ! command -v node >/dev/null 2>&1; then
  echo "reports collector tests: 没找到 node —— 跳过(这半边这次没有被验证)" >&2
  exit 2
fi
cd "$root/tools/reports-collector"
node --test worker_test.mjs
echo "reports collector tests passed"
