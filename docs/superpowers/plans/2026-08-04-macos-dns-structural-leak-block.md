# macOS DNS 结构性防泄漏 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 macOS 上的 DNS 查询在系统 DNS 被改动时也无法明文离开物理网卡,同时保证任何失败路径都不会把用户永久锁在断网状态。

**Architecture:** 新增 pf anchor 提供结构性封堵(不依赖轮询及时性),10s 巡检降级为"修复 + 如实上报"职责,退出语义如实告知真实原因。三者共用既有的 Guardian 屏障事务与 `CommandRunner` 抽象,不发明新的失败路径。

**Tech Stack:** Go 1.26,macOS `pfctl`,既有 `internal/guardian` 屏障/DNS 生命周期,Swift(BxMenu 文案)。

设计文档:`docs/superpowers/specs/2026-08-04-macos-dns-structural-leak-block-design.md`

## Global Constraints

- 平台:pf 相关代码一律 `//go:build darwin`,非 darwin 走 no-op 实现;`GOOS=linux`/`windows` 构建不得受影响。
- **绝不写入 `/etc/pf.conf`**。这是"重启必然恢复"逃生口的前提,由源码守卫测试钉死。
- anchor 名固定 `com.getbx.bx`。救命命令固定 `sudo pfctl -a com.getbx.bx -F all`。
- anchor 的存在严格跟随保护**终态**:终态为关闭则必须不残留;终态为受保护(如 DNS 还原失败被回滚)则应当保留。不存在"无条件拆除"。
- 不得在实现期间执行 `bx up`/`down`/`reconnect`/`dns on|off`/`networksetup`/`pfctl` 改动类命令。真机验证由用户执行。
- 纯逻辑测试免 root(用 `t.TempDir()` 与假 runner),不碰真实 pf、路由或设备。
- TDD:先写失败测试→跑红→最小实现→跑绿→提交。中文 conventional commits,结尾带 `Co-Authored-By: Claude …`。
- 验证命令:`go build ./... && go vet ./... && go test ./...`;跨平台 `GOOS=linux/windows GOARCH=amd64 go build -o /dev/null ./...`。

---

### Task 1: pf 机制真机验证脚本

**为什么这是第一个任务:** macOS 的 `/etc/pf.conf` 只引用 `com.apple/*`,**没有引用 `com.getbx.bx`**。pf 规则装进未被主规则集引用的 anchor 里完全不生效。设计文档的 4 条待验证项(规则 `quick` 顺序、TUN 名替换、`pfctl -e` 状态管理、与 route 屏障的相互作用)全部依赖真机答案。先写代码等于在未验证的假设上盖楼。

本任务交付一个**只读为主、带死手复原**的探测脚本,由用户在真机执行,把答案填回设计文档。

**Files:**
- Create: `scripts/darwin-dns-firewall-probe.sh`
- Modify: `docs/superpowers/specs/2026-08-04-macos-dns-structural-leak-block-design.md`(填入验证结果)

**Interfaces:**
- Consumes: 无
- Produces: 真机验证结论,供 Task 2 决定规则文本与 anchor 引用方式

- [ ] **Step 1: 写探测脚本**

创建 `scripts/darwin-dns-firewall-probe.sh`:

```bash
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
```

- [ ] **Step 2: 语法检查并提交脚本**

```bash
bash -n scripts/darwin-dns-firewall-probe.sh
chmod +x scripts/darwin-dns-firewall-probe.sh
git add scripts/darwin-dns-firewall-probe.sh
git commit -m "test(macos): 加 pf DNS 封堵机制真机探测脚本"
```

- [ ] **Step 3: 请用户在真机执行并回填结果**

先让用户跑 dry-run 确认无副作用(会说明将验证路径 B、路径 A 两条路径):

```bash
scripts/darwin-dns-firewall-probe.sh
```

再在 bx 运行状态下执行(带死手,60s 自动复原)。**默认只验证路径 B**(挂进
`com.apple/100.bxprobe`,完全不碰主规则集,不需要任何护栏开关):

```bash
sudo bx up
sudo scripts/darwin-dns-firewall-probe.sh --execute
```

脚本结尾会打印一段"结论摘要",直接回答路径 B 是否可行。**停在这里等用户回报结果。**
需要确认:

1. **路径 B 结论摘要**(脚本自动打印,是本设计的头号决策依据):装载是否成功(SIP
   是否拦截了往 `com.apple` 命名空间写规则)、`pfctl -a com.apple/100.bxprobe -s rules`
   规则是否列出、a/b/c 三项行为验证是否分别为"已阻断 / 仍可解析 / 已放行"、
   `200.AirDrop` / `250.ApplicationFirewall` 两个 Apple 子 anchor 装载前后内容是否
   一致(未受影响)。**若结论为"可行",本设计应优先采用路径 B**——完全不碰主规则
   集,与 Little Snitch / MDM 等第三方 pf 用户零冲突,原先"anchor 未被求值就要换路
   子"的头号风险不再适用。
2. 若结论为"不可行",看摘要里具体是哪一项失败:装载失败通常意味着 SIP 挡了
   `com.apple` 命名空间;规则未列出说明装载看似成功但未生效;a/b/c 失败说明
   `quick` 顺序或规则内容有误;Apple 子 anchor 受影响则路径 B 本身不安全,不能采用。
