# 泄漏检测(bx leakcheck)实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 bx 在**保护关着**、甚至**别的 VPN 在跑**的时候也有用 —— 用一个一次性的本机页面把「浏览器暴露给第三方的那一半事实」与「只有本机能看到的那一半事实」在 Go 里对起来,给出可展开看依据的三态结论。

**Architecture:** 三层,依赖方向单向。① `internal/leakcheck` 是**纯判据**包(无 `net/http`、无 `os/exec`、无平台代码):`Judge(BrowserReport, LocalFacts) Report`,表驱动、可变异验证 —— 这是本功能唯一值钱的部分。② `internal/leakserve` 是**一次性 loopback 服务**(四道自我约束)+ 本机事实采集(darwin 实现 / 非 darwin 桩)+ 内嵌页面;页面 POST 回原始数据,服务端调 `Judge`,**把已经判完的结论**回给页面渲染。③ `internal/cli` 的 `bx leakcheck` 以**普通用户身份**编排(root 直接拒绝),macOS 菜单加一个入口项 `Check for leaks ↗`。

**Tech Stack:** Go 1.26(标准库 `net/http`、`crypto/rand`、`crypto/subtle`、`html/template`、`embed`);复用 `internal/observe.Tristate`(谓词型事实)、`internal/supervisor.LookupRoute`、`internal/install.InspectDNSContext`、`internal/guardian.Client`(读 `/v1/status`,0666,普通用户可读)、`internal/route.NewDomainSet` + `internal/embedded.ChinaDomain()`(钉住第三方端点不在直连列表);Swift/AppKit(菜单一项)。

## Global Constraints

以下每一条对**每一个任务**都成立,不再逐任务重复。

- **TDD**:先写失败测试 → 跑红(必须看到红,且红的原因就是这次要修的那件事)→ 最小实现 → 跑绿 → 提交。
- **提交信息**:中文 conventional commits,结尾带一行 `Co-Authored-By: Claude <noreply@anthropic.com>`。
- **直接提交到 `master`**(单人项目,不开分支、不开 PR)。
- **验证命令**(每个任务的「跑绿」步骤都跑全套,不是只跑本包):
  - `go build ./... && go vet ./... && go test ./... -count=1`
  - 碰过的包再跑 `go test -race ./internal/... -count=1`
  - 交叉编译:`GOOS=linux GOARCH=amd64 go build ./...`、`GOOS=linux GOARCH=arm64 go build ./...`、`GOOS=darwin GOARCH=amd64 go build ./...`、`GOOS=darwin GOARCH=arm64 go build ./...`、`GOOS=windows GOARCH=amd64 go build ./...`、`GOOS=windows GOARCH=arm64 go build ./...`
  - 碰过 Swift 就再跑 `swift build --package-path apps/macos/BxMenu` 与 `bash scripts/test-macos-menu.sh`
  - 格式:`git ls-files '*.go' | grep -v 'embedded/assets\|internal/winfw' | xargs "$(go env GOPATH)/bin/gofumpt" -l` —— **按打印出来的内容判断,不按退出码**:它列出待格式化文件时**照样退出 0**,只看 `$?` 等于没检查。有输出就是没过。
- **实现者绝不在本机执行** `bx`、`sudo bx`、`launchctl`、`route`、`networksetup`、`ifconfig`。本计划的所有测试都不需要它们:本机事实采集全部经注入的函数替身测试,loopback 服务只绑 `127.0.0.1`。
- **绝不** `git stash` / `git reset` / `git checkout -- <path>` / `git clean`。变异验证靠**字节快照**恢复:改动前 `cp <file> /tmp/<file>.orig`,验证后 `cp /tmp/<file>.orig <file>`,再跑一次全套确认回到绿。
- **变异验证一律不加 `-run` 过滤**(本仓库有过 `-run` 静默匹配 0 条测试、于是「变异没让任何测试转红」是假结论的先例)。跑 `go test ./... -count=1` 全量,数转红的测试条数。
- `GOOS=linux go test` 在这台 darwin 上**跑不起来**(只能编译不能执行)。跨平台正确性用 `go vet` + 交叉编译验证,不要假装跑过。
- **恒绿的检查一条都不许留**(设计风险二):任何一项如果在真机上恒为 `ok`,要么它测错了东西,要么它不该存在。**这条会在实施期间逼出删除** —— 遇到一项无论如何都判不出 `bad` 的「检查」,把它降级成证据行(evidence),不要留一个永远说「没问题」的结论行。本计划已按此把「出口位置」「默认路由归谁」放进 evidence 而不是 findings,后续新增项照此裁决。
- **不许推断出 `ok`**(设计诚实性规则):页面没跑、STUN 被挡、没网、第三方超时 —— 一律 `not checked`。「没看到泄漏」不是 `ok`。
- **JS 保持愚蠢**:页面只采集、只回传、只渲染 Go 给的**成品字符串**,一个比较、一个判断都不做。这一点靠**结构**保证而不是靠 review:页面从头到尾拿不到本机事实那一半的原料(见 Task 12 的两条守卫)。本仓库的测试基建覆盖不到 JS,所以判据放进 JS 就等于放进测不到的地方。
- **本期只做 darwin**。非 darwin 上本机事实采集是桩,`Judge` 因此只会产出 `not checked` —— 这是诚实的,不是退化。
- **不留存任何结果**:不写盘、不进日志、不进 `bx status`。
- **不动菜单里 `Exit Location` / `IPv6 Leak` / `WebRTC` 那三行**(`MenuRows.swift`),它们本期保持 `.unknown`。菜单只加一个入口项。

---

## 与既有代码的关系(动手前必读)

**仓库里已经有 `bx leak-check` 与 `bx webrtc-check`(`internal/cli/cli.go:80-81`),设计文档一个字没提。** 实施者必须知道这件事,否则会重复造轮子或误删。三条事实:

1. `bx webrtc-check --browser` 里的 `runBrowserICECheck`(`internal/cli/cli.go` 约 3190 行)**已经**在做「起 loopback 服务 + 开浏览器 + 收 ICE」,但它**四道自我约束一道都没有**:没有 token、不校验 `Origin`/`Host`、`/` 与 `/result` 对同机任何进程敞开。它是本期设计风险一描述的那个攻击面的现存实例。
2. `collectNetworkProbe`(约 2526 行)拿 `api.ipify.org` / `api64.ipify.org` 做出口探测,而 **`ipify.org` 就在 `internal/embedded/assets/china_domain.txt` 第 6045 行**,`route.DomainSet` 是后缀匹配 —— 也就是说这两个域名在 bx 开着的时候是**直连**的。这正是设计文档点名的 `ifconfig.me` 那个坑,原样复发了一次。
3. 新命令叫 `leakcheck`(无连字符),与既有 `leak-check`**只差一个连字符**。

**本期的处置(不扩大范围)**:新代码全部新写,**不改、不删既有的 `leak-check` / `webrtc-check`**(它们有 MCP 只读工具与 `internal/mcp/liveops.go:235` 在依赖,动它们是另一期的事)。Task 14 只做两件小事:给两条命令的 `Usage` 加上互相区分的措辞,并加一条测试钉住两者并存且用途不同。上面第 1、2 条作为**已知缺陷**记录在此,留给后续单独立项。

## 文件结构

| 文件 | 职责 |
|---|---|
| `internal/leakcheck/verdict.go` | `Verdict` 三态(零值 `NotChecked`)、`Finding`、`Report`、`AnomalyCount` |
| `internal/leakcheck/endpoints.go` | 三个第三方端点常量 + `EndpointDisclosure` |
| `internal/leakcheck/input.go` | `BrowserReport`(页面上报)、`LocalFacts`(本机事实)、`InterfaceRef` |
| `internal/leakcheck/judge.go` | `Judge(BrowserReport, LocalFacts) Report` —— 三条组合规则,纯函数 |
| `internal/leakcheck/describe.go` | `DescribeInterface` 把 `utun4` 翻成人话;翻不出就说翻不出 |
| `internal/leakcheck/*_test.go` | 表驱动判据测试 + 纯度守卫 + china 列表守卫 |
| `internal/leakserve/server.go` | 一次性 loopback 服务:四道自我约束 |
| `internal/leakserve/page.go` | 内嵌页面模板 + 渲染(页面只拿成品结论) |
| `internal/leakserve/page.html` | HTML/JS(愚蠢:采集 → POST → 渲染 Go 给的字符串) |
| `internal/leakserve/facts_darwin.go` | 本机事实采集(route / DNS / Guardian / scutil) |
| `internal/leakserve/facts_other.go` | 非 darwin 桩:全部不可观测 |
| `internal/cli/leakcheck.go` | `bx leakcheck`:拒绝 root、编排、渲染到 stdout、`--json` |
| `apps/macos/BxMenu/Sources/BxMenu/main.swift` | 新增 `Check for leaks ↗` 菜单项与动作 |
| `internal/cli/cli_test.go` | 更新 spawn 链守卫白名单(Task 15) |

## 任务顺序的理由

1. **纯判据先落地(Task 1-7)。** 它不开任何端口、不碰系统,任何一次提交树都是安全的;而且它是本功能唯一值钱的部分,先把它钉死,后面几层就只是接线。反过来先做服务端,就会在判据还没定形的时候先把一个 HTTP 端口开出去。
2. **四道自我约束(Task 8-11)在页面之前。** 页面一旦存在就会有人手工去点它;先把闸门装好,再放东西进来。四道各自一个任务、各自一条测试(设计明写是硬要求,不是加分项)。
3. **页面(Task 12)在服务之后**,因为它渲染的是服务端已经判完的 `Report`;顺序反过来就必然要先在 JS 里放判断。
4. **采集(Task 13)在 CLI(Task 14)之前**,CLI 是把前面全部接起来的那一步。
5. **菜单(Task 15)最后**:它只是一个入口,前面全绿之前加它没有意义,而且它要改一条既有守卫的白名单,越晚越容易论证。

每个任务结束时树都能 `go build ./... && go test ./...` 全绿,且没有半成品端口对外开着。

---

### Task 1: 三态判定与报告类型

**为什么不复用 `internal/observe.Tristate`(必须写进代码注释里的决定)**:`observe.Tristate` 是**谓词**结果 —— 它回答「劫持生效了吗」这类是非题,`True` 就是「是」。而一条泄漏结论的两极是 `ok` / `bad`,把「有泄漏」映射成 `True` 会让每个调用点都要停下来想一次「true 是好还是坏」。零值纪律是同一条(零值 = 没问出来),但**极性不同,所以是两个类型**。反过来,**本机事实里的谓词字段照常复用 `observe.Tristate`**(Task 3),那里它真的合身。

**为什么不复用 `internal/cli` 的 `checkReport{Name,Status,Detail,Hint}`**:它的 `Status` 是四值字符串(`ok`/`warn`/`fail`/`info`),而「not checked」在那套词汇里是 `Status:"info"` 外加 `Detail:"not checked"` —— 也就是说「没检查」要从散文里读出来,零值 `checkReport{}` 的 `Status` 是空串。本功能的失败模式恰恰是「把没检查渲染成正常」,所以它需要一个**零值就是 not checked 的真类型**。`bx doctor` 那套一个字不动。

**Files:**
- Create: `internal/leakcheck/verdict.go`
- Test: `internal/leakcheck/verdict_test.go`

**Interfaces:**
- Produces: `leakcheck.Verdict`(`NotChecked`/`OK`/`Bad`,零值 `NotChecked`)、`Verdict.String()`、`leakcheck.Finding{ID,Title,Verdict,Summary,Evidence}`、`leakcheck.Report{GeneratedAt,Endpoints,Findings,Evidence,AnomalyCount}`、`leakcheck.NewReport(now, endpoints, findings, evidence) Report`

- [ ] **Step 1: 写失败测试**

创建 `internal/leakcheck/verdict_test.go`:

```go
package leakcheck

import "testing"

// 零值必须是 NotChecked。**这是本包的地基**:一个零值读作 OK 的三态,会让
// 「页面没跑成」在下游渲染成「一切正常」——设计风险四点名的那种最坏失败。
func TestZeroVerdictIsNotChecked(t *testing.T) {
	var v Verdict
	if v != NotChecked {
		t.Fatalf("零值必须是 NotChecked,得到 %v", v)
	}
	if got := v.String(); got != "not checked" {
		t.Fatalf("零值必须渲染成 %q,得到 %q", "not checked", got)
	}
}

// 词汇与 MenuRows.swift 的 MenuRowMark 一一对应(ok/bad/unknown 的 unknown 在
// 这里叫 not checked,是同一格)。**串是用户可见契约**,改它等于改菜单与页面上
// 的字,必须先改这条测试。
func TestVerdictVocabulary(t *testing.T) {
	for _, tc := range []struct {
		verdict Verdict
		want    string
	}{
		{NotChecked, "not checked"},
		{OK, "ok"},
		{Bad, "bad"},
	} {
		if got := tc.verdict.String(); got != tc.want {
			t.Errorf("%d 应渲染成 %q,得到 %q", tc.verdict, tc.want, got)
		}
	}
}

// anomalyCount 只数 bad。**not checked 永远不计入异常** —— MenuRows.swift 的
// anomalyCount 已经这么写了,这里是接上它,不是新发明。把 NotChecked 计进来,
// 一台没联网的机器就会显示一堆异常,而它什么问题都没有。
func TestAnomalyCountCountsOnlyBad(t *testing.T) {
	rep := NewReport(fixedTime(), EndpointDisclosure{}, []Finding{
		{ID: "a", Verdict: Bad},
		{ID: "b", Verdict: NotChecked},
		{ID: "c", Verdict: OK},
		{ID: "d", Verdict: NotChecked},
		{ID: "e", Verdict: Bad},
	}, nil)
	if rep.AnomalyCount != 2 {
		t.Fatalf("只有 2 条 bad,AnomalyCount 应为 2,得到 %d", rep.AnomalyCount)
	}
}

// 一份完全没有 finding 的报告,异常数是 0 而不是负数/panic,且不谎称健康:
// 判据由调用方看 Findings 是否为空。这里只钉住计数不炸。
func TestAnomalyCountOfEmptyReport(t *testing.T) {
	rep := NewReport(fixedTime(), EndpointDisclosure{}, nil, nil)
	if rep.AnomalyCount != 0 {
		t.Fatalf("空报告的 AnomalyCount 应为 0,得到 %d", rep.AnomalyCount)
	}
	if !rep.GeneratedAt.Equal(fixedTime()) {
		t.Fatalf("GeneratedAt 必须原样带出注入的时间,得到 %v", rep.GeneratedAt)
	}
}
```

再创建 `internal/leakcheck/testsupport_test.go`(后续任务共用):

```go
package leakcheck

import "time"

// 固定时刻。测试永不读 time.Now():一份带真实时钟的 fixture 会让「报告是不是
// 这一轮产出的」变成不可断言的事。
func fixedTime() time.Time {
	return time.Date(2026, 8, 11, 10, 30, 0, 0, time.UTC)
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/leakcheck/ -count=1`
Expected: FAIL — `undefined: Verdict` / `undefined: NewReport` / `undefined: EndpointDisclosure`(包还不存在,`go build` 也会报 no Go files)

- [ ] **Step 3: 最小实现**

创建 `internal/leakcheck/verdict.go`:

```go
// Package leakcheck 是泄漏检测的**纯判据**层:给定浏览器上报的那一半事实与本机
// 观测到的那一半,产出一组三态结论。
//
// 本包不做 I/O:没有 net/http、没有 os/exec、没有平台代码(由 purity_test.go 钉住)。
// 理由是这个功能唯一值钱的部分就是判据,而判据必须能被表驱动测试与变异验证覆盖;
// 一旦它与「起服务、开浏览器、跑命令」混在一起,就又变成只能靠人读的代码。
package leakcheck

import "time"

// Verdict 是一条结论的三态。**零值必须是 NotChecked。**
//
// 刻意不复用 internal/observe.Tristate:那个类型是**谓词**结果(True = 「是」),
// 而这里的两极是「好」与「坏」,把「有泄漏」写成 True 会让每个调用点都要先想一下
// true 是好是坏。零值纪律两者相同,极性不同,所以是两个类型。
//
// 词汇与 apps/macos/BxMenu/Sources/BxMenu/MenuRows.swift 的 MenuRowMark 对齐
// (ok / bad / unknown),那里的 anomalyCount 也只数 bad。
type Verdict uint8

const (
	// NotChecked:没问出来。页面被关掉、STUN 被挡、没网、第三方超时,全在这一格。
	// **绝不因为「没看到泄漏」就升格成 OK。**
	NotChecked Verdict = iota
	OK
	Bad
)

func (v Verdict) String() string {
	switch v {
	case OK:
		return "ok"
	case Bad:
		return "bad"
	default:
		return "not checked"
	}
}

// MarshalJSON 让 JSON 里也是这三个词,而不是 0/1/2。页面直接显示这个串。
func (v Verdict) MarshalJSON() ([]byte, error) {
	return []byte(`"` + v.String() + `"`), nil
}

// Finding 是一条可展开看依据的结论。
//
// Evidence 是**必须**的那一半:一个不肯出示依据的检测工具,用户没有理由信它,
// 而且 bx 判错时用户看得出它是怎么错的。
type Finding struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Verdict  Verdict  `json:"verdict"`
	Summary  string   `json:"summary"`
	Evidence []string `json:"evidence,omitempty"`
}

// Report 是一次检测的全部产出。**没有任何字段是 BrowserReport 或 LocalFacts** ——
// 页面拿到的必须是成品结论,不是可判断的原料(见 Task 12 的守卫)。
type Report struct {
	GeneratedAt  time.Time          `json:"generated_at"`
	Endpoints    EndpointDisclosure `json:"endpoints"`
	Findings     []Finding          `json:"findings"`
	Evidence     []string           `json:"evidence,omitempty"`
	AnomalyCount int                `json:"anomaly_count"`
}

// NewReport 组装报告并算出异常数。**只数 Bad。**
func NewReport(now time.Time, endpoints EndpointDisclosure, findings []Finding, evidence []string) Report {
	anomalies := 0
	for _, f := range findings {
		if f.Verdict == Bad {
			anomalies++
		}
	}
	return Report{
		GeneratedAt:  now,
		Endpoints:    endpoints,
		Findings:     findings,
		Evidence:     evidence,
		AnomalyCount: anomalies,
	}
}
```

`EndpointDisclosure` 在 Task 2 定义;为让本任务能编过,先在 `endpoints.go` 里放它的**空壳**:

```go
package leakcheck

// EndpointDisclosure 是页面在联网**之前**必须原样显示的第三方清单。
// 具体地址与选取理由见 Task 2。
type EndpointDisclosure struct {
	EchoV4 string `json:"echo_v4"`
	EchoV6 string `json:"echo_v6"`
	STUN   string `json:"stun"`
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/leakcheck/ -count=1 && go build ./... && go vet ./... && go test ./... -count=1`
Expected: PASS

- [ ] **Step 5: 变异验证**

对每一处改动,先 `cp internal/leakcheck/verdict.go /tmp/verdict.go.orig`,改完跑 `go test ./... -count=1`(**不加 `-run`**),记下转红条数,再 `cp /tmp/verdict.go.orig internal/leakcheck/verdict.go` 并重跑确认回绿。

| 变异 | 必须转红的测试 | 它防住的生产改动 |
|---|---|---|
| 常量顺序改成 `OK Verdict = iota; Bad; NotChecked` | `TestZeroVerdictIsNotChecked` | 有人「按重要性排序」把零值让给了 OK |
| `String()` 的 `default` 改成返回 `"ok"` | `TestZeroVerdictIsNotChecked`、`TestVerdictVocabulary` | 渲染时把未知当正常 |
| `NewReport` 的判据改成 `if f.Verdict != OK` | `TestAnomalyCountCountsOnlyBad` | 把 not checked 当异常,没联网就满屏告警 |
| `String()` 的 `"not checked"` 改成 `"unknown"` | `TestZeroVerdictIsNotChecked`、`TestVerdictVocabulary` | 用户可见词汇漂移,与菜单/文档对不上 |

- [ ] **Step 6: 提交**

```bash
git add internal/leakcheck/verdict.go internal/leakcheck/endpoints.go internal/leakcheck/verdict_test.go internal/leakcheck/testsupport_test.go
git commit -m "$(cat <<'EOF'
feat(leakcheck): 三态判定,零值就是 not checked

极性与 observe.Tristate 相反,故不复用它;词汇与 MenuRows.swift 的
MenuRowMark 对齐,anomalyCount 同样只数 bad。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: 第三方端点常量 + 两条守卫

**选取理由(设计要求「计划照此选并写明理由」)**:

| 端点 | 选的是 | 为什么 |
|---|---|---|
| v4 回声 | `https://ipv4.icanhazip.com` | ① **A-only 主机名**,拿得到「v4 出口是谁」;② 返回体就是一个 IP 加换行,`text/plain`,没有 JSON 结构可以变;③ **实测带 `access-control-allow-origin: *`**(2026-08-11 用 `curl -H 'Origin: http://127.0.0.1:12345'` 验证,`server: cloudflare`)—— 页面是 loopback origin,没有 CORS 就一个字都拿不到;④ **`icanhazip.com` 不在 `internal/embedded/assets/china_domain.txt` 里**(实测 0 命中) |
| v6 回声 | `https://ipv6.icanhazip.com` | 同一运营方的 **AAAA-only** 主机名,配对使用;同样实测 CORS `*`、不在 china 列表 |
| STUN | `stun:stun.cloudflare.com:3478` | 设计说「公共且长期稳定的一个即可」。选 Cloudflare 而不是 `stun.l.google.com:19302`:后者的域名在中国大陆链路上经常解析不出或被丢,而**一个连不上 STUN 的泄漏检测只会输出 `not checked`** —— 诚实,但没用。只取 srflx,不做任何中继 |

**被否掉的**:`api.ipify.org` / `api64.ipify.org` —— **`ipify.org` 就在 `china_domain.txt` 第 6045 行**,而 `route.DomainSet` 是后缀匹配,所以 bx 开着时它们**直连**,回声会报出真实 ISP 出口、把一台完全正常的机器判成泄漏。这正是设计文档点名的 `ifconfig.me` 那个坑。(既有 `collectNetworkProbe` 正用着它俩,见「与既有代码的关系」。)`ifconfig.co`、`ip.sb`、`seeip.org` 同样在列表里,一并否掉。`v4.ident.me`/`v6.ident.me` 实测 CORS 也是 `*` 且不在列表里,作为**备选**记在这里,但运营规模小于 Cloudflare,本期不用。

**Files:**
- Modify: `internal/leakcheck/endpoints.go`
- Test: `internal/leakcheck/endpoints_test.go`、`internal/leakcheck/purity_test.go`

**Interfaces:**
- Consumes: `leakcheck.EndpointDisclosure`(Task 1)
- Produces: `leakcheck.EchoV4URL`、`leakcheck.EchoV6URL`、`leakcheck.STUNURL`、`leakcheck.Endpoints() EndpointDisclosure`

- [ ] **Step 1: 写失败测试**

创建 `internal/leakcheck/endpoints_test.go`:

