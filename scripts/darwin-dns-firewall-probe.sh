#!/usr/bin/env bash
# macOS pf DNS 封堵机制探测。默认 dry-run 只打印计划;--execute 后先验证
# 路径 B(挂进 com.apple 命名空间,不碰主规则集,默认执行);仅当路径 B 未
# 通过且用户显式设 PROBE_REPLACE_MAIN=1 时,才验证路径 A(整体替换主规则
# 集,侵入式兜底)。两条路径各自都有死手:后台子进程在 DEADMAN 秒后无条件
# 清空各自可能留下的东西并恢复 pf 状态。
set -euo pipefail

ANCHOR_A="com.getbx.bx.probe"     # 路径 A:独立 anchor,配合整体替换主规则集
ANCHOR_B="com.apple/100.bxprobe"  # 路径 B:挂进 com.apple 命名空间,不碰主规则集
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
  log "dry-run 结束。加 --execute 才会验证两条路径(带 ${DEADMAN}s 死手):"
  log "  路径 B(默认,非侵入):挂进 com.apple/100.bxprobe,完全不碰主规则集"
  log "  路径 A(兜底,侵入,需 PROBE_REPLACE_MAIN=1):整体替换主规则集,仅在路径 B 未通过时才需要"
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
log "--- 本次先验证路径 B(不碰主规则集);若未通过且设了 PROBE_REPLACE_MAIN=1 再验证路径 A ---"

RULES="$(mktemp)"
MAIN="$(mktemp)"
TOKEN_FILE="$(mktemp)"
DEADMAN_SCRIPT="$(mktemp)"
MAIN_REPLACED_FILE="$(mktemp)"
PATHB_ANCHOR_LOADED_FILE="$(mktemp)"
PATHA_ANCHOR_LOADED_FILE="$(mktemp)"
DEADMAN_LOG="${TMPDIR:-/tmp}/darwin-dns-probe-deadman-$$.log"
PF_TOKEN=""
BACKUP=""

cleanup() {
  local pf_ok=1
  local out
  local replaced
  local pathb_loaded
  local patha_loaded

  pathb_loaded=$(cat "$PATHB_ANCHOR_LOADED_FILE" 2>/dev/null || true)
  if [[ "$pathb_loaded" == "1" ]]; then
    if out=$(sudo pfctl -a "$ANCHOR_B" -F all 2>&1); then
      log "✓ 路径 B anchor($ANCHOR_B)已清空"
    else
      pf_ok=0
      log "✗ 清空路径 B anchor 失败:$out"
      log "  请手动执行:sudo pfctl -a $ANCHOR_B -F all"
    fi
  else
    log "--- 路径 B anchor 未曾装载过,无需清理 ---"
  fi

  patha_loaded=$(cat "$PATHA_ANCHOR_LOADED_FILE" 2>/dev/null || true)
  if [[ "$patha_loaded" == "1" ]]; then
    if out=$(sudo pfctl -a "$ANCHOR_A" -F all 2>&1); then
      log "✓ 路径 A anchor($ANCHOR_A)已清空"
    else
      pf_ok=0
      log "✗ 清空路径 A anchor 失败:$out"
      log "  请手动执行:sudo pfctl -a $ANCHOR_A -F all"
    fi
  else
    log "--- 路径 A anchor 未曾装载过,无需清理 ---"
  fi

  replaced=$(cat "$MAIN_REPLACED_FILE" 2>/dev/null || true)
  if [[ "$replaced" == "1" ]]; then
    if out=$(sudo pfctl -f /etc/pf.conf 2>&1); then
      log "✓ 主规则集已还原为 /etc/pf.conf"
    else
      pf_ok=0
      log "✗ 还原主规则集失败:$out"
      log "  请手动执行:sudo pfctl -f /etc/pf.conf"
    fi
  else
    log "--- 主规则集未被本脚本替换过,无需还原 ---"
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

  rm -f "$RULES" "$MAIN" "$TOKEN_FILE" "$DEADMAN_SCRIPT" "$MAIN_REPLACED_FILE" \
        "$PATHB_ANCHOR_LOADED_FILE" "$PATHA_ANCHOR_LOADED_FILE"
  if [[ -n "$BACKUP" ]]; then
    log "--- 备份文件保留供事后排查(不会被自动删除):$BACKUP ---"
  fi

  if [[ "$pf_ok" -eq 1 ]]; then
    log "已复原:两条路径各自的 anchor 清空 + 主规则集还原(如替换过)+ pf 引用计数释放"
  else
    log "!!! 复原未完全成功,请检查上面的手动救命命令"
  fi
}
trap cleanup EXIT