3. 若路径 B 不可行,需要验证兜底路径 A,让用户显式加 `PROBE_REPLACE_MAIN=1` 重跑一次:
   ```bash
   sudo PROBE_REPLACE_MAIN=1 scripts/darwin-dns-firewall-probe.sh --execute
   ```
   再确认路径 A 自己的几点:`pfctl -a com.getbx.bx.probe -s rules` 是否列出四条规则
   (为空说明 anchor 未被主规则集求值,整个方案要重新评估)、a/b/c 是否符合预期、
   救命命令(`sudo pfctl -a com.getbx.bx.probe -F all`)后 `com.apple` anchor 是否完好。
4. 第 2 步只读探测里 pf 原本是否已启用(决定 Remove 时是否需要恢复原状态)——两条
   路径共用同一次启用,只需确认一次。

- [ ] **Step 4: 把结果写回设计文档**

将四项答案填入设计文档的"待验证项"一节,把已确认的改写为结论,未通过的记为需要改方案。提交:

```bash
git add docs/superpowers/specs/2026-08-04-macos-dns-structural-leak-block-design.md
git commit -m "docs(macos): 回填 pf DNS 封堵机制真机验证结果"
```

---

### Task 2: pf 规则与救命命令的纯逻辑

**Files:**
- Create: `internal/guardian/dnsfirewall.go`
- Test: `internal/guardian/dnsfirewall_test.go`

**Interfaces:**
- Consumes: Task 1 确认的规则文本与 anchor 引用方式
- Produces:
  - `const DNSFirewallAnchor = "com.getbx.bx"`
  - `func DNSFirewallRecoveryCommand() string`
  - `func dnsFirewallRules(tunDevice string) string`
  - `func dnsFirewallMainRuleset(anchor string) string`

- [ ] **Step 1: 写失败测试**

创建 `internal/guardian/dnsfirewall_test.go`:

```go
package guardian

import (
	"strings"
	"testing"
)

func TestDNSFirewallRulesPassBeforeBlock(t *testing.T) {
	rules := dnsFirewallRules("utun4")
	blockIdx := strings.Index(rules, "block out")
	if blockIdx < 0 {
		t.Fatal("规则必须封禁出站 :53")
	}
	for _, pass := range []string{
		"pass out quick on lo0",
		"pass out quick on utun4",
		"user root",
	} {
		idx := strings.Index(rules, pass)
		if idx < 0 {
			t.Fatalf("缺少放行规则:%s", pass)
		}
		if idx > blockIdx {
			t.Fatalf("放行规则 %q 排在 block 之后,quick 语义下永远不会生效", pass)
		}
	}
}

func TestDNSFirewallRulesAllQuick(t *testing.T) {
	for _, line := range strings.Split(strings.TrimSpace(dnsFirewallRules("utun4")), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.Contains(line, " quick ") {
			t.Errorf("规则缺少 quick,匹配顺序将不确定:%s", line)
		}
	}
}

func TestDNSFirewallRulesUseGivenTunDevice(t *testing.T) {
	rules := dnsFirewallRules("utun9")
	if !strings.Contains(rules, "on utun9") {
		t.Error("必须使用传入的 TUN 接口名")
	}
	if strings.Contains(rules, "<tun>") {
		t.Error("模板占位符未被替换")
	}
}

func TestDNSFirewallRecoveryCommandIsAnchorScoped(t *testing.T) {
	got := DNSFirewallRecoveryCommand()
	want := "sudo pfctl -a com.getbx.bx -F all"
	if got != want {
		t.Errorf("救命命令 = %q, want %q", got, want)
	}
	if strings.Contains(got, "-f /etc/pf.conf") || strings.Contains(got, "pfctl -d") {
		t.Error("救命命令不得是全局操作,那会抹掉用户自己的 pf 配置")
	}
}

func TestDNSFirewallMainRulesetKeepsAppleAnchors(t *testing.T) {
	main := dnsFirewallMainRuleset(DNSFirewallAnchor)
	for _, apple := range []string{
		`scrub-anchor "com.apple/*"`,
		`nat-anchor "com.apple/*"`,
		`rdr-anchor "com.apple/*"`,
		`dummynet-anchor "com.apple/*"`,
		`anchor "com.apple/*"`,
		`load anchor "com.apple" from "/etc/pf.anchors/com.apple"`,
	} {
		if !strings.Contains(main, apple) {
			t.Errorf("主规则集必须保留 Apple 的 anchor:%s", apple)
		}
	}
	if !strings.Contains(main, `anchor "com.getbx.bx"`) {
		t.Error("主规则集必须引用 bx 的 anchor,否则规则不会被求值")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/guardian/ -run TestDNSFirewall -count=1`
Expected: FAIL,`undefined: dnsFirewallRules`

- [ ] **Step 3: 最小实现**

创建 `internal/guardian/dnsfirewall.go`:

