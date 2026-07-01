#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  scripts/darwin-testkit.sh --server-bypass A.B.C.D/32 [options]
  sudo BX_LINK='bx://...' scripts/darwin-testkit.sh --execute --server-bypass A.B.C.D/32 [options]

Options:
  --execute                 Actually run the short macOS test. Without this, only logs a dry-run plan.
  --bx PATH                 bx binary path. Default: /tmp/bx-mac/bx, built automatically if missing.
  --brook PATH              Internal transport binary override for debugging.
  --link LINK               bx:// link. Default: $BX_LINK; old $BX_BROOK_LINK still works.
  --udp-link LINK           Optional UDP transport link. Default: $BX_UDP_LINK.
  --server-bypass CIDR      Server IP/CIDR that must bypass utun. Required; may be repeated.
  --bypass CIDR             Extra user bypass CIDR. May be repeated.
  --gateway IP              Physical default gateway. Default: detected from route -n get default.
  --tun NAME                Requested utun name. Default: utun
  --duration SECONDS        bx --test-timeout duration. Default: 45
  --probe HOST:PORT         bx health probe target. Default: github.com:443
  --udp-probe HOST:PORT     UDP smoke probe target. May be repeated. Default: 1.1.1.1:443 and stun.l.google.com:19302
  --no-udp-probe            Skip UDP smoke probes.
  --health-timeout SECONDS  bx tunnel health startup timeout. Default: 45
  --rollback-after SECONDS  External rollback delay. Default: 75
  --log-dir DIR             Log directory. Default: ./.bx-test-logs/bx-mac-test-YYYYmmdd-HHMMSS
  --set-system-dns          Temporarily set the active macOS network service DNS to 127.0.0.1.
  --dns-service NAME        Network service to change with --set-system-dns. Default: detected from default route.
  --webrtc-browser          Run bx webrtc-check with a real browser ICE candidate test.
  --block-v6                Include IPv6 reject route plan. Default: enabled.
  --no-block-v6             Do not include IPv6 reject route plan.
  -h, --help                Show this help.

This script writes logs and a rollback script before touching routes. It never
executes route/ifconfig unless --execute is set.
EOF
}

die() {
  echo "error: $*" >&2
  exit 1
}

detect_gateway() {
  route -n get default 2>/dev/null | awk '/gateway:/{print $2; exit}'
}

detect_default_device() {
  route -n get default 2>/dev/null | awk '/interface:/{print $2; exit}'
}

detect_service_for_device() {
  local dev="$1"
  networksetup -listnetworkserviceorder 2>/dev/null | awk -v dev="$dev" '
    /^\([0-9]+\)/ {
      service=$0
      sub(/^\([0-9]+\) /, "", service)
      next
    }
    index($0, "Device: " dev ")") {
      print service
      exit
    }
  '
}

udp_probe_once() {
  local target="$1"
  local py
  py="$(command -v python3 || true)"
  if [[ -z "$py" ]]; then
    echo "udp probe $target: skipped: python3 not found"
    return 0
  fi
  "$py" - "$target" <<'PY'
import os
import socket
import struct
import sys
import time

target = sys.argv[1]
if ":" not in target:
    print(f"udp probe {target}: skipped: expected HOST:PORT")
    raise SystemExit(0)
host, port_text = target.rsplit(":", 1)
try:
    port = int(port_text)
except ValueError:
    print(f"udp probe {target}: skipped: bad port")
    raise SystemExit(0)

payload = b"bx-udp-probe"
if port == 19302 or "stun" in host.lower():
    txid = os.urandom(12)
    payload = struct.pack("!HHI12s", 0x0001, 0, 0x2112A442, txid)

sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
sock.settimeout(3)
started = time.time()
try:
    sent = sock.sendto(payload, (host, port))
    print(f"udp probe {target}: sent {sent} bytes")
    try:
        data, peer = sock.recvfrom(1500)
        elapsed = int((time.time() - started) * 1000)
        print(f"udp probe {target}: response {len(data)} bytes from {peer[0]}:{peer[1]} in {elapsed}ms")
    except socket.timeout:
        print(f"udp probe {target}: no response within 3s")
except Exception as exc:
    print(f"udp probe {target}: error: {exc}")
finally:
    sock.close()
PY
}