```go
package leakcheck

import (
	"net/url"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/embedded"
	"github.com/getbx/bx/internal/route"
)

// 这三个地址是**用户可见契约**:页面在联网前照原样显示它们,换掉它们等于换掉
// 用户的暴露对象。所以改这三个串必须先改这条测试,不能顺手改。
func TestEndpointsArePinned(t *testing.T) {
	if EchoV4URL != "https://ipv4.icanhazip.com" {
		t.Errorf("v4 回声端变了:%q", EchoV4URL)
	}
	if EchoV6URL != "https://ipv6.icanhazip.com" {
		t.Errorf("v6 回声端变了:%q", EchoV6URL)
	}
	if STUNURL != "stun:stun.cloudflare.com:3478" {
		t.Errorf("STUN 变了:%q", STUNURL)
	}
	d := Endpoints()
	if d.EchoV4 != EchoV4URL || d.EchoV6 != EchoV6URL || d.STUN != STUNURL {
		t.Fatalf("Endpoints() 必须原样带出三个常量,得到 %+v", d)
	}
}

// v4 与 v6 必须是**两个不同的主机名**。用同一个主机名(比如某个双栈地址)就
// 问不出「v6 出口是谁」,而那正是别的 VPN 最常见的漏洞。
func TestEchoEndpointsAreTwoDistinctHosts(t *testing.T) {
	v4, err := url.Parse(EchoV4URL)
	if err != nil {
		t.Fatal(err)
	}
	v6, err := url.Parse(EchoV6URL)
	if err != nil {
		t.Fatal(err)
	}
	if v4.Hostname() == v6.Hostname() {
		t.Fatalf("v4 与 v6 回声端不能是同一个主机名(%q):双栈主机名问不出 v6 出口", v4.Hostname())
	}
	for _, u := range []*url.URL{v4, v6} {
		if u.Scheme != "https" {
			t.Errorf("%s 必须是 https:明文回声在路上可被改写,判据就整个失效", u)
		}
	}
}

// **这条测试是为一个真实发生过的错误写的。** 项目拿 ifconfig.me 当出口探测,
// 而它本就在直连列表里,于是「漏直连」是自摆乌龙。同一个坑今天还活在
// internal/cli 的 collectNetworkProbe 里(api.ipify.org,而 ipify.org 在列表里)。
//
// 判据用的就是生产环境那一份 DomainSet 与那一份内嵌列表 —— 不是抄一份规则。
func TestEchoEndpointsAreNotOnTheChinaDirectList(t *testing.T) {
	patterns := strings.Split(string(embedded.ChinaDomain()), "\n")
	if len(patterns) < 1000 {
		t.Fatalf("内嵌 china 列表只有 %d 行,本守卫读不懂现在的资产,请连同它一起重写", len(patterns))
	}
	set := route.NewDomainSet(patterns)
	// 自检:列表里确实有东西能命中,否则下面的「没命中」是假绿。
	if !set.Match("ipify.org") {
		t.Fatal("自检失败:ipify.org 本应在 china 直连列表里(第 6045 行)——" +
			"若上游列表变了,请更新这条自检,而不是删掉它")
	}
	for _, raw := range []string{EchoV4URL, EchoV6URL} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if set.Match(u.Hostname()) {
			t.Errorf("%s 在 china 直连列表里:bx 开着时它会直连,回声报出真实 ISP 出口,"+
				"把一台完全正常的机器判成泄漏(ifconfig.me 那个坑)", u.Hostname())
		}
	}
	// STUN 的主机名同理:它是 UDP,不走 DomainSet,但域名维度的分流规则一样适用。
	host := strings.TrimPrefix(STUNURL, "stun:")
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	if set.Match(host) {
		t.Errorf("STUN 主机 %s 在 china 直连列表里,srflx 会报出真实出口", host)
	}
}
```

创建 `internal/leakcheck/purity_test.go`:

```go
package leakcheck

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// 本包必须保持**纯判据**:不起服务、不跑命令、不读文件。
//
// 这不是洁癖。判据是本功能唯一值钱的部分,它必须能被表驱动测试与变异验证完整
// 覆盖;一旦 net/http 或 os/exec 溜进来,「判得对不对」与「接线对不对」就重新
// 变成同一件事,而本仓库的全部事故都在后者。
//
// 读不懂目录时**必须响亮失败**:一个找不到源文件就自动通过的守卫,等于没有守卫。
func TestLeakcheckPackageStaysPure(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读不到本包目录,守卫失去意义: %v", err)
	}
	banned := map[string]string{
		"net/http": "起服务/发请求属于 internal/leakserve",
		"os/exec":  "跑命令属于 internal/leakserve 的事实采集",
		"net":      "拨号与地址解析都是 I/O;判据只吃已经采到的字符串",
		"os":       "读环境/读文件会让判据依赖运行环境",
	}
	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		checked++
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(".", name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("解析 %s 失败,守卫读不懂现在的代码: %v", name, err)
		}
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("%s 的 import 解析失败: %v", name, err)
			}
			if why, bad := banned[path]; bad {
				t.Errorf("%s import 了 %q:本包必须保持纯判据 —— %s", name, path, why)
			}
			if strings.HasPrefix(path, "github.com/getbx/bx/internal/") &&
				path != "github.com/getbx/bx/internal/observe" {
				t.Errorf("%s import 了 %q:纯判据只允许依赖 internal/observe(三态类型),"+
					"依赖控制面/安装/监督任何一个包都会把它拖回不可测的位置", name, path)
			}
		}
	}
	if checked == 0 {
		t.Fatal("本包一个非测试 .go 文件都没找到:守卫读不懂现在的目录结构,请连同它一起重写")
	}
}
```

> 注:`endpoints_test.go` import 了 `internal/embedded` 与 `internal/route`,那是**测试文件**,不受纯度守卫约束(守卫显式跳过 `_test.go`)。这是有意的:守卫要挡的是生产代码的依赖面。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/leakcheck/ -count=1`
Expected: FAIL — `undefined: EchoV4URL` / `undefined: Endpoints`

- [ ] **Step 3: 最小实现**

改写 `internal/leakcheck/endpoints.go`:

```go
package leakcheck

// 三个第三方端点。**它们是用户可见契约**:页面在发起任何网络请求**之前**必须
// 原样显示这三个串(设计风险三:第三方暴露是用户明确接受的,但必须可见)。
//
// 选取理由(每一条都是硬约束,换端点前逐条复核,守卫见 endpoints_test.go):
//
//   - 回声端必须**同时有 v4-only 与 v6-only 主机名**。用一个双栈主机名就问不出
//     「v6 出口是谁」,而那正是别的 VPN 最常见的漏洞。
//   - 返回体要小且格式稳定:icanhazip 返回的就是一个 IP 加换行,text/plain,
//     没有 JSON 结构可以在某次改版里换形状。
//   - 必须带 CORS。页面是 loopback origin,没有 `Access-Control-Allow-Origin`
//     就一个字节都读不到。两个回声端 2026-08-11 实测均返回 `*`。
//   - **不用 china 直连列表里的域名。** 本项目在这上面栽过:拿 ifconfig.me 当
//     出口探测,而它本就在直连列表里。api.ipify.org 有同样问题(ipify.org 在
//     列表第 6045 行,DomainSet 是后缀匹配),故一并否掉。
//   - STUN 只要一个公共且长期稳定的。**不用 stun.l.google.com**:它在中国大陆
//     链路上经常解析不出,而一个连不上 STUN 的检测只会输出 not checked。
const (
	EchoV4URL = "https://ipv4.icanhazip.com"
	EchoV6URL = "https://ipv6.icanhazip.com"
	STUNURL   = "stun:stun.cloudflare.com:3478"
)

// EndpointDisclosure 是页面在联网**之前**必须原样显示的第三方清单。
type EndpointDisclosure struct {
	EchoV4 string `json:"echo_v4"`
	EchoV6 string `json:"echo_v6"`
	STUN   string `json:"stun"`
}