```go
package guardian

import "strings"

// DNSFirewallAnchor 是 bx 独占的 pf anchor 名。用独立 anchor 而非写入
// /etc/pf.conf,使救命命令可以只清 bx 的规则、不碰用户配置,并让重启必然清空。
const DNSFirewallAnchor = "com.getbx.bx"

// DNSFirewallRecoveryCommand 返回用户手动解除封堵的命令。任何无法自行拆除
// 规则的失败路径都必须把它展示给用户。
func DNSFirewallRecoveryCommand() string {
	return "sudo pfctl -a " + DNSFirewallAnchor + " -F all"
}

// dnsFirewallRules 渲染 anchor 内的规则。
//
// 顺序是正确性核心:pf 的 quick 表示"先匹配先生效",放行规则必须全部排在
// block 之前。放行三类——loopback(bx 解析器监听 127.0.0.1:53)、TUN(漂移到
// 公网 DNS 时查询会路由进 TUN 并由引擎正常处理,这条合法路径不能误封)、
// root(bx 解析器要查上游 DNS 做分流判断)。其余出站 :53 一律封死。
func dnsFirewallRules(tunDevice string) string {
	lines := []string{
		"pass out quick on lo0 proto { udp, tcp } to port 53",
		"pass out quick on " + tunDevice + " proto { udp, tcp } to port 53",
		"pass out quick proto { udp, tcp } to port 53 user root",
		"block out quick proto { udp, tcp } to port 53",
	}
	return strings.Join(lines, "\n") + "\n"
}

// dnsFirewallMainRuleset 渲染主规则集。
//
// macOS 的 /etc/pf.conf 只引用 com.apple/*,规则装进未被引用的 anchor 里
// 完全不生效,所以必须让主规则集引用 bx 的 anchor。这份主规则集只在内存中
// 装载(pfctl -f),绝不写回 /etc/pf.conf——重启后系统自动恢复原状,这是
// 用户永远可用的逃生口。
func dnsFirewallMainRuleset(anchor string) string {
	lines := []string{
		`scrub-anchor "com.apple/*"`,
		`nat-anchor "com.apple/*"`,
		`rdr-anchor "com.apple/*"`,
		`dummynet-anchor "com.apple/*"`,
		`anchor "com.apple/*"`,
		`load anchor "com.apple" from "/etc/pf.anchors/com.apple"`,
		`anchor "` + anchor + `"`,
	}
	return strings.Join(lines, "\n") + "\n"
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/guardian/ -run TestDNSFirewall -count=1`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/guardian/dnsfirewall.go internal/guardian/dnsfirewall_test.go
git commit -m "feat(guardian): 定义 DNS 防泄漏 pf 规则与救命命令"
```

---

### Task 3: pf 执行层与"永不写 /etc/pf.conf"守卫

**Files:**
- Create: `internal/guardian/dnsfirewall_darwin.go`
- Create: `internal/guardian/dnsfirewall_other.go`
- Modify: `internal/guardian/types.go`(新增 `DNSFirewall` 接口)
- Test: `internal/guardian/dnsfirewall_exec_test.go`

**Interfaces:**
- Consumes: `dnsFirewallRules`、`dnsFirewallMainRuleset`、`DNSFirewallAnchor`(Task 2);既有 `CommandRunner`、`Command`(`barrier.go:20-34`)
- Produces:
  - `type DNSFirewall interface { Install(context.Context, string) error; Remove(context.Context) error }`
  - `func NewDNSFirewall(runner CommandRunner, rulesDir string) DNSFirewall`

- [ ] **Step 1: 写失败测试**

创建 `internal/guardian/dnsfirewall_exec_test.go`:

```go
package guardian

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type recordingRunner struct {
	commands []Command
	fail     map[string]error
}

func (r *recordingRunner) Run(_ context.Context, command Command) error {
	r.commands = append(r.commands, command)
	if err, ok := r.fail[strings.Join(command.Args, " ")]; ok {
		return err
	}
	return nil
}

func (r *recordingRunner) joined() []string {
	out := make([]string, 0, len(r.commands))
	for _, c := range r.commands {
		out = append(out, c.String())
	}
	return out
}

func TestDNSFirewallInstallLoadsAnchorAndMainRuleset(t *testing.T) {
	dir := t.TempDir()
	runner := &recordingRunner{}
	if err := NewDNSFirewall(runner, dir).Install(context.Background(), "utun4"); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(runner.joined(), "\n")
	if !strings.Contains(got, "-a "+DNSFirewallAnchor+" -f ") {
		t.Errorf("必须把规则装进 bx 的 anchor,实际执行:\n%s", got)
	}
	if !strings.Contains(got, "-f "+filepath.Join(dir, "pf-main.conf")) {
		t.Errorf("必须装载引用了该 anchor 的主规则集,实际执行:\n%s", got)
	}
	for _, c := range runner.commands {
		if strings.Contains(strings.Join(c.Args, " "), "/etc/pf.conf") {
			t.Fatal("绝不能装载或写入 /etc/pf.conf,那会毁掉重启逃生口")
		}
	}
}

