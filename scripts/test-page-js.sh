#!/usr/bin/env bash
# 跑 leakcheck 页面里那段**纯解析** JS 的断言。
#
# 为什么单独一个脚本:那半个文件在此之前**一行测试都盖不到**(page.html 里原来
# 那段注释自己写着「this repo's tests cannot see JS — so a drift would be
# silent」),而它包含整页最承重的两个解析 —— 一个决定 WebRTC 那条结论看不看得见
# 你的公网出口,一个决定出口 IP 与国家。解析错了不会报错,只会让结论悄悄变成
# 「没检查」或者一个错的 IP。**对一个泄漏检测工具,静默的假阴性是最坏的一种失效。**
#
# 形状照抄 scripts/test-macos-menu.sh:那也是「Go 测试进不去的那半边,单独一个
# 运行器 + verify.sh 挂闸门 + CI 跑」。
#
# **判据是退出码。** 断言全部由 node 的 process.exit 决定,脚本不 grep 任何输出。
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
page="$root/internal/leakserve/page.html"

if ! command -v node >/dev/null 2>&1; then
  # **说出来,而不是安静地过去。** 一个静默跳过的闸门与没有闸门在退出码上完全
  # 一样,而这个仓库为「提前 exit 0 仍然退 0」栽过一次。
  echo "page js tests: 没找到 node —— 跳过(这半边这次没有被验证)" >&2
  exit 2
fi