// Endpoints 返回这一版要联系的第三方。页面与 CLI 都从这里取,**不各自写一份**:
// 两处各写一份时,页面上说的与实际请求的会静默分叉,而「事先明说要联系谁」这条
// 缓解措施正是靠它们一致才成立的。
func Endpoints() EndpointDisclosure {
	return EndpointDisclosure{EchoV4: EchoV4URL, EchoV6: EchoV6URL, STUN: STUNURL}
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/leakcheck/ -count=1 && go build ./... && go vet ./... && go test ./... -count=1`
Expected: PASS

- [ ] **Step 5: 变异验证**

`cp internal/leakcheck/endpoints.go /tmp/endpoints.go.orig`,逐个改、全量跑 `go test ./... -count=1`、再恢复重跑确认回绿。

| 变异 | 必须转红的测试 | 它防住的生产改动 |
|---|---|---|
| `EchoV4URL` 改成 `"https://api.ipify.org"` | `TestEndpointsArePinned`、`TestEchoEndpointsAreNotOnTheChinaDirectList` | 换回一个在直连列表里的域名(本项目已犯过) |
| `EchoV6URL` 改成 `EchoV4URL` 的值 | `TestEndpointsArePinned`、`TestEchoEndpointsAreTwoDistinctHosts` | 「反正都能回声」把 v6 那半悄悄合并掉 |
| `EchoV4URL` 的 `https` 改成 `http` | `TestEndpointsArePinned`、`TestEchoEndpointsAreTwoDistinctHosts` | 明文回声可被路上改写 |
| `Endpoints()` 里 `EchoV6` 字段改成填 `EchoV4URL` | `TestEndpointsArePinned` | 页面显示的与实际请求的分叉 |
| `purity_test.go` 的 `banned` 里删掉 `"net/http"`,同时在 `endpoints.go` 顶部加 `import _ "net/http"` | `TestLeakcheckPackageStaysPure`(删守卫后**不转红** = 证明这条守卫确实是唯一拦着它的东西) | 判据包长出 I/O 依赖 |

> 最后一行是**双向**变异:先只加 `import _ "net/http"` 确认守卫转红(证明它有效),再连同 `banned` 一起改确认转绿(证明红是它给的,不是别的测试顺带)。两步都做完再恢复。

- [ ] **Step 6: 提交**

```bash
git add internal/leakcheck/endpoints.go internal/leakcheck/endpoints_test.go internal/leakcheck/purity_test.go
git commit -m "$(cat <<'EOF'
feat(leakcheck): 钉住三个第三方端点,并守住它们不在 china 直连列表里

ipify 被否是因为 ipify.org 就在 china_domain.txt 里(DomainSet 后缀匹配),
与当年拿 ifconfig.me 当出口探测是同一个坑。另加一条纯度守卫,拦住 net/http
与 os/exec 溜进判据包。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: 输入类型 + `Judge` 骨架 + **空上报必须是 not checked**(设计风险四)

设计风险四单独点名了这条:「一份空的浏览器上报必须产出 `not checked` 而不是 `ok`,这条要有独立测试并变异验证 —— 否则『页面没跑成』会渲染成『一切正常』,而那正是最坏的那种失败。」所以它有**自己的任务**,而且必须在任何一条规则落地**之前**就绿,并在 Task 4-6 全程保持绿。

**Files:**
- Create: `internal/leakcheck/input.go`、`internal/leakcheck/judge.go`
- Test: `internal/leakcheck/judge_test.go`

**Interfaces:**
- Consumes: `Verdict`/`Finding`/`Report`/`NewReport`/`Endpoints`(Task 1-2)
- Produces:
  - `leakcheck.BrowserReport{UserAgent, ExitV4, ExitV4Err, ExitV6, ExitV6Err, SRFLX []string, STUNErr}` 及方法 `Empty() bool`
  - `leakcheck.InterfaceRef{Name, Display, Err string}` 及方法 `Known() bool`
  - `leakcheck.LocalFacts{DefaultRouteV4, DefaultRouteV6 InterfaceRef, IPv6DefaultPresent observe.Tristate, DNSServers []string, DNSServerEgress map[string]string, DNSErr, BXTunInterface, BXProtection string}`
  - `leakcheck.Judge(now time.Time, browser BrowserReport, local LocalFacts) Report`
  - finding ID 常量:`FindingWebRTC = "webrtc_srflx"`、`FindingIPv6 = "ipv6_leak"`、`FindingDNS = "dns_path"`

- [ ] **Step 1: 写失败测试**

创建 `internal/leakcheck/judge_test.go`:

```go
package leakcheck

import "testing"

// **设计风险四。** 空上报 + 空本机事实 = 什么都没问出来。三条结论必须全是
// not checked,一条 ok 都不许有,异常数必须是 0(not checked 不是异常)。
//
// 这份 fixture 是**生产环境造得出来的**:用户点了菜单、浏览器没打开、或者打开了
// 但用户直接关掉标签页,Wait 超时后送进 Judge 的就正是这个零值。本仓库栽过
// 「用生产造不出的输入喂测试」的跟头,这条刻意不是那样。
func TestEmptyBrowserReportYieldsNotChecked(t *testing.T) {
	rep := Judge(fixedTime(), BrowserReport{}, LocalFacts{})

	if len(rep.Findings) != 3 {
		t.Fatalf("结论条数应恒为 3(webrtc / ipv6 / dns),得到 %d", len(rep.Findings))
	}
	for _, f := range rep.Findings {
		if f.Verdict != NotChecked {
			t.Errorf("%s 在什么都没问出来时必须是 not checked,得到 %s(summary=%q)",
				f.ID, f.Verdict, f.Summary)
		}
		if f.Summary == "" {
			t.Errorf("%s 是 not checked 也必须说清为什么没问出来,summary 不能是空串", f.ID)
		}
	}
	if rep.AnomalyCount != 0 {
		t.Fatalf("全 not checked 时异常数必须是 0,得到 %d", rep.AnomalyCount)
	}
}

// 只有浏览器那一半缺席(本机事实齐全)时,依然不许升格成 ok。
// 「本地看着没问题」不是「没有泄漏」——组合规则的另一半根本没到。
func TestMissingBrowserHalfStillNotChecked(t *testing.T) {
	local := LocalFacts{
		DefaultRouteV4:  InterfaceRef{Name: "utun4", Display: "Unidentified VPN (utun4)"},
		DNSServers:      []string{"127.0.0.1"},
		DNSServerEgress: map[string]string{"127.0.0.1": "lo0"},
	}
	rep := Judge(fixedTime(), BrowserReport{}, local)
	for _, f := range rep.Findings {
		if f.ID == FindingDNS {
			continue // DNS 只用本机那一半,由 Task 6 单独覆盖
		}
		if f.Verdict != NotChecked {
			t.Errorf("%s 缺了浏览器那一半就必须是 not checked,得到 %s", f.ID, f.Verdict)
		}
	}
}

// 结论的 ID 与顺序是页面与 CLI 共同依赖的契约,固定次序好让输出可 diff。
func TestFindingIDsAndOrderAreStable(t *testing.T) {
	rep := Judge(fixedTime(), BrowserReport{}, LocalFacts{})
	want := []string{FindingWebRTC, FindingIPv6, FindingDNS}
	if len(rep.Findings) != len(want) {
		t.Fatalf("结论条数应为 %d,得到 %d", len(want), len(rep.Findings))
	}
	for i, id := range want {
		if rep.Findings[i].ID != id {
			t.Errorf("第 %d 条应是 %q,得到 %q", i, id, rep.Findings[i].ID)
		}
		if rep.Findings[i].Title == "" {
			t.Errorf("%s 必须有标题", id)
		}
	}
}

// 报告必须原样带出要联系的第三方,页面才能照原样显示(设计风险三)。
func TestJudgeCarriesEndpointDisclosure(t *testing.T) {
	rep := Judge(fixedTime(), BrowserReport{}, LocalFacts{})
	if rep.Endpoints != Endpoints() {
		t.Fatalf("报告必须带出 Endpoints(),得到 %+v", rep.Endpoints)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/leakcheck/ -count=1`
Expected: FAIL — `undefined: Judge` / `undefined: BrowserReport` / `undefined: FindingWebRTC`

- [ ] **Step 3: 最小实现**

创建 `internal/leakcheck/input.go`:

```go
package leakcheck

import "github.com/getbx/bx/internal/observe"

// BrowserReport 是页面 POST 回来的**原始**观测。全部是字符串:页面不判断、
// 不归一化、不比较,它只是把看到的东西原样送回来。
//
// 每一项都配一个 Err:失败与「没跑」在下游必须分得开,而两者都绝不能变成 ok。
type BrowserReport struct {
	UserAgent string `json:"user_agent"`
	// ExitV4 是 v4-only 回声端看到的源地址。
	ExitV4    string `json:"exit_v4"`
	ExitV4Err string `json:"exit_v4_err"`
	// ExitV6 是 v6-only 回声端看到的源地址。
	ExitV6    string `json:"exit_v6"`
	ExitV6Err string `json:"exit_v6_err"`
	// SRFLX 是 STUN 问出来的 server-reflexive 地址。**只要 srflx。**
	// host candidate 早被浏览器 mDNS 混淆了(<uuid>.local),按那个年代的做法
	// 实现会得到一条恒绿的检查,而恒绿比没有更糟。
	SRFLX   []string `json:"srflx"`
	STUNErr string   `json:"stun_err"`
}

// Empty 判断这份上报是不是「什么都没有」。页面没跑成、用户直接关掉标签页、
// 服务硬超时,送进来的都是零值。
func (b BrowserReport) Empty() bool {
	return b.ExitV4 == "" && b.ExitV6 == "" && len(b.SRFLX) == 0
}

// InterfaceRef 是一个网络接口,以及它翻成人话之后的名字。
//
// Display 为空表示**翻不出来**,不是「没有接口」——两者在渲染时必须分开说
// (设计诚实性规则:「无法识别」是一个合法答案,猜不是)。
type InterfaceRef struct {
	Name    string `json:"name,omitempty"`
	Display string `json:"display,omitempty"`
	Err     string `json:"err,omitempty"`
}

// Known 表示这一项确实观测到了一个接口。
func (r InterfaceRef) Known() bool { return r.Name != "" && r.Err == "" }

// LocalFacts 是只有本机能看到的那一半。全部只读采集,采不到就留空并记 Err。
type LocalFacts struct {
	// DefaultRouteV4/V6:发往公网时内核把包交给谁。
	DefaultRouteV4 InterfaceRef `json:"default_route_v4"`
	DefaultRouteV6 InterfaceRef `json:"default_route_v6"`
	// IPv6DefaultPresent 是**谓词**,所以这里复用 observe.Tristate:零值 Unknown
	// 就是「没问出来」,与「问了,确实没有 v6 默认路由」必须分开 —— 后者能支撑
	// 一句诚实的 ok,前者不能。
	IPv6DefaultPresent observe.Tristate `json:"ipv6_default_present"`
	// DNSServers 是系统当前的解析器。
	DNSServers []string `json:"dns_servers,omitempty"`
	// DNSServerEgress 是每个解析器地址的出口接口(route 查出来的)。
	// 少一个键 = 那个解析器的去向没问出来。
	DNSServerEgress map[string]string `json:"dns_server_egress,omitempty"`
	DNSErr          string            `json:"dns_err,omitempty"`
	// BXTunInterface / BXProtection 来自 Guardian 的 /v1/status(普通用户可读)。
	// 空串表示 Guardian 没答上话,不表示 bx 没在跑。
	BXTunInterface string `json:"bx_tun_interface,omitempty"`
	BXProtection   string `json:"bx_protection,omitempty"`
}
```

创建 `internal/leakcheck/judge.go`:

```go
package leakcheck

import "time"

// 三条结论的 ID。页面与 CLI 都按它们取,固定不变。
const (
	FindingWebRTC = "webrtc_srflx"
	FindingIPv6   = "ipv6_leak"
	FindingDNS    = "dns_path"
)

// Judge 把两半事实对起来,产出一组三态结论。**纯函数**:同样的输入永远同样的
// 输出,时间也是传进来的。
//
// 结论只有三条,而且每一条都能真的判成 bad。**刻意没有「出口位置」「默认路由
// 归谁」这两条**:它们在真机上永远是 ok(没有任何输入能让它们变坏),而一条
// 恒绿的检查会把整个界面训练成装饰(设计风险二)。它们作为 evidence 出现,
// 用户照样看得到,只是不冒充结论。
func Judge(now time.Time, browser BrowserReport, local LocalFacts) Report {
	findings := []Finding{
		judgeWebRTC(browser, local),
		judgeIPv6(browser, local),
		judgeDNS(browser, local),
	}
	return NewReport(now, Endpoints(), findings, collectEvidence(browser, local))
}

func judgeWebRTC(browser BrowserReport, local LocalFacts) Finding {
	return Finding{
		ID:      FindingWebRTC,
		Title:   "WebRTC vs HTTP exit",
		Verdict: NotChecked,
		Summary: "No browser report was received.",
	}
}

func judgeIPv6(browser BrowserReport, local LocalFacts) Finding {
	return Finding{
		ID:      FindingIPv6,
		Title:   "IPv6 exposure",
		Verdict: NotChecked,
		Summary: "No browser report was received.",
	}
}

func judgeDNS(browser BrowserReport, local LocalFacts) Finding {
	return Finding{
		ID:      FindingDNS,
		Title:   "DNS path",
		Verdict: NotChecked,
		Summary: "Local DNS facts were not observed.",
	}
}

// collectEvidence 在 Task 7 填内容;现在返回 nil,让报告结构先成立。
func collectEvidence(browser BrowserReport, local LocalFacts) []string { return nil }
```

> **注意**:上面三个 `judge*` 现在都忽略参数、恒返回 `NotChecked`。这是**有意的最小实现** —— Task 4/5/6 各自把其中一条变成真判据,而 `TestEmptyBrowserReportYieldsNotChecked` 必须在这三次改动之后**依然绿**。它在 Task 4-6 里从「恒真的空断言」变成「真正在守东西的断言」,这正是它必须先落地的原因。Go 允许未使用的函数参数,`go vet` 不会报错。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/leakcheck/ -count=1 && go build ./... && go vet ./... && go test ./... -count=1`
Expected: PASS

- [ ] **Step 5: 变异验证**

`cp internal/leakcheck/judge.go /tmp/judge.go.orig`,逐个改、全量跑 `go test ./... -count=1`、恢复重跑确认回绿。

| 变异 | 必须转红的测试 | 它防住的生产改动 |
|---|---|---|
| `judgeWebRTC` 的 `Verdict: NotChecked` 改成 `OK` | `TestEmptyBrowserReportYieldsNotChecked`、`TestMissingBrowserHalfStillNotChecked` | 「没看到泄漏就是没泄漏」——设计风险四那种最坏失败 |
| 三个 `Summary` 都改成 `""` | `TestEmptyBrowserReportYieldsNotChecked` | not checked 不说为什么,用户无从判断是自己没等够还是被挡了 |
| `Judge` 里把 `judgeDNS(...)` 那一项删掉 | `TestEmptyBrowserReportYieldsNotChecked`、`TestFindingIDsAndOrderAreStable` | 悄悄少一条结论 |
| `Judge` 里三条顺序对调 | `TestFindingIDsAndOrderAreStable` | 输出次序漂移,页面与 CLI 对不上 |
| `NewReport(now, Endpoints(), ...)` 改成 `NewReport(now, EndpointDisclosure{}, ...)` | `TestJudgeCarriesEndpointDisclosure` | 页面显示不出要联系谁(设计风险三的缓解措施失效) |

- [ ] **Step 6: 提交**

```bash
git add internal/leakcheck/input.go internal/leakcheck/judge.go internal/leakcheck/judge_test.go
git commit -m "$(cat <<'EOF'
feat(leakcheck): 输入类型与 Judge 骨架,空上报恒判 not checked

设计风险四单列的那条:页面没跑成必须是「没检查」,绝不是「一切正常」。
本机事实里的谓词字段复用 observe.Tristate——那里它的极性是合身的。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: 规则一 —— WebRTC srflx ≠ HTTP 出口

设计原文:「WebRTC 通过 STUN 拿到 srflx candidate = `1.2.3.4`,而 HTTP 出口显示 = `5.6.7.8` → **WebRTC 绕过了隧道,真泄漏。**」这是整个功能的立身之本那条 —— 网页拿不到本机事实,native app 拿不到浏览器事实,只有两半都在才能下这个结论。

**Files:**
- Modify: `internal/leakcheck/judge.go`(`judgeWebRTC`)
- Test: `internal/leakcheck/judge_webrtc_test.go`

**Interfaces:**
- Consumes: `BrowserReport`、`Finding`、`Verdict`(Task 1、3)
- Produces: 无新导出;`judgeWebRTC` 变成真判据

- [ ] **Step 1: 写失败测试**

创建 `internal/leakcheck/judge_webrtc_test.go`:

```go
package leakcheck

import (
	"strings"
	"testing"
)

func webrtcFinding(t *testing.T, browser BrowserReport, local LocalFacts) Finding {
	t.Helper()
	for _, f := range Judge(fixedTime(), browser, local).Findings {
		if f.ID == FindingWebRTC {
			return f
		}
	}
	t.Fatalf("报告里没有 %s", FindingWebRTC)
	return Finding{}
}

func TestWebRTCRule(t *testing.T) {
	for _, tc := range []struct {
		name        string
		browser     BrowserReport
		want        Verdict
		wantMention string // summary 里必须出现的关键字符串
	}{
		{
			// 核心那条:srflx 与 HTTP 出口不是同一个地址 = WebRTC 走了别的路。
			name:        "srflx 与出口不同即为泄漏",
			browser:     BrowserReport{ExitV4: "5.6.7.8", SRFLX: []string{"1.2.3.4"}},
			want:        Bad,
			wantMention: "1.2.3.4",
		},
		{
			// 多个 srflx,只要有一个对不上就是泄漏 —— 一条泄漏通路就够了。
			name:        "多个 srflx 里有一个对不上",
			browser:     BrowserReport{ExitV4: "5.6.7.8", SRFLX: []string{"5.6.7.8", "1.2.3.4"}},
			want:        Bad,
			wantMention: "1.2.3.4",
		},
		{
			name:        "srflx 与出口一致",
			browser:     BrowserReport{ExitV4: "5.6.7.8", SRFLX: []string{"5.6.7.8"}},
			want:        OK,
			wantMention: "5.6.7.8",
		},
		{
			// STUN 被挡。**这不是 ok。**「没拿到 candidate」不等于「没有泄漏」。
			name:        "STUN 被挡",
			browser:     BrowserReport{ExitV4: "5.6.7.8", STUNErr: "ICE gathering failed"},
			want:        NotChecked,
			wantMention: "ICE gathering failed",
		},
		{
			// 拿到了 srflx,但 HTTP 出口没问出来 —— 另一半缺席,判不了。
			name:        "出口缺席",
			browser:     BrowserReport{SRFLX: []string{"1.2.3.4"}, ExitV4Err: "load failed"},
			want:        NotChecked,
			wantMention: "load failed",
		},
		{
			// 两半都没有。
			name:    "什么都没有",
			browser: BrowserReport{},
			want:    NotChecked,
		},
		{
			// srflx 是空列表但 STUN 没报错(ICE 收集完成、一个 srflx 都没有):
			// 常见于 UDP 被完全阻断。**仍然是 not checked** —— 我们没能观测到
			// WebRTC 的出口,不是观测到它没问题。
			name:    "ICE 完成但零 srflx",
			browser: BrowserReport{ExitV4: "5.6.7.8", SRFLX: nil},
			want:    NotChecked,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := webrtcFinding(t, tc.browser, LocalFacts{})
			if f.Verdict != tc.want {
				t.Fatalf("判定应为 %s,得到 %s(summary=%q)", tc.want, f.Verdict, f.Summary)
			}
			if tc.wantMention != "" && !strings.Contains(f.Summary+strings.Join(f.Evidence, " "), tc.wantMention) {
				t.Errorf("结论必须出示依据 %q,summary=%q evidence=%v", tc.wantMention, f.Summary, f.Evidence)
			}
		})
	}
}

// 每条结论都要能展开看到依据(设计诚实性规则)。判成 bad 时尤其:
// 用户得看得出 bx 是拿哪两个地址做的比较,才可能发现 bx 判错了。
func TestWebRTCBadFindingCarriesBothHalves(t *testing.T) {
	f := webrtcFinding(t, BrowserReport{ExitV4: "5.6.7.8", SRFLX: []string{"1.2.3.4"}}, LocalFacts{})
	joined := strings.Join(f.Evidence, "\n")
	if !strings.Contains(joined, "5.6.7.8") || !strings.Contains(joined, "1.2.3.4") {
		t.Fatalf("bad 结论的 evidence 必须同时含 HTTP 出口与 srflx,得到 %v", f.Evidence)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/leakcheck/ -count=1`
Expected: FAIL — 除「什么都没有」「ICE 完成但零 srflx」两个子用例外全部失败(`judgeWebRTC` 现在恒返回 `NotChecked`)

- [ ] **Step 3: 最小实现**

替换 `internal/leakcheck/judge.go` 里的 `judgeWebRTC`:

```go
func judgeWebRTC(browser BrowserReport, local LocalFacts) Finding {
	f := Finding{ID: FindingWebRTC, Title: "WebRTC vs HTTP exit"}

	// 顺序刻意是「先说没问出来,再说好坏」。任何一半缺席都到不了比较那一步。
	switch {
	case browser.STUNErr != "":
		f.Summary = "WebRTC could not be checked: " + browser.STUNErr
		f.Evidence = append(f.Evidence, "stun: "+STUNURL, "stun error: "+browser.STUNErr)
		return f
	case len(browser.SRFLX) == 0:
		// ICE 跑完了但一个 srflx 都没有(常见于 UDP 被完全阻断)。
		// 「没拿到」不是「没泄漏」。
		f.Summary = "WebRTC could not be checked: no server-reflexive candidate was returned."
		f.Evidence = append(f.Evidence, "stun: "+STUNURL)
		return f
	case browser.ExitV4 == "":
		reason := browser.ExitV4Err
		if reason == "" {
			reason = "the HTTP exit address was not observed"
		}
		f.Summary = "WebRTC could not be compared: " + reason
		f.Evidence = append(f.Evidence, "srflx: "+strings.Join(browser.SRFLX, ", "), "echo v4: "+EchoV4URL)
		return f
	}

	f.Evidence = append(f.Evidence,
		"http exit (v4): "+browser.ExitV4+"  via "+EchoV4URL,
		"webrtc srflx: "+strings.Join(browser.SRFLX, ", ")+"  via "+STUNURL,
	)
	var mismatched []string
	for _, candidate := range browser.SRFLX {
		if candidate != browser.ExitV4 {
			mismatched = append(mismatched, candidate)
		}
	}
	if len(mismatched) > 0 {
		f.Verdict = Bad
		f.Summary = "WebRTC reached the internet from " + strings.Join(mismatched, ", ") +
			", but HTTP traffic left from " + browser.ExitV4 + ". WebRTC is bypassing the tunnel."
		return f
	}
	f.Verdict = OK
	f.Summary = "WebRTC and HTTP both left from " + browser.ExitV4 + "."
	return f
}
```

`judge.go` 顶部的 import 补上 `"strings"`。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/leakcheck/ -count=1 && go build ./... && go vet ./... && go test ./... -count=1`
Expected: PASS —— 特别确认 `TestEmptyBrowserReportYieldsNotChecked`(Task 3)**仍然绿**

- [ ] **Step 5: 变异验证**

`cp internal/leakcheck/judge.go /tmp/judge.go.orig`,逐个改、全量跑、恢复重跑确认回绿。

| 变异 | 必须转红的测试 | 它防住的生产改动 |
|---|---|---|
| 删掉整个 `if len(mismatched) > 0` 分支(直接落到 `OK`) | `TestWebRTCRule/srflx 与出口不同即为泄漏`、`/多个 srflx 里有一个对不上`、`TestWebRTCBadFindingCarriesBothHalves` | 规则被整条删掉 |
| `case len(browser.SRFLX) == 0` 里的 `f.Verdict` 改成 `OK` | `TestWebRTCRule/ICE 完成但零 srflx`、`TestEmptyBrowserReportYieldsNotChecked` | 「一个 candidate 都没有=安全」这个最诱人的错误 |
| `case browser.STUNErr != ""` 整个删掉 | `TestWebRTCRule/STUN 被挡` | STUN 被挡时落进「零 srflx」分支仍是 not checked,**但依据里丢了原因** —— 若此变异不转红,说明 `wantMention` 断言没生效,当场修断言 |
| 比较改成 `if len(mismatched) == len(browser.SRFLX)`(要求**全部**都不一致才报) | `TestWebRTCRule/多个 srflx 里有一个对不上` | 「大部分对得上就算了」——一条泄漏通路就够了 |
| `f.Evidence` 三处 append 全删 | `TestWebRTCBadFindingCarriesBothHalves`、`TestWebRTCRule` 的多个子用例 | 结论不出示依据,用户没有理由信它 |
| `case browser.ExitV4 == ""` 改成 `f.Verdict = OK` | `TestWebRTCRule/出口缺席`、`TestMissingBrowserHalfStillNotChecked` | 半份事实就下结论 |

- [ ] **Step 6: 提交**

```bash
git add internal/leakcheck/judge.go internal/leakcheck/judge_webrtc_test.go
git commit -m "$(cat <<'EOF'
feat(leakcheck): 规则一,srflx 与 HTTP 出口不一致即判 WebRTC 绕过隧道

零 srflx 与 STUN 报错都停在 not checked——「没拿到 candidate」不是「没泄漏」。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: 规则二 —— IPv6 泄漏(两半合起来才判得了)

设计原文:「v4 走 VPN 而 v6 出口是 ISP → v6 泄漏(别的 VPN 最常见的漏洞)」。

**这条规则有一个必须先想清楚的坑,不想清楚就会造出一条会误报的检查。** v6-only 主机名**不保证浏览器真的用了 IPv6**:在 fake-IP DNS(bx 自己,或别的同类 VPN)之下,`ipv6.icanhazip.com` 会被答一个 A 记录,请求实际走 v4 经隧道出去,回声返回的是一个 **IPv4 字面量**。所以判据必须先看**回声返回的地址族**:回来的不是 v6 地址,就说明浏览器没走 v6 —— 那是 `not checked` 里的一格(「问了,但问到的不是 v6」),不是 `ok`,更不是 `bad`。

**Files:**
- Modify: `internal/leakcheck/judge.go`(`judgeIPv6`)
- Test: `internal/leakcheck/judge_ipv6_test.go`

**Interfaces:**
- Consumes: `BrowserReport`、`LocalFacts`、`observe.Tristate`
- Produces: 无新导出;`judgeIPv6` 变成真判据

- [ ] **Step 1: 写失败测试**

创建 `internal/leakcheck/judge_ipv6_test.go`:

```go
package leakcheck

import (
	"strings"
	"testing"

	"github.com/getbx/bx/internal/observe"
)

func ipv6Finding(t *testing.T, browser BrowserReport, local LocalFacts) Finding {
	t.Helper()
	for _, f := range Judge(fixedTime(), browser, local).Findings {
		if f.ID == FindingIPv6 {
			return f
		}
	}
	t.Fatalf("报告里没有 %s", FindingIPv6)
	return Finding{}
}

func TestIPv6Rule(t *testing.T) {
	tunnelV4 := InterfaceRef{Name: "utun4", Display: "Unidentified VPN (utun4)"}
	physV6 := InterfaceRef{Name: "en0", Display: "en0"}

	for _, tc := range []struct {
		name        string
		browser     BrowserReport
		local       LocalFacts
		want        Verdict
		wantMention string
	}{
		{
			// 教科书那条:v4 交给某个隧道接口,而 v6 出口是一个公网 v6 地址、
			// 且 v6 默认路由归的是另一个(物理)接口。**两半都用上了。**
			name:    "v4 走隧道而 v6 从物理口漏出去",
			browser: BrowserReport{ExitV4: "5.6.7.8", ExitV6: "2001:db8:1::1"},
			local: LocalFacts{
				DefaultRouteV4:     tunnelV4,
				DefaultRouteV6:     physV6,
				IPv6DefaultPresent: observe.True,
			},
			want:        Bad,
			wantMention: "2001:db8:1::1",
		},
		{
			// v6 也交给同一个隧道接口:没漏。
			name:    "v6 与 v4 同归一个隧道",
			browser: BrowserReport{ExitV4: "5.6.7.8", ExitV6: "2001:db8:1::1"},
			local: LocalFacts{
				DefaultRouteV4:     tunnelV4,
				DefaultRouteV6:     tunnelV4,
				IPv6DefaultPresent: observe.True,
			},
			want: OK,
		},
		{
			// 这台机器压根没有 v6:既没有 v6 默认路由,回声也失败。
			// 这是一句**诚实的 ok** —— 两半都观测到了,结论是没有 v6 通路。
			name:    "根本没有 IPv6",
			browser: BrowserReport{ExitV4: "5.6.7.8", ExitV6Err: "load failed"},
			local: LocalFacts{
				DefaultRouteV4:     tunnelV4,
				IPv6DefaultPresent: observe.False,
			},
			want:        OK,
			wantMention: "no IPv6",
		},
		{
			// 回声失败,但本机确实**有** v6 默认路由 —— 那就是没问出来,不是没漏。
			name:    "有 v6 默认路由但回声失败",
			browser: BrowserReport{ExitV4: "5.6.7.8", ExitV6Err: "load failed"},
			local: LocalFacts{
				DefaultRouteV4:     tunnelV4,
				DefaultRouteV6:     physV6,
				IPv6DefaultPresent: observe.True,
			},
			want:        NotChecked,
			wantMention: "load failed",
		},
		{
			// **fake-IP 的那个坑**:v6-only 主机名被答了 A 记录,回声返回的是
			// 一个 IPv4 字面量 —— 浏览器根本没走 v6,判不了。
			name:    "v6 回声返回了 IPv4 字面量",
			browser: BrowserReport{ExitV4: "5.6.7.8", ExitV6: "5.6.7.8"},
			local: LocalFacts{
				DefaultRouteV4:     tunnelV4,
				DefaultRouteV6:     physV6,
				IPv6DefaultPresent: observe.True,
			},
			want:        NotChecked,
			wantMention: "not an IPv6 address",
		},
		{
			// 本机 v6 观测不出来(observe.Unknown 是零值)——不许拿它凑结论。
			name:    "本机 v6 事实观测不到",
			browser: BrowserReport{ExitV4: "5.6.7.8", ExitV6Err: "load failed"},
			local:   LocalFacts{DefaultRouteV4: tunnelV4, IPv6DefaultPresent: observe.Unknown},
			want:    NotChecked,
		},
		{
			// v4 那半不知道归谁:「v4 走 VPN 而 v6 是 ISP」的前半句立不住。
			name:    "v4 默认路由未知",
			browser: BrowserReport{ExitV6: "2001:db8:1::1"},
			local:   LocalFacts{DefaultRouteV6: physV6, IPv6DefaultPresent: observe.True},
			want:    NotChecked,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := ipv6Finding(t, tc.browser, tc.local)
			if f.Verdict != tc.want {
				t.Fatalf("判定应为 %s,得到 %s(summary=%q)", tc.want, f.Verdict, f.Summary)
			}
			if tc.wantMention != "" && !strings.Contains(f.Summary+strings.Join(f.Evidence, " "), tc.wantMention) {
				t.Errorf("结论必须出示依据 %q,summary=%q evidence=%v", tc.wantMention, f.Summary, f.Evidence)
			}
		})
	}
}

// 判成 bad 时,两半的依据都要在:v6 出口地址(浏览器那半)与两条默认路由的
// 归属(本机那半)。少任何一半,用户就看不出这个结论是怎么来的。
func TestIPv6BadFindingCarriesBothHalves(t *testing.T) {
	f := ipv6Finding(t,
		BrowserReport{ExitV4: "5.6.7.8", ExitV6: "2001:db8:1::1"},
		LocalFacts{
			DefaultRouteV4:     InterfaceRef{Name: "utun4", Display: "Unidentified VPN (utun4)"},
			DefaultRouteV6:     InterfaceRef{Name: "en0", Display: "en0"},
			IPv6DefaultPresent: observe.True,
		})
	joined := strings.Join(f.Evidence, "\n")
	for _, want := range []string{"2001:db8:1::1", "utun4", "en0"} {
		if !strings.Contains(joined, want) {
			t.Errorf("bad 结论的 evidence 缺 %q,得到 %v", want, f.Evidence)
		}
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/leakcheck/ -count=1`
Expected: FAIL — `TestIPv6Rule` 的多个子用例(凡是期望 `Bad` / `OK` 的)与 `TestIPv6BadFindingCarriesBothHalves`

- [ ] **Step 3: 最小实现**

替换 `judgeIPv6`,并在 `judge.go` 顶部补 import `"net"` 与 `"github.com/getbx/bx/internal/observe"`:

```go
func judgeIPv6(browser BrowserReport, local LocalFacts) Finding {
	f := Finding{ID: FindingIPv6, Title: "IPv6 exposure"}

	// 「v4 走 VPN 而 v6 是 ISP」的前半句:v4 归谁必须先知道。
	if !local.DefaultRouteV4.Known() {
		f.Summary = "IPv6 could not be judged: the local IPv4 default route was not observed."
		return f
	}
	f.Evidence = append(f.Evidence, "default route (v4): "+describeRef(local.DefaultRouteV4))

	if browser.ExitV6 == "" {
		// 回声没答上来。这时**本机那一半**决定它是 ok 还是 not checked:
		// 确知没有 v6 默认路由 = 确实没有 v6 通路(诚实的 ok);
		// 有 v6 默认路由、或本机 v6 事实压根没问出来 = 没检查。
		reason := browser.ExitV6Err
		if reason == "" {
			reason = "the IPv6 echo returned nothing"
		}
		switch local.IPv6DefaultPresent {
		case observe.False:
			f.Verdict = OK
			f.Summary = "This machine has no IPv6 default route and no IPv6 exit was observed, " +
				"so there is no IPv6 path to leak through."
			f.Evidence = append(f.Evidence, "ipv6 default route: none", "echo v6: "+reason)
		default:
			f.Summary = "IPv6 could not be checked: " + reason
			f.Evidence = append(f.Evidence,
				"ipv6 default route: "+describeRef(local.DefaultRouteV6),
				"echo v6: "+EchoV6URL)
		}
		return f
	}

	// **fake-IP 的坑**:v6-only 主机名可能被答成 A 记录,回声于是返回一个 v4
	// 字面量。那说明浏览器没走 IPv6,判不了 —— 不是 ok,也不是 bad。
	ip := net.ParseIP(browser.ExitV6)
	if ip == nil || ip.To4() != nil {
		f.Summary = "IPv6 could not be checked: the IPv6 echo answered with " +
			browser.ExitV6 + ", which is not an IPv6 address — the browser did not use IPv6."
		f.Evidence = append(f.Evidence, "echo v6 answered: "+browser.ExitV6+"  via "+EchoV6URL)
		return f
	}
	f.Evidence = append(f.Evidence,
		"ipv6 exit: "+browser.ExitV6+"  via "+EchoV6URL,
		"default route (v6): "+describeRef(local.DefaultRouteV6))

	if local.DefaultRouteV6.Known() && local.DefaultRouteV6.Name == local.DefaultRouteV4.Name {
		f.Verdict = OK
		f.Summary = "IPv6 leaves through the same interface as IPv4 (" +
			describeRef(local.DefaultRouteV4) + ")."
		return f
	}
	if !local.DefaultRouteV6.Known() {
		f.Summary = "A public IPv6 exit was observed (" + browser.ExitV6 +
			") but the local IPv6 default route was not observed, so it cannot be attributed."
		return f
	}
	f.Verdict = Bad
	f.Summary = "IPv4 leaves through " + describeRef(local.DefaultRouteV4) +
		" but IPv6 reached the internet as " + browser.ExitV6 + " through " +
		describeRef(local.DefaultRouteV6) + ". IPv6 is bypassing the tunnel."
	return f
}

// describeRef 渲染一个接口:能翻成人话就用人话,翻不出就说翻不出。
// 详见 Task 7 的 DescribeInterface —— 这里只负责挑 Display 还是 Name。
func describeRef(ref InterfaceRef) string {
	switch {
	case ref.Display != "":
		return ref.Display
	case ref.Name != "":
		return ref.Name
	case ref.Err != "":
		return "not observed (" + ref.Err + ")"
	default:
		return "not observed"
	}
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/leakcheck/ -count=1 && go build ./... && go vet ./... && go test ./... -count=1`
Expected: PASS,且 Task 3、4 的测试全部仍绿

- [ ] **Step 5: 变异验证**

| 变异 | 必须转红的测试 | 它防住的生产改动 |
|---|---|---|
| 删掉 `ip == nil \|\| ip.To4() != nil` 那段地址族检查 | `TestIPv6Rule/v6 回声返回了 IPv4 字面量` | fake-IP 之下把一台没走 v6 的机器判成 v6 泄漏(误报) |
| `switch local.IPv6DefaultPresent` 的 `default` 分支改成 `f.Verdict = OK` | `TestIPv6Rule/有 v6 默认路由但回声失败`、`/本机 v6 事实观测不到`、`TestEmptyBrowserReportYieldsNotChecked` | 拿 Unknown 当 False 用,把「没问出来」写成「没有 v6」 |
| `case observe.False` 改成 `case observe.False, observe.Unknown` | `TestIPv6Rule/本机 v6 事实观测不到` | 同上,换一个写法 |
| 最后的 `f.Verdict = Bad` 改成 `OK` | `TestIPv6Rule/v4 走隧道而 v6 从物理口漏出去`、`TestIPv6BadFindingCarriesBothHalves` | 规则被反转 |
| 接口比较 `Name == Name` 改成 `Display == Display` | 不一定转红 → **必须补一条测试**:两个不同接口恰好有相同 Display(比如都翻不出、都是 `""`)时不得判 ok。写完再跑 | 拿一个可能为空的展示名做同一性判断 |
| `if !local.DefaultRouteV4.Known()` 整个删掉 | `TestIPv6Rule/v4 默认路由未知` | 前半句立不住也照下结论 |
| `describeRef` 的 `default` 改成返回 `"ok"` | `TestIPv6Rule` 多个子用例的 `wantMention` | 把「没观测到」渲染成一个好听的词 |

> 第五行是**发现缺口的变异**:如果它不转红,当场补测试再继续 —— 这正是变异验证的用途,不是走过场。

- [ ] **Step 6: 提交**

```bash
git add internal/leakcheck/judge.go internal/leakcheck/judge_ipv6_test.go
git commit -m "$(cat <<'EOF'
feat(leakcheck): 规则二,v4 走隧道而 v6 从别的口出去即判 IPv6 泄漏

先看回声返回的地址族:fake-IP 之下 v6-only 主机名会被答 A 记录、回来一个
v4 字面量,那是「浏览器没走 v6」,判 not checked 而不是泄漏。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: 规则三 —— DNS 可能绕过默认路由

设计原文:「默认路由归 X,而 DNS 解析器在 X 之外 → DNS 可能绕过」。判据全在本机那一半:逐个 DNS 解析器地址查它的**出口接口**,与 v4 默认路由的接口比。注意措辞是「**可能**绕过」——这是一条谨慎的结论,summary 必须照此写,别说成确定的泄漏。

**Files:**
- Modify: `internal/leakcheck/judge.go`(`judgeDNS`)
- Test: `internal/leakcheck/judge_dns_test.go`

- [ ] **Step 1: 写失败测试**

创建 `internal/leakcheck/judge_dns_test.go`:

```go
package leakcheck

import (
	"strings"
	"testing"
)

func dnsFinding(t *testing.T, local LocalFacts) Finding {
	t.Helper()
	for _, f := range Judge(fixedTime(), BrowserReport{}, local).Findings {
		if f.ID == FindingDNS {
			return f
		}
	}
	t.Fatalf("报告里没有 %s", FindingDNS)
	return Finding{}
}

func TestDNSRule(t *testing.T) {
	tunnel := InterfaceRef{Name: "utun4", Display: "Unidentified VPN (utun4)"}

	for _, tc := range []struct {
		name        string
		local       LocalFacts
		want        Verdict
		wantMention string
	}{
		{
			// 解析器的包从 en0 出去,而默认路由归 utun4:DNS 可能绕过隧道。
			name: "解析器走在默认路由之外",
			local: LocalFacts{
				DefaultRouteV4:  tunnel,
				DNSServers:      []string{"192.0.2.53"},
				DNSServerEgress: map[string]string{"192.0.2.53": "en0"},
			},
			want:        Bad,
			wantMention: "192.0.2.53",
		},
		{
			name: "解析器与默认路由同一个接口",
			local: LocalFacts{
				DefaultRouteV4:  tunnel,
				DNSServers:      []string{"192.0.2.53"},
				DNSServerEgress: map[string]string{"192.0.2.53": "utun4"},
			},
			want: OK,
		},
		{
			// bx 自己接管 DNS 时解析器就是 127.0.0.1,它当然走 lo0。
			// 这不是绕过,是设计如此 —— 恒把 loopback 判成 bad 会让每台开着
			// bx 的机器常年红灯。
			name: "解析器是本机 loopback",
			local: LocalFacts{
				DefaultRouteV4:  tunnel,
				DNSServers:      []string{"127.0.0.1"},
				DNSServerEgress: map[string]string{"127.0.0.1": "lo0"},
			},
			want:        OK,
			wantMention: "127.0.0.1",
		},
		{
			// 多个解析器,只要有一个在外面就要报 —— 一条泄漏通路就够了。
			name: "多个解析器里有一个在外面",
			local: LocalFacts{
				DefaultRouteV4: tunnel,
				DNSServers:     []string{"127.0.0.1", "192.0.2.53"},
				DNSServerEgress: map[string]string{
					"127.0.0.1":  "lo0",
					"192.0.2.53": "en0",
				},
			},
			want:        Bad,
			wantMention: "192.0.2.53",
		},
		{
			name:  "DNS 采集失败",
			local: LocalFacts{DefaultRouteV4: tunnel, DNSErr: "networksetup failed"},
			want:  NotChecked, wantMention: "networksetup failed",
		},
		{
			// 有解析器,但它的出口接口没查出来:少一个键 = 没问出来。
			name: "解析器出口未知",
			local: LocalFacts{
				DefaultRouteV4:  tunnel,
				DNSServers:      []string{"192.0.2.53"},
				DNSServerEgress: nil,
			},
			want: NotChecked,
		},
		{
			name:  "默认路由未知",
			local: LocalFacts{DNSServers: []string{"192.0.2.53"}, DNSServerEgress: map[string]string{"192.0.2.53": "en0"}},
			want:  NotChecked,
		},
		{
			// 一个解析器都没有:系统没配。这是没问出来,不是没问题。
			name:  "没有解析器",
			local: LocalFacts{DefaultRouteV4: tunnel},
			want:  NotChecked,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := dnsFinding(t, tc.local)
			if f.Verdict != tc.want {
				t.Fatalf("判定应为 %s,得到 %s(summary=%q)", tc.want, f.Verdict, f.Summary)
			}
			if tc.wantMention != "" && !strings.Contains(f.Summary+strings.Join(f.Evidence, " "), tc.wantMention) {
				t.Errorf("结论必须出示依据 %q,summary=%q evidence=%v", tc.wantMention, f.Summary, f.Evidence)
			}
		})
	}
}