save_dns_state() {
  local service="$1"
  networksetup -getdnsservers "$service" >"$LOG_DIR/dns-original.txt" 2>&1 || true
}

restore_dns_state() {
  local service="$1"
  [[ -n "$service" ]] || return 0
  if [[ ! -s "$LOG_DIR/dns-original.txt" ]]; then
    return 0
  fi
  if grep -q "There aren't any DNS Servers set" "$LOG_DIR/dns-original.txt"; then
    networksetup -setdnsservers "$service" Empty
    return
  fi
  dns_servers=()
  while IFS= read -r line; do
    [[ -n "$line" ]] && dns_servers+=("$line")
  done <"$LOG_DIR/dns-original.txt"
  if [[ ${#dns_servers[@]} -eq 0 ]]; then
    networksetup -setdnsservers "$service" Empty
    return
  fi
  networksetup -setdnsservers "$service" "${dns_servers[@]}"
}

capture_state() {
  local phase="$1"
  route -n get default >"$LOG_DIR/${phase}-route-default.txt" 2>&1 || true
  netstat -rn -f inet >"$LOG_DIR/${phase}-netstat-inet.txt" 2>&1 || true
  netstat -rn -f inet6 >"$LOG_DIR/${phase}-netstat-inet6.txt" 2>&1 || true
  ifconfig >"$LOG_DIR/${phase}-ifconfig.txt" 2>&1 || true
  scutil --dns >"$LOG_DIR/${phase}-dns.txt" 2>&1 || true
}

fix_log_permissions() {
  if [[ -n "${SUDO_USER:-}" && "${SUDO_USER:-}" != "root" && -d "${LOG_DIR:-}" ]]; then
    chown -R "$SUDO_USER":staff "$LOG_DIR" 2>/dev/null || true
  fi
}

decode_bx_links() {
  local link="$1"
  local py
  py="$(command -v python3 || true)"
  [[ -n "$py" ]] || die "python3 is required to expand bx:// bundles in the testkit"
  "$py" - "$link" <<'PY'
import base64
import json
import sys

link = sys.argv[1].strip()
prefix = "bx://"
if link.startswith("blink://"):
    prefix = "blink://"
if not link.startswith(prefix):
    raise SystemExit("not a bx/blink link")
raw = link[len(prefix):]
raw += "=" * (-len(raw) % 4)
payload = base64.urlsafe_b64decode(raw.encode())
text = payload.decode()
if text.lstrip().startswith("{"):
    data = json.loads(text)
    links = data.get("links") or [data.get("link", "")]
else:
    links = [text]
for item in links:
    if item:
        print(item)
PY
}

normalize_links_for_config() {
  local link="$1"
  if [[ "$link" == bx://* && "$link" == *"wssserver="* ]]; then
    link="brook://${link#bx://}"
  fi
  if [[ "$link" == bx://* || "$link" == blink://* ]]; then
    while IFS= read -r internal; do
      [[ -n "$internal" ]] && "$BX" blink "$internal"
    done < <(decode_bx_links "$link")
    return
  fi
  "$BX" blink "$link"
}

BX="/tmp/bx-mac/bx"
BX_PROVIDED=0
BROOK=""
LINK="${BX_LINK:-${BX_BROOK_LINK:-}}"
UDP_LINK="${BX_UDP_LINK:-}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
GATEWAY=""
TUN="utun"
DURATION="45"
PROBE="github.com:443"
UDP_PROBES=("1.1.1.1:443" "stun.l.google.com:19302")
UDP_PROBE_ARGS=()
HEALTH_TIMEOUT="45"
ROLLBACK_AFTER="75"
LOG_DIR=""
EXECUTE=0
BLOCK_V6=1
SET_SYSTEM_DNS=0
DNS_SERVICE=""
WEBRTC_BROWSER=0
SERVER_BYPASS=()
USER_BYPASS=()

while [[ $# -gt 0 ]]; do
  case "$1" in
    --execute) EXECUTE=1; shift ;;
    --bx) BX="${2:-}"; BX_PROVIDED=1; shift 2 ;;
    --brook) BROOK="${2:-}"; shift 2 ;;
    --link) LINK="${2:-}"; shift 2 ;;
    --udp-link) UDP_LINK="${2:-}"; shift 2 ;;
    --server-bypass) SERVER_BYPASS+=("${2:-}"); shift 2 ;;
    --bypass) USER_BYPASS+=("${2:-}"); shift 2 ;;
    --gateway) GATEWAY="${2:-}"; shift 2 ;;
    --tun) TUN="${2:-}"; shift 2 ;;
    --duration) DURATION="${2:-}"; shift 2 ;;
    --probe) PROBE="${2:-}"; shift 2 ;;
    --udp-probe) UDP_PROBES+=("${2:-}"); UDP_PROBE_ARGS+=(--udp-probe "${2:-}"); shift 2 ;;
    --no-udp-probe) UDP_PROBES=(); UDP_PROBE_ARGS+=(--no-udp-probe); shift ;;
    --health-timeout) HEALTH_TIMEOUT="${2:-}"; shift 2 ;;
    --rollback-after) ROLLBACK_AFTER="${2:-}"; shift 2 ;;
    --log-dir) LOG_DIR="${2:-}"; shift 2 ;;
    --set-system-dns) SET_SYSTEM_DNS=1; shift ;;
    --dns-service) DNS_SERVICE="${2:-}"; shift 2 ;;
    --webrtc-browser) WEBRTC_BROWSER=1; shift ;;
    --block-v6) BLOCK_V6=1; shift ;;
    --no-block-v6) BLOCK_V6=0; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

