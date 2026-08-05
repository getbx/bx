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

# TUN 接口选择必须校验:bx 默认使用 198.51.100.1/30
TUN="${BX_TUN:-}"
if [[ -z "$TUN" ]]; then
  # 遍历所有 utun*,找出 inet 地址属于 198.51.100.0/30 的那一个
  while IFS= read -r line; do
    iface="${line%%:*}"
    if [[ "$iface" =~ ^utun[0-9]+$ ]]; then
      addr=$(ifconfig "$iface" 2>/dev/null | grep 'inet ' | awk '{print $2}' || true)
      if [[ "$addr" == 198.51.100.* ]]; then
        TUN="$iface"
        break
      fi
    fi
  done < <(ifconfig | grep -E '^utun[0-9]+:')
fi

if [[ -z "$TUN" ]]; then
  log "!!! 未找到 bx TUN 接口(198.51.100.0/30 网段);可用 BX_TUN=utunX 环境变量指定,或先 sudo bx up"
  exit 1
fi

log "--- 选定 TUN 接口:$TUN ---"

RULES="$(mktemp)"
MAIN="$(mktemp)"
TOKEN_FILE="$(mktemp)"
DEADMAN_SCRIPT="$(mktemp)"
DEADMAN_LOG="${TMPDIR:-/tmp}/darwin-dns-probe-deadman-$$.log"
PF_TOKEN=""

cleanup() {
  local pf_ok=1
  local out

  if out=$(sudo pfctl -a "$ANCHOR" -F all 2>&1); then
    log "✓ anchor 已清空"
  else
    pf_ok=0
    log "✗ 清空 anchor 失败:$out"
    log "  请手动执行:sudo pfctl -a $ANCHOR -F all"
  fi

  if [[ -n "$PF_TOKEN" ]]; then
    if out=$(sudo pfctl -X "$PF_TOKEN" 2>&1); then
      log "✓ pf 引用计数已释放(token=$PF_TOKEN)"
    else
      pf_ok=0
      log "✗ 释放 pf 引用计数失败:$out"
      log "  请手动执行:sudo pfctl -X $PF_TOKEN"
    fi
  fi

  rm -f "$RULES" "$MAIN" "$TOKEN_FILE" "$DEADMAN_SCRIPT"

  if [[ "$pf_ok" -eq 1 ]]; then
    log "已复原:anchor 清空 + pf 引用计数释放"
  else
    log "!!! 复原未完全成功,请检查上面的手动救命命令"
  fi
}
trap cleanup EXIT

# 死手脚本写入独立文件、由 nohup 的 bash 进程执行。token 在此刻(死手刚启动时)
# 还是空的——必须让死手在 DEADMAN 秒后真正触发的那一刻才去读 TOKEN_FILE 的
# 当时内容,而不是把启动时刻的(空)变量值烘焙进命令行。
cat >"$DEADMAN_SCRIPT" <<EOF
sleep '$DEADMAN'
{
  if ! sudo pfctl -a '$ANCHOR' -F all 2>&1; then
    echo '[死手] 清空 anchor 失败,请手动执行:sudo pfctl -a $ANCHOR -F all'
  fi
  dm_token="\$(cat '$TOKEN_FILE' 2>/dev/null || true)"
  if [[ -n "\$dm_token" ]]; then
    if ! sudo pfctl -X "\$dm_token" 2>&1; then
      echo "[死手] 释放 pf 引用计数失败,请手动执行:sudo pfctl -X \$dm_token"
    fi
  fi
  echo '[死手] ${DEADMAN}s 到,已强制复原'
} >> '$DEADMAN_LOG' 2>&1
EOF

# 死手:独立于终端会话(nohup + disown),DEADMAN 秒后强制复原
# nohup 使进程忽略 SIGHUP,disown 移出作业表防止 shell 干涉
nohup bash "$DEADMAN_SCRIPT" >/dev/null 2>&1 &
DEADMAN_PID=$!
disown $DEADMAN_PID 2>/dev/null || true

# 启用 pf,捕获 token 以便后续释放。系统 grep 是 BSD grep,不支持 -P/PCRE,
# 用 awk 取最后一个字段(pfctl -E 的输出形如 "Token : 12345678")。
pf_enable_out=$(sudo pfctl -E 2>&1 || true)
PF_TOKEN=$(printf '%s\n' "$pf_enable_out" | awk '/Token/{print $NF}')
if [[ -z "$PF_TOKEN" ]]; then
  # 检查是否已启用
  if printf '%s\n' "$pf_enable_out" | grep -qi "already enabled"; then
    log "--- pf 已启用(无新 token) ---"
  else
    log "!!! 启用 pf 失败:$pf_enable_out"
    exit 1
  fi
else
  printf '%s' "$PF_TOKEN" > "$TOKEN_FILE"
  log "--- pf 已启用,token = $PF_TOKEN ---"
fi

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
sudo pfctl -f "$MAIN" || {
  log "!!! 主规则集装载失败"
  exit 1
}
sudo pfctl -a "$ANCHOR" -f "$RULES" || {
  log "!!! anchor 规则装载失败"
  exit 1
}
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