// 这条结论是「**可能**绕过」,不是确定的泄漏。措辞必须谨慎:一条把可能说成
// 确定的结论,会让用户去追一个不存在的问题,然后学会不信这个界面。
func TestDNSBadFindingIsWordedAsPossibility(t *testing.T) {
	f := dnsFinding(t, LocalFacts{
		DefaultRouteV4:  InterfaceRef{Name: "utun4", Display: "Unidentified VPN (utun4)"},
		DNSServers:      []string{"192.0.2.53"},
		DNSServerEgress: map[string]string{"192.0.2.53": "en0"},
	})
	if !strings.Contains(strings.ToLower(f.Summary), "may bypass") {
		t.Fatalf("DNS 的 bad 结论必须写成「可能绕过」,得到 %q", f.Summary)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/leakcheck/ -count=1`
Expected: FAIL — 期望 `Bad` / `OK` 的子用例全部失败,`TestDNSBadFindingIsWordedAsPossibility` 失败

- [ ] **Step 3: 最小实现**

替换 `judgeDNS`:

```go
func judgeDNS(browser BrowserReport, local LocalFacts) Finding {
	f := Finding{ID: FindingDNS, Title: "DNS path"}

	if local.DNSErr != "" {
		f.Summary = "DNS could not be checked: " + local.DNSErr
		f.Evidence = append(f.Evidence, "dns error: "+local.DNSErr)
		return f
	}
	if len(local.DNSServers) == 0 {
		f.Summary = "DNS could not be checked: no resolver was observed on this machine."
		return f
	}
	if !local.DefaultRouteV4.Known() {
		f.Summary = "DNS could not be judged: the local IPv4 default route was not observed, " +
			"so there is nothing to compare the resolvers against."
		f.Evidence = append(f.Evidence, "resolvers: "+strings.Join(local.DNSServers, ", "))
		return f
	}

	route := local.DefaultRouteV4
	f.Evidence = append(f.Evidence, "default route (v4): "+describeRef(route))

	var outside, unknown []string
	for _, server := range local.DNSServers {
		egress, ok := local.DNSServerEgress[server]
		if !ok || egress == "" {
			unknown = append(unknown, server)
			f.Evidence = append(f.Evidence, "resolver "+server+": egress not observed")
			continue
		}
		f.Evidence = append(f.Evidence, "resolver "+server+": via "+egress)
		if egress == route.Name {
			continue
		}
		// 本机 loopback 上的解析器(bx 自己接管 DNS 时就是这个形状)不算绕过:
		// 它根本没离开这台机器,真正出网的是 bx,而 bx 就在默认路由上。
		if isLoopbackResolver(server) {
			continue
		}
		outside = append(outside, server)
	}

	switch {
	case len(outside) > 0:
		f.Verdict = Bad
		f.Summary = "Resolver(s) " + strings.Join(outside, ", ") +
			" are reached outside " + describeRef(route) +
			", which owns the default route. DNS queries may bypass the tunnel."
	case len(unknown) > 0:
		f.Summary = "DNS could not be fully checked: the egress interface of " +
			strings.Join(unknown, ", ") + " was not observed."
	default:
		f.Verdict = OK
		f.Summary = "All resolvers (" + strings.Join(local.DNSServers, ", ") +
			") are reached through " + describeRef(route) + " or the local machine."
	}
	return f
}

// isLoopbackResolver 判断解析器是不是本机。net.ParseIP 认不出来时返回 false ——
// 「看不懂的地址」不能当成安全的那一边。
func isLoopbackResolver(server string) bool {
	ip := net.ParseIP(strings.TrimSpace(server))
	return ip != nil && ip.IsLoopback()
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/leakcheck/ -count=1 && go build ./... && go vet ./... && go test ./... -count=1`
Expected: PASS,Task 3-5 的测试全部仍绿

- [ ] **Step 5: 变异验证**

| 变异 | 必须转红的测试 | 它防住的生产改动 |
|---|---|---|
| `case len(outside) > 0` 分支删掉(落到 `default` 判 OK) | `TestDNSRule/解析器走在默认路由之外`、`/多个解析器里有一个在外面`、`TestDNSBadFindingIsWordedAsPossibility` | 规则被整条删掉 |
| `case len(unknown) > 0` 删掉 | `TestDNSRule/解析器出口未知` | 查不出出口的解析器被静默当成没问题 |
| `isLoopbackResolver` 改成恒 `true` | `TestDNSRule/解析器走在默认路由之外`、`/多个解析器里有一个在外面` | 「反正大多是 127.0.0.1」把整条规则短路掉 |
| `isLoopbackResolver` 改成恒 `false` | `TestDNSRule/解析器是本机 loopback` | 每台开着 bx 的机器常年红灯(设计风险二的反面:恒红同样毁掉信任) |
| `ip != nil && ip.IsLoopback()` 改成 `ip == nil \|\| ip.IsLoopback()` | `TestDNSRule/解析器走在默认路由之外` | 认不出的地址被当成安全的那一边 |
| `len(local.DNSServers) == 0` 分支的 `f.Verdict` 改成 `OK` | `TestDNSRule/没有解析器`、`TestEmptyBrowserReportYieldsNotChecked` | 「一个解析器都没有」被读成「没问题」 |
| summary 里的 `"may bypass"` 改成 `"bypasses"` | `TestDNSBadFindingIsWordedAsPossibility` | 把可能说成确定 |

- [ ] **Step 6: 提交**

```bash
git add internal/leakcheck/judge.go internal/leakcheck/judge_dns_test.go
git commit -m "$(cat <<'EOF'
feat(leakcheck): 规则三,解析器出口不在默认路由上即报「DNS 可能绕过」

loopback 上的解析器不算绕过(bx 接管 DNS 时就是这个形状),否则每台开着
bx 的机器都会常年红灯;认不出的地址不当成安全的那一边。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: 把 `utun4` 翻成人话 + 证据清单

设计诚实性规则:「`utun4` 必须翻成人话,翻不出就说翻不出……认不出时显示**「无法识别的 VPN(utun4)」**——**「无法识别」是一个合法答案,猜不是。**」

**语言的决定(设计没写,这里定死)**:页面与 CLI 输出**一律英文**。理由:菜单入口项是英文(`Check for leaks ↗`),页面是它的延伸;`MenuRows.swift` 已有的 `Not checked` 也是英文,三处混语会让同一个概念长出两个名字。所以那句话落地成 `Unidentified VPN (utun4)`。

**Files:**
- Create: `internal/leakcheck/describe.go`
- Modify: `internal/leakcheck/judge.go`(`collectEvidence`)
- Test: `internal/leakcheck/describe_test.go`

**Interfaces:**
- Produces: `leakcheck.DescribeInterface(name string, services []VPNService, bxTun string) string`、`leakcheck.VPNService{Name string, Connected bool}`

- [ ] **Step 1: 写失败测试**

创建 `internal/leakcheck/describe_test.go`:

```go
package leakcheck

import (
	"strings"
	"testing"
)

func TestDescribeInterface(t *testing.T) {
	for _, tc := range []struct {
		name     string
		iface    string
		services []VPNService
		bxTun    string
		want     string
	}{
		{
			name: "bx 自己的 TUN 认得出来",
			iface: "utun11", bxTun: "utun11",
			want: "bx (utun11)",
		},
		{
			// **唯一一个**已连接的系统集成 VPN,而接口是 utun:可以归因。
			name:  "唯一一个已连接的系统 VPN",
			iface: "utun4",
			services: []VPNService{
				{Name: "Work VPN", Connected: true},
				{Name: "Old VPN", Connected: false},
			},
			want: "Work VPN (utun4)",
		},
		{
			// 两个都连着 —— 哪个占着 utun4 无从判断。**猜不是答案。**
			name:  "两个已连接就不许猜",
			iface: "utun4",
			services: []VPNService{
				{Name: "Work VPN", Connected: true},
				{Name: "Home VPN", Connected: true},
			},
			want: "Unidentified VPN (utun4)",
		},
		{
			name:  "utun 但一个系统 VPN 都没连",
			iface: "utun4",
			want:  "Unidentified VPN (utun4)",
		},
		{
			// 物理网卡不是 VPN。把 en0 说成「无法识别的 VPN」是另一种撒谎。
			name:  "物理网卡",
			iface: "en0",
			services: []VPNService{{Name: "Work VPN", Connected: true}},
			want: "en0",
		},
		{
			name:  "接口名为空",
			iface: "",
			want:  "not observed",
		},
		{
			// bxTun 匹配优先于系统 VPN 归因:bx 自己那条是确知的。
			name:  "bx TUN 优先",
			iface: "utun11", bxTun: "utun11",
			services: []VPNService{{Name: "Work VPN", Connected: true}},
			want: "bx (utun11)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DescribeInterface(tc.iface, tc.services, tc.bxTun); got != tc.want {
				t.Fatalf("DescribeInterface(%q, %+v, %q) = %q,want %q",
					tc.iface, tc.services, tc.bxTun, got, tc.want)
			}
		})
	}
}

// 证据清单必须把两半的原始事实都列出来 —— 包括那些**不冒充结论**的项
// (出口地址、默认路由归属、User-Agent)。它们是「小白看结论、老鸟展开」的落点。
func TestEvidenceListsBothHalves(t *testing.T) {
	rep := Judge(fixedTime(),
		BrowserReport{
			UserAgent: "TestBrowser/1.0",
			ExitV4:    "5.6.7.8",
			ExitV6:    "2001:db8:1::1",
			SRFLX:     []string{"5.6.7.8"},
		},
		LocalFacts{
			DefaultRouteV4: InterfaceRef{Name: "utun4", Display: "Work VPN (utun4)"},
			DefaultRouteV6: InterfaceRef{Name: "utun4", Display: "Work VPN (utun4)"},
			DNSServers:     []string{"127.0.0.1"},
			BXProtection:   "off",
		})
	joined := strings.Join(rep.Evidence, "\n")
	for _, want := range []string{
		"5.6.7.8",          // 浏览器那半:v4 出口
		"2001:db8:1::1",    // 浏览器那半:v6 出口
		"Work VPN (utun4)", // 本机那半:默认路由归谁,翻成人话
		"127.0.0.1",        // 本机那半:解析器
		"TestBrowser/1.0",  // 谁在报告
		"off",              // bx 自己怎么看自己
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("证据清单缺 %q,得到:\n%s", want, joined)
		}
	}
}

// 什么都没观测到时,证据清单不能编:每一项都要如实说「没观测到」。
func TestEvidenceSaysNotObservedWhenBlind(t *testing.T) {
	rep := Judge(fixedTime(), BrowserReport{}, LocalFacts{})
	joined := strings.Join(rep.Evidence, "\n")
	if joined == "" {
		t.Fatal("即使什么都没观测到,证据清单也要在场并如实说明,不能是空的")
	}
	if strings.Contains(joined, "5.6.7.8") {
		t.Fatal("证据清单里出现了不存在的观测值")
	}
	if !strings.Contains(joined, "not observed") {
		t.Fatalf("全盲时证据清单必须逐项说 not observed,得到:\n%s", joined)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/leakcheck/ -count=1`
Expected: FAIL — `undefined: DescribeInterface` / `undefined: VPNService`;`TestEvidenceListsBothHalves` 因 `collectEvidence` 返回 nil 而失败

- [ ] **Step 3: 最小实现**

创建 `internal/leakcheck/describe.go`:

```go
package leakcheck

import "strings"

// VPNService 是系统集成 VPN 的一条(来自 `scutil --nc list`)。
//
// 刻意**不含接口名**:scutil 给的是服务显示名与状态,它不告诉你哪条服务占着
// utun4。这个类型如实反映那个限制,归因规则见 DescribeInterface。
type VPNService struct {
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
}

// DescribeInterface 把 `utun4` 翻成人话。**翻不出就说翻不出。**
//
// 归因只在**唯一确定**时发生:
//   - 接口就是 bx 自己的 TUN(Guardian 报的)→ 确知,直接说 bx;
//   - 接口是 utun 且**恰好只有一条**已连接的系统 VPN → 那条就是它;
//   - 其余一切 utun → "Unidentified VPN (utunN)"。
//
// **两条都连着时不许猜。** scutil 不给服务到接口的映射,挑一条填上去就是编造,
// 而一个编造出来的 VPN 名字比 "Unidentified" 有害得多:用户会照着它去关一个
// 根本没在占路由的 VPN。设计原文:「无法识别」是一个合法答案,猜不是。
//
// 非 utun 的接口(en0、bridge0……)原样返回:把物理网卡说成「无法识别的 VPN」
// 是另一种撒谎。用户态工具(Tailscale / WireGuard / Mullvad)按进程识别是下一期
// 的事;本期它们落在 "Unidentified VPN",那是诚实的。
func DescribeInterface(name string, services []VPNService, bxTun string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "not observed"
	}
	if bxTun != "" && name == bxTun {
		return "bx (" + name + ")"
	}
	if !strings.HasPrefix(name, "utun") {
		return name
	}
	var connected []string
	for _, service := range services {
		if service.Connected && strings.TrimSpace(service.Name) != "" {
			connected = append(connected, service.Name)
		}
	}
	if len(connected) == 1 {
		return connected[0] + " (" + name + ")"
	}
	return "Unidentified VPN (" + name + ")"
}
```

替换 `judge.go` 里的 `collectEvidence`:

```go
// collectEvidence 列出这一轮采到的**原始事实**,两半都在。
//
// 「谁占着默认路由」「公网出口是什么」放在这里而**不是**作为结论行,是因为它们
// 在真机上永远判不出 bad —— 一条恒绿的检查会把整个界面训练成装饰(设计风险二)。
// 放进证据里,用户照样看得到,只是不冒充结论。
func collectEvidence(browser BrowserReport, local LocalFacts) []string {
	value := func(observed, err string) string {
		switch {
		case observed != "":
			return observed
		case err != "":
			return "not observed (" + err + ")"
		default:
			return "not observed"
		}
	}
	evidence := []string{
		"http exit (v4): " + value(browser.ExitV4, browser.ExitV4Err) + "  via " + EchoV4URL,
		"http exit (v6): " + value(browser.ExitV6, browser.ExitV6Err) + "  via " + EchoV6URL,
		"webrtc srflx: " + value(strings.Join(browser.SRFLX, ", "), browser.STUNErr) + "  via " + STUNURL,
		"default route (v4): " + describeRef(local.DefaultRouteV4),
		"default route (v6): " + describeRef(local.DefaultRouteV6),
		"resolvers: " + value(strings.Join(local.DNSServers, ", "), local.DNSErr),
		"browser: " + value(browser.UserAgent, ""),
		"bx protection: " + value(local.BXProtection, ""),
	}
	return evidence
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/leakcheck/ -count=1 && go build ./... && go vet ./... && go test ./... -count=1`
Expected: PASS,Task 1-6 全部仍绿

- [ ] **Step 5: 变异验证**

| 变异 | 必须转红的测试 | 它防住的生产改动 |
|---|---|---|
| `len(connected) == 1` 改成 `len(connected) >= 1`(取第一条) | `TestDescribeInterface/两个已连接就不许猜` | 编造归因,用户照着去关一个没在占路由的 VPN |
| `!strings.HasPrefix(name, "utun")` 那段删掉 | `TestDescribeInterface/物理网卡` | 把 en0 说成「无法识别的 VPN」 |
| `bxTun != "" && name == bxTun` 那段删掉 | `TestDescribeInterface/bx 自己的 TUN 认得出来`、`/bx TUN 优先` | bx 自己的接口也说成「无法识别」 |
| `"Unidentified VPN ("` 改成 `"VPN ("` | `TestDescribeInterface` 三个子用例 | 把「不知道是哪个」写成「就是个 VPN」,读起来像已经识别了 |
| `collectEvidence` 直接 `return nil` | `TestEvidenceListsBothHalves`、`TestEvidenceSaysNotObservedWhenBlind` | 结论不出示依据 |
| `value` 的 `default` 改成返回 `""` | `TestEvidenceSaysNotObservedWhenBlind` | 空白被读成「没这回事」,而不是「没问过」 |
| `collectEvidence` 里 `"default route (v6): "` 那行删掉 | `TestEvidenceListsBothHalves`(若不红则**当场补断言**) | 证据静默少一项 |

- [ ] **Step 6: 提交**

```bash
git add internal/leakcheck/describe.go internal/leakcheck/judge.go internal/leakcheck/describe_test.go
git commit -m "$(cat <<'EOF'
feat(leakcheck): utun4 翻成人话,翻不出就说 Unidentified VPN

只在唯一确定时归因:scutil 不给服务到接口的映射,两条都连着时挑一条填上去
就是编造。出口地址与默认路由归属放进证据清单而不是结论行——它们判不出 bad,
恒绿的检查比没有更糟。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 8: 一次性服务(骨架)+ 约束二:每个请求都校验 token

> **Task 8-11 是设计里那张「自我约束」表的四行,一行一个任务、一行一条测试。** 设计原文:「这个功能自己就是一个新的攻击面……四道约束是硬要求,不是加分项,每一道都要有自己的测试。」把它们塞进一个任务就等于让一次复审同时判四件事,而这正是本仓库守卫被绕过八次的形状。

**「单次 token」的落地定义(设计文档在这里有歧义,这里定死)**:token 是**每次运行生成一个**、随机 ≥256 bit、**每个请求都校验**、常数时间比较。它**不是**「用一次就作废」——页面至少要发两个请求(GET 页面 + POST 上报),用一次作废会让上报永远打不进来。「单次」指的是**这一次运行专属**,进程退出即消失、不落盘、不复用。

**Files:**
- Create: `internal/leakserve/server.go`
- Test: `internal/leakserve/server_test.go`

**Interfaces:**
- Consumes: `leakcheck.BrowserReport`、`leakcheck.Report`、`leakcheck.Judge`
- Produces:
  - `leakserve.Options{Judge func(leakcheck.BrowserReport) leakcheck.Report; HardTimeout time.Duration}`
  - `leakserve.Listen(opts Options) (*Server, error)`
  - `(*Server).URL() string`(带 token 的完整入口)、`(*Server).Addr() net.Addr`、`(*Server).Serve()`、`(*Server).Wait(ctx context.Context) leakcheck.Report`、`(*Server).Close() error`
  - `leakserve.DefaultHardTimeout = 2 * time.Minute`

- [ ] **Step 1: 写失败测试**

创建 `internal/leakserve/server_test.go`:

```go
package leakserve

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/getbx/bx/internal/leakcheck"
)

// newTestServer 起一个真实的 loopback 服务。**不用 httptest**:约束一要断言的
// 正是「绑在哪」,而 httptest 自己决定绑哪,那就把被测的东西替换掉了。
func newTestServer(t *testing.T) *Server {
	t.Helper()
	srv, err := Listen(Options{
		Judge: func(b leakcheck.BrowserReport) leakcheck.Report {
			return leakcheck.Judge(time.Unix(0, 0).UTC(), b, leakcheck.LocalFacts{})
		},
		HardTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	go srv.Serve()
	return srv
}

// get 按浏览器的样子发一个顶层导航请求(Host 正确、无 Origin —— 浏览器导航
// 本来就不带 Origin)。
func get(t *testing.T, srv *Server, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+srv.Addr().String()+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestTokenIsRequiredOnEveryRequest(t *testing.T) {
	srv := newTestServer(t)

	// 无 token
	if resp := get(t, srv, "/"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("无 token 的 GET / 必须被拒,得到 %d", resp.StatusCode)
	}
	// 错 token
	if resp := get(t, srv, "/?t=wrong"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("错 token 的 GET / 必须被拒,得到 %d", resp.StatusCode)
	}
	// 对 token
	resp := get(t, srv, "/?t="+srv.Token())
	if resp.StatusCode != http.StatusOK {
		t.Errorf("对 token 的 GET / 应放行,得到 %d", resp.StatusCode)
	}
	// **同一个 token 必须还能再用一次**:页面至少要发两个请求(取页面 + 上报)。
	// 「单次」是「本次运行专属」,不是「用一次作废」。
	if resp := get(t, srv, "/?t="+srv.Token()); resp.StatusCode != http.StatusOK {
		t.Errorf("同一 token 的第二个请求也应放行(GET 页面 + POST 上报),得到 %d", resp.StatusCode)
	}

	// 上报端点同样校验。
	body := strings.NewReader(`{"exit_v4":"5.6.7.8"}`)
	req, err := http.NewRequest(http.MethodPost, "http://"+srv.Addr().String()+"/report", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", srv.Origin())
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Errorf("无 token 的 POST /report 必须被拒,得到 %d", resp2.StatusCode)
	}
}

// token 必须够长、够随机:两次运行不能撞。**长度是硬要求** —— 一个 8 位 token
// 在本机是可枚举的,而它挡的正是「同机其它进程顺手读到这台机器的网络姿态」。
func TestTokenIsLongAndUnique(t *testing.T) {
	a := newTestServer(t)
	b := newTestServer(t)
	if a.Token() == b.Token() {
		t.Fatal("两次运行的 token 撞了:token 必须是每次运行现生成的随机值")
	}
	if len(a.Token()) < 32 {
		t.Fatalf("token 太短(%d 字符):本机可枚举,等于没有", len(a.Token()))
	}
}

// URL() 必须自带 token,否则调用方会自己拼一个,而拼错了不会有任何报错 ——
// 只会得到一个打不开的页面。
func TestURLCarriesToken(t *testing.T) {
	srv := newTestServer(t)
	u, err := url.Parse(srv.URL())
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query().Get("t"); got != srv.Token() {
		t.Fatalf("URL() 里的 token 是 %q,应为 %q", got, srv.Token())
	}
	if u.Path != "/" {
		t.Fatalf("URL() 应指向 /,得到 %q", u.Path)
	}
}

// 除本次检测所需之外不提供任何端点(设计:最小面)。
func TestOnlyTwoEndpointsExist(t *testing.T) {
	srv := newTestServer(t)
	for _, path := range []string{"/debug/pprof/", "/status", "/facts", "/index.html"} {
		resp := get(t, srv, path+"?t="+srv.Token())
		if resp.StatusCode != http.StatusNotFound {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
			t.Errorf("%s 应当是 404(只提供 / 与 /report),得到 %d: %s", path, resp.StatusCode, body)
		}
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/leakserve/ -count=1`
Expected: FAIL — 包不存在 / `undefined: Listen`

- [ ] **Step 3: 最小实现**

创建 `internal/leakserve/server.go`:

```go
// Package leakserve 起一次性的本机检测服务:127.0.0.1 上一个随机端口、
// 一个随机 token、拿到结果就关。
//
// **它必须比它检测的东西更干净。** 一个「报告本机网络姿态」的 loopback 服务,
// 做砸了就是让任意网页读到用户的网络指纹。四道约束(只绑 loopback+随机端口 /
// 每请求校验 token / 校验 Origin 与 Host 挡 DNS rebinding / 一次性+硬超时)
// 是硬要求,每一道都有自己的测试,删任何一道都必须有测试转红。
package leakserve

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/getbx/bx/internal/leakcheck"
)

// DefaultHardTimeout 是这个服务活着的绝对上限。用户关掉标签页就走人是常态,
// 没有它就会留一个开着的口。
const DefaultHardTimeout = 2 * time.Minute

// maxReportBytes 限制上报体大小。页面上报是几百字节量级。
const maxReportBytes = 1 << 20

type Options struct {
	// Judge 把浏览器上报变成成品结论。注入而不是直接调 leakcheck.Judge,
	// 是因为本机事实那一半由调用方采集(平台相关),而本包不该知道怎么采。
	Judge func(leakcheck.BrowserReport) leakcheck.Report
	// HardTimeout 为零时用 DefaultHardTimeout。
	HardTimeout time.Duration
}

type Server struct {
	listener    net.Listener
	http        *http.Server
	token       string
	hostPort    string
	judge       func(leakcheck.BrowserReport) leakcheck.Report
	hardTimeout time.Duration

	reports  chan leakcheck.Report
	closeOnce sync.Once
}

// Listen 绑定 127.0.0.1 上一个随机端口(约束一,见 Task 9)。
func Listen(opts Options) (*Server, error) {
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	timeout := opts.HardTimeout
	if timeout <= 0 {
		timeout = DefaultHardTimeout
	}
	srv := &Server{
		listener:    listener,
		token:       token,
		hostPort:    listener.Addr().String(),
		judge:       opts.Judge,
		hardTimeout: timeout,
		reports:     make(chan leakcheck.Report, 1),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", srv.handlePage)
	mux.HandleFunc("/report", srv.handleReport)
	srv.http = &http.Server{
		Handler:           srv.guard(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return srv, nil
}

func newToken() (string, error) {
	raw := make([]byte, 32) // 256 bit
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func (s *Server) Token() string  { return s.token }
func (s *Server) Addr() net.Addr { return s.listener.Addr() }

// Origin 是这个服务**唯一**合法的 Origin(约束三用它比对)。
func (s *Server) Origin() string { return "http://" + s.hostPort }

// URL 是给浏览器的入口,自带 token。
func (s *Server) URL() string { return s.Origin() + "/?t=" + s.token }

func (s *Server) Serve() { _ = s.http.Serve(s.listener) }

func (s *Server) Close() error {
	var err error
	s.closeOnce.Do(func() { err = s.http.Close() })
	return err
}

// guard 是四道约束里的三道的落点:token(本任务)、Host 与 Origin(Task 10)。
// **顺序刻意是先 token 再来源**:token 不对的请求连「这个服务存在什么端点」
// 都不该看出来。
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.tokenOK(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// tokenOK 用常数时间比较。**每个请求都校验**,不做「第一次之后就信这个连接」
// 这类优化:HTTP 连接是可以被复用的,而 token 挡的就是同机别的进程。
func (s *Server) tokenOK(r *http.Request) bool {
	got := r.URL.Query().Get("t")
	if got == "" {
		got = r.Header.Get("X-Bx-Leakcheck-Token")
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// 页面内容在 Task 12 接上;先给一个占位,让约束测试能跑。
	_, _ = io.WriteString(w, "<!doctype html><title>bx leak check</title>")
}

func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()
	var browser leakcheck.BrowserReport
	if err := json.NewDecoder(io.LimitReader(r.Body, maxReportBytes)).Decode(&browser); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	report := s.judge(browser)
	select {
	case s.reports <- report:
	default:
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(report)
}

// Wait 等一份上报,或等到硬超时。**超时返回的是「什么都没问出来」的报告**,
// 不是错误:调用方要渲染的正是那份 not checked。
func (s *Server) Wait(ctx context.Context) leakcheck.Report {
	timer := time.NewTimer(s.hardTimeout)
	defer timer.Stop()
	select {
	case report := <-s.reports:
		return report
	case <-timer.C:
		return s.judge(leakcheck.BrowserReport{})
	case <-ctx.Done():
		return s.judge(leakcheck.BrowserReport{})
	}
}
```

> `handleReport` 现在还没有 Origin 校验、也还没有一次性关闭 —— 那是 Task 10 与 Task 11 的事。本任务的测试里 POST 那一段只断言 token,不断言别的。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/leakserve/ -count=1 && go build ./... && go vet ./... && go test ./... -count=1 && go test -race ./internal/leakserve/ -count=1`
Expected: PASS

- [ ] **Step 5: 变异验证**

`cp internal/leakserve/server.go /tmp/server.go.orig`,逐个改、全量跑 `go test ./... -count=1`、恢复重跑确认回绿。

| 变异 | 必须转红的测试 | 它防住的生产改动 |
|---|---|---|
| `guard` 里 `if !s.tokenOK(r)` 整段删掉 | `TestTokenIsRequiredOnEveryRequest` | 约束二被摘掉,同机任何进程都能读 |
| `tokenOK` 改成 `return true` | `TestTokenIsRequiredOnEveryRequest` | 同上,换一个写法 |
| `guard` 只在 `r.Method == http.MethodPost` 时校验 | `TestTokenIsRequiredOnEveryRequest`(GET 那三条) | 「页面又没有秘密」——页面本身就是入口,拿到它就能触发上报 |
| `newToken` 的 32 改成 4 | `TestTokenIsLongAndUnique` | token 可枚举 |
| `newToken` 改成返回固定串 | `TestTokenIsLongAndUnique` | token 可预测 |
| `subtle.ConstantTimeCompare(...)` 改成 `got == s.token` | **不转红**(行为等价)—— 记录在案:这条靠 review,不靠测试。**不要**为它编一个计时测试,那种测试在 CI 上必然不稳 | 时间侧信道 |
| `URL()` 里去掉 `"?t="+s.token` | `TestURLCarriesToken` | 调用方拿到一个必然 403 的 URL,症状是「浏览器打开一片空白」 |
| `handlePage` 里 `if r.URL.Path != "/"` 删掉 | `TestOnlyTwoEndpointsExist` | `/` 变成通配,任何路径都返回页面 |

- [ ] **Step 6: 提交**

```bash
git add internal/leakserve/server.go internal/leakserve/server_test.go
git commit -m "$(cat <<'EOF'
feat(leakserve): 一次性 loopback 服务骨架,每个请求都校验 token

token 是「本次运行专属」而不是「用一次作废」——页面至少要发两个请求
(取页面 + 上报),用一次作废会让上报永远打不进来。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 9: 约束一 —— 只绑 `127.0.0.1`,随机端口

**Files:**
- Modify: `internal/leakserve/server.go`(把监听地址抽成常量,便于钉住)
- Test: `internal/leakserve/bind_test.go`

- [ ] **Step 1: 写失败测试**

创建 `internal/leakserve/bind_test.go`:

```go
package leakserve

import (
	"net"
	"testing"
)

// 约束一:只对本机开口。**断言打在真实 listener 的地址上**,不是打在源码里的
// 那个字符串 —— 后者证明不了内核到底把它绑到了哪。
func TestListenerBindsLoopbackOnly(t *testing.T) {
	srv := newTestServer(t)
	addr, ok := srv.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("监听地址不是 TCP:%T", srv.Addr())
	}
	if !addr.IP.IsLoopback() {
		t.Fatalf("必须只绑 loopback,实际绑在 %s —— 这台机器所在的局域网现在能读它", addr.IP)
	}
	if addr.IP.IsUnspecified() {
		t.Fatalf("绑到了 %s(0.0.0.0/::):对整个局域网开口", addr.IP)
	}
	if addr.Port == 0 {
		t.Fatal("端口为 0:listener 没真正绑上")
	}
}

// 随机端口:两个同时活着的服务不能落在同一个端口,而且不能是某个固定值。
// 固定端口会让「同机其它进程猜到在哪」变成一件不用猜的事。
func TestPortIsRandom(t *testing.T) {
	a := newTestServer(t).Addr().(*net.TCPAddr).Port
	b := newTestServer(t).Addr().(*net.TCPAddr).Port
	if a == b {
		t.Fatalf("两个服务落在同一个端口 %d:端口不是内核分配的随机端口", a)
	}
}

// 监听地址是硬编码的 loopback,**不吃任何外部输入**。可配置的监听地址是这类
// 工具最常见的一个洞:某个人为了「在虚拟机里也能开」把它做成 flag,于是默认
// 之外的每一次使用都在局域网上裸奔。
func TestListenAddressIsNotConfigurable(t *testing.T) {
	if listenAddress != "127.0.0.1:0" {
		t.Fatalf("监听地址被改成了 %q:必须恒为 127.0.0.1 上的随机端口", listenAddress)
	}
	// Options 里不许有任何看起来像监听地址的字段。
	var opts Options
	_ = opts
	// 这条断言由类型系统承担:Options 只有 Judge 与 HardTimeout 两个字段,
	// 加第三个会让下面这行编译失败,从而逼加字段的人来读这段注释。
	opts = Options{Judge: nil, HardTimeout: 0}
	_ = opts
}
```

> 最后那条用**结构体字面量的字段名穷举**来钉住「不许加监听地址字段」:Go 的带字段名字面量不会因为多了字段而编译失败,所以它其实**挡不住**加字段。改用反射穷举字段名,写成:

```go
// TestOptionsHasNoListenAddressField 用反射穷举字段名。
// 带字段名的复合字面量不会因为结构体多了字段而编译失败,所以「写一个字面量」
// 挡不住加字段 —— 必须真的去数。
func TestOptionsHasNoListenAddressField(t *testing.T) {
	typ := reflect.TypeOf(Options{})
	want := map[string]bool{"Judge": true, "HardTimeout": true}
	if typ.NumField() != len(want) {
		t.Fatalf("Options 现在有 %d 个字段,守卫只认识 %d 个 —— 加字段请连同这条守卫"+
			"一起论证,尤其别加任何形式的监听地址", typ.NumField(), len(want))
	}
	for i := 0; i < typ.NumField(); i++ {
		if !want[typ.Field(i).Name] {
			t.Errorf("Options 多了字段 %q:监听地址必须恒为 127.0.0.1 上的随机端口,不可配置",
				typ.Field(i).Name)
		}
	}
}
```

(把上面 `TestListenAddressIsNotConfigurable` 里那两段无效的字面量断言删掉,只保留 `listenAddress` 常量断言;并在文件顶部 import `"reflect"`。)

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/leakserve/ -count=1`
Expected: FAIL — `undefined: listenAddress`(其余几条会通过,因为 Task 8 已经绑了 loopback;这是**预期的**:本任务真正新增的守卫是常量与字段穷举那两条)

- [ ] **Step 3: 最小实现**

在 `internal/leakserve/server.go` 里加常量并改用它:

```go
// listenAddress 是这个服务唯一的监听地址,**不可配置**。
//
// 可配置的监听地址是这类工具最常见的一个洞:有人为了「在虚拟机/容器里也能开」
// 把它做成 flag,于是默认之外的每一次使用都在局域网上裸奔。这个服务报告的是
// 本机网络姿态 —— 那正是不该对局域网开口的东西。端口交给内核随机分配(:0),
// 固定端口会让「同机其它进程猜到它在哪」变成一件不用猜的事。
const listenAddress = "127.0.0.1:0"
```

`Listen` 里 `net.Listen("tcp", "127.0.0.1:0")` 改成 `net.Listen("tcp", listenAddress)`。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/leakserve/ -count=1 && go build ./... && go vet ./... && go test ./... -count=1`
Expected: PASS

- [ ] **Step 5: 变异验证**

| 变异 | 必须转红的测试 | 它防住的生产改动 |
|---|---|---|
| `listenAddress` 改成 `":0"` | `TestListenerBindsLoopbackOnly`、`TestListenAddressIsNotConfigurable` | 对整个局域网开口 —— 本约束要挡的那件事 |
| `listenAddress` 改成 `"0.0.0.0:0"` | 同上 | 同上,换一个写法 |
| `listenAddress` 改成 `"127.0.0.1:18080"` | `TestPortIsRandom`、`TestListenAddressIsNotConfigurable` | 固定端口,同机进程不用猜 |
| 给 `Options` 加一个 `ListenAddress string` 字段(即便不使用) | `TestOptionsHasNoListenAddressField` | 把监听地址做成可配置的第一步 |

- [ ] **Step 6: 提交**

```bash
git add internal/leakserve/server.go internal/leakserve/bind_test.go
git commit -m "$(cat <<'EOF'
feat(leakserve): 约束一,监听地址恒为 127.0.0.1 随机端口且不可配置

断言打在真实 listener 的地址上,不是打在源码里那个字符串;另用反射穷举
Options 的字段,挡住「为了在虚拟机里也能开」而加出来的监听地址 flag。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 10: 约束三 —— `Host` 与 `Origin` 必须是那个精确的 loopback(挡 DNS rebinding)

**设计在这里有一处必须补齐的空白**:顶层导航的 GET **不带 `Origin` 头**(浏览器规范如此),所以「`Origin` 必须等于那个 loopback」这句话不能对所有请求一刀切,否则页面自己都打不开。落地规则定死为:

- **`Host` 无条件校验**,必须逐字等于 `127.0.0.1:<端口>`。DNS rebinding 的攻击面正是这里:攻击者的域名解析到 127.0.0.1,浏览器带着 `Host: evil.example` 打过来 —— 拒掉它就把这条路堵死了。
- **`Origin` 存在时必须匹配**;**`/report` 上 `Origin` 必须存在且匹配**(浏览器的 `fetch` POST 一定带 Origin,不带的只可能是非浏览器客户端)。

**Files:**
- Modify: `internal/leakserve/server.go`(`guard`、`handleReport`)
- Test: `internal/leakserve/origin_test.go`

- [ ] **Step 1: 写失败测试**

创建 `internal/leakserve/origin_test.go`:

```go
package leakserve

import (
	"net/http"
	"strings"
	"testing"
)

// raw 直接对 listener 的地址发请求,但**自己指定 Host 头** —— 这正是 DNS
// rebinding 的形状:包打到 127.0.0.1,Host 却是攻击者的域名。
func raw(t *testing.T, srv *Server, method, path, host, origin string) int {
	t.Helper()
	body := strings.NewReader(`{"exit_v4":"5.6.7.8"}`)
	req, err := http.NewRequest(method, "http://"+srv.Addr().String()+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host // net/http 用 req.Host 覆盖 Host 头
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// **DNS rebinding。** 用户访问 evil.example,那个域名解析到 127.0.0.1,浏览器
// 于是把请求打到本机、Host 是 evil.example。不校验 Host,用户访问的**任何**
// 网页都能读到这台机器的网络姿态。
func TestWrongHostIsRejected(t *testing.T) {
	srv := newTestServer(t)
	tok := "?t=" + srv.Token()
	if got := raw(t, srv, http.MethodGet, "/"+tok, "evil.example", ""); got != http.StatusForbidden {
		t.Errorf("Host=evil.example 的 GET 必须被拒(DNS rebinding),得到 %d", got)
	}
	if got := raw(t, srv, http.MethodGet, "/"+tok, "localhost:1", ""); got != http.StatusForbidden {
		t.Errorf("Host 端口不对也必须被拒,得到 %d", got)
	}
	// 逐字相等才放行:localhost 与 127.0.0.1 解析到同一处,但不是同一个 origin。
	if got := raw(t, srv, http.MethodGet, "/"+tok,
		strings.Replace(srv.Addr().String(), "127.0.0.1", "localhost", 1), ""); got != http.StatusForbidden {
		t.Errorf("Host=localhost:<port> 必须被拒:约束是「那个精确的 loopback」,得到 %d", got)
	}
	if got := raw(t, srv, http.MethodGet, "/"+tok, srv.Addr().String(), ""); got != http.StatusOK {
		t.Errorf("正确 Host 应放行,得到 %d", got)
	}
}

// 顶层导航不带 Origin —— 浏览器规范如此。要求它必须存在会让页面自己都打不开。
func TestNavigationWithoutOriginIsAllowed(t *testing.T) {
	srv := newTestServer(t)
	if got := raw(t, srv, http.MethodGet, "/?t="+srv.Token(), srv.Addr().String(), ""); got != http.StatusOK {
		t.Fatalf("无 Origin 的顶层导航必须放行,否则页面打不开,得到 %d", got)
	}
}

// Origin 存在就必须匹配。
func TestWrongOriginIsRejected(t *testing.T) {
	srv := newTestServer(t)
	tok := "?t=" + srv.Token()
	for _, origin := range []string{
		"http://evil.example",
		"https://" + srv.Addr().String(),      // scheme 不同
		"http://localhost:" + portOf(t, srv),  // 主机名不同
		"null",
	} {
		if got := raw(t, srv, http.MethodGet, "/"+tok, srv.Addr().String(), origin); got != http.StatusForbidden {
			t.Errorf("Origin=%q 必须被拒,得到 %d", origin, got)
		}
	}
	if got := raw(t, srv, http.MethodGet, "/"+tok, srv.Addr().String(), srv.Origin()); got != http.StatusOK {
		t.Errorf("正确 Origin 应放行,得到 %d", got)
	}
}

// /report 上 Origin 必须**存在**:浏览器的 fetch POST 一定带它,不带的只可能
// 是非浏览器客户端 —— 那正是这道闸门要挡的。
func TestReportRequiresOrigin(t *testing.T) {
	srv := newTestServer(t)
	tok := "?t=" + srv.Token()
	if got := raw(t, srv, http.MethodPost, "/report"+tok, srv.Addr().String(), ""); got != http.StatusForbidden {
		t.Errorf("无 Origin 的 POST /report 必须被拒,得到 %d", got)
	}
	if got := raw(t, srv, http.MethodPost, "/report"+tok, srv.Addr().String(), srv.Origin()); got != http.StatusOK {
		t.Errorf("带正确 Origin 的 POST /report 应放行,得到 %d", got)
	}
}

func portOf(t *testing.T, srv *Server) string {
	t.Helper()
	addr := srv.Addr().String()
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		t.Fatalf("地址读不出端口:%s", addr)
	}
	return addr[i+1:]
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/leakserve/ -count=1`
Expected: FAIL — `TestWrongHostIsRejected`、`TestWrongOriginIsRejected`、`TestReportRequiresOrigin` 全部失败(现在什么来源都放行)

- [ ] **Step 3: 最小实现**

改 `guard`,并在 `handleReport` 顶部加 Origin 必需检查:

```go
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.tokenOK(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if !s.originOK(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// originOK 挡 DNS rebinding。
//
// **Host 无条件校验、逐字相等。** rebinding 的形状就是:攻击者的域名解析到
// 127.0.0.1,浏览器把请求打到本机、Host 写着攻击者的域名。不校验 Host,用户
// 访问的任何网页都能读到这台机器的网络姿态。
//
// **Origin 存在时才校验** —— 顶层导航(用户打开页面那一下)按规范不带 Origin,
// 要求它必须存在会让页面自己都打不开。「必须存在」这条只加在 /report 上
// (见 handleReport):浏览器的 fetch POST 一定带 Origin。
//
// 逐字相等,不做 localhost/127.0.0.1 等价化:那两个是不同的 origin,而这道
// 闸门的全部价值就在于它比「差不多是本机」严格。
func (s *Server) originOK(r *http.Request) bool {
	if r.Host != s.hostPort {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != s.Origin() {
		return false
	}
	return true
}
```

`handleReport` 里,在 `if r.Method != http.MethodPost` 之后加:

```go
	// 浏览器的 fetch POST 一定带 Origin;不带的只可能是非浏览器客户端。
	if r.Header.Get("Origin") == "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/leakserve/ -count=1 && go build ./... && go vet ./... && go test ./... -count=1 && go test -race ./internal/leakserve/ -count=1`
Expected: PASS,Task 8/9 的测试仍绿

- [ ] **Step 5: 变异验证**

| 变异 | 必须转红的测试 | 它防住的生产改动 |
|---|---|---|
| `originOK` 里 `if r.Host != s.hostPort` 删掉 | `TestWrongHostIsRejected` | DNS rebinding 那条路整个敞开 |
| `r.Host != s.hostPort` 改成 `!strings.HasSuffix(r.Host, portOfServer)` 之类的宽松比较 | `TestWrongHostIsRejected`(`evil.example:<port>` 那种)—— 若不红,**当场补一条 `Host: evil.example:<真端口>` 的用例** | 「端口对就行」式放宽 |
| 把 `localhost` 也当合法(`origin != s.Origin() && origin != "http://localhost:"+port`) | `TestWrongOriginIsRejected` | 「localhost 也是本机嘛」——它是另一个 origin,放宽就重新打开一片可注册的域名空间 |
| `originOK` 里 Origin 那段改成无条件校验(去掉 `!= ""`) | `TestNavigationWithoutOriginIsAllowed` | 页面自己打不开,症状是「点了菜单浏览器一片空白」 |
| `handleReport` 里新加的 Origin 必需检查删掉 | `TestReportRequiresOrigin` | 非浏览器客户端可以直接投喂上报 |
| `guard` 里 `originOK` 那段挪到 `tokenOK` 之前 | **不转红**(状态码相同)—— 记录在案:顺序只影响信息泄露程度,靠 review | 无 token 的请求能从状态码差异里区分出端点是否存在 |

- [ ] **Step 6: 提交**

```bash
git add internal/leakserve/server.go internal/leakserve/origin_test.go
git commit -m "$(cat <<'EOF'
feat(leakserve): 约束三,Host 无条件逐字校验,Origin 存在即校验、/report 必需

设计只写了「Origin/Host 必须是那个精确的 loopback」,但顶层导航按规范不带
Origin——一刀切会让页面自己都打不开。Host 那一半才是挡 DNS rebinding 的,
所以它无条件;localhost 不等价于 127.0.0.1,不做等价化。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 11: 约束四 —— 拿到结果即关,外加 2 分钟硬超时

**Files:**
- Modify: `internal/leakserve/server.go`(`handleReport` 收尾关闭、`Wait` 超时关闭)
- Test: `internal/leakserve/oneshot_test.go`

- [ ] **Step 1: 写失败测试**

创建 `internal/leakserve/oneshot_test.go`:

```go
package leakserve

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/getbx/bx/internal/leakcheck"
)

func postReport(t *testing.T, srv *Server, body string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost,
		"http://"+srv.Addr().String()+"/report?t="+srv.Token(), strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", srv.Origin())
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /report: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// 拿到结果即关:上报之后端口必须不再接受连接。用户关掉标签页就走人是常态,
// 留一个开着的口就是把这个新攻击面一直挂在那儿。
func TestServerClosesAfterOneReport(t *testing.T) {
	srv := newTestServer(t)
	if got := postReport(t, srv, `{"exit_v4":"5.6.7.8"}`); got != http.StatusOK {
		t.Fatalf("第一次上报应成功,得到 %d", got)
	}
	report := srv.Wait(context.Background())
	if len(report.Findings) == 0 {
		t.Fatal("Wait 应拿到那份上报判出来的报告")
	}
	// 端口关掉之后,连都连不上(不是「返回 403」)。
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", srv.Addr().String(), 200*time.Millisecond)
		if err != nil {
			return // 已经关了,符合预期
		}
		_ = conn.Close()
		if time.Now().After(deadline) {
			t.Fatal("上报之后端口仍然接受连接:一次性没有生效,这个口会一直挂着")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// 硬超时:没有任何上报时,服务必须自己关掉,而且 Wait 要返回一份
// **not checked** 的报告(设计风险四:不许把「没跑成」渲染成「一切正常」)。
func TestHardTimeoutClosesAndYieldsNotChecked(t *testing.T) {
	srv, err := Listen(Options{
		Judge: func(b leakcheck.BrowserReport) leakcheck.Report {
			return leakcheck.Judge(time.Unix(0, 0).UTC(), b, leakcheck.LocalFacts{})
		},
		HardTimeout: 120 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	go srv.Serve()

	start := time.Now()
	report := srv.Wait(context.Background())
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Wait 应在硬超时后返回,实际等了 %s", elapsed)
	}
	if len(report.Findings) == 0 {
		t.Fatal("超时也要返回一份报告(全 not checked),不是空结构")
	}
	for _, f := range report.Findings {
		if f.Verdict != leakcheck.NotChecked {
			t.Errorf("超时后 %s 必须是 not checked,得到 %s", f.ID, f.Verdict)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", srv.Addr().String(), 200*time.Millisecond)
		if err != nil {
			return
		}
		_ = conn.Close()
		if time.Now().After(deadline) {
			t.Fatal("硬超时之后端口仍然接受连接")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// 2 分钟这个数字是设计定的,钉住它。改它要先改这条测试 —— 一个被悄悄改成
// 半小时的「硬超时」,与没有硬超时的区别只是慢一点。
func TestDefaultHardTimeoutIsTwoMinutes(t *testing.T) {
	if DefaultHardTimeout != 2*time.Minute {
		t.Fatalf("硬超时应为 2 分钟,得到 %s", DefaultHardTimeout)
	}
	srv, err := Listen(Options{Judge: func(leakcheck.BrowserReport) leakcheck.Report {
		return leakcheck.Report{}
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	if srv.hardTimeout != DefaultHardTimeout {
		t.Fatalf("HardTimeout 留空时应回落到 %s,得到 %s", DefaultHardTimeout, srv.hardTimeout)
	}
}

// Close 可以被调用多次(defer 一次、超时路径一次),不许 panic。
func TestCloseIsIdempotent(t *testing.T) {
	srv := newTestServer(t)
	_ = srv.Close()
	_ = srv.Close()
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/leakserve/ -count=1`
Expected: FAIL — `TestServerClosesAfterOneReport`(上报后端口还开着)、`TestHardTimeoutClosesAndYieldsNotChecked`(超时后端口还开着)

- [ ] **Step 3: 最小实现**

`handleReport` 的结尾改成:先把响应写完再关 —— 顺序很重要,先关会让页面拿不到结论:

```go
	report := s.judge(browser)
	select {
	case s.reports <- report:
	default:
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(report)
	// **拿到结果即关。** 关闭放在写完响应之后:先关会让页面拿不到它要渲染的
	// 结论。用 goroutine 是因为 http.Server.Close 会等待处理中的连接结束,
	// 而我们正身处其中一条。
	go func() { _ = s.Close() }()
```

`Wait` 的两条退出路径都补上关闭:

```go
func (s *Server) Wait(ctx context.Context) leakcheck.Report {
	timer := time.NewTimer(s.hardTimeout)
	defer timer.Stop()
	select {
	case report := <-s.reports:
		return report
	case <-timer.C:
		// 硬超时:用户关掉标签页就走人是常态,没有这一步就会留一个开着的口。
		// 返回的是「什么都没问出来」的报告,**不是** ok。
		_ = s.Close()
		return s.judge(leakcheck.BrowserReport{})
	case <-ctx.Done():
		_ = s.Close()
		return s.judge(leakcheck.BrowserReport{})
	}
}
```

`Close` 里除了关 `http.Server`,还要显式关 listener(`http.Server.Close` 会关它,但 `Serve` 尚未被调用时不会 —— 这条路径在 `Listen` 成功而调用方直接 `Close` 时会走到):

```go
func (s *Server) Close() error {
	var err error
	s.closeOnce.Do(func() {
		err = s.http.Close()
		_ = s.listener.Close()
	})
	return err
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/leakserve/ -count=1 && go build ./... && go vet ./... && go test ./... -count=1 && go test -race ./internal/leakserve/ -count=1`
Expected: PASS(`-race` 尤其重要:`handleReport` 里那个关闭 goroutine 与 `Wait` 并发)

- [ ] **Step 5: 变异验证**

| 变异 | 必须转红的测试 | 它防住的生产改动 |
|---|---|---|
| `handleReport` 末尾的 `go func() { _ = s.Close() }()` 删掉 | `TestServerClosesAfterOneReport` | 一次性失效,口一直挂着 |
| 把那句关闭挪到 `json.NewEncoder(w).Encode(report)` **之前** | `TestServerClosesAfterOneReport` 可能仍绿 → **必须补一条断言 POST 响应体含结论的测试**(Task 12 的 `TestReportResponseCarriesFinishedConclusions` 会覆盖它;若本任务先做,当场补一条) | 页面拿不到结论,用户看到一片空白 |
| `Wait` 的 `case <-timer.C` 里 `s.Close()` 删掉 | `TestHardTimeoutClosesAndYieldsNotChecked` | 硬超时只让 CLI 退出,端口留着 |
| `DefaultHardTimeout` 改成 `30 * time.Minute` | `TestDefaultHardTimeoutIsTwoMinutes` | 「硬超时」被悄悄改成与没有差不多 |
| `Wait` 超时那支改成 `return leakcheck.Report{}` | `TestHardTimeoutClosesAndYieldsNotChecked`(`len(Findings)==0`) | 返回空结构,下游渲染出一份没有任何结论的报告,读起来像「没发现问题」 |
| `closeOnce` 换成裸 `s.http.Close()` | `TestCloseIsIdempotent`(若不 panic 则不红)—— 记录在案 | 重复关闭 |

- [ ] **Step 6: 提交**

```bash
git add internal/leakserve/server.go internal/leakserve/oneshot_test.go
git commit -m "$(cat <<'EOF'
feat(leakserve): 约束四,拿到结果即关 + 2 分钟硬超时

关闭放在写完响应之后——先关会让页面拿不到它要渲染的结论;超时返回的是一份
全 not checked 的报告而不是空结构,空结构在下游读起来像「没发现问题」。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 12: 页面 —— 采集、回传、渲染 Go 给的结论

**「JS 保持愚蠢」怎么靠结构保证(设计明确要求写清楚)**:这个仓库的测试基建覆盖不到 JS(`apps/macos/BxMenu/Tests` 那套教训:不在 target 里的东西一次都不会跑而 CI 全绿)。所以约束不能靠「审 JS 时注意别写判断」,必须让**页面根本拿不到可判断的原料**:

1. **本机事实那一半从不下发。** `LocalFacts` 只存在于 Go 进程里,页面从头到尾没见过它。没有它,「srflx ≠ 出口」这类结论在 JS 里**不可能**算得出来 —— 缺的不是代码,是数据。
2. **POST 的响应体就是成品 `leakcheck.Report`**:`Finding.Verdict` 已经是 `"ok"`/`"bad"`/`"not checked"` 三个词之一,`Summary`/`Evidence` 已经是成句的字符串。页面能做的只有 `textContent = f.summary`。
3. 两条守卫钉住这个结构:`Report` 的字段类型里**不许**出现 `BrowserReport`/`LocalFacts`;`/report` 响应的顶层键集合**逐字锁定**。

**Files:**
- Create: `internal/leakserve/page.html`、`internal/leakserve/page.go`
- Modify: `internal/leakserve/server.go`(`handlePage` 渲染真页面)
- Test: `internal/leakserve/page_test.go`

**Interfaces:**
- Produces: `leakserve.pageHTML`(embed)、`(*Server).handlePage` 渲染带 token 与端点披露的页面

- [ ] **Step 1: 写失败测试**

创建 `internal/leakserve/page_test.go`:

```go
package leakserve

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/leakcheck"
)

// 页面必须在**联网之前**原样显示要联系谁(设计风险三:第三方暴露是用户明确
// 接受的,但必须可见)。断言打在真实响应体上,不是打在模板文件上。
func TestPageDisclosesEndpointsVerbatim(t *testing.T) {
	srv := newTestServer(t)
	resp := get(t, srv, "/?t="+srv.Token())
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	html := string(body)
	for _, want := range []string{leakcheck.EchoV4URL, leakcheck.EchoV6URL, leakcheck.STUNURL} {
		if !strings.Contains(html, want) {
			t.Errorf("页面必须原样显示 %q,它是用户可见契约", want)
		}
	}
	// 页面还必须带上 token,否则它自己发不出 POST。
	if !strings.Contains(html, srv.Token()) {
		t.Error("页面里必须带 token,否则它的上报请求会被自己的闸门拒掉")
	}
}

// **JS 愚蠢的结构性保证之一**:页面拿到的响应里,只有已经判完的结论,
// 没有任何可供它自己判断的原料。顶层键集合逐字锁定。
func TestReportResponseCarriesFinishedConclusions(t *testing.T) {
	srv := newTestServer(t)
	req, err := http.NewRequest(http.MethodPost,
		"http://"+srv.Addr().String()+"/report?t="+srv.Token(),
		strings.NewReader(`{"exit_v4":"5.6.7.8","srflx":["1.2.3.4"]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", srv.Origin())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var top map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&top); err != nil {
		t.Fatalf("上报响应必须是 JSON: %v", err)
	}
	want := map[string]bool{
		"generated_at": true, "endpoints": true,
		"findings": true, "evidence": true, "anomaly_count": true,
	}
	for key := range top {
		if !want[key] {
			t.Errorf("上报响应多了顶层键 %q:页面只能拿到成品结论,多一个原料键就是"+
				"给 JS 递上了可判断的东西,而这个仓库的测试覆盖不到 JS", key)
		}
	}
	for key := range want {
		if _, ok := top[key]; !ok {
			t.Errorf("上报响应缺了 %q", key)
		}
	}

	// findings 里的 verdict 必须已经是三个词之一,不是数字/布尔。
	var report struct {
		Findings []struct {
			Verdict string `json:"verdict"`
			Summary string `json:"summary"`
		} `json:"findings"`
	}
	raw, _ := json.Marshal(top)
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Findings) == 0 {
		t.Fatal("响应里没有 findings")
	}
	for _, f := range report.Findings {
		switch f.Verdict {
		case "ok", "bad", "not checked":
		default:
			t.Errorf("verdict 必须是三个词之一,得到 %q —— 数字/布尔会逼 JS 自己映射", f.Verdict)
		}
		if f.Summary == "" {
			t.Error("summary 不能为空:页面除了显示它没有别的事可做")
		}
	}
}

// **JS 愚蠢的结构性保证之二**:Report 的字段类型里不许出现 BrowserReport 或
// LocalFacts。本机事实那一半从不下发 —— 没有它,「srflx ≠ 出口」这类结论在 JS
// 里不可能算得出来,缺的不是代码,是数据。
func TestReportTypeCarriesNoRawMaterial(t *testing.T) {
	banned := map[reflect.Type]string{
		reflect.TypeOf(leakcheck.BrowserReport{}): "浏览器原始上报",
		reflect.TypeOf(leakcheck.LocalFacts{}):    "本机事实",
	}
	typ := reflect.TypeOf(leakcheck.Report{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		ft := field.Type
		for ft.Kind() == reflect.Ptr || ft.Kind() == reflect.Slice {
			ft = ft.Elem()
		}
		if why, bad := banned[ft]; bad {
			t.Errorf("Report.%s 的类型是 %s(%s):页面拿到 Report,把原料塞进去就等于"+
				"把判断权交给了测不到的 JS", field.Name, ft, why)
		}
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/leakserve/ -count=1`
Expected: FAIL — `TestPageDisclosesEndpointsVerbatim`(占位页面里没有端点也没有 token)

- [ ] **Step 3: 最小实现**

创建 `internal/leakserve/page.html`(模板变量用 Go `html/template`;注意 `{{.Token}}` 出现在 JS 字符串里,用 `template.JSStr` 语境由 `html/template` 自动处理):

```html
<!doctype html>
<meta charset="utf-8">
<title>bx leak check</title>
<style>
 body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;margin:40px auto;max-width:44rem;line-height:1.5;color:#111}
 h1{font-size:1.4rem}
 .f{border:1px solid #ddd;border-radius:8px;padding:12px 16px;margin:12px 0}
 .v{font-weight:600;text-transform:uppercase;font-size:.75rem;letter-spacing:.06em}
 .ok .v{color:#137333} .bad .v{color:#a50e0e} .nc .v{color:#5f6368}
 pre{background:#f6f6f6;padding:10px;border-radius:6px;overflow-x:auto;font-size:.8rem;white-space:pre-wrap}
 .disc{background:#fffbe6;border:1px solid #f0e0a0;border-radius:8px;padding:12px 16px}
 code{font-size:.85rem}
</style>
<h1>bx leak check</h1>

<div class="disc">
  <p><strong>Before anything is sent:</strong> this page will contact the following
  third parties from your browser. They will see your real exit address.</p>
  <ul>
    <li>IPv4 echo: <code>{{.Endpoints.EchoV4}}</code></li>
    <li>IPv6 echo: <code>{{.Endpoints.EchoV6}}</code></li>
    <li>STUN (WebRTC): <code>{{.Endpoints.STUN}}</code></li>
  </ul>
  <p>Nothing is stored. bx keeps no history of this check.</p>
  <button id="go">Run the check</button>
</div>

<div id="out"></div>

<script>
(function () {
  // This page is deliberately dumb: it collects, it posts, it renders whatever
  // bx sends back. It never compares anything, because it never receives the
  // other half of the evidence (the local routing/DNS facts stay in the bx
  // process). All judgement lives in Go, where the tests are.
  var TOKEN = "{{.Token}}";
  var ECHO4 = "{{.Endpoints.EchoV4}}";
  var ECHO6 = "{{.Endpoints.EchoV6}}";
  var STUN  = "{{.Endpoints.STUN}}";
  var out = document.getElementById("out");

  function text(el, s) { el.textContent = s; }

  function fetchEcho(url) {
    return fetch(url, { cache: "no-store" })
      .then(function (r) { return r.text(); })
      .then(function (t) { return { value: t.trim(), err: "" }; })
      .catch(function (e) { return { value: "", err: String(e && e.message ? e.message : e) }; });
  }

  function gatherSrflx() {
    return new Promise(function (resolve) {
      var result = { srflx: [], err: "" };
      var pc;
      try {
        pc = new RTCPeerConnection({ iceServers: [{ urls: STUN }] });
      } catch (e) {
        result.err = String(e && e.message ? e.message : e);
        resolve(result);
        return;
      }
      pc.createDataChannel("bx");
      pc.onicecandidate = function (ev) {
        if (!ev.candidate || !ev.candidate.candidate) return;
        var parts = ev.candidate.candidate.split(" ");
        // a=candidate:<foundation> <comp> <proto> <pri> <ip> <port> typ <type>
        if (parts.length > 7 && parts[7] === "srflx" && result.srflx.indexOf(parts[4]) < 0) {
          result.srflx.push(parts[4]);
        }
      };
      pc.createOffer().then(function (o) { return pc.setLocalDescription(o); }).catch(function (e) {
        result.err = String(e && e.message ? e.message : e);
      });
      var started = Date.now();
      var timer = setInterval(function () {
        if (pc.iceGatheringState === "complete" || Date.now() - started > 9000) {
          clearInterval(timer);
          try { pc.close(); } catch (e) {}
          resolve(result);
        }
      }, 150);
    });
  }

  function render(report) {
    out.innerHTML = "";
    var h = document.createElement("h2");
    text(h, "Results");
    out.appendChild(h);
    report.findings.forEach(function (f) {
      var cls = f.verdict === "ok" ? "ok" : (f.verdict === "bad" ? "bad" : "nc");
      var box = document.createElement("div");
      box.className = "f " + cls;
      var v = document.createElement("div");
      v.className = "v";
      text(v, f.verdict);              // already one of ok / bad / not checked
      var t = document.createElement("strong");
      text(t, f.title);
      var s = document.createElement("p");
      text(s, f.summary);              // already a finished sentence
      box.appendChild(v); box.appendChild(t); box.appendChild(s);
      if (f.evidence && f.evidence.length) {
        var d = document.createElement("details");
        var sum = document.createElement("summary");
        text(sum, "Evidence");
        var pre = document.createElement("pre");
        text(pre, f.evidence.join("\n"));
        d.appendChild(sum); d.appendChild(pre);
        box.appendChild(d);
      }
      out.appendChild(box);
    });
    if (report.evidence && report.evidence.length) {
      var d2 = document.createElement("details");
      var s2 = document.createElement("summary");
      text(s2, "What bx observed");
      var pre2 = document.createElement("pre");
      text(pre2, report.evidence.join("\n"));
      d2.appendChild(s2); d2.appendChild(pre2);
      out.appendChild(d2);
    }
    var done = document.createElement("p");
    text(done, "You can close this tab. bx has already shut the local server down.");
    out.appendChild(done);
  }

  document.getElementById("go").addEventListener("click", function () {
    document.getElementById("go").disabled = true;
    text(out, "Running…");
    Promise.all([fetchEcho(ECHO4), fetchEcho(ECHO6), gatherSrflx()]).then(function (r) {
      var payload = {
        user_agent: navigator.userAgent,
        exit_v4: r[0].value, exit_v4_err: r[0].err,
        exit_v6: r[1].value, exit_v6_err: r[1].err,
        srflx: r[2].srflx, stun_err: r[2].err
      };
      return fetch("/report?t=" + encodeURIComponent(TOKEN), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload)
      });
    }).then(function (resp) {
      return resp.json();
    }).then(render).catch(function (e) {
      text(out, "Could not complete the check: " + e + "\nbx will report this as “not checked”.");
    });
  });
})();
</script>
```

创建 `internal/leakserve/page.go`:

```go
package leakserve

import (
	_ "embed"
	"html/template"

	"github.com/getbx/bx/internal/leakcheck"
)

//go:embed page.html
var pageHTML string

var pageTemplate = template.Must(template.New("leakcheck").Parse(pageHTML))

// pageData 是页面拿到的**全部**东西:一个 token 和一份第三方披露。
//
// **本机事实那一半从不出现在这里,这是「JS 保持愚蠢」的结构性保证。**
// 没有 LocalFacts,「srflx ≠ 出口」这类结论在页面里不可能算得出来——缺的不是
// 代码,是数据。判断只可能发生在 Go 里,而那是这个仓库测得到的地方。
type pageData struct {
	Token     string
	Endpoints leakcheck.EndpointDisclosure
}
```

`server.go` 的 `handlePage` 改成:

```go
func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// 页面不该被缓存:它带着这一次运行专属的 token。
	w.Header().Set("Cache-Control", "no-store")
	if err := pageTemplate.Execute(w, pageData{Token: s.token, Endpoints: leakcheck.Endpoints()}); err != nil {
		// 头已经发出去了,这里只能停止写入。
		return
	}
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/leakserve/ -count=1 && go build ./... && go vet ./... && go test ./... -count=1 && go test -race ./internal/leakserve/ -count=1`
Expected: PASS,Task 8-11 全部仍绿

- [ ] **Step 5: 变异验证**

| 变异 | 必须转红的测试 | 它防住的生产改动 |
|---|---|---|
| `pageData` 里加一个 `Local leakcheck.LocalFacts` 字段并在模板里注入 | **不转红** → 说明守卫不够 → **当场补一条**:反射穷举 `pageData` 的字段名,只许 `Token`/`Endpoints`(照 `TestOptionsHasNoListenAddressField` 的形状写) | 把本机事实下发给页面,判断权就漏到了测不到的 JS 里 |
| `Report` 里加 `Raw BrowserReport` 字段 | `TestReportTypeCarriesNoRawMaterial`、`TestReportResponseCarriesFinishedConclusions`(多了顶层键) | 同上 |
| `Verdict.MarshalJSON` 删掉(退回数字) | `TestReportResponseCarriesFinishedConclusions` | 页面被迫自己做 0/1/2 到词的映射 —— 判断的第一步 |
| 页面模板里三个 `{{.Endpoints.*}}` 删掉 | `TestPageDisclosesEndpointsVerbatim` | 用户不知道自己的 IP 递给了谁(设计风险三的缓解措施只有两条,这是其中一条) |
| 页面模板里 `{{.Token}}` 删掉 | `TestPageDisclosesEndpointsVerbatim` | 页面发不出上报,症状是「点了运行之后什么都不发生」 |
| `handlePage` 里 `Cache-Control: no-store` 删掉 | 不转红 —— 记录在案,靠 review | 带 token 的页面进磁盘缓存 |

> 第一行是**发现缺口的变异**:它现在不会转红。做到这一步时**必须**把那条 `pageData` 字段穷举守卫补上,再重跑确认它转红。这条不是可选的 —— 「本机事实从不下发」是本任务全部论证的支点。

- [ ] **Step 6: 提交**

```bash
git add internal/leakserve/page.go internal/leakserve/page.html internal/leakserve/server.go internal/leakserve/page_test.go
git commit -m "$(cat <<'EOF'
feat(leakserve): 页面只采集、回传、渲染成品结论

JS 愚蠢靠结构保证而不是靠 review:页面拿到的 pageData 里只有 token 与第三方
披露,本机事实那一半从不下发——没有它,「srflx ≠ 出口」在 JS 里算不出来,
缺的不是代码是数据。响应顶层键集合与 verdict 词汇都由测试锁死。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 13: 本机事实采集(darwin 实现 + 非 darwin 桩)

**Files:**
- Create: `internal/leakserve/facts.go`(平台无关的编排与类型)、`internal/leakserve/facts_darwin.go`、`internal/leakserve/facts_other.go`
- Test: `internal/leakserve/facts_test.go`

**Interfaces:**
- Produces:
  - `leakserve.FactDeps{LookupRoute func(ctx, dest string, ipv6 bool) (iface string, err error); InspectDNS func(ctx) (servers []string, err error); GuardianStatus func(ctx) (tun, protection string, err error); ListVPNServices func(ctx) ([]leakcheck.VPNService, error)}`
  - `leakserve.CollectFacts(ctx context.Context, deps FactDeps) leakcheck.LocalFacts`
  - `leakserve.LiveFactDeps() FactDeps`(darwin 真实接线;非 darwin 全 nil)

- [ ] **Step 1: 写失败测试**

创建 `internal/leakserve/facts_test.go`:

```go
package leakserve

import (
	"context"
	"errors"
	"testing"

	"github.com/getbx/bx/internal/leakcheck"
	"github.com/getbx/bx/internal/observe"
)

// 全部依赖缺席(非 darwin 上就是这个形状)时,采到的必须是「什么都不知道」,
// 而不是一堆零值冒充的事实。IPv6DefaultPresent 必须是 Unknown,不是 False ——
// 后者会让 Judge 说出一句「这台机器没有 IPv6,不可能漏」的假话。
func TestCollectFactsWithNoDepsIsBlind(t *testing.T) {
	facts := CollectFacts(context.Background(), FactDeps{})
	if facts.DefaultRouteV4.Known() || facts.DefaultRouteV6.Known() {
		t.Fatalf("依赖缺席时不该有任何已知接口:%+v", facts)
	}
	if facts.IPv6DefaultPresent != observe.Unknown {
		t.Fatalf("依赖缺席时 IPv6DefaultPresent 必须是 Unknown,得到 %v", facts.IPv6DefaultPresent)
	}
	if len(facts.DNSServers) != 0 {
		t.Fatalf("依赖缺席时不该有解析器:%v", facts.DNSServers)
	}
	// 而且送进 Judge 之后不许有任何 ok。
	for _, f := range leakcheck.Judge(fixedNow(), leakcheck.BrowserReport{}, facts).Findings {
		if f.Verdict == leakcheck.OK {
			t.Errorf("全盲的事实不许判出 ok:%s = %q", f.ID, f.Summary)
		}
	}
}

func TestCollectFactsHappyPath(t *testing.T) {
	deps := FactDeps{
		LookupRoute: func(_ context.Context, dest string, ipv6 bool) (string, error) {
			switch {
			case ipv6:
				return "utun4", nil
			case dest == "192.0.2.53":
				return "en0", nil
			default:
				return "utun4", nil
			}
		},
		InspectDNS: func(context.Context) ([]string, error) {
			return []string{"192.0.2.53"}, nil
		},
		GuardianStatus: func(context.Context) (string, string, error) {
			return "utun11", "off", nil
		},
		ListVPNServices: func(context.Context) ([]leakcheck.VPNService, error) {
			return []leakcheck.VPNService{{Name: "Work VPN", Connected: true}}, nil
		},
	}
	facts := CollectFacts(context.Background(), deps)

	if facts.DefaultRouteV4.Name != "utun4" {
		t.Errorf("v4 默认路由应是 utun4,得到 %+v", facts.DefaultRouteV4)
	}
	// **翻成人话必须在采集时就做好**:Judge 是纯函数,它拿不到 scutil。
	if facts.DefaultRouteV4.Display != "Work VPN (utun4)" {
		t.Errorf("v4 接口应翻成 %q,得到 %q", "Work VPN (utun4)", facts.DefaultRouteV4.Display)
	}
	if facts.IPv6DefaultPresent != observe.True {
		t.Errorf("查到了 v6 默认路由,应为 True,得到 %v", facts.IPv6DefaultPresent)
	}
	if facts.DNSServerEgress["192.0.2.53"] != "en0" {
		t.Errorf("解析器出口应是 en0,得到 %v", facts.DNSServerEgress)
	}
	if facts.BXTunInterface != "utun11" || facts.BXProtection != "off" {
		t.Errorf("Guardian 那一半没接上:%+v", facts)
	}
}

// **单项失败不许连累别人,也不许伪装成事实**(与 internal/observe 同一条纪律)。
func TestCollectFactsPartialFailuresStayIsolated(t *testing.T) {
	deps := FactDeps{
		LookupRoute: func(_ context.Context, _ string, ipv6 bool) (string, error) {
			if ipv6 {
				return "", errors.New("no route to host")
			}
			return "utun4", nil
		},
		InspectDNS: func(context.Context) ([]string, error) {
			return nil, errors.New("networksetup failed")
		},
		GuardianStatus: func(context.Context) (string, string, error) {
			return "", "", errors.New("dial guardian: no such file")
		},
		ListVPNServices: func(context.Context) ([]leakcheck.VPNService, error) {
			return nil, errors.New("scutil failed")
		},
	}
	facts := CollectFacts(context.Background(), deps)

	if !facts.DefaultRouteV4.Known() {
		t.Error("v4 那条明明查到了,不该被别人的失败连累")
	}
	// v6 查失败 ⇒ 不是「没有 v6 默认路由」,是「没问出来」。
	if facts.IPv6DefaultPresent != observe.Unknown {
		t.Errorf("v6 查询失败时必须是 Unknown,得到 %v —— False 会让 Judge 说出"+
			"「这台机器没有 IPv6」这句假话", facts.IPv6DefaultPresent)
	}
	if facts.DefaultRouteV6.Err == "" {
		t.Error("v6 那条要留下失败原因")
	}
	if facts.DNSErr == "" {
		t.Error("DNS 失败要留下原因,否则用户不知道是没配还是没问出来")
	}
	// scutil 失败只是翻不成人话,接口名照样在。
	if facts.DefaultRouteV4.Name != "utun4" {
		t.Error("scutil 失败不该抹掉接口名")
	}
	if facts.DefaultRouteV4.Display != "Unidentified VPN (utun4)" {
		t.Errorf("翻不出来时必须说翻不出,得到 %q", facts.DefaultRouteV4.Display)
	}
}

// **「路由查不到」与「查询本身失败」必须分开。** 前者(比如根本没有 v6 默认
// 路由)是一个确定的观测,能支撑一句诚实的 ok;后者不能。
func TestNoV6RouteIsObservedAbsenceNotFailure(t *testing.T) {
	deps := FactDeps{
		LookupRoute: func(_ context.Context, _ string, ipv6 bool) (string, error) {
			if ipv6 {
				return "", ErrNoRoute
			}
			return "utun4", nil
		},
	}
	facts := CollectFacts(context.Background(), deps)
	if facts.IPv6DefaultPresent != observe.False {
		t.Fatalf("确知没有 v6 默认路由时应为 False(它支撑一句诚实的 ok),得到 %v",
			facts.IPv6DefaultPresent)
	}
}
```

在 `facts_test.go` 顶部补一个 `fixedNow()` helper:

```go
func fixedNow() time.Time { return time.Date(2026, 8, 11, 10, 30, 0, 0, time.UTC) }
```

(并 import `"time"`。)

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/leakserve/ -count=1`
Expected: FAIL — `undefined: CollectFacts` / `undefined: FactDeps` / `undefined: ErrNoRoute`

- [ ] **Step 3: 最小实现**

创建 `internal/leakserve/facts.go`:

```go
package leakserve

import (
	"context"
	"errors"

	"github.com/getbx/bx/internal/leakcheck"
	"github.com/getbx/bx/internal/observe"
)

// ErrNoRoute 表示**确知**没有这条路由(不是查询失败)。
//
// 这两件事必须分得开:「确知没有 v6 默认路由」能支撑一句诚实的 ok(这台机器
// 没有 v6 通路可漏),而「v6 查询失败」什么都支撑不了。把后者当成前者,就是
// 让 bx 对一台可能正在漏 v6 的机器说「没问题」。
var ErrNoRoute = errors.New("no route")

// captureV4 / captureV6 是用来问「默认路由归谁」的目的地。选公共 anycast 解析器
// 是因为它们必定落在默认路由上,不在任何私网/carve-out 网段里。
const (
	probeV4 = "1.1.1.1"
	probeV6 = "2606:4700:4700::1111"
)

// FactDeps 是采集本机事实所需的外部能力。**用注入而不是直接调**,是为了让
// 采集逻辑在任何平台上都能测(本机永远不跑 route / networksetup / scutil)。
type FactDeps struct {
	// LookupRoute 回答「发往 dest 的包交给哪个接口」。确知无路由时返回 ErrNoRoute。
	LookupRoute func(ctx context.Context, dest string, ipv6 bool) (string, error)
	// InspectDNS 返回系统当前的解析器。
	InspectDNS func(ctx context.Context) ([]string, error)
	// GuardianStatus 返回 bx 自己的 TUN 名与保护状态(经 Guardian 的 /v1/status,
	// 0666,**普通用户可读** —— 这正是这个功能不需要 root 的原因之一)。
	GuardianStatus func(ctx context.Context) (tun string, protection string, err error)
	// ListVPNServices 列系统集成 VPN(scutil --nc list)。
	ListVPNServices func(ctx context.Context) ([]leakcheck.VPNService, error)
}

// CollectFacts 采一轮本机事实。
//
// **任一项失败只让那一项变成「没问出来」,绝不中断其余项、绝不让调用方失败**
// (与 internal/observe 同一条纪律)。零值一律读作 Unknown,不读作「没有」。
func CollectFacts(ctx context.Context, deps FactDeps) leakcheck.LocalFacts {
	facts := leakcheck.LocalFacts{}

	var services []leakcheck.VPNService
	if deps.ListVPNServices != nil {
		if list, err := deps.ListVPNServices(ctx); err == nil {
			services = list
		}
		// 失败不记 Err:它只影响「翻不翻得成人话」,而 DescribeInterface 对
		// 翻不出来已经有一个诚实的答案。
	}

	bxTun := ""
	if deps.GuardianStatus != nil {
		tun, protection, err := deps.GuardianStatus(ctx)
		if err == nil {
			bxTun = tun
			facts.BXTunInterface = tun
			facts.BXProtection = protection
		}
		// Guardian 拨不通不是错误:这个功能的主场景之一就是 bx 没在跑。
	}

	facts.DefaultRouteV4 = lookupInterface(ctx, deps, probeV4, false, services, bxTun)
	facts.DefaultRouteV6 = lookupInterface(ctx, deps, probeV6, true, services, bxTun)

	switch {
	case facts.DefaultRouteV6.Known():
		facts.IPv6DefaultPresent = observe.True
	case errors.Is(facts.defaultRouteV6Err, ErrNoRoute):
		facts.IPv6DefaultPresent = observe.False
	default:
		facts.IPv6DefaultPresent = observe.Unknown
	}

	if deps.InspectDNS != nil {
		servers, err := deps.InspectDNS(ctx)
		if err != nil {
			facts.DNSErr = err.Error()
		} else {
			facts.DNSServers = servers
			facts.DNSServerEgress = map[string]string{}
			for _, server := range servers {
				if deps.LookupRoute == nil {
					continue
				}
				iface, err := deps.LookupRoute(ctx, server, false)
				if err != nil || iface == "" {
					continue // 少一个键 = 那个解析器的去向没问出来
				}
				facts.DNSServerEgress[server] = iface
			}
		}
	}
	return facts
}

func lookupInterface(ctx context.Context, deps FactDeps, dest string, ipv6 bool,
	services []leakcheck.VPNService, bxTun string) leakcheck.InterfaceRef {
	if deps.LookupRoute == nil {
		return leakcheck.InterfaceRef{Err: "route lookup is not available on this platform"}
	}
	iface, err := deps.LookupRoute(ctx, dest, ipv6)
	if err != nil {
		return leakcheck.InterfaceRef{Err: err.Error()}
	}
	if iface == "" {
		return leakcheck.InterfaceRef{Err: "route lookup returned no interface"}
	}
	return leakcheck.InterfaceRef{
		Name:    iface,
		Display: leakcheck.DescribeInterface(iface, services, bxTun),
	}
}
```

> **上面 `facts.defaultRouteV6Err` 在 `LocalFacts` 里不存在** —— 不要给 `LocalFacts` 加这个字段(它是判据的输入,不该背一个只有采集用得上的 error)。改成在 `CollectFacts` 里把 v6 的 error 留在局部变量里:把 `lookupInterface` 改成同时返回 `(leakcheck.InterfaceRef, error)`,`CollectFacts` 里写
> ```go
> var v6Err error
> facts.DefaultRouteV6, v6Err = lookupInterface(ctx, deps, probeV6, true, services, bxTun)
> ```
> 然后 `case errors.Is(v6Err, ErrNoRoute):`。v4 那一侧用 `_` 丢弃 error(它已经进了 `InterfaceRef.Err`)。

创建 `internal/leakserve/facts_darwin.go`:

```go
//go:build darwin

package leakserve

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/getbx/bx/internal/guardian"
	"github.com/getbx/bx/internal/install"
	"github.com/getbx/bx/internal/leakcheck"
	"github.com/getbx/bx/internal/supervisor"
)

// LiveFactDeps 把生产环境的真实能力接上。**全部只读,全部不需要 root。**
func LiveFactDeps() FactDeps {
	return FactDeps{
		LookupRoute: func(ctx context.Context, dest string, ipv6 bool) (string, error) {
			selection, err := supervisor.LookupRoute(ctx, dest, ipv6)
			if err != nil {
				// route -n get 对「没有到达该目的地的路由」也是报错退出。
				// 把它翻成 ErrNoRoute,让「确知没有」与「问不出来」分开。
				if isNoRouteError(err) {
					return "", ErrNoRoute
				}
				return "", err
			}
			return selection.Interface, nil
		},
		InspectDNS: func(ctx context.Context) ([]string, error) {
			status, err := install.InspectDNSContext(ctx, "")
			if err != nil {
				return nil, err
			}
			return status.Servers, nil
		},
		GuardianStatus: func(ctx context.Context) (string, string, error) {
			// Guardian 的 socket 是 0666,**普通用户读 /v1/status 返回 200**
			// (真机已验,uid 501)。这是这个功能不需要 root 的关键一环。
			status, err := guardian.NewClient(guardian.SocketPath).Status(ctx)
			if err != nil {
				return "", "", err
			}
			tun := ""
			if status.Core != nil {
				tun = status.Core.TunName
			}
			return tun, status.Protection, nil
		},
		ListVPNServices: listDarwinVPNServices,
	}
}

var noRouteRe = regexp.MustCompile(`(?i)no route to host|host is down|not in table`)

func isNoRouteError(err error) bool {
	return err != nil && noRouteRe.MatchString(err.Error())
}

// scutil --nc list 的行形如:
//   * (Disconnected)   6E1B…  PPP (L2TP)   "Work VPN"        [OnDemand]
//   * (Connected)      A2C4…  IPSec        "Home VPN"
var scutilLineRe = regexp.MustCompile(`^\s*\*?\s*\((\w+)\)\s+\S+\s+.*?"([^"]+)"`)

func listDarwinVPNServices(parent context.Context) ([]leakcheck.VPNService, error) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "scutil", "--nc", "list").Output()
	if err != nil {
		return nil, err
	}
	return parseSCUtilNCList(string(out)), nil
}

// parseSCUtilNCList 是纯解析,单测覆盖(免 root、不跑 scutil)。
func parseSCUtilNCList(out string) []leakcheck.VPNService {
	var services []leakcheck.VPNService
	for _, line := range strings.Split(out, "\n") {
		m := scutilLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		services = append(services, leakcheck.VPNService{
			Name:      m[2],
			Connected: strings.EqualFold(m[1], "Connected"),
		})
	}
	return services
}
```

创建 `internal/leakserve/facts_other.go`:

```go
//go:build !darwin

package leakserve

// LiveFactDeps 在非 darwin 上一个能力都没有:本机事实的原语(route -n get /
// networksetup / scutil)目前只有 macOS 实现。
//
// **返回空 FactDeps 是诚实的**:CollectFacts 会产出一份「什么都不知道」的事实,
// Judge 于是只会输出 not checked。伪造几个零值填进去才是撒谎。
func LiveFactDeps() FactDeps { return FactDeps{} }
```

再给 `parseSCUtilNCList` 补一个 darwin-only 的解析测试 `internal/leakserve/facts_darwin_test.go`:

```go
//go:build darwin

package leakserve

import "testing"

func TestParseSCUtilNCList(t *testing.T) {
	out := `Available network connection services in the current set (*=enabled):
* (Disconnected)   6E1B0000-0000-0000-0000-000000000001 PPP (L2TP)  "Old VPN"    [OnDemand]
* (Connected)      A2C40000-0000-0000-0000-000000000002 IPSec       "Work VPN"
  (Invalid)        B3D50000-0000-0000-0000-000000000003 VPN         "Broken VPN"
`
	services := parseSCUtilNCList(out)
	if len(services) != 3 {
		t.Fatalf("应解析出 3 条,得到 %d:%+v", len(services), services)
	}
	connected := 0
	for _, s := range services {
		if s.Connected {
			connected++
			if s.Name != "Work VPN" {
				t.Errorf("已连接的应是 Work VPN,得到 %q", s.Name)
			}
		}
	}
	if connected != 1 {
		t.Fatalf("只有一条是 Connected,得到 %d", connected)
	}
}
```

> **接线前先核对真实签名**(行号会漂,签名也可能变):`supervisor.LookupRoute` 现在的签名是 `func(ctx context.Context, destination string, ipv6 bool) (RouteSelection, error)`,`RouteSelection` 有 `Interface`;`install.InspectDNSContext(ctx, service string) (DNSStatus, error)`,`DNSStatus` 有 `Servers []string` 与 `Supported bool` —— **`Supported == false` 时要当作「没问过」返回错误**,照 `internal/observe/wire.go` 里 `unsupportedDNSError` 的做法,别把它当成「DNS 不归 bx」。`guardian.SocketPath` 是常量;`guardian.Status` 里 `Core *CoreRuntime` 可能为 nil,`Protection` 是字符串。`CoreRuntime` 上是否有 `TunName` 字段**要现场确认**;没有就退回 `supervisor.FetchRuntimeState(supervisor.SockPath)`(它有 `TunName`),那条路同样不需要 root。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/leakserve/ -count=1 && go build ./... && go vet ./... && go test ./... -count=1`,再逐个跑六个交叉编译
Expected: PASS(注意 `GOOS=linux`/`windows` 只编译不执行 —— `facts_other.go` 的正确性靠 `go vet` 与编译)

- [ ] **Step 5: 变异验证**

| 变异 | 必须转红的测试 | 它防住的生产改动 |
|---|---|---|
| `CollectFacts` 里 `default: facts.IPv6DefaultPresent = observe.Unknown` 改成 `observe.False` | `TestCollectFactsWithNoDepsIsBlind`、`TestCollectFactsPartialFailuresStayIsolated` | 把「没问出来」写成「没有 v6」,Judge 于是说出一句「不可能漏」的假话 |
| `errors.Is(v6Err, ErrNoRoute)` 那支删掉 | `TestNoV6RouteIsObservedAbsenceNotFailure` | 「确知没有 v6 通路」这句诚实的 ok 说不出来了(功能退化,不是安全问题,但会让一台干净机器常年显示 not checked) |
| `lookupInterface` 里 `deps.LookupRoute == nil` 那支改成返回空 `InterfaceRef{}`(不带 Err) | `TestCollectFactsWithNoDepsIsBlind`(若不红则**当场补**对 `.Err` 的断言) | 非 darwin 上「没有这个能力」被压成「查了,没有接口」 |
| `ListVPNServices` 失败时把 `services` 设成一条假数据 | `TestCollectFactsPartialFailuresStayIsolated` | 编造 VPN 名字 |
| DNS 逐个查出口那段的 `if err != nil { continue }` 改成 `facts.DNSServerEgress[server] = ""` | `TestCollectFactsPartialFailuresStayIsolated`(若不红则补一条:出口查失败时该键必须**缺席**) | 空串会被 `judgeDNS` 读成「查到了,出口是空」 |
| `facts_other.go` 的 `LiveFactDeps` 改成填几个返回零值的函数 | 无 Go 测试可覆盖(非 darwin 跑不了) → **靠 `TestCollectFactsWithNoDepsIsBlind` 的语义 + review**;记录在案 | 非 darwin 上伪造事实 |

- [ ] **Step 6: 提交**

```bash
git add internal/leakserve/facts.go internal/leakserve/facts_darwin.go internal/leakserve/facts_other.go internal/leakserve/facts_test.go internal/leakserve/facts_darwin_test.go
git commit -m "$(cat <<'EOF'
feat(leakserve): 本机事实采集,单项失败只让那一项变成「没问出来」

「确知没有 v6 默认路由」与「v6 查询失败」用 ErrNoRoute 分开:前者能支撑一句
诚实的 ok,后者什么都支撑不了。Guardian 的 /v1/status 是 0666,普通用户可读
——这是整个功能不需要 root 的关键一环。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 14: `bx leakcheck` —— 拒绝 root、编排、渲染

**Files:**
- Create: `internal/cli/leakcheck.go`
- Modify: `internal/cli/cli.go`(命令注册,约 80-81 行那一段;**行号会漂,按 `{Name: "leak-check"` 定位**)
- Test: `internal/cli/leakcheck_test.go`

**Interfaces:**
- Consumes: `leakserve.Listen/Options/CollectFacts/LiveFactDeps`、`leakcheck.Judge/Report`
- Produces:
  - `cli` 包内:`guardLeakCheckPrivileges(euid int) error`、`renderLeakCheckReport(rep leakcheck.Report) []string`、`leakCheckAction`
  - 新命令 `bx leakcheck`(flag:`--json`)

- [ ] **Step 1: 写失败测试**

创建 `internal/cli/leakcheck_test.go`:

```go
package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/getbx/bx/internal/leakcheck"
)

// **`sudo bx leakcheck` 必须被拒绝**,不是「照跑但更强大」。
//
// 以 root 跑会让那个 loopback HTTP 服务变成 root 进程的端口,把「一个隐私工具
// 不该为了做体检而让 root 进程对浏览器开 HTTP」这条理由一笔勾销;而且答案不会
// 更准 —— 需要的本机事实一个都不需要 root。
//
// 判据抽成吃 euid 的纯函数,好让这条测试在任何身份下都跑得起来(CI 里跑测试的
// 不是 root,直接判 os.Geteuid() 的话这条测试在开发机上恒为「没走到那一支」)。
func TestLeakCheckRefusesRoot(t *testing.T) {
	err := guardLeakCheckPrivileges(0)
	if err == nil {
		t.Fatal("以 root 跑 bx leakcheck 必须被拒绝")
	}
	msg := err.Error()
	// 必须告诉用户**换成不带 sudo 再跑**,别让他以为是权限不够 —— 后者会让他
	// 去想办法「提更高的权」,正好走反方向。
	if !strings.Contains(msg, "sudo") {
		t.Errorf("拒绝信息必须提到 sudo,得到 %q", msg)
	}
	for _, want := range []string{"without", "bx leakcheck"} {
		if !strings.Contains(msg, want) {
			t.Errorf("拒绝信息必须指出重新以普通用户身份运行(缺 %q):%q", want, msg)
		}
	}
	if strings.Contains(strings.ToLower(msg), "permission denied") ||
		strings.Contains(strings.ToLower(msg), "requires root") {
		t.Errorf("拒绝信息不得读起来像「权限不够」:%q", msg)
	}
	if err := guardLeakCheckPrivileges(501); err != nil {
		t.Fatalf("普通用户身份必须放行,得到 %v", err)
	}
}

// 命令必须注册,而且与既有的 leak-check 并存、用途可区分。
func TestLeakCheckCommandIsRegisteredAlongsideLeakCheck(t *testing.T) {
	app := newApp()
	if !appHasCommand(app, "leakcheck") {
		t.Fatal("app 必须暴露 bx leakcheck")
	}
	if !appHasCommand(app, "leak-check") {
		t.Fatal("既有的 bx leak-check 不许被这一期删掉:MCP 只读工具依赖它")
	}
	newCmd := findAppCommand(app, "leakcheck")
	oldCmd := findAppCommand(app, "leak-check")
	if newCmd.Usage == oldCmd.Usage {
		t.Fatal("两条命令只差一个连字符,Usage 必须能让用户分辨它们")
	}
	if !strings.Contains(newCmd.Usage, "浏览器") {
		t.Errorf("bx leakcheck 的 Usage 应点明它开浏览器页面,得到 %q", newCmd.Usage)
	}
}

// 渲染:三态必须逐字出现,not checked 不许被写成 ok,异常数要说出来。
func TestRenderLeakCheckReport(t *testing.T) {
	rep := leakcheck.Judge(time.Unix(0, 0).UTC(),
		leakcheck.BrowserReport{ExitV4: "5.6.7.8", SRFLX: []string{"1.2.3.4"}},
		leakcheck.LocalFacts{})
	out := strings.Join(renderLeakCheckReport(rep), "\n")
	if !strings.Contains(out, "bad") {
		t.Errorf("渲染必须写出 bad:\n%s", out)
	}
	if !strings.Contains(out, "not checked") {
		t.Errorf("渲染必须写出 not checked:\n%s", out)
	}
	for _, want := range []string{leakcheck.EchoV4URL, leakcheck.STUNURL} {
		if !strings.Contains(out, want) {
			t.Errorf("渲染必须列出联系过的第三方 %q:\n%s", want, out)
		}
	}
}

// **一份全 not checked 的报告不许渲染成一句好听的总结。**
func TestRenderBlindReportDoesNotClaimHealth(t *testing.T) {
	rep := leakcheck.Judge(time.Unix(0, 0).UTC(), leakcheck.BrowserReport{}, leakcheck.LocalFacts{})
	out := strings.ToLower(strings.Join(renderLeakCheckReport(rep), "\n"))
	for _, forbidden := range []string{"no leaks", "all good", "everything is fine", "一切正常"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("全 not checked 的报告不得渲染出 %q:\n%s", forbidden, out)
		}
	}
	if !strings.Contains(out, "not checked") {
		t.Errorf("全 not checked 的报告必须如实说明:\n%s", out)
	}
}
```

> `newApp`、`appHasCommand`、`findAppCommand` 在 `internal/cli/cli_test.go` 里已经存在(见既有的 `leak-check` 注册测试),直接复用;若名字对不上就照那边的写法改。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/cli/ -count=1`
Expected: FAIL — `undefined: guardLeakCheckPrivileges` / `undefined: renderLeakCheckReport`;命令未注册

- [ ] **Step 3: 最小实现**

创建 `internal/cli/leakcheck.go`:

```go
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/getbx/bx/internal/leakcheck"
	"github.com/getbx/bx/internal/leakserve"
	"github.com/urfave/cli/v2"
)

func leakcheckFlags() []cli.Flag {
	return []cli.Flag{
		&cli.BoolFlag{Name: "json", Usage: "输出机器可读结果"},
	}
}

// guardLeakCheckPrivileges 拒绝 root。
//
// **不是「照跑但更强大」。** 以 root 跑会让那个 loopback HTTP 服务变成 root
// 进程的端口 —— 一个隐私工具不该为了做体检而让 root 进程对浏览器开 HTTP。
// 而且答案不会更准:需要的本机事实(route -n get / scutil / networksetup /
// Guardian 的 0666 socket)一个都不需要 root。
//
// 吃 euid 而不是自己去读,是为了让它在任何身份下都可测。
func guardLeakCheckPrivileges(euid int) error {
	if euid != 0 {
		return nil
	}
	return errors.New(
		"bx leakcheck 不能用 sudo 运行。它需要的本机事实一个都不需要 root," +
			"而以 root 打开一个对浏览器开放的本机端口会把这个检测本身变成风险。\n" +
			"请去掉 sudo,直接运行:bx leakcheck  (run it again without sudo)")
}

func leakcheckAction(c *cli.Context) error {
	if err := guardLeakCheckPrivileges(os.Geteuid()); err != nil {
		return cli.Exit(err.Error(), 1)
	}

	ctx := c.Context
	facts := leakserve.CollectFacts(ctx, leakserve.LiveFactDeps())

	srv, err := leakserve.Listen(leakserve.Options{
		Judge: func(browser leakcheck.BrowserReport) leakcheck.Report {
			return leakcheck.Judge(time.Now(), browser, facts)
		},
	})
	if err != nil {
		return cli.Exit("起不了本机检测服务:"+err.Error(), 1)
	}
	defer func() { _ = srv.Close() }()
	go srv.Serve()

	if !c.Bool("json") {
		fmt.Println("bx leakcheck")
		fmt.Println("正在打开浏览器页面。页面会先列出要联系的第三方,点「Run the check」才开始。")
		fmt.Println("入口(浏览器没自动打开就手动粘贴):", srv.URL())
		fmt.Println("最多等待 2 分钟,之后本机服务会自己关闭。")
	}
	if err := openBrowserURL(ctx, srv.URL()); err != nil {
		// 打不开浏览器不是失败:URL 已经打印出来了,用户可以自己粘。
		if !c.Bool("json") {
			fmt.Println("没能自动打开浏览器(", err, "),请手动打开上面那个地址。")
		}
	}

	report := srv.Wait(ctx)

	if c.Bool("json") {
		return json.NewEncoder(os.Stdout).Encode(report)
	}
	for _, line := range renderLeakCheckReport(report) {
		fmt.Println(line)
	}
	return nil
}

// renderLeakCheckReport 把成品结论渲染成终端里的行。
//
// **不做任何总结性的好话。** 一份全 not checked 的报告渲染出「没有发现泄漏」
// 就是设计风险四那种最坏的失败:把「没跑成」说成「一切正常」。
func renderLeakCheckReport(rep leakcheck.Report) []string {
	lines := []string{
		"",
		"Contacted: " + rep.Endpoints.EchoV4 + " , " + rep.Endpoints.EchoV6 + " , " + rep.Endpoints.STUN,
		"",
	}
	for _, f := range rep.Findings {
		lines = append(lines, fmt.Sprintf("[%s] %s", f.Verdict, f.Title))
		lines = append(lines, "    "+f.Summary)
		for _, e := range f.Evidence {
			lines = append(lines, "      · "+e)
		}
		lines = append(lines, "")
	}
	if len(rep.Evidence) > 0 {
		lines = append(lines, "Observed:")
		for _, e := range rep.Evidence {
			lines = append(lines, "  · "+e)
		}
		lines = append(lines, "")
	}
	notChecked := 0
	for _, f := range rep.Findings {
		if f.Verdict == leakcheck.NotChecked {
			notChecked++
		}
	}
	lines = append(lines, fmt.Sprintf("%d finding(s) marked bad, %d not checked.", rep.AnomalyCount, notChecked))
	lines = append(lines, "Nothing was stored: bx keeps no history of this check.")
	return lines
}

// openBrowserURL 已存在于 internal/cli/cli.go(webrtc-check 那条路在用),
// 这里直接复用,不再写第二份。若它被移动/改名,按同名函数定位。
var _ = func() struct{} { _ = runtime.GOOS; _ = exec.Command; _ = strings.TrimSpace; _ = context.Background; return struct{}{} }()
```

> **上面那行 `var _ = func() ...` 是占位噪声,不要写进代码。** 正确做法:`leakcheck.go` 只 import 它真正用到的包(`context` 若未直接使用就删掉,`os/exec`/`runtime`/`strings` 同理),`openBrowserURL` 直接调用即可 —— 它是同包函数。写完跑 `go vet` 会把多余 import 报出来。

在 `internal/cli/cli.go` 的命令表里,紧挨 `{Name: "leak-check", ...}` 之后加一行,并把既有两条的 Usage 改得能互相区分:

```go
			{Name: "webrtc-check", Usage: "检查 WebRTC 泄漏风险(只读,不开页面)", Flags: webrtcCheckFlags(), Action: webrtcCheckAction},
			{Name: "leak-check", Usage: "聚合网络路径泄漏风险(只读,纯本机判断)", Flags: leakCheckFlags(), Action: leakCheckAction},
			{Name: "leakcheck", Usage: "泄漏检测:开浏览器页面,把浏览器与本机两半事实对起来(普通用户身份)", Flags: leakcheckFlags(), Action: leakcheckAction},
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/cli/ -count=1 && go build ./... && go vet ./... && go test ./... -count=1`,再跑六个交叉编译
Expected: PASS

> **注意**:`internal/cli` 自 2026-08-09 起在 ubuntu/windows 上编不过(`d8f1d9c`),所以这个包的测试在三条 CI 腿里只有 macOS 那条真跑。交叉编译**必须**手工跑,不能指望 CI。

- [ ] **Step 5: 变异验证**

| 变异 | 必须转红的测试 | 它防住的生产改动 |
|---|---|---|
| `guardLeakCheckPrivileges` 改成 `return nil` | `TestLeakCheckRefusesRoot` | `sudo bx leakcheck` 照跑,root 进程对浏览器开 HTTP |
| `leakcheckAction` 里 `guardLeakCheckPrivileges(os.Geteuid())` 那两行删掉 | **不转红**(测试打在纯函数上)→ **当场补一条守卫**:读 `leakcheck.go` 源码断言 `leakcheckAction` 的函数体里出现 `guardLeakCheckPrivileges(` —— 判据在纯函数里,而**接线**必须另有证明(本仓库的教训:证明判定存在 ≠ 证明它被调用了) | 闸门装了但没接上 |
| 拒绝信息里 `sudo` / `without` 去掉 | `TestLeakCheckRefusesRoot` | 用户读成「权限不够」,去找更高的权限 |
| `renderLeakCheckReport` 末尾加一行 `if rep.AnomalyCount == 0 { lines = append(lines, "No leaks found.") }` | `TestRenderBlindReportDoesNotClaimHealth` | 「异常数为 0」被渲染成「没有泄漏」,而它可能是三条全没检查 |
| 渲染里 `f.Verdict` 换成只在 `Bad` 时打印 | `TestRenderLeakCheckReport`、`TestRenderBlindReportDoesNotClaimHealth` | not checked 从输出里消失 = 看起来一切正常 |
| 新命令的 `Usage` 改成与 `leak-check` 一致 | `TestLeakCheckCommandIsRegisteredAlongsideLeakCheck` | 两条只差连字符的命令用户分不清 |
| 删掉 `{Name: "leak-check"...}` 那一行 | `TestLeakCheckCommandIsRegisteredAlongsideLeakCheck` | 顺手删掉 MCP 依赖的命令 |

- [ ] **Step 6: 提交**

```bash
git add internal/cli/leakcheck.go internal/cli/cli.go internal/cli/leakcheck_test.go
git commit -m "$(cat <<'EOF'
feat(cli): bx leakcheck,以普通用户身份跑;sudo 直接拒绝

拒绝信息说的是「去掉 sudo 再跑」,不是「权限不够」——后者会让用户去想办法
提更高的权,正好走反方向。渲染刻意不做任何总结性好话:异常数为 0 可能是
三条全没检查。既有的 leak-check / webrtc-check 一行未动,只把 Usage 改得
能互相分辨。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 15: macOS 菜单入口项 `Check for leaks ↗`

**这一步要改一条既有守卫的白名单,必须先读懂它再动。** `internal/cli/cli_test.go` 的 `TestMacMenuSpawnsOnlyFromTheActionPath` 用一条闭合的调用链证明「进程创建只能沿这条链发生」:`Process` 只许出现在 `runBx` / `runPrivilegedScriptOffMainThread`;`runBx(` 只许被 `cliRuns` 调;`cliRuns(` 只许被 `ensureCLIUsable` 调;`ensureCLIUsable(` 只许被 `setUpBx` / `updateBx` 调。新菜单项要执行 `bx leakcheck`,所以链的最后一环必须**显式**扩到 `checkForLeaks`。

**为什么走 `runBx` 而不是 `openTerminal`**(`runDoctor` 用的是后者):`bx leakcheck` 的产出是浏览器页面,终端里没有用户必须读的东西,弹一个 Terminal 窗口纯属噪声;而 `runBx` 的退出码让菜单能在失败时给一句可操作的提示。代价就是这次白名单扩容 —— 它是**有意的、被记录的**,不是顺手加的。

**Files:**
- Modify: `apps/macos/BxMenu/Sources/BxMenu/main.swift`
- Modify: `internal/cli/cli_test.go`(白名单 + 一条新守卫)

- [ ] **Step 1: 写失败测试**

在 `internal/cli/cli_test.go` 末尾新增:

```go
// 菜单必须有一个泄漏检测入口,而且它跑的是**不提权**的 bx leakcheck。
//
// 这条守卫住在 Go 侧,因为 main.swift 要 AppKit、编不进
// scripts/test-macos-menu.sh —— 在那里把它改成 runPrivileged 不会有任何编译
// 错误,也不会有任何 Swift 测试转红。
//
// 读不懂 main.swift 时**必须响亮失败**(本文件的守卫被绕过八次,每一次都是
// 「安静放过」)。
func TestMacMenuLeakCheckRunsUnprivileged(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatal(err)
	}
	text := swiftCodeOnly(string(source))

	if !strings.Contains(text, "Check for leaks") {
		t.Fatal("菜单必须有 Check for leaks 入口项")
	}
	body, ok := swiftFunctionBody(text, "private func checkForLeaks(")
	if !ok {
		t.Fatal("找不到 checkForLeaks 的函数体 —— 本守卫读不懂现在的 main.swift,请连同它一起重写")
	}
	// 判据:必须跑 leakcheck 子命令。
	if !strings.Contains(body, `"leakcheck"`) {
		t.Error("checkForLeaks 必须执行 bx leakcheck")
	}
	// **绝不提权。** 以 root 跑会让那个 loopback 服务变成 root 进程的端口,
	// 而 bx leakcheck 自己也会拒绝 —— 用户会看到一个弹了密码框然后失败的操作。
	for _, forbidden := range []string{"runPrivileged", "osascript", "administrator privileges", "sudo"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("checkForLeaks 不得提权(出现了 %q):bx leakcheck 以 root 跑会被自己拒绝,"+
				"用户只会看到一个弹了密码框然后失败的操作", forbidden)
		}
	}
	// 必须在后台队列跑:leakcheck 最长 2 分钟,压在主线程上会把整个菜单冻住
	// ——阶段①那次 71 分钟的冻结就是这么来的。
	if !strings.Contains(body, "DispatchQueue.global(") {
		t.Error("checkForLeaks 必须在后台队列执行:leakcheck 最长跑 2 分钟," +
			"压在主线程上会把菜单冻住")
	}
}
```

并修改既有的 `TestMacMenuSpawnsOnlyFromTheActionPath`,把两条 `callerRule` 的白名单扩容(**只扩这两条,别的一个字不动**):

```go
		{
			pattern: call("runBx("), label: "runBx(", callers: []string{"cliRuns", "checkForLeaks"},
			why: "正当的 spawn 有两处:动作路径上那次 exec 探测(「装了但跑不起来」只有真执行" +
				"一次才知道),以及 Check for leaks 那个用户点击触发的、不提权的 bx leakcheck;" +
				"轮询路径上的任何 spawn 都是让 UI 变回第三个控制面",
		},
		...
		{
			pattern: call("ensureCLIUsable("), label: "ensureCLIUsable(", callers: []string{"setUpBx", "updateBx", "checkForLeaks"},
			why: "闸门只许出现在真要 shell out 到 CLI 的动作里;出现在别处就意味着有别的路径通向 spawn",
		},
```

外加一条新的 `callerRule`,把新入口也锁成「只能由用户点击触发」:

```go
		{
			pattern: call("checkForLeaks("), label: "checkForLeaks(", callers: nil,
			why: "它是 #selector 的菜单入口,只能由用户点击触发,不该被任何代码调用 —— " +
				"尤其不能被轮询路径调,那会变成每隔几秒起一个 loopback 服务",
		},
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/cli/ -count=1`
Expected: FAIL — `TestMacMenuLeakCheckRunsUnprivileged` 报「菜单必须有 Check for leaks 入口项」

- [ ] **Step 3: 最小实现**

在 `main.swift` 的 `rebuildMenu()` 里,把入口加在诊断入口那一组(`View Logs` / `Run Doctor` 之后)。**在那两个 `switch state` 之后、`switch state { case .connected, .warning: menu.addItem(.separator()) ... }` 之前**插入一行 —— 它对**所有**状态都在场,因为泄漏检测的价值恰恰在保护关着、或别的 VPN 在跑的时候:

```swift
        // Check for leaks 在**每一个**状态下都在场。这是刻意的:这个功能的
        // 立身之本就是「保护关着也有用」——只在 .connected 里给它,等于把它
        // 藏在最不需要它的那个状态里。
        menu.addAction("Check for leaks ↗", symbol: "magnifyingglass", target: self, action: #selector(checkForLeaks))
```

加动作(放在 `runDoctor` 附近):

```swift
    /// 打开泄漏检测。**不提权** —— bx leakcheck 自己会拒绝 root,提权只会换来
    /// 一个弹了密码框然后失败的操作。浏览器由 bx 自己打开,菜单只负责起它。
    ///
    /// 在后台队列跑:leakcheck 最长等 2 分钟,压在主线程上会把整个菜单冻住
    /// (阶段①那次 71 分钟的冻结就是这么来的)。
    @objc private func checkForLeaks() {
        guard ensureCLIUsable() else { return }
        if leakCheckInFlight { return }
        leakCheckInFlight = true
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            guard let self else { return }
            let result = self.runBx(["leakcheck"])
            DispatchQueue.main.async {
                self.leakCheckInFlight = false
                if result.exitCode != 0 {
                    self.showMessage(
                        "Leak Check Did Not Start",
                        "bx could not start the leak check. Run `bx leakcheck` in Terminal to see why."
                    )
                }
            }
        }
    }
```

在类的属性区加:

```swift
    /// 同一时刻只跑一次:每次点击都会起一个新的 loopback 服务,连点会攒一堆。
    private var leakCheckInFlight = false
```

> **接线前先核对**:`runBx` 的返回类型(`CommandResult`)里表示退出码的字段名要现场确认(`main.swift` 约 1542 行),`showMessage` 的签名同理。`menu.addAction` 的 `symbol:` 参数存在与否也要照现有调用抄。

**Swift 测试登记**:本任务**没有**新增 `apps/macos/BxMenu/Tests/*.swift` 文件(`checkForLeaks` 在 `main.swift` 里,编不进 `scripts/test-macos-menu.sh`),所以 `scripts/test-macos-menu.sh` 与 `TestEveryMacOSMenuTestSuiteIsRegistered` 都不需要改 —— 但**必须跑一遍确认它仍绿**。若后续把判定抽进兄弟文件(推荐做法),那个新文件与它的 `Tests/*.swift` 必须同时登记进脚本,否则它一次都不会跑而 CI 全绿。

- [ ] **Step 4: 跑测试确认通过**

Run:
```
go test ./internal/cli/ -count=1 && go build ./... && go vet ./... && go test ./... -count=1
swift build --package-path apps/macos/BxMenu
bash scripts/test-macos-menu.sh
```
Expected: 全部 PASS;`test-macos-menu.sh` 末尾必须打印 `macOS menu tests passed`(**看输出,不只看退出码** —— 脚本提前 `exit 0` 时退出码也是 0)

- [ ] **Step 5: 变异验证**

| 变异 | 必须转红的测试 | 它防住的生产改动 |
|---|---|---|
| `checkForLeaks` 里 `runBx(["leakcheck"])` 改成 `runPrivileged("'\(bxPath)' leakcheck")` | `TestMacMenuLeakCheckRunsUnprivileged`、`TestMacMenuSpawnsOnlyFromTheActionPath` | 提权跑,用户看到一个弹了密码框然后失败的操作 |
| 去掉 `DispatchQueue.global(` 改成主线程直跑 | `TestMacMenuLeakCheckRunsUnprivileged` | 菜单冻住最长 2 分钟,一个字都没有 |
| 把 `menu.addAction("Check for leaks ↗"...)` 挪进 `case .connected:` 那一支 | `TestMacMenuLeakCheckRunsUnprivileged` 只查「文件里有这个字符串」→ **不转红** → **当场补一条**:照 `TestMacMenuQuitActionPresentInEveryState` 的形状,按**函数体花括号深度**断言它出现在 `rebuildMenu()` 顶层而不是某个 case 里(只数出现次数抓不到挪进去,次数还是 1) | 把「保护关着也有用」的功能藏进只有保护开着才有的菜单 |
| 在 `loadState` 里加一句 `checkForLeaks()` | `TestMacMenuSpawnsOnlyFromTheActionPath`(新加的那条 `callerRule`) | 轮询路径每几秒起一个 loopback 服务 |
| 把 `runBx(` 的白名单改回只有 `cliRuns` | `TestMacMenuSpawnsOnlyFromTheActionPath`(会把 `checkForLeaks` 报出来)—— 证明白名单确实在起作用,不是摆设 | — |
| 删掉 `leakCheckInFlight` 判断 | 无测试覆盖 —— 记录在案,靠 review;连点会攒一堆 loopback 服务(每个都有自己的 2 分钟超时) | 资源泄漏 |

> 第三行是**发现缺口的变异**,必须当场补测试再继续。

- [ ] **Step 6: 提交**

```bash
git add apps/macos/BxMenu/Sources/BxMenu/main.swift internal/cli/cli_test.go
git commit -m "$(cat <<'EOF'
feat(menu): 加一个不提权的 Check for leaks 入口,所有状态下都在场

只在 .connected 里给它等于把它藏在最不需要它的状态里——这个功能的立身之本
就是「保护关着也有用」。spawn 链的白名单为此显式扩容(runBx 与
ensureCLIUsable 各加一个调用方),并给新入口自己加一条「只能由点击触发」的
规则,免得它被轮询路径调成每几秒起一个 loopback 服务。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

## 自检(写完计划后按规格逐条对过)

**规格覆盖**:

| 设计里的要求 | 落在哪 |
|---|---|
| 一次性 loopback 服务、开浏览器、页面采集、Go 判断 | Task 8-14 |
| **普通用户身份跑,不是 root**;`sudo bx leakcheck` 必须被拒绝 | Task 14(纯函数 + 接线守卫)、Task 15(菜单不提权) |
| 四道自我约束各一条测试 | Task 9(绑 loopback+随机端口)、Task 8(token)、Task 10(Origin/Host)、Task 11(一次性+2 分钟) |
| 除本次检测所需之外不提供任何端点 | Task 8 `TestOnlyTwoEndpointsExist` |
| 检查项:默认路由 v4/v6、IPv6 暴露、DNS 归谁、公网出口 v4/v6、WebRTC srflx | Task 5/6/7(结论)+ Task 7 证据清单(出口与路由归属) |
| 三条组合规则各有测试,删掉必须转红 | Task 4/5/6 各自的变异表 |
| 三态复用菜单词汇,not checked 不计入异常 | Task 1 |
| 绝不推断:页面关掉/STUN 被挡/没网/超时一律 not checked | Task 3(空上报)、4(STUN 被挡/零 srflx)、5(回声失败)、6(采集失败)、11(硬超时) |
| `utun4` 翻成人话,翻不出就说翻不出 | Task 7 |
| 每条结论都能展开看依据 | Task 4/5/6 的 evidence 断言 + Task 7 证据清单 + Task 12 页面 `<details>` |
| 联网之前页面先说要联系谁 | Task 2(常量)、Task 12(`TestPageDisclosesEndpointsVerbatim`)、Task 14(终端也打印) |
| 第三方选取标准(v4/v6-only、小而稳、不在 china 列表、一个 STUN 只取 srflx) | Task 2(含理由与实测证据) |
| 不留存 | 全程无写盘;Task 12/14 的文案明说 |
| 不做非 darwin | Task 13 桩 |
| 不驱动浏览器,只打开一个页面 | Task 14 复用既有 `openBrowserURL` |
| 菜单只加一个入口项,不动那三行 | Task 15;Global Constraints 里写死 |
| 风险一(新攻击面) | Task 8-11 四个独立任务 |
| 风险二(恒绿更糟) | Global Constraints 的常规;Task 7 把两项降级成 evidence |
| 风险三(第三方暴露必须可见) | Task 2/12/14 |
| 风险四(全零 fixture) | Task 3 独立任务 + 每个后续任务的变异表都带一条 |
| JS 保持愚蠢靠结构 | Task 12(两条守卫 + `pageData` 不含本机事实) |

**未覆盖 / 明确不做**:菜单那三行的接线(设计明写本期不做);结果留存(不做);用户态 VPN 按进程识别(Task 7 注释里记为下一期);既有 `leak-check` 的两个缺陷(记在「与既有代码的关系」,单独立项)。

**真机验收(无人执行,列出来免得被当成已验)**:① 普通用户跑 `bx leakcheck`,浏览器打开、页面先列出三个第三方、点了才联网;② `sudo bx leakcheck` 被拒且提示可操作;③ 关掉标签页后 2 分钟内端口消失(`lsof -iTCP -sTCP:LISTEN` 看不到);④ 菜单项在保护**关着**时也能点、不弹密码框;⑤ 开着另一个 VPN 时三条结论至少有一条不是 not checked ——**若真机上三条恒为 ok,按 Global Constraints 那条规矩,该删的删**。