# 行为验证:规则装进去不等于规则被求值,这是唯一的真凭据。B/A 两条路径复用
# 同一套检查;BH_* 是脚本级变量(非 local),调用后由调用方立即读取并计入
# 自己的判定,下一次调用会先重置。
run_behavior_checks() {
  local GW
  BH_A_RAN=0
  BH_A_OK=0
  BH_B_OK=0
  BH_C_OK=0

  log "--- a) 到私网 DNS 的查询应被阻断(期望:超时/失败) ---"
  GW="$(route -n get default 2>/dev/null | awk '/gateway/{print $2}')"
  if [[ -n "$GW" ]]; then
    BH_A_RAN=1
    if dig +time=2 +tries=1 @"$GW" example.com >/dev/null 2>&1; then
      log "  !!! 未阻断(泄漏面仍在)"
    else
      log "  已阻断 ✓"
      BH_A_OK=1
    fi
  else
    log "  (未取得默认网关,跳过此项)"
  fi

  log "--- b) 经 TUN 的公网 DNS 应仍可解析(期望:成功) ---"
  if dig +time=3 +tries=1 @8.8.8.8 example.com >/dev/null 2>&1; then
    log "  仍可解析 ✓"
    BH_B_OK=1
  else
    log "  !!! 被误封(quick 顺序有问题)"
  fi

  log "--- c) root 身份查询应放行(期望:成功) ---"
  if sudo dig +time=3 +tries=1 @8.8.8.8 example.com >/dev/null 2>&1; then
    log "  已放行 ✓"
    BH_C_OK=1
  else
    log "  !!! root 放行规则无效"
  fi
}

