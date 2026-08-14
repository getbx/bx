#!/usr/bin/env bash
# 变异验证的安全外壳。**恢复必须能在超时/中断/失败时也发生** ——
# 本次会话已经栽了两回:一次 `git checkout` 对未跟踪文件静默无效,
# 一次外层命令超时被 kill,变异原样留在盘上而后续测试为此转红。
#
# 用法:mutate.sh <文件> <原串> <替换串> <说明> -- <测试命令...>
set -uo pipefail
FILE="$1"; FROM="$2"; TO="$3"; LABEL="$4"; shift 4; [ "${1:-}" = "--" ] && shift
BAK="$(mktemp)"; cp "$FILE" "$BAK"
# trap 覆盖 EXIT/INT/TERM:超时(SIGTERM)与 Ctrl-C 都会恢复。
restore() { cp "$BAK" "$FILE"; rm -f "$BAK"; }
trap restore EXIT INT TERM
python3 - "$FILE" "$FROM" "$TO" <<'PY' || exit 2
import io,sys
p,a,b=sys.argv[1],sys.argv[2],sys.argv[3]
s=io.open(p,encoding='utf-8').read()
n=s.count(a)
if n!=1:
    sys.stderr.write(f"变异串出现 {n} 次,应为 1 次\n"); raise SystemExit(2)
io.open(p,'w',encoding='utf-8').write(s.replace(a,b,1))
PY
"$@" >/dev/null 2>&1; code=$?
restore; trap - EXIT INT TERM
# 恢复必须**逐字确认**,而不是假定 cp 成功。
# **127/126 不是「守卫有效」,是这个外壳自己没跑起来。**
# 只看「退出码非零」会把 command-not-found 读成守卫成功 —— 本次会话真的栽过一次
# (zsh 不对未加引号的变量做词分割,整条测试命令被当成一个命令名)。
if [ $code -eq 127 ] || [ $code -eq 126 ]; then
	echo "$LABEL → ⚠️ 测试命令没跑起来(退出码 $code),本次判定无效"; exit 2
fi
if grep -qF "$FROM" "$FILE"; then
	echo "$LABEL → 退出码=$code $([ $code -ne 0 ] && echo '(守卫有效)' || echo '⚠️(守卫没抓住)')"
else
	echo "$LABEL → ⚠️ 恢复失败,请手动检查 $FILE"; exit 1
fi