func TestDNSFirewallInstallWritesRuleFilesUnderGivenDir(t *testing.T) {
	dir := t.TempDir()
	if err := NewDNSFirewall(&recordingRunner{}, dir).Install(context.Background(), "utun7"); err != nil {
		t.Fatal(err)
	}
	rules, err := os.ReadFile(filepath.Join(dir, "pf-anchor.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rules), "on utun7") {
		t.Error("规则文件必须使用实际 TUN 接口名")
	}
	info, err := os.Stat(filepath.Join(dir, "pf-anchor.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("规则文件权限 = %o, want 600", perm)
	}
}

func TestDNSFirewallRemoveFlushesOnlyOwnAnchor(t *testing.T) {
	runner := &recordingRunner{}
	if err := NewDNSFirewall(runner, t.TempDir()).Remove(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(runner.joined(), "\n")
	if !strings.Contains(got, "-a "+DNSFirewallAnchor+" -F all") {
		t.Errorf("必须按 anchor 清除,实际执行:\n%s", got)
	}
	if strings.Contains(got, "pfctl -d") {
		t.Error("不得全局禁用 pf,那会抹掉用户自己的规则")
	}
}

// 这条守卫保护的是"重启必然恢复"逃生口。它一旦被破坏,用户可能被永久锁在
// 断网状态,因此必须钉死在源码层面。
func TestGuardianNeverWritesSystemPfConf(t *testing.T) {
	entries, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range entries {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(source), `"/etc/pf.conf"`) {
			t.Errorf("%s 引用了 /etc/pf.conf;bx 绝不能读写它,否则重启逃生口失效", name)
		}
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/guardian/ -run 'TestDNSFirewall(Install|Remove)|TestGuardianNeverWrites' -count=1`
Expected: FAIL,`undefined: NewDNSFirewall`

- [ ] **Step 3: 加接口定义**

在 `internal/guardian/types.go` 的 `DNSManager` 接口之后追加:

```go
// DNSFirewall 提供结构性 DNS 防泄漏:系统 DNS 被改成什么,查询都无法明文
// 离开物理网卡。与轮询互补——轮询天然有窗口,这里没有。
type DNSFirewall interface {
	// Install 装载封堵规则。tunDevice 是当前 TUN 接口名,其上的 :53 必须放行。
	Install(ctx context.Context, tunDevice string) error
	// Remove 清除 bx 自己的规则,不触碰用户其它 pf 配置。
	Remove(ctx context.Context) error
}
```

- [ ] **Step 4: 实现 darwin 执行层**

创建 `internal/guardian/dnsfirewall_darwin.go`:

```go
//go:build darwin

package guardian

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const darwinPfctlPath = "/sbin/pfctl"

type darwinDNSFirewall struct {
	runner   CommandRunner
	rulesDir string
}

// NewDNSFirewall 构造 macOS 的结构性 DNS 封堵器。rulesDir 用于存放临时规则
// 文件(bx 运行时目录),绝不使用 /etc/pf.conf。
func NewDNSFirewall(runner CommandRunner, rulesDir string) DNSFirewall {
	if runner == nil {
		runner = darwinRunner{}
	}
	return darwinDNSFirewall{runner: runner, rulesDir: rulesDir}
}

func (f darwinDNSFirewall) Install(ctx context.Context, tunDevice string) error {
	anchorPath := filepath.Join(f.rulesDir, "pf-anchor.conf")
	mainPath := filepath.Join(f.rulesDir, "pf-main.conf")
	if err := writeRuleFile(anchorPath, dnsFirewallRules(tunDevice)); err != nil {
		return err
	}
	if err := writeRuleFile(mainPath, dnsFirewallMainRuleset(DNSFirewallAnchor)); err != nil {
		return err
	}
	// pf 可能本来就启用,重复启用会报错,容忍之。
	if err := f.run(ctx, []string{"-e"}, isPfAlreadyEnabled); err != nil {
		return err
	}
	// 先装主规则集(它引用了 anchor),再往 anchor 里装规则。
	if err := f.run(ctx, []string{"-f", mainPath}, nil); err != nil {
		return err
	}
	return f.run(ctx, []string{"-a", DNSFirewallAnchor, "-f", anchorPath}, nil)
}

func (f darwinDNSFirewall) Remove(ctx context.Context) error {
	// 只清自己的 anchor。刻意不执行 pfctl -d,也不重载 /etc/pf.conf——
	// 那会抹掉用户自己的 pf 配置。主规则集里残留的 anchor 引用是惰性的
	// (anchor 为空则不匹配任何包),且重启后随 /etc/pf.conf 自动消失。
	if err := f.run(ctx, []string{"-a", DNSFirewallAnchor, "-F", "all"}, isPfAnchorEmpty); err != nil {
		return err
	}
	for _, name := range []string{"pf-anchor.conf", "pf-main.conf"} {
		if err := os.Remove(filepath.Join(f.rulesDir, name)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", name, err)
		}
	}
	return nil
}

func (f darwinDNSFirewall) run(ctx context.Context, args []string, tolerated func(error) bool) error {
	command := Command{Name: darwinPfctlPath, Args: args}
	if err := f.runner.Run(ctx, command); err != nil {
		if tolerated != nil && tolerated(err) {
			return nil
		}
		return fmt.Errorf("run %s: %w", command.String(), err)
	}
	return nil
}

func writeRuleFile(path, contents string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create pf rules dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		return fmt.Errorf("write pf rules %s: %w", path, err)
	}
	return nil
}

func isPfAlreadyEnabled(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "already enabled")
}

func isPfAnchorEmpty(err error) bool {
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "no such anchor") || strings.Contains(lower, "not found")
}
```

- [ ] **Step 5: 实现非 darwin 的 no-op**

创建 `internal/guardian/dnsfirewall_other.go`:

```go
//go:build !darwin

package guardian

import "context"

type noopDNSFirewall struct{}

// NewDNSFirewall 在非 macOS 平台返回 no-op。Linux 与 Windows 的 DNS 防泄漏
// 由各自的数据面负责(DNS-into-TUN、WFP),不走 pf。
func NewDNSFirewall(CommandRunner, string) DNSFirewall { return noopDNSFirewall{} }

func (noopDNSFirewall) Install(context.Context, string) error { return nil }

func (noopDNSFirewall) Remove(context.Context) error { return nil }
```

- [ ] **Step 6: 跑测试确认通过**

```bash
go test ./internal/guardian/ -run 'TestDNSFirewall|TestGuardianNeverWrites' -count=1
go test ./internal/guardian/ -count=1
GOOS=linux GOARCH=amd64 go build -o /dev/null ./...
GOOS=windows GOARCH=amd64 go build -o /dev/null ./...
```
Expected: 全部 PASS / rc=0

- [ ] **Step 7: 验证守卫确实有牙齿**

守卫写完即通过等于没验证过。临时在 `dnsfirewall_darwin.go` 顶部加一行
`var _ = "/etc/pf.conf"`,跑:

```bash
go test ./internal/guardian/ -run TestGuardianNeverWritesSystemPfConf -count=1
```
Expected: FAIL,提示该文件引用了 /etc/pf.conf。**删除该行**后重跑,Expected: PASS。

- [ ] **Step 8: 提交**

```bash
git add internal/guardian/dnsfirewall_darwin.go internal/guardian/dnsfirewall_other.go \
        internal/guardian/dnsfirewall_exec_test.go internal/guardian/types.go
git commit -m "feat(guardian): 实现 pf DNS 封堵执行层与逃生口守卫"
```

---

### Task 4: Manager 接线——按保护终态装拆,启动时对齐

**Files:**
- Modify: `internal/guardian/manager.go`(`ManagerOptions`、`NewManager`、`ensureDNSManaged`、`Down`、`RunStartupRecovery`)
- Modify: `internal/guardian/daemon.go:322` 附近(生产接线)
- Test: `internal/guardian/dnsfirewall_manager_test.go`

**Interfaces:**
- Consumes: `DNSFirewall`(Task 3)
- Produces:
  - `ManagerOptions.DNSFirewall DNSFirewall`
  - `func (m *Manager) reconcileDNSFirewall(ctx context.Context, desired DesiredState) error`

- [ ] **Step 1: 写失败测试**

创建 `internal/guardian/dnsfirewall_manager_test.go`:

```go
package guardian

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type fakeDNSFirewall struct {
	mu        sync.Mutex
	installed bool
	installs  int
	removes   int
	removeErr error
}

func (f *fakeDNSFirewall) Install(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installs++
	f.installed = true
	return nil
}

func (f *fakeDNSFirewall) Remove(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removes++
	if f.removeErr != nil {
		return f.removeErr
	}
	f.installed = false
	return nil
}

func (f *fakeDNSFirewall) isInstalled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.installed
}

