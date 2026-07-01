#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  scripts/open-privacy-checks.sh [--yes]

Opens third-party privacy/fingerprint reference pages in the default browser.
Default is dry-run: it prints the pages and opens nothing.

Options:
  --yes       Open the pages.
  -h,--help  Show help.

This script does not collect, parse, upload, or judge browser fingerprint data.
EOF
}

OPEN=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --yes) OPEN=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "error: unknown argument: $1" >&2; usage >&2; exit 1 ;;
  esac
done

URLS=(
  "https://browserleaks.com/ip"
  "https://browserleaks.com/webrtc"
  "https://coveryourtracks.eff.org/"
  "https://abrahamjuliot.github.io/creepjs/"
)

echo "Privacy reference pages:"
for url in "${URLS[@]}"; do
  echo "  $url"
done

if [[ "$OPEN" != "1" ]]; then
  echo
  echo "Dry-run only. To open: scripts/open-privacy-checks.sh --yes"
  exit 0
fi

open_url() {
  local url="$1"
  case "$(uname -s)" in
    Darwin) open "$url" ;;
    Linux)
      if command -v xdg-open >/dev/null 2>&1; then
        xdg-open "$url" >/dev/null 2>&1 &
      else
        echo "warning: xdg-open not found; open manually: $url" >&2
      fi
      ;;
    *)
      echo "warning: unsupported platform; open manually: $url" >&2
      ;;
  esac
}

for url in "${URLS[@]}"; do
  open_url "$url"
done

echo "Opened reference pages. bx does not read or interpret their results."
