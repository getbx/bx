#!/usr/bin/env bash
# macOS pf DNS 封堵机制探测。默认 dry-run 只打印计划;--execute 才真正装载规则。
# 装载路径带死手:后台子进程在 DEADMAN 秒后无条件清空 anchor 并恢复 pf 状态。
set -euo pipefail

ANCHOR="com.getbx.bx.probe"
DEADMAN="${DEADMAN:-60}"
EXECUTE=0
[[ "${1:-}" == "--execute" ]] && EXECUTE=1

log() { printf '%s\n' "$*"; }

log "=== 1. 只读:主规则集是否引用第三方 anchor ==="
log "--- /etc/pf.conf(应只有 com.apple) ---"
grep -vE '^\s*#|^\s*$' /etc/pf.conf || true
log "--- 开机加载器 ---"
plutil -p /System/Library/LaunchDaemons/com.apple.pfctl.plist | grep -A4 ProgramArguments || true

log "=== 2. 只读:当前 pf 启用状态(决定待验证项 3) ==="
sudo pfctl -s info 2>&1 | head -3 || true

log "=== 3. 只读:当前 TUN 接口名(决定待验证项 2) ==="
ifconfig | grep -oE '^utun[0-9]+' || log "(无 utun,bx 未运行)"

if [[ "$EXECUTE" -ne 1 ]]; then
  log ""
  log "dry-run 结束。加 --execute 才会装载探测规则(带 ${DEADMAN}s 死手)。"
  exit 0
fi

TUN="$(ifconfig | grep -oE '^utun[0-9]+' | tail -1 || true)"
if [[ -z "$TUN" ]]; then
  log "!!! 需要 bx 正在运行以提供 utun 接口,先 sudo bx up 再重跑"
  exit 1
fi

RULES="$(mktemp)"
MAIN="$(mktemp)"
cleanup() {
  sudo pfctl -a "$ANCHOR" -F all >/dev/null 2>&1 || true
  sudo pfctl -f /etc/pf.conf >/dev/null 2>&1 || true
  rm -f "$RULES" "$MAIN"
  log "已复原:anchor 清空 + 主规则集重载 /etc/pf.conf"
}
trap cleanup EXIT

# 死手:无论脚本发生什么,DEADMAN 秒后强制复原
( sleep "$DEADMAN"; sudo pfctl -a "$ANCHOR" -F all >/dev/null 2>&1 || true; \
  sudo pfctl -f /etc/pf.conf >/dev/null 2>&1 || true; \
  echo "[死手] ${DEADMAN}s 到,已强制复原" ) &
DEADMAN_PID=$!

cat >"$RULES" <<EOF
pass out quick on lo0 proto { udp, tcp } to port 53
pass out quick on $TUN proto { udp, tcp } to port 53
pass out quick proto { udp, tcp } to port 53 user root
block out quick proto { udp, tcp } to port 53
EOF

# 关键验证:主规则集必须引用我们的 anchor,规则才会被求值。
# 只在内存中替换主规则集(保留 Apple 的 anchor),不写 /etc/pf.conf。
cat >"$MAIN" <<EOF
scrub-anchor "com.apple/*"
nat-anchor "com.apple/*"
rdr-anchor "com.apple/*"
dummynet-anchor "com.apple/*"
anchor "com.apple/*"
load anchor "com.apple" from "/etc/pf.anchors/com.apple"
anchor "$ANCHOR"
EOF

log "=== 4. 装载(内存中),验证 anchor 是否真的被求值 ==="
sudo pfctl -e 2>&1 | head -2 || true
sudo pfctl -f "$MAIN"
sudo pfctl -a "$ANCHOR" -f "$RULES"
log "--- anchor 内规则(为空则说明未生效,这是头号风险) ---"
sudo pfctl -a "$ANCHOR" -s rules

log "=== 5. 行为验证 ==="
log "--- a) 到私网 DNS 的查询应被阻断(期望:超时/失败) ---"
GW="$(route -n get default 2>/dev/null | awk '/gateway/{print $2}')"
if [[ -n "$GW" ]]; then
  dig +time=2 +tries=1 @"$GW" example.com >/dev/null 2>&1 \
    && log "  !!! 未阻断(泄漏面仍在)" || log "  已阻断 ✓"
fi
log "--- b) 经 TUN 的公网 DNS 应仍可解析(期望:成功) ---"
dig +time=3 +tries=1 @8.8.8.8 example.com >/dev/null 2>&1 \
  && log "  仍可解析 ✓" || log "  !!! 被误封(quick 顺序有问题)"
log "--- c) root 身份查询应放行(期望:成功) ---"
sudo dig +time=3 +tries=1 @8.8.8.8 example.com >/dev/null 2>&1 \
  && log "  已放行 ✓" || log "  !!! root 放行规则无效"

log "=== 6. 验证救命命令只清 bx 规则 ==="
sudo pfctl -a "$ANCHOR" -F all
log "--- anchor 应为空,而 com.apple 应仍在 ---"
sudo pfctl -a "$ANCHOR" -s rules || true
sudo pfctl -a "com.apple" -s rules 2>/dev/null | head -3 || true

kill "$DEADMAN_PID" 2>/dev/null || true
log "=== 探测完成,trap 将复原 ==="