[ -f "$page" ] || { echo "找不到 $page" >&2; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# 抽出哨兵之间那一段。**用 awk 而不是靠行号**:行号会随页面改动漂移,而漂移的
# 表现是抽到半段代码、node 报语法错 —— 吵,看得见,可以接受。
awk '/==== BX-PURE-BEGIN ====/{f=1;next} /==== BX-PURE-END ====/{f=0} f' "$page" > "$work/pure.js"

if [ ! -s "$work/pure.js" ]; then
  echo "BX-PURE 区段是空的 —— 哨兵还在吗?" >&2
  exit 1
fi

cat >> "$work/pure.js" <<'JS'

// ---- 断言 ----
let failures = 0;
function eq(got, want, what) {
  const g = JSON.stringify(got), w = JSON.stringify(want);
  if (g !== w) { console.error(`FAIL ${what}\n  got  ${g}\n  want ${w}`); failures++; }
}

// —— bxParseTrace ——
eq(bxParseTrace("fl=abc\nip=203.0.113.9\nts=1\nloc=US\ntls=TLSv1.3\n"),
   {ip: "203.0.113.9", loc: "US"}, "trace 取 ip 与 loc");
// CRLF:值 trim 掉 \r,否则 IP 会带着一个看不见的字符去跟 Go 那边比对
eq(bxParseTrace("ip=203.0.113.9\r\nloc=US\r\n"),
   {ip: "203.0.113.9", loc: "US"}, "trace 认 CRLF");
// 只有 ip 没有 loc:另一半必须是空串,不是 undefined —— 它要进 JSON 发给 Go
eq(bxParseTrace("ip=1.1.1.1\n"), {ip: "1.1.1.1", loc: ""}, "trace 缺 loc");
eq(bxParseTrace("garbage\nno-equals-here\n"), {ip: "", loc: ""}, "trace 全是垃圾行");
eq(bxParseTrace(""), {ip: "", loc: ""}, "trace 空 body");
eq(bxParseTrace(null), {ip: "", loc: ""}, "trace null 不抛异常");
// IPv6 出口:冒号不许被当成分隔符
eq(bxParseTrace("ip=2606:4700:4700::1111\nloc=US\n"),
   {ip: "2606:4700:4700::1111", loc: "US"}, "trace 认 IPv6");
// 值里有等号(cloudflare 的 uag 行就有):只在**第一个**等号处切
eq(bxParseTrace("uag=Mozilla/5.0 (a=b)\nip=1.2.3.4\n"), {ip: "1.2.3.4", loc: ""},
   "trace 只在第一个等号处切");

// —— bxParseCandidate ——
const srflx = "candidate:842163049 1 udp 1677729535 203.0.113.5 54321 typ srflx " +
              "raddr 192.168.1.5 rport 54321 generation 0 ufrag Xy network-cost 999";
eq(bxParseCandidate(srflx), {typ: "srflx", address: "203.0.113.5"}, "srflx 取地址与类型");
eq(bxParseCandidate("candidate:1 1 udp 2122260223 192.168.1.5 60000 typ host generation 0"),
   {typ: "host", address: "192.168.1.5"}, "host 候选");
// mDNS:**地址原样返回,不按 IP 形状筛掉** —— 「浏览器还给不给真地址」本身就是
// 一条要报告的事实,筛掉它等于把一条结论悄悄变成「没检查」
eq(bxParseCandidate("candidate:1 1 udp 2122260223 3f1a2b3c-0000-4000-8000-000000000001.local 60000 typ host"),
   {typ: "host", address: "3f1a2b3c-0000-4000-8000-000000000001.local"}, "mDNS host 原样返回");
eq(bxParseCandidate("candidate:1 1 tcp 1518214911 192.168.1.5 9 typ host tcptype active generation 0"),
   {typ: "host", address: "192.168.1.5"}, "tcptype 候选");
eq(bxParseCandidate("candidate:1 1 udp 100 2001:db8::1 60000 typ srflx"),
   {typ: "srflx", address: "2001:db8::1"}, "IPv6 srflx");
// 太短:下标 7 不存在
eq(bxParseCandidate("candidate:1 1 udp 100 1.2.3.4 60000 typ"), null, "少一个词就认不出");
// **下标 6 不是字面量 typ 就整条不认。** 少了这一句,一条形状意外的 candidate 会
// 让下标 7 上那个词被当成类型直接采信 —— 一个不是 srflx 的地址被报成公网出口。
eq(bxParseCandidate("candidate:1 1 udp 100 1.2.3.4 60000 xxx srflx"), null, "第 7 个词不是 typ");
eq(bxParseCandidate(""), null, "空串");
eq(bxParseCandidate(null), null, "null 不抛异常");
// 多余空格与首尾空白不许让下标错位
eq(bxParseCandidate("  candidate:1 1  udp   100 1.2.3.4 60000 typ srflx  "),
   {typ: "srflx", address: "1.2.3.4"}, "多余空白不错位");

// —— bxEchoOutcome ——
// **这一条是本轮修的那个 bug 的守卫。** 空 body 那一支此前既不报 landed=true
// 也不报 landed=false(早退跳过了那一行),于是格子永远停在「还在等」的样子,
// 而报告其实已经发出并渲染完了。
eq(bxEchoOutcome("203.0.113.9\n"), {value: "203.0.113.9", err: "", landed: true},
   "正常 echo:去掉换行、算落地");
eq(bxEchoOutcome(""), {value: "", err: "the echo returned an empty body", landed: false},
   "空 body 必须 landed=false —— 不报极性会让格子停在「还在等」");
eq(bxEchoOutcome("   \n\t "), {value: "", err: "the echo returned an empty body", landed: false},
   "只有空白也算空 body");
eq(bxEchoOutcome(null), {value: "", err: "the echo returned an empty body", landed: false},
   "null 不抛异常");
// IPv6 与前后空白
eq(bxEchoOutcome("  2001:db8::1  "), {value: "2001:db8::1", err: "", landed: true},
   "IPv6 与前后空白");
// **有值就是落地了,哪怕内容看起来不像 IP** —— 判定在 Go 里,这里只报「拿到了
// 一个非空的答案」。在这里按 IP 形状筛,等于把一条结论悄悄变成「没检查」。
eq(bxEchoOutcome("<html>nope</html>"), {value: "<html>nope</html>", err: "", landed: true},
   "非 IP 内容也算落地(判定在 Go 里)");

// —— bxTraceOutcome ——
eq(bxTraceOutcome("ip=203.0.113.9\nloc=US\n"),
   {ip: "203.0.113.9", loc: "US", err: "", landed: true}, "trace 两项都在");
// 只有 loc 也算落地:少一项是缺一半答案,不是没答案
eq(bxTraceOutcome("loc=JP\n"), {ip: "", loc: "JP", err: "", landed: true}, "trace 只有 loc");
// **强制门户 / 拦截页会以 200 返回 HTML** —— 解析出来两项皆空,必须是没落地。
// 报成落地等于把一次没拿到答案的探测说成拿到了。
eq(bxTraceOutcome("<html><body>Sign in to continue</body></html>"),
   {ip: "", loc: "", err: "the trace endpoint returned nothing usable", landed: false},
   "拦截页(200 但不是 key=value)不算落地");
eq(bxTraceOutcome(""),
   {ip: "", loc: "", err: "the trace endpoint returned nothing usable", landed: false},
   "空 body 不算落地");

// —— bxSrflxLanded ——
eq(bxSrflxLanded({srflx: ["203.0.113.5"], host: [], err: ""}), true, "拿到 srflx = 落地");
// **只看 srflx,不看 host。** 把 host 也算落地,会让一次「WebRTC 被禁/被挡」的
// 探测显示成已完成,而 Go 那边拿到空的 srflx 列表 —— 界面说查过了、判据说没查过。
eq(bxSrflxLanded({srflx: [], host: ["3f1a.local"], err: ""}), false, "只有 host 不算落地");
eq(bxSrflxLanded({srflx: [], host: [], err: ""}), false, "什么都没有");
eq(bxSrflxLanded({srflx: ["203.0.113.5"], host: [], err: "boom"}), false, "有错就不算落地");
eq(bxSrflxLanded(null), false, "null 不抛异常");

// —— bxSurfaceLanded ——
eq(bxSurfaceLanded({a: "abc", screen: ""}), true, "canvas 有值");
// canvas 被指纹防护挡掉是**要报告的事实**,不是「这一段没跑成」——屏幕尺寸还在
eq(bxSurfaceLanded({a: "", screen: "1512x982"}), true, "canvas 空但屏幕在");
eq(bxSurfaceLanded({a: "", screen: ""}), false, "两项都空才算没跑成");
eq(bxSurfaceLanded(null), false, "null 不抛异常");

if (failures > 0) { console.error(`\n${failures} 条断言失败`); process.exit(1); }
console.log("page js tests passed");
JS

node "$work/pure.js"
