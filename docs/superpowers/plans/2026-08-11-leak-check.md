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