# 死手脚本写入独立文件、由 nohup 的 bash 进程执行。两条路径各自留下的东西、
# token、主规则集是否被替换过——这些状态在此刻(死手刚启动时)都还不知道,
# 必须让死手在 DEADMAN 秒后真正触发的那一刻才去读各自标记文件的当时内容,
# 而不是把启动时刻的(空)变量值烘焙进命令行。
cat >"$DEADMAN_SCRIPT" <<EOF
sleep '$DEADMAN'
{
  dm_pathb="\$(cat '$PATHB_ANCHOR_LOADED_FILE' 2>/dev/null || true)"
  if [[ "\$dm_pathb" == "1" ]]; then
    if ! sudo pfctl -a '$ANCHOR_B' -F all 2>&1; then
      echo '[死手] 清空路径 B anchor 失败,请手动执行:sudo pfctl -a $ANCHOR_B -F all'
    fi
  fi
  dm_patha="\$(cat '$PATHA_ANCHOR_LOADED_FILE' 2>/dev/null || true)"
  if [[ "\$dm_patha" == "1" ]]; then
    if ! sudo pfctl -a '$ANCHOR_A' -F all 2>&1; then
      echo '[死手] 清空路径 A anchor 失败,请手动执行:sudo pfctl -a $ANCHOR_A -F all'
    fi
  fi
  dm_replaced="\$(cat '$MAIN_REPLACED_FILE' 2>/dev/null || true)"
  if [[ "\$dm_replaced" == "1" ]]; then
    if ! sudo pfctl -f /etc/pf.conf 2>&1; then
      echo '[死手] 还原主规则集失败,请手动执行:sudo pfctl -f /etc/pf.conf'
    fi
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
# 用 awk 取最后一个字段(pfctl -E 的输出形如 "Token : 12345678")。两条路径
# 都需要 pf 实际处于启用状态,行为验证才有意义,故只需启用一次。
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

# DNS 封堵规则,两条路径共用同一份内容(anchor 无关,只依赖 $TUN)。
cat >"$RULES" <<EOF
pass out quick on lo0 proto { udp, tcp } to port 53
pass out quick on $TUN proto { udp, tcp } to port 53
pass out quick proto { udp, tcp } to port 53 user root
block out quick proto { udp, tcp } to port 53
EOF

# ============================================================
# 路径 B(默认,非侵入):man 5 pf.conf 明示 anchor "com.apple/*" 会按字母序
# 求值所有挂在 com.apple 下的子 anchor(Apple 自己就是这么组织 AirDrop /
# ApplicationFirewall 的)。若我们能把规则挂进 com.apple/100.bxprobe 并被
# 真正求值,就完全不需要碰主规则集,与 Little Snitch / MDM 等第三方 pf 用
# 户零冲突。但 SIP 会不会拦、第三方能不能往 com.apple 命名空间装,必须真机
# 验证——下面只做验证,不预设结论。
# ============================================================
log ""
log "=== 路径 B(默认,非侵入):挂进 $ANCHOR_B,完全不碰主规则集 ==="

log "--- 装载前 com.apple 下已有子 anchor(供比对) ---"
apple_anchors_before=$(sudo pfctl -a com.apple -s Anchors 2>&1 || true)
printf '%s\n' "$apple_anchors_before"
airdrop_before=$(sudo pfctl -a "com.apple/200.AirDrop" -s rules 2>&1 || true)
appfw_before=$(sudo pfctl -a "com.apple/250.ApplicationFirewall" -s rules 2>&1 || true)

PATHB_LOAD_OK=0
PATHB_RULES_LISTED_OK=0
PATHB_APPLE_UNAFFECTED_OK=0
BH_A_RAN=0; BH_A_OK=0; BH_B_OK=0; BH_C_OK=0

log "--- 尝试装载:sudo pfctl -a $ANCHOR_B -f \$RULES ---"
if out=$(sudo pfctl -a "$ANCHOR_B" -f "$RULES" 2>&1); then
  echo 1 > "$PATHB_ANCHOR_LOADED_FILE"
  PATHB_LOAD_OK=1
  log "✓ 装载成功(SIP 未拦截往 com.apple 命名空间写规则)"
else
  log "✗ 装载失败:$out"
  log "  (可能是 SIP 拦截了往 com.apple 命名空间写规则,这正是本次要验证的问题)"
fi

if [[ "$PATHB_LOAD_OK" -eq 1 ]]; then
  log "--- 实际求值顺序(pfctl -a com.apple -s Anchors,按字母序,数字前缀决定谁先匹配) ---"
  sudo pfctl -a com.apple -s Anchors || true

  log "--- 规则是否列出(pfctl -a $ANCHOR_B -s rules) ---"
  pathb_rules_listed=$(sudo pfctl -a "$ANCHOR_B" -s rules 2>&1 || true)
  printf '%s\n' "$pathb_rules_listed"
  if [[ -n "$pathb_rules_listed" ]]; then
    PATHB_RULES_LISTED_OK=1
  else
    log "!!! anchor 内规则为空,装载看似成功但未生效"
  fi

  log "--- 行为验证(真凭据:规则装进去不等于规则被求值) ---"
  run_behavior_checks

  log "--- 清理路径 B:sudo pfctl -a $ANCHOR_B -F all ---"
  if out=$(sudo pfctl -a "$ANCHOR_B" -F all 2>&1); then
    log "✓ 已清空"
  else
    log "✗ 清空失败:$out"
    log "  请手动执行:sudo pfctl -a $ANCHOR_B -F all"
  fi

  log "--- 核对 com.apple 其它子 anchor 是否受影响(这点必须显式检查,因为我们动的是 Apple 的命名空间) ---"
  airdrop_after=$(sudo pfctl -a "com.apple/200.AirDrop" -s rules 2>&1 || true)
  appfw_after=$(sudo pfctl -a "com.apple/250.ApplicationFirewall" -s rules 2>&1 || true)
  apple_anchors_after=$(sudo pfctl -a com.apple -s Anchors 2>&1 || true)
  log "--- 清理后 com.apple 下的子 anchor ---"
  printf '%s\n' "$apple_anchors_after"
  if [[ "$airdrop_before" == "$airdrop_after" && "$appfw_before" == "$appfw_after" ]]; then
    log "✓ 200.AirDrop / 250.ApplicationFirewall 内容前后一致,未受影响"
    PATHB_APPLE_UNAFFECTED_OK=1
  else
    log "!!! 200.AirDrop 或 250.ApplicationFirewall 内容前后不一致,可能被路径 B 影响到了"
    if [[ "$airdrop_before" != "$airdrop_after" ]]; then
      log "  200.AirDrop 装载前:"
      printf '%s\n' "$airdrop_before"
      log "  200.AirDrop 清理后:"
      printf '%s\n' "$airdrop_after"
    fi
    if [[ "$appfw_before" != "$appfw_after" ]]; then
      log "  250.ApplicationFirewall 装载前:"
      printf '%s\n' "$appfw_before"
      log "  250.ApplicationFirewall 清理后:"
      printf '%s\n' "$appfw_after"
    fi
  fi
fi

PATHB_VERDICT="不可行"
if [[ "$PATHB_LOAD_OK" -eq 1 && "$PATHB_RULES_LISTED_OK" -eq 1 && "$BH_B_OK" -eq 1 \
      && "$BH_C_OK" -eq 1 && ( "$BH_A_RAN" -eq 0 || "$BH_A_OK" -eq 1 ) \
      && "$PATHB_APPLE_UNAFFECTED_OK" -eq 1 ]]; then
  PATHB_VERDICT="可行"
fi

load_status="否"; [[ "$PATHB_LOAD_OK" -eq 1 ]] && load_status="是"

# 下面几项只有在装载成功、真的跑过对应检查时才有意义;装载失败时它们从未
# 被执行,必须显示为"未测"而不是"否"——"否"意味着"测过且不通过",两者
# 语义不同,不能混淆,否则会被误读成"已验证为受影响/不通过"。
listed_status="未测"
a_status="未测"
b_status="未测"
c_status="未测"
unaffected_status="未测"
if [[ "$PATHB_LOAD_OK" -eq 1 ]]; then
  listed_status="否"; [[ "$PATHB_RULES_LISTED_OK" -eq 1 ]] && listed_status="是"
  if [[ "$BH_A_RAN" -eq 1 ]]; then
    if [[ "$BH_A_OK" -eq 1 ]]; then a_status="是"; else a_status="否"; fi
  fi
  b_status="否"; [[ "$BH_B_OK" -eq 1 ]] && b_status="是"
  c_status="否"; [[ "$BH_C_OK" -eq 1 ]] && c_status="是"
  unaffected_status="否"; [[ "$PATHB_APPLE_UNAFFECTED_OK" -eq 1 ]] && unaffected_status="是"
fi

log ""
log "=== 结论摘要:路径 B(挂进 com.apple 命名空间)是否可行? ==="
log "  装载是否成功(SIP 未拦截):    $load_status"
log "  规则是否列出:                  $listed_status"
log "  a) 私网 DNS 被阻断:            $a_status"
log "  b) 经 TUN 公网 DNS 可解析:     $b_status"
log "  c) root 身份放行:              $c_status"
log "  Apple 其它子 anchor 未受影响:  $unaffected_status"
log "  >>> 路径 B 结论:$PATHB_VERDICT <<<"

# ============================================================
# 路径 A(兜底,侵入):仅当"路径 B 未通过验证"与"用户显式设
# PROBE_REPLACE_MAIN=1"两个条件同时成立才执行——是"与"不是"或"。哪怕环境
# 变量因为别的原因(比如 shell 里有残留 export)恰好是 1,只要路径 B 已判定
# 可行,也绝不执行这一步侵入式的主规则集替换。完整保留备份 / 第三方内容护
# 栏 / FORCE_REPLACE_MAIN / 主规则集条件还原这一整套机制。
# ============================================================
log ""
if [[ "$PATHB_VERDICT" == "可行" ]]; then
  log "=== 路径 B 已验证可行,路径 A 无需验证,已跳过 ==="
  if [[ "${PROBE_REPLACE_MAIN:-0}" == "1" ]]; then
    log "--- 检测到 PROBE_REPLACE_MAIN=1,但路径 B 已可行,忽略该设置,不执行侵入式路径 A ---"
  fi
else
  log "=== 路径 B 未通过验证 ==="
  if [[ "${PROBE_REPLACE_MAIN:-0}" != "1" ]]; then
    log "如需验证兜底路径 A(会整体替换主规则集,带完整备份/第三方内容护栏),设 PROBE_REPLACE_MAIN=1 重跑。"
  fi
fi

if [[ "$PATHB_VERDICT" != "可行" && "${PROBE_REPLACE_MAIN:-0}" == "1" ]]; then
  log ""
  log "=== 路径 A(兜底,侵入):路径 B 未通过 + PROBE_REPLACE_MAIN=1 已设置,开始验证 ==="

  # 关键验证:主规则集必须引用我们的 anchor,规则才会被求值。
  # 只在内存中替换主规则集(保留 Apple 的 anchor),不写 /etc/pf.conf。
  cat >"$MAIN" <<EOF
scrub-anchor "com.apple/*"
nat-anchor "com.apple/*"
rdr-anchor "com.apple/*"
dummynet-anchor "com.apple/*"
anchor "com.apple/*"
load anchor "com.apple" from "/etc/pf.anchors/com.apple"
anchor "$ANCHOR_A"
EOF

  # pfctl -f 是整体替换语义,会把内存里当前实时的主规则集整个换掉,不是追加。
  # /etc/pf.conf 在磁盘上是出厂状态不代表内存里的实时规则集也是——可能有别
  # 的工具装了东西。装载前先把实时规则集备份下来(不自动删除,供事后排
  # 查),再核对一遍:如果发现非 com.apple、非本脚本 anchor 的内容,说明会
  # 覆盖到第三方的东西,默认中止,除非用户显式设 FORCE_REPLACE_MAIN=1 确认
  # 接受。
  log "=== 装载前:备份当前实时 pf 规则集,并核对是否有第三方内容 ==="
  BACKUP="${TMPDIR:-/tmp}/darwin-dns-probe-backup-$$.txt"
  live_rules=$(sudo pfctl -s rules 2>&1 || true)
  live_nat=$(sudo pfctl -s nat 2>&1 || true)
  live_anchors=$(sudo pfctl -s Anchors 2>&1 || true)
  {
    echo "=== pfctl -s rules ($(date)) ==="
    printf '%s\n' "$live_rules"
    echo
    echo "=== pfctl -s nat ==="
    printf '%s\n' "$live_nat"
    echo
    echo "=== pfctl -s Anchors ==="
    printf '%s\n' "$live_anchors"
  } > "$BACKUP"
  log "--- 已备份到:$BACKUP(不会被自动删除,供事后排查) ---"

  bad_rules=$(printf '%s\n' "$live_rules" | grep -vE '^[[:space:]]*$' | grep -viE 'com\.apple' | grep -vF "$ANCHOR_A" || true)
  bad_nat=$(printf '%s\n' "$live_nat" | grep -vE '^[[:space:]]*$' | grep -viE 'com\.apple' | grep -vF "$ANCHOR_A" || true)
  bad_anchors=$(printf '%s\n' "$live_anchors" | grep -vE '^[[:space:]]*$' | grep -viE '^com\.apple' | grep -vF "$ANCHOR_A" || true)

  if [[ -n "$bad_rules" || -n "$bad_nat" || -n "$bad_anchors" ]]; then
    log ""
    log "!!! 警告:实时 pf 主规则集里存在非 com.apple、非本脚本 anchor 的内容 !!!"
    log "!!! sudo pfctl -f \"\$MAIN\" 会整体替换主规则集,可能覆盖第三方工具装的规则,且无法保证完美复原 !!!"
    if [[ -n "$bad_rules" ]]; then
      log "--- 可疑规则行(pfctl -s rules) ---"
      printf '%s\n' "$bad_rules" | while IFS= read -r l; do log "  $l"; done
    fi
    if [[ -n "$bad_nat" ]]; then
      log "--- 可疑 NAT 规则(pfctl -s nat) ---"
      printf '%s\n' "$bad_nat" | while IFS= read -r l; do log "  $l"; done
    fi
    if [[ -n "$bad_anchors" ]]; then
      log "--- 可疑 anchor(pfctl -s Anchors) ---"
      printf '%s\n' "$bad_anchors" | while IFS= read -r l; do log "  $l"; done
    fi
    log "完整内容见备份:$BACKUP"
    if [[ "${FORCE_REPLACE_MAIN:-0}" != "1" ]]; then
      log "已中止,未替换主规则集,系统状态未改动。若确认可以接受覆盖以上内容,设 FORCE_REPLACE_MAIN=1 重跑。"
      exit 1
    fi
    log "--- FORCE_REPLACE_MAIN=1 已设置,按用户确认继续覆盖 ---"
  else
    log "--- 实时主规则集与出厂状态(仅 com.apple)一致,可以安全替换 ---"
  fi

  log "=== 装载(内存中),验证 anchor 是否真的被求值 ==="
  sudo pfctl -f "$MAIN" || {
    log "!!! 主规则集装载失败"
    exit 1
  }
  echo 1 > "$MAIN_REPLACED_FILE"
  sudo pfctl -a "$ANCHOR_A" -f "$RULES" || {
    log "!!! anchor 规则装载失败"
    exit 1
  }
  echo 1 > "$PATHA_ANCHOR_LOADED_FILE"
  log "--- anchor 内规则(为空则说明未生效,这是头号风险) ---"
  sudo pfctl -a "$ANCHOR_A" -s rules

  log "--- 行为验证 ---"
  run_behavior_checks

  log "=== 验证救命命令只清 bx 规则 ==="
  sudo pfctl -a "$ANCHOR_A" -F all
  log "--- anchor 应为空,而 com.apple 应仍在 ---"
  sudo pfctl -a "$ANCHOR_A" -s rules || true
  sudo pfctl -a "com.apple" -s rules 2>/dev/null | head -3 || true
fi

kill "$DEADMAN_PID" 2>/dev/null || true
log "=== 探测完成,trap 将复原 ==="
