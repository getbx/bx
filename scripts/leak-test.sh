#!/bin/sh
# leak-test.sh — quick egress/leak check for a bx-protected client.
# Run FROM a LAN client (laptop/phone-termux) after deploying bx router mode,
# OR on the router itself. Read-only: only outbound curls, changes nothing.
#
#   ./leak-test.sh <EXPECTED_EXIT_IP>
#   e.g. ./leak-test.sh 203.0.113.10
#
# Checks (the leak vectors router mode closes):
#   1. IPv4 egress IP  → must equal the VPS exit IP (not your real/corp IP)
#   2. IPv6 egress     → must FAIL (no v6 path; globally-unique v6 would leak via ICE)
#   3. DNS answer      → fake-IP (198.18/15) means resolution goes through bx
# Machine-readable bx summary:
#   bx leak-check --network --json --expected-ip <EXPECTED_EXIT_IP>
# WebRTC/STUN needs a real browser. Use:
#   bx webrtc-check --browser --json --expected-ip <EXPECTED_EXIT_IP>

set -u
EXPECT="${1:-}"
pass=0; fail=0
ok()   { echo "  PASS: $1"; pass=$((pass+1)); }
bad()  { echo "  FAIL: $1"; fail=$((fail+1)); }

echo "== bx leak-test =="

echo "[1] IPv4 egress IP"
ip4="$(curl -4 -s --max-time 12 https://api.ipify.org 2>/dev/null)"
if [ -z "$ip4" ]; then
	bad "no IPv4 egress (tunnel down? that's fail-closed, not a leak — but no internet)"
elif [ -n "$EXPECT" ] && [ "$ip4" = "$EXPECT" ]; then
	ok "egress IPv4 = $ip4 (matches expected exit)"
elif [ -n "$EXPECT" ]; then
	bad "egress IPv4 = $ip4, expected $EXPECT — unexpected exit path"
else
	echo "  INFO: egress IPv4 = $ip4 (pass an expected exit IP to assert)"
fi

echo "[2] IPv6 egress (must fail — no v6 leak)"
ip6="$(curl -6 -s --max-time 6 https://api64.ipify.org 2>/dev/null)"
if [ -z "$ip6" ]; then
	ok "no IPv6 egress (v6 blocked — no ICE/WebRTC v6 leak)"
else
	case "$ip6" in
	*:*) bad "IPv6 egress works ($ip6) — V6 LEAK: block LAN IPv6 forwarding" ;;
	*)   ok "no usable IPv6 ($ip6)" ;;
	esac
fi

echo "[3] DNS path (fake-IP means resolution goes through bx)"
fip="$(nslookup www.google.com 2>/dev/null | awk '/^Address/{a=$NF} END{print a}')"
case "$fip" in
198.1[89].*) ok "www.google.com → $fip (bx fake-IP; DNS not leaking)" ;;
"")          echo "  INFO: could not resolve (corp DNS may have blocked google — try from a LAN client)" ;;
*)           echo "  INFO: www.google.com → $fip (not a fake-IP; if on the router this is expected — router uses corp DNS)" ;;
esac

echo "== summary: $pass pass, $fail fail =="
echo "Reminder: run 'bx leak-check --network --json --expected-ip <EXPECTED_EXIT_IP>' for machine-readable exit checks, 'bx webrtc-check --browser --json --expected-ip <EXPECTED_EXIT_IP>' for browser WebRTC, or scripts/open-privacy-checks.sh --yes for third-party reference pages."
[ "$fail" -eq 0 ]