[[ ${#SERVER_BYPASS[@]} -gt 0 ]] || die "--server-bypass A.B.C.D/32 is required"

if [[ "$BX_PROVIDED" != "1" || ! -x "$BX" ]]; then
  mkdir -p "$(dirname "$BX")"
  go build -o "$BX" .
fi
[[ -x "$BX" ]] || die "bx binary not executable: $BX"

if [[ -z "$GATEWAY" ]]; then
  GATEWAY="$(detect_gateway)"
fi
[[ -n "$GATEWAY" ]] || die "could not detect gateway; pass --gateway"

if [[ "$SET_SYSTEM_DNS" == "1" && -z "$DNS_SERVICE" ]]; then
  DEFAULT_DEVICE="$(detect_default_device)"
  [[ -n "$DEFAULT_DEVICE" ]] || die "could not detect default interface for --set-system-dns"
  DNS_SERVICE="$(detect_service_for_device "$DEFAULT_DEVICE")"
  [[ -n "$DNS_SERVICE" ]] || die "could not detect network service; pass --dns-service"
fi

if [[ -z "$LOG_DIR" ]]; then
  LOG_DIR="$REPO_ROOT/.bx-test-logs/bx-mac-test-$(date +%Y%m%d-%H%M%S)"
fi
mkdir -p "$LOG_DIR"
chmod 700 "$LOG_DIR"

PLAN_ARGS=(darwin-plan --tun "$TUN" --gateway "$GATEWAY")
for cidr in "${SERVER_BYPASS[@]}"; do
  PLAN_ARGS+=(--server-bypass "$cidr")
done
for cidr in "${USER_BYPASS[@]}"; do
  PLAN_ARGS+=(--bypass "$cidr")
done
if [[ "$BLOCK_V6" == "1" ]]; then
  PLAN_ARGS+=(--block-v6)
fi

{
  echo "log_dir=$LOG_DIR"
  echo "bx=$BX"
  echo "transport_override=$BROOK"
  echo "udp_transport_configured=$([[ -n "$UDP_LINK" ]] && echo 1 || echo 0)"
  echo "gateway=$GATEWAY"
  echo "tun=$TUN"
  echo "duration=$DURATION"
  echo "probe=$PROBE"
  echo "udp_probes=${UDP_PROBES[*]}"
  echo "health_timeout=$HEALTH_TIMEOUT"
  echo "rollback_after=$ROLLBACK_AFTER"
  echo "execute=$EXECUTE"
  echo "set_system_dns=$SET_SYSTEM_DNS"
  echo "dns_service=$DNS_SERVICE"
  echo "webrtc_browser=$WEBRTC_BROWSER"
  echo "server_bypass=${SERVER_BYPASS[*]}"
  echo "user_bypass=${USER_BYPASS[*]}"
  sw_vers 2>/dev/null || true
  uname -a
  if [[ -n "$BROOK" && -x "$BROOK" ]]; then
    "$BROOK" --version || true
  fi
} >"$LOG_DIR/meta.txt" 2>&1

capture_state before
"$BX" "${PLAN_ARGS[@]}" | tee "$LOG_DIR/plan.txt"

if [[ "$SET_SYSTEM_DNS" == "1" ]]; then
  {
    echo "# dns apply"
    echo "networksetup -setdnsservers $DNS_SERVICE 127.0.0.1"
    echo "# dns cleanup"
    echo "networksetup -setdnsservers $DNS_SERVICE <original values from dns-original.txt>"
  } | tee -a "$LOG_DIR/plan.txt"
fi

awk '
  /^# cleanup$/ { in_cleanup=1; next }
  in_cleanup && /^#/ { in_cleanup=0; next }
  in_cleanup && $0 !~ /^#/ && NF { print }
' "$LOG_DIR/plan.txt" >"$LOG_DIR/cleanup.commands"

{
  echo '#!/usr/bin/env bash'
  echo 'set +e'
  echo 'date'
  if [[ "$SET_SYSTEM_DNS" == "1" ]]; then
    printf 'if [ -s %q ]; then\n' "$LOG_DIR/dns-original.txt"
    printf '  if grep -q "There aren'"'"'t any DNS Servers set" %q; then\n' "$LOG_DIR/dns-original.txt"
    printf '    networksetup -setdnsservers %q Empty\n' "$DNS_SERVICE"
    printf '  else\n'
    printf '    networksetup -setdnsservers %q $(cat %q)\n' "$DNS_SERVICE" "$LOG_DIR/dns-original.txt"
    printf '  fi\n'
    printf '  dscacheutil -flushcache || true\n'
    printf '  killall -HUP mDNSResponder || true\n'
    printf 'fi\n'
  fi
  while IFS= read -r cmd; do
    printf '%s\n' "$cmd"
  done <"$LOG_DIR/cleanup.commands"
  echo 'date'
} >"$LOG_DIR/rollback.sh"
chmod 700 "$LOG_DIR/rollback.sh"

if [[ "$EXECUTE" != "1" ]]; then
  echo
  echo "Dry-run complete. Logs: $LOG_DIR"
  echo "No network changes were made."
  execute_hint=(sudo env 'BX_LINK=bx://...')
  if [[ -n "$UDP_LINK" ]]; then
    execute_hint+=('BX_UDP_LINK=bx://...')
  fi
  execute_hint+=("$0" --execute --bx "$BX" --gateway "$GATEWAY" --duration "$DURATION" --health-timeout "$HEALTH_TIMEOUT" --rollback-after "$ROLLBACK_AFTER")
  execute_hint+=("${UDP_PROBE_ARGS[@]}")
  for cidr in "${SERVER_BYPASS[@]}"; do
    execute_hint+=(--server-bypass "$cidr")
  done
  for cidr in "${USER_BYPASS[@]}"; do
    execute_hint+=(--bypass "$cidr")
  done
  if [[ "$SET_SYSTEM_DNS" == "1" ]]; then
    execute_hint+=(--set-system-dns)
    [[ -n "$DNS_SERVICE" ]] && execute_hint+=(--dns-service "$DNS_SERVICE")
  fi
  if [[ "$WEBRTC_BROWSER" == "1" ]]; then
    execute_hint+=(--webrtc-browser)
  fi
  printf 'To execute:'
  printf ' %q' "${execute_hint[@]}"
  printf '\n'
  fix_log_permissions
  exit 0
fi

[[ "$(id -u)" == "0" ]] || die "--execute must run as root via sudo"
[[ -n "$LINK" ]] || die "--execute requires --link or BX_LINK"

if [[ "$SET_SYSTEM_DNS" == "1" ]]; then
  save_dns_state "$DNS_SERVICE"
fi

{
  dscacheutil -flushcache || true
  killall -HUP mDNSResponder || true
} >"$LOG_DIR/flush-dns.log" 2>&1 || true

CONFIG="$LOG_DIR/config.yaml"
DATA_DIR="$LOG_DIR/data"
mkdir -p "$DATA_DIR"
CONFIG_LINKS=()
while IFS= read -r config_link; do
  [[ -n "$config_link" ]] && CONFIG_LINKS+=("$config_link")
done < <(normalize_links_for_config "$LINK") || die "could not normalize link for config"
[[ ${#CONFIG_LINKS[@]} -gt 0 ]] || die "could not normalize link for config"
ESCAPED_UDP_LINK=""
if [[ -n "$UDP_LINK" ]]; then
  CONFIG_UDP_LINK="$(normalize_links_for_config "$UDP_LINK" | head -n 1)" || die "could not normalize UDP link for config"
  [[ -n "$CONFIG_UDP_LINK" ]] || die "could not normalize UDP link for config"
  ESCAPED_UDP_LINK="${CONFIG_UDP_LINK//\'/\'\'}"
fi
{
  if [[ ${#CONFIG_LINKS[@]} -eq 1 ]]; then
    ESCAPED_LINK="${CONFIG_LINKS[0]//\'/\'\'}"
    echo "server: '$ESCAPED_LINK'"
  else
    echo "transports:"
    for config_link in "${CONFIG_LINKS[@]}"; do
      ESCAPED_LINK="${config_link//\'/\'\'}"
      echo "  - '$ESCAPED_LINK'"
    done
  fi
  if [[ -n "$ESCAPED_UDP_LINK" ]]; then
    echo "udp:"
    echo "  transport: '$ESCAPED_UDP_LINK'"
  fi
  echo "global: true"
  echo "killswitch: true"
  echo "data_dir: '$DATA_DIR'"
} >"$CONFIG"
chmod 600 "$CONFIG"
{
  if [[ ${#CONFIG_LINKS[@]} -eq 1 ]]; then
    echo "server: '<redacted>'"
  else
    echo "transports:"
    for _ in "${CONFIG_LINKS[@]}"; do
      echo "  - '<redacted>'"
    done
  fi
  if [[ -n "$ESCAPED_UDP_LINK" ]]; then
    echo "udp:"
    echo "  transport: '<redacted>'"
  fi
  echo "global: true"
  echo "killswitch: true"
  echo "data_dir: '$DATA_DIR'"
} >"$LOG_DIR/config.redacted.yaml"

(
  sleep "$ROLLBACK_AFTER"
  "$LOG_DIR/rollback.sh"
) >"$LOG_DIR/rollback.log" 2>&1 &
echo "$!" >"$LOG_DIR/rollback.pid"

RUN_ARGS=(run -c "$CONFIG" --probe "$PROBE" --health-timeout "${HEALTH_TIMEOUT}s" --test-timeout "${DURATION}s")
if [[ -n "$BROOK" ]]; then
  [[ -x "$BROOK" ]] || die "transport override not executable: $BROOK"
  RUN_ARGS+=(--brook "$BROOK")
fi
if [[ "$SET_SYSTEM_DNS" == "1" ]]; then
  RUN_ARGS+=(--listen-dns 127.0.0.1:53)
fi

set +e
BX_DEBUG=1 "$BX" "${RUN_ARGS[@]}" >"$LOG_DIR/bx-run.log" 2>&1 &
BX_PID=$!
echo "$BX_PID" >"$LOG_DIR/bx-run.pid"
set -e

if [[ "$SET_SYSTEM_DNS" == "1" ]]; then
  {
    echo "service=$DNS_SERVICE"
    echo "before:"
    cat "$LOG_DIR/dns-original.txt" 2>/dev/null || true
    echo "waiting for bx local DNS listener..."
  } >"$LOG_DIR/dns-change.log" 2>&1
  dns_ready=0
  for _ in {1..100}; do
    if grep -q "本地 DNS 已监听" "$LOG_DIR/bx-run.log" 2>/dev/null; then
      dns_ready=1
      break
    fi
    if ! kill -0 "$BX_PID" 2>/dev/null; then
      break
    fi
    sleep 0.1
  done
  if [[ "$dns_ready" == "1" ]]; then
    {
      echo "apply: networksetup -setdnsservers $DNS_SERVICE 127.0.0.1"
      networksetup -setdnsservers "$DNS_SERVICE" 127.0.0.1
      dscacheutil -flushcache || true
      killall -HUP mDNSResponder || true
      echo "after:"
      networksetup -getdnsservers "$DNS_SERVICE" || true
      scutil --dns >"$LOG_DIR/active-dns.txt" 2>&1 || true
      route -n get default >"$LOG_DIR/active-route-default.txt" 2>&1 || true
    } >>"$LOG_DIR/dns-change.log" 2>&1
  else
    echo "skip: bx local DNS listener did not become ready" >>"$LOG_DIR/dns-change.log"
  fi
fi

(
  sleep 8
  date
  echo "status: before probes"
  "$BX" status || true
  echo "active route: inet"
  netstat -rn -f inet || true
  echo "active route: inet6"
  netstat -rn -f inet6 || true
  echo "probe: gateway"
  ping -c 2 "$GATEWAY" || true
  echo "probe: explicit fake-dns github"
  if command -v dig >/dev/null 2>&1; then
    echo "probe: local listener github"
    local_fake_ip="$(dig +short @127.0.0.1 github.com A 2>/dev/null | awk '/^198[.]18[.]/{print; exit}')"
    echo "$local_fake_ip"
    echo "probe: local listener baidu"
    dig +short @127.0.0.1 www.baidu.com A || true
    echo "probe: route to local fake github"
    if [[ -n "$local_fake_ip" ]]; then
      route -n get "$local_fake_ip" || true
      echo "probe: forced local fake github"
      curl -4 -I --max-time 10 --resolve "github.com:443:$local_fake_ip" https://github.com/ || true
    else
      echo "skip: local listener did not return fake github IP"
    fi
    fake_ip="$(dig +short @8.8.8.8 github.com A 2>/dev/null | awk '/^198[.]18[.]/{print; exit}')"
    echo "fake_ip=$fake_ip"
    if [[ -n "$fake_ip" ]]; then
      echo "probe: route to system fake github"
      route -n get "$fake_ip" || true
      echo "probe: forced system fake github"
      curl -4 -I --max-time 10 --resolve "github.com:443:$fake_ip" https://github.com/ || true
    fi
  else
    echo "dig not found; skipping explicit fake-dns probe"
  fi
  echo "probe: system dns github"
  dscacheutil -q host -a name github.com || true
  if command -v dig >/dev/null 2>&1; then
    echo "dig default:"
    dig +short github.com A || true
    echo "dig gateway dns:"
    dig +short @"$GATEWAY" github.com A || true
    if netstat -rn -f inet | awk '$1 == "100.100.100.100/32" || $1 == "100.100.100.100" { found=1 } END { exit found ? 0 : 1 }'; then
      echo "dig tailscale dns:"
      dig +short @100.100.100.100 github.com A || true
    fi
  fi
  echo "probe: baidu"
  curl -4 -I --max-time 8 https://www.baidu.com/ || true
  echo "probe: github"
  curl -4 -I --max-time 8 https://github.com/ || true
  echo "probe: udp smoke"
  if [[ ${#UDP_PROBES[@]} -eq 0 ]]; then
    echo "udp probe: skipped"
  else
    for udp_target in "${UDP_PROBES[@]}"; do
      udp_probe_once "$udp_target" || true
    done
    sleep 1
  fi
  echo "probe: webrtc-check"
  WEBRTC_ARGS=(webrtc-check --json --config "$CONFIG")
  if [[ -n "$DNS_SERVICE" ]]; then
    WEBRTC_ARGS+=(--dns-service "$DNS_SERVICE")
  fi
  if [[ "$WEBRTC_BROWSER" == "1" ]]; then
    WEBRTC_ARGS+=(--browser)
  fi
  "$BX" "${WEBRTC_ARGS[@]}" || true
  echo "probe: bx log markers"
  grep -E "domain sniffed:.*github.com|udp proxy|udp blocked|socks connect udp|network not implemented" "$LOG_DIR/bx-run.log" || true
  echo "status: after probes"
  "$BX" status || true
) >"$LOG_DIR/probes.log" 2>&1 &

set +e
wait "$BX_PID"
RUN_STATUS=$?
set -e

if [[ "$SET_SYSTEM_DNS" == "1" ]]; then
  restore_dns_state "$DNS_SERVICE" >>"$LOG_DIR/dns-change.log" 2>&1 || true
  {
    dscacheutil -flushcache || true
    killall -HUP mDNSResponder || true
  } >>"$LOG_DIR/flush-dns.log" 2>&1 || true
fi
"$LOG_DIR/rollback.sh" >>"$LOG_DIR/rollback.log" 2>&1 || true
capture_state after
echo "$RUN_STATUS" >"$LOG_DIR/bx-run.exit"
rm -f "$CONFIG"
fix_log_permissions

echo "Test complete. Logs: $LOG_DIR"
echo "bx run exit status: $RUN_STATUS"
