#!/usr/bin/env bash
# 把一个已经用 Developer ID 签好的 Bx.app 或 .dmg 送去 Apple 公证,通过后把票据钉进去。
#
#   scripts/macos-notarize.sh <path>
#
# 凭据三个变量齐了才做:
#   BX_NOTARY_KEY_ID     App Store Connect API 密钥的 Key ID
#   BX_NOTARY_ISSUER_ID  同一页上的 Issuer ID
#   BX_NOTARY_KEY_PATH   那个 .p8 文件的路径(只能从 App Store Connect 下载一次,别进仓库)
# 一个都没设就安静跳过(本机无证书的开发打包);设了一半是配置错误,响亮失败。
# **跳过不是绿灯**:verify-macos-release.sh 在身份是 Developer ID 时会要求票据真的在。
#
# 为什么公证:Developer ID 签名只证明「是谁签的」,Gatekeeper 从 macOS 10.15 起还要
# Apple 的票据;没有票据,用户看到的仍是「无法验证开发者」,与没签一样。
# 为什么钉票据(staple):Gatekeeper 默认联网查票据,钉进去之后离线也能过。
set -euo pipefail

TARGET="${1:?usage: macos-notarize.sh <Bx.app or .dmg>}"
[[ -e "$TARGET" ]] || { echo "notarize: $TARGET does not exist" >&2; exit 1; }

KEY_ID="${BX_NOTARY_KEY_ID:-}"
ISSUER_ID="${BX_NOTARY_ISSUER_ID:-}"
KEY_PATH="${BX_NOTARY_KEY_PATH:-}"
if [[ -z "$KEY_ID" && -z "$ISSUER_ID" && -z "$KEY_PATH" ]]; then
  echo "Notarization skipped: BX_NOTARY_KEY_ID / BX_NOTARY_ISSUER_ID / BX_NOTARY_KEY_PATH are not set."
  exit 0
fi
if [[ -z "$KEY_ID" || -z "$ISSUER_ID" || -z "$KEY_PATH" ]]; then
  echo "notarize: set all three of BX_NOTARY_KEY_ID, BX_NOTARY_ISSUER_ID, BX_NOTARY_KEY_PATH (or none)" >&2
  exit 1
fi
[[ -r "$KEY_PATH" ]] || { echo "notarize: cannot read the API key at $KEY_PATH" >&2; exit 1; }
# Apple 只公证 Developer ID 签的东西;ad-hoc 签的送上去必败,而且失败原因埋在日志里。
# 先取整段输出再匹配:`codesign … | grep -q` 在 pipefail 下会因 grep 提前关管道而
# 把 codesign 判成失败(本机首跑当场撞上,一个签好的包被报成「没签」)。
if ! grep -q 'Authority=Developer ID Application' <<<"$(codesign -dvv "$TARGET" 2>&1)"; then
  echo "notarize: $TARGET is not signed with a Developer ID Application certificate (set BX_CODESIGN_IDENTITY)" >&2
  exit 1
fi

# notarytool 只收 zip / dmg / pkg;app bundle 先压成 zip(ditto 保留签名与扩展属性)。
SUBMIT="$TARGET"
CLEANUP=""
if [[ -d "$TARGET" ]]; then
  SUBMIT="$(mktemp -d)/$(basename "$TARGET").zip"
  CLEANUP="$(dirname "$SUBMIT")"
  ditto -c -k --keepParent "$TARGET" "$SUBMIT"
fi
trap '[[ -n "$CLEANUP" ]] && rm -rf "$CLEANUP"' EXIT

echo "Notarizing $(basename "$TARGET") (this waits for Apple, usually minutes; a new team's first submissions can take an hour)..."
NOTARY=(--key "$KEY_PATH" --key-id "$KEY_ID" --issuer "$ISSUER_ID")
# **先提交、再单独等,而不是 submit --wait。** 首跑实测:提交成功(Apple 那边有记录、
# 后来也 Accepted),而 `submit --wait` 在等的途中因一次网络抖动非零退出、一个字没
# 打 —— 于是脚本报「提交失败」,票据没钉,而重跑又会再传一遍 45MB。拆开之后提交
# id 在手上,等待可以重试,不用重传。
RESULT="$(xcrun notarytool submit "$SUBMIT" "${NOTARY[@]}" --output-format json)" || {
  echo "notarize: notarytool submit failed" >&2
  echo "$RESULT" >&2
  exit 1
}
SUBMISSION_ID="$(python3 -c 'import json,sys; print(json.loads(sys.stdin.read()).get("id",""))' <<<"$RESULT")"
[[ -n "$SUBMISSION_ID" ]] || { echo "notarize: Apple accepted the upload but returned no submission id: $RESULT" >&2; exit 1; }
echo "Submitted as $SUBMISSION_ID; waiting..."
STATUS=""
for attempt in 1 2 3 4 5 6; do
  if WAITED="$(xcrun notarytool wait "$SUBMISSION_ID" "${NOTARY[@]}" --timeout 30m --output-format json)"; then
    STATUS="$(python3 -c 'import json,sys; print(json.loads(sys.stdin.read()).get("status",""))' <<<"$WAITED")"
    break
  fi
  echo "notarize: waiting on $SUBMISSION_ID failed (attempt $attempt); retrying in 30s" >&2
  sleep 30
done
if [[ -z "$STATUS" ]]; then
  echo "notarize: could not learn the outcome of submission $SUBMISSION_ID; rerun 'xcrun notarytool wait $SUBMISSION_ID' and staple by hand" >&2
  exit 1
fi
if [[ "$STATUS" != "Accepted" ]]; then
  # **失败时把 Apple 的日志打出来**:那里面才写着是哪个文件、缺了什么(没 hardened
  # runtime / 没时间戳 / 用了 ad-hoc)。只报一句「Invalid」等于什么也没说。
  echo "notarize: Apple returned status '$STATUS' for submission $SUBMISSION_ID" >&2
  xcrun notarytool log "$SUBMISSION_ID" "${NOTARY[@]}" >&2 || true
  exit 1
fi
echo "Notarization accepted (submission $SUBMISSION_ID). Stapling the ticket..."
xcrun stapler staple -q "$TARGET"
xcrun stapler validate -q "$TARGET"
echo "Stapled: $TARGET"