func TestReconcileDNSFirewallRemovesWhenDesiredOff(t *testing.T) {
	firewall := &fakeDNSFirewall{installed: true}
	m := &Manager{dnsFirewall: firewall}
	if err := m.reconcileDNSFirewall(context.Background(), DesiredOff); err != nil {
		t.Fatal(err)
	}
	if firewall.isInstalled() {
		t.Error("期望状态为 off 时不得残留封堵规则,否则用户被锁在断网状态")
	}
}

func TestReconcileDNSFirewallDoesNotInstallWhenDesiredOff(t *testing.T) {
	firewall := &fakeDNSFirewall{}
	m := &Manager{dnsFirewall: firewall}
	if err := m.reconcileDNSFirewall(context.Background(), DesiredOff); err != nil {
		t.Fatal(err)
	}
	if firewall.installs != 0 {
		t.Error("期望状态为 off 时绝不能装载封堵规则")
	}
}

func TestReconcileDNSFirewallSurfacesRemovalFailure(t *testing.T) {
	firewall := &fakeDNSFirewall{installed: true, removeErr: errors.New("pfctl busy")}
	m := &Manager{dnsFirewall: firewall}
	err := m.reconcileDNSFirewall(context.Background(), DesiredOff)
	if err == nil {
		t.Fatal("拆除失败必须显式报错,不得静默")
	}
	// 此时用户的网络可能仍被封堵,错误信息必须带上自救办法。
	if !strings.Contains(err.Error(), DNSFirewallRecoveryCommand()) {
		t.Errorf("错误信息必须包含救命命令,实际:%s", err.Error())
	}
}
```

该测试文件的 import 需包含 `strings`:

```go
import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/guardian/ -run TestReconcileDNSFirewall -count=1`
Expected: FAIL,`unknown field dnsFirewall in struct literal`

- [ ] **Step 3: 加字段与 reconcile 实现**

在 `Manager` 结构体的 `dnsStatus DNSStatus` 之后加一行:

```go
	dnsFirewall            DNSFirewall
```

在 `ManagerOptions` 的 `DNS DNSManager` 之后加一行:

```go
	DNSFirewall      DNSFirewall
```

在 `NewManager` 里,`options.DNS == nil` 校验之后补默认值(允许为空以兼容既有测试):

```go
	dnsFirewall := options.DNSFirewall
	if dnsFirewall == nil {
		dnsFirewall = noDNSFirewall{}
	}
```

并在构造 `&Manager{...}` 时传入 `dnsFirewall: dnsFirewall`。

在 `manager.go` 末尾追加:

```go
// noDNSFirewall 是未注入防泄漏器时的空实现,保持既有调用方不受影响。
type noDNSFirewall struct{}

func (noDNSFirewall) Install(context.Context, string) error { return nil }

func (noDNSFirewall) Remove(context.Context) error { return nil }

// reconcileDNSFirewall 让封堵规则跟随保护的终态,而不是跟随某个动作是否被
// 调用。终态为 off 必须不残留;拆除失败必须显式报错并带上救命命令,因为此时
// 用户的网络可能仍被封堵,而他需要知道怎么自救。
func (m *Manager) reconcileDNSFirewall(ctx context.Context, desired DesiredState) error {
	if m.dnsFirewall == nil {
		return nil
	}
	if desired == DesiredOn {
		return nil
	}
	if err := m.dnsFirewall.Remove(ctx); err != nil {
		return fmt.Errorf("移除 DNS 防泄漏规则失败,网络可能仍被封堵;"+
			"可手动执行 %s 解除,重启同样可恢复: %w", DNSFirewallRecoveryCommand(), err)
	}
	return nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/guardian/ -run TestReconcileDNSFirewall -count=1`
Expected: PASS

- [ ] **Step 5: 接入激活路径**

在 `ensureDNSManaged`(`manager.go:706`)的成功返回前,装载封堵规则。找到:

```go
	if status.State != DNSManaged {
		return m.failDNSActivation(ctx, runtimeState, "dns_verification_failed", fmt.Errorf("verify managed DNS: state is %q", status.State))
	}
	return nil
```

改为:

```go
	if status.State != DNSManaged {
		return m.failDNSActivation(ctx, runtimeState, "dns_verification_failed", fmt.Errorf("verify managed DNS: state is %q", status.State))
	}
	if err := m.dnsFirewall.Install(ctx, runtimeState.TunName); err != nil {
		return m.failDNSActivation(ctx, runtimeState, "dns_firewall_install_failed",
			fmt.Errorf("install DNS leak firewall: %w", err))
	}
	return nil
```

`TunName` 是 `supervisor.RuntimeState` 的既有字段(`internal/supervisor/runtime_state.go:12`,
tag `json:"tun_name"`),直接使用即可。

- [ ] **Step 6: 接入关闭与启动对齐**

在 `Down`(`manager.go:414`)成功落到关闭终态处,即 `m.store.SaveDesired(DesiredOff)` 成功之后、`removeBarrier` 之前,插入:

```go
	if err := m.reconcileDNSFirewall(ctx, DesiredOff); err != nil {
		m.needsAttention(DesiredOff, "dns_firewall_remove_failed")
		return err
	}
```

**不要**在 DNS 还原失败的分支(`manager.go:485-497`)里拆除——那条分支的终态是"保护已恢复",此时拆掉反而制造泄漏。

在 `RunStartupRecovery`(`manager.go:579`)的 `if desired == DesiredOff {` 块内,`restoreDNS` 之后插入:

```go
		if err := m.reconcileDNSFirewall(ctx, DesiredOff); err != nil {
			m.needsAttention(DesiredOff, "dns_firewall_remove_failed")
			m.recoveryBlocked = true
			return err
		}
```

这就是恢复保证的 L2:Guardian 崩溃后被 launchd 拉起,启动时无条件把规则对齐到期望状态。

- [ ] **Step 7: 生产接线**

修改 `internal/guardian/daemon.go:322` 附近的 DNS 构造处,同时提供防泄漏器。该处既有
`return NewDNSManager("")`,在构造 `ManagerOptions` 的地方补上:

```go
	DNSFirewall: NewDNSFirewall(nil, RuntimeDir),
```

`RuntimeDir` 是同文件既有常量(`internal/guardian/daemon.go:25`,值 `/var/run/bx`),
与 `SocketPath` 同源,不要新造路径。传 `nil` runner 时 darwin 实现会用默认的
`darwinRunner{}`。

- [ ] **Step 8: 全量验证并提交**

```bash
go build ./... && go vet ./... && go test ./... -count=1
GOOS=linux GOARCH=amd64 go build -o /dev/null ./...
GOOS=windows GOARCH=amd64 go build -o /dev/null ./...
git add internal/guardian/
git commit -m "feat(guardian): DNS 封堵规则跟随保护终态并在启动时对齐"
```

---

### Task 5: 稳态漂移巡检与自动修复

**Files:**
- Create: `internal/guardian/dnswatch.go`
- Modify: `internal/guardian/manager.go`(启动巡检循环)
- Test: `internal/guardian/dnswatch_test.go`

**Interfaces:**
- Consumes: `DNSStatus`、`DNSManaged`/`DNSUnmanaged`/`DNSUnknown`(`types.go:33-42`)、`ensureDNSManaged`
- Produces:
  - `const dnsWatchInterval = 10 * time.Second`
  - `func dnsDriftDetected(status DNSStatus, err error) bool`
  - `func (m *Manager) WatchDNS(ctx context.Context)`

- [ ] **Step 1: 写失败测试**

创建 `internal/guardian/dnswatch_test.go`:

```go
package guardian

import (
	"errors"
	"testing"
	"time"
)

func TestDNSDriftDetection(t *testing.T) {
	cases := []struct {
		name   string
		status DNSStatus
		err    error
		want   bool
	}{
		{"managed 不算漂移", DNSStatus{State: DNSManaged}, nil, false},
		{"unmanaged 算漂移", DNSStatus{State: DNSUnmanaged}, nil, true},
		{"unknown 算漂移", DNSStatus{State: DNSUnknown}, nil, true},
		{"探测出错按漂移处理", DNSStatus{}, errors.New("networksetup failed"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dnsDriftDetected(tc.status, tc.err); got != tc.want {
				t.Errorf("dnsDriftDetected = %v, want %v", got, tc.want)
			}
		})
	}
}

// 探测失败按漂移处理是刻意的 fail-safe:宁可多修一次,不可漏判。
func TestDNSWatchIntervalIsBounded(t *testing.T) {
	if dnsWatchInterval <= 0 || dnsWatchInterval > 30*time.Second {
		t.Errorf("巡检间隔 = %v,应为正且不超过 30s(否则漂移后不可用时间过长)", dnsWatchInterval)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/guardian/ -run 'TestDNSDrift|TestDNSWatchInterval' -count=1`
Expected: FAIL,`undefined: dnsDriftDetected`

- [ ] **Step 3: 实现纯判据与巡检循环**

创建 `internal/guardian/dnswatch.go`:

```go
package guardian

import (
	"context"
	"time"
)

// dnsWatchInterval 是稳态 DNS 巡检间隔。
//
// 巡检不是安全防线——pf 封堵已经保证漂移期间不泄漏。它的职责是修复(把 DNS
// 改回 127.0.0.1 让解析恢复可用)与如实上报(让状态不再是陈旧缓存),所以
// 间隔按"用户能容忍多久解析异常"来定,而不是按泄漏窗口来定。
const dnsWatchInterval = 10 * time.Second

// dnsDriftDetected 判断当前 DNS 是否偏离受管状态。
// 探测出错按漂移处理:宁可多走一次修复事务,不可漏判。
func dnsDriftDetected(status DNSStatus, err error) bool {
	if err != nil {
		return true
	}
	return status.State != DNSManaged
}

// WatchDNS 周期性巡检系统 DNS,发现漂移就在屏障保护下重新接管。
// pf 封堵在此期间始终保持,因此修复过程本身不泄漏。
func (m *Manager) WatchDNS(ctx context.Context) {
	ticker := time.NewTicker(dnsWatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.checkDNSDriftOnce(ctx)
		}
	}
}

func (m *Manager) checkDNSDriftOnce(ctx context.Context) {
	if m.dns == nil {
		return
	}
	if desired, err := m.store.LoadDesired(); err != nil || desired != DesiredOn {
		return
	}
	status, err := m.dns.Inspect(ctx)
	m.cacheDNSStatus(status)
	if !dnsDriftDetected(status, err) {
		return
	}
	recoveryCtx, done, ok := m.admitRecovery()
	if !ok {
		return
	}
	defer done()
	if err := m.acquireMutation(recoveryCtx); err != nil {
		return
	}
	defer m.releaseMutation()
	if m.current.PID == 0 {
		return
	}
	if err := m.ensureDNSManaged(recoveryCtx, m.runtime); err != nil {
		m.needsAttention(DesiredOn, "dns_drift_repair_failed")
	}
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/guardian/ -run 'TestDNSDrift|TestDNSWatchInterval' -count=1`
Expected: PASS

- [ ] **Step 5: 启动巡检循环**

在 `internal/guardian/daemon.go:138-140` 启动网络观察器生命周期的同一处并列启动
巡检。该处现有:

```go
	var observer *daemonNetworkObserverLifecycle
	if options.networkObserver != nil {
		observer = newDaemonNetworkObserverLifecycle(ctx, options.networkObserver)
	}
```

在其后追加(`manager` 取该函数中已构造的 Manager 变量名):

```go
	go manager.WatchDNS(ctx)
```

刻意不复用 `NetworkObserver` 的 1 分钟 tick:那个组件的职责是底层代际变化,
把 DNS 塞进去会混杂职责,且 60s 的修复延迟对可用性不利。

- [ ] **Step 6: 全量验证并提交**

```bash
go build ./... && go vet ./... && go test ./... -count=1
go test -race ./internal/guardian -count=1
git add internal/guardian/dnswatch.go internal/guardian/dnswatch_test.go internal/guardian/daemon.go
git commit -m "feat(guardian): 巡检 DNS 漂移并在屏障内自动重新接管"
```

---

### Task 6: 退出语义与如实文案

**Files:**
- Modify: `internal/cli/cli.go`(down 失败文案)
- Modify: `apps/macos/BxMenu/Sources/BxMenu/main.swift:785-796`(`turnOffBx`)
- Test: `internal/cli/cli_test.go`

**Interfaces:**
- Consumes: `DNSFirewallRecoveryCommand()`(Task 2)
- Produces: 无新接口

- [ ] **Step 1: 写失败测试**

在 `internal/cli/cli_test.go` 末尾追加:

```go
// 菜单栏原本在 down 失败时只说 "bx did not stop",既含糊又误导:bx 确实停了,
// 是 DNS 还原失败才被主动重新拉起。用户据此以为 Guardian 在跟他作对。
func TestMacMenuTurnOffFailureExplainsRealCause(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if strings.Contains(text, `"bx did not stop."`) {
		t.Error("必须替换掉误导性的 down 失败文案")
	}
	if !strings.Contains(text, "DNS") {
		t.Error("down 失败提示应说明 DNS 还原这一真实原因")
	}
	if !strings.Contains(text, "pfctl -a com.getbx.bx -F all") {
		t.Error("阻断态下必须把救命命令直接展示给用户,而不是让他去翻文档")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/cli/ -run TestMacMenuTurnOffFailureExplainsRealCause -count=1`
Expected: FAIL,提示仍存在 `"bx did not stop."`

- [ ] **Step 3: 改菜单栏文案**

把 `main.swift` 的 `turnOffBx()` 中:

```swift
        if !runPrivileged("'\(bxPath)' down") {
            showFailure("Turn Off Failed", "bx did not stop.")
        }
```

替换为:

```swift
        if !runPrivileged("'\(bxPath)' down") {
            showFailure(
                "Turn Off Incomplete",
                """
                bx stopped but could not finish turning off — usually because it \
                could not restore your original DNS settings. Protection was \
                re-enabled so you are not left without working DNS.

                Run Doctor for details. If your network stays blocked, you can \
                clear bx's DNS rules with:
                sudo pfctl -a com.getbx.bx -F all

                Restarting your Mac also clears them.
                """
            )
        }
```

- [ ] **Step 4: 跑测试确认通过**

```bash
go test ./internal/cli/ -run TestMacMenuTurnOffFailureExplainsRealCause -count=1
scripts/test-macos-menu.sh
swift build --package-path apps/macos/BxMenu
```
Expected: 全部 PASS

- [ ] **Step 5: 提交**

```bash
git add internal/cli/cli_test.go apps/macos/BxMenu/Sources/BxMenu/main.swift
git commit -m "fix(macos): 退出失败时说明真实原因并给出救命命令"
```

---

### Task 7: 文档与最终验证

**Files:**
- Modify: `README.md`
- Modify: `CLAUDE.md`

**Interfaces:**
- Consumes: 前六个任务的全部成果
- Produces: 无

- [ ] **Step 1: 更新 README**

在 macOS DNS 一节(现"正常使用不需要这三条命令"段落之后)追加:

```markdown
**DNS 防泄漏与恢复**:macOS 上 bx 会装一条 pf 规则(anchor `com.getbx.bx`),
封掉一切不经 bx 的出站 :53。即使系统 DNS 被其它软件改走,查询也无法明文外泄。

规则只在保护开启时存在,`sudo bx down` 会清除。若遇到异常导致网络被封堵,有三条
恢复途径,任选其一:

1. `sudo bx down`
2. `sudo pfctl -a com.getbx.bx -F all`(只清 bx 的规则,不影响你自己的 pf 配置)
3. 重启 Mac(bx 的规则不写入 `/etc/pf.conf`,重启后必然消失)
```

- [ ] **Step 2: 更新 CLAUDE.md**

在 macOS 相关段落补一句,记录 anchor 名、逃生口与"绝不写 /etc/pf.conf"的不变量,
以及该不变量由 `TestGuardianNeverWritesSystemPfConf` 守卫。

- [ ] **Step 3: 全量验证**

```bash
go build ./... && go vet ./... && go test ./... -count=1
go test -race ./internal/guardian ./internal/cli -count=1
go vet ./...
GOOS=darwin  GOARCH=arm64 go build -o /dev/null ./...
GOOS=linux   GOARCH=amd64 go build -o /dev/null ./...
GOOS=windows GOARCH=amd64 go build -o /dev/null ./...
scripts/test-macos-menu.sh
swift build --package-path apps/macos/BxMenu
bash -n scripts/darwin-dns-firewall-probe.sh
git diff --check
```
Expected: 全部 rc=0

- [ ] **Step 4: 提交**

```bash
git add README.md CLAUDE.md
git commit -m "docs(macos): 说明 DNS 防泄漏规则与三条恢复途径"
```

- [ ] **Step 5: 停在真机验证之前**

不要执行 `bx up`/`down`/`reconnect`/`dns on|off`、`networksetup`、`pfctl` 改动类命令。
向用户报告自动化验证结果,并给出真机验收梯度:

```bash
# 1. 只读探测,确认无副作用
scripts/darwin-dns-firewall-probe.sh

# 2. 带死手的机制验证
sudo bx up
sudo scripts/darwin-dns-firewall-probe.sh --execute

# 3. 端到端:开启 → 手动制造漂移 → 观察自动修复 → 关闭 → 确认无残留
sudo bx up
bx status                                    # 应显示 DNS 已接管
networksetup -getdnsservers Wi-Fi            # 应为 127.0.0.1
sudo networksetup -setdnsservers Wi-Fi 192.168.1.1   # 制造漂移
# 等 10s,观察菜单栏转黄后自动修复回绿
bx status
sudo bx down
sudo pfctl -a com.getbx.bx -s rules          # 应为空
networksetup -getdnsservers Wi-Fi            # 应已还原

# 4. 崩溃恢复(L2)
sudo bx up
sudo pkill -9 -f 'bx guardian'               # 模拟崩溃
# 等 launchd 拉起,确认规则与期望状态一致
sudo bx down && sudo pfctl -a com.getbx.bx -s rules   # 应为空
```
