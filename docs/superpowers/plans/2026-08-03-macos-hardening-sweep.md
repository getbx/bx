# macOS 收尾小扫(损坏布局守卫 / doctor 共存检测 / server up --open-ufw / 文档与 UX)Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 四件独立小事一次扫掉:① 损坏的统一布局不再被 `setup`/`update` 误判为 legacy 而覆盖 bridge;② `bx doctor` 接入已实现的 VPN/Tailscale 共存检测(设计稿唯一未落地的缺口);③ `bx server up/install` 支持 `--open-ufw`(协议感知 tcp/udp);④ README 补两个验收脚本 + Repair 弹窗按钮文案。

**Architecture:** 全部是在既有结构上的窄插入:守卫复用 `unifiedTeardownNeeded` 的存在性检查(其注释就是本 bug 的文档);doctor 复用 `collectPlatformChecks` + 既有 `recoveryDoctorCheck` 的插入模式;`--open-ufw` 复用 `openUFW`,新增纯函数把协议映射成 ufw 规则表。无任何数据面/Guardian 改动。

**Tech Stack:** Go 1.26(internal/cli)、Swift 5.9(一个参数)、README。

## Global Constraints

- 不执行 bx/sudo/launchctl/ufw;涉及系统的只写代码与测试。
- 验证:`go build ./... && go vet ./... && go test ./internal/cli/`;`GOOS=linux GOARCH=amd64 go build -o /dev/null ./...` + `GOOS=windows GOARCH=amd64 go build -o /dev/null ./...`;`gofumpt -l` 触碰文件为空;Swift 改动跑 `bash scripts/test-macos-menu.sh` + `swift build`。
- 提交:中文 conventional commits,直接 master,结尾 `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`。
- doctor 共存检测**只读**(design 原文:"do not add setup/up/down side effects"),误报宁 `info` 不 `warn`。
- `--open-ufw` 语义与既有 share/invite 一致:纯 flag 门控、无确认弹窗、失败即返错;capabilities 的 SafeNotes 必须同步声明。

---

## File Structure

- Modify: `internal/cli/unifiedlayout.go` + `unifiedlayout_test.go` — 新增 degraded 判定(纯函数)。
- Modify: `internal/cli/cli.go` — setupAction 守卫 + 收尾行分支化(:3542-3559);doctorAction(:1515-1526)与 collectClientDoctor(:2194)接入共存检测;serverInstallFlags(:447)加 flag;serverInstallAction(:771 附近)/serverUpAction(:1338)接 ufw;capabilities 表(:2107 风格)补 SafeNotes。
- Modify: `internal/cli/update.go` — legacy 路径前的 degraded 守卫(:244 else 分支)。
- Create: `internal/cli/serverufw.go` + `serverufw_test.go` — `serverUFWRules` 纯函数。
- Modify: `apps/macos/BxMenu/Sources/BxMenu/main.swift` — `runEmbeddedInstaller` 加 `confirmButton` 参数(:518/:522/:505/:512)。
- Modify: `README.md` — :282 后插验收脚本段;:359 后补命令表两行。

---

### Task 1: 损坏统一布局守卫(setup/update 不再覆盖 bridge)

**Files:**
- Modify: `internal/cli/unifiedlayout.go`、`internal/cli/unifiedlayout_test.go`
- Modify: `internal/cli/cli.go`(setupAction :3542-3559)
- Modify: `internal/cli/update.go`(updateAction :244 的 else/legacy 入口前)

**Interfaces:**
- Produces(unifiedlayout.go 追加):

```go
// unifiedLayoutDegraded:统一布局产物存在但健康检查不过(半途安装/损坏)。
// 此状态下 setup/update 绝不能回退 legacy 路径(SelfInstall/ReplaceBinary 会覆盖 bridge)。
func unifiedLayoutDegraded() bool {
	return unifiedLayoutDegradedWith(runtime.GOOS,
		unifiedTeardownNeeded(darwinRuntimeRootPath, darwinAppBundlePath),
		runtimedir.Installed(runtimedir.Root))
}

func unifiedLayoutDegradedWith(goos string, artifactsPresent, healthy bool) bool {
	return goos == "darwin" && artifactsPresent && !healthy
}

const unifiedRepairHint = "统一安装已损坏(runtime/current 不完整):请打开 /Applications/Bx.app 点 Install bx 修复,或运行 sudo /Applications/Bx.app/Contents/Resources/bx-cli app-install。为避免覆盖 CLI bridge,本命令不会回退 legacy 安装。"
```

- setupAction:`else` 分支(:3547)开头插 `if unifiedLayoutDegraded() { return errors.New(unifiedRepairHint) }`。
- updateAction:进入 legacy 下载路径前(update.go :244 `if unifiedLayoutActive()` 之后、FindAsset 之前)插同样守卫。
- 顺手修 Phase-1 挂账的收尾行(:3559):把 `✅ bx 已装到 %s、写好配置 %s…` 拆进两个分支——unified 分支打印 `✅ 配置已写好 %s,Guardian 已指向统一 runtime。下一步:sudo bx up`(不再谎称装到 /usr/local/bin);legacy 分支保留原句。

- [ ] **Step 1: 写失败测试**(unifiedlayout_test.go 追加)

```go
func TestUnifiedLayoutDegradedWith(t *testing.T) {
	cases := []struct {
		goos              string
		artifacts, healthy bool
		want              bool
	}{
		{"darwin", true, false, true},   // 损坏:产物在、健康检查挂
		{"darwin", true, true, false},   // 健康统一布局
		{"darwin", false, false, false}, // 纯 legacy(无产物)
		{"linux", true, false, false},   // 非 darwin 恒 false
	}
	for _, tc := range cases {
		if got := unifiedLayoutDegradedWith(tc.goos, tc.artifacts, tc.healthy); got != tc.want {
			t.Fatalf("(%s,%v,%v)=%v want %v", tc.goos, tc.artifacts, tc.healthy, got, tc.want)
		}
	}
}

func TestUnifiedRepairHintMentionsRepairPath(t *testing.T) {
	for _, want := range []string{"Bx.app", "app-install", "bridge"} {
		if !strings.Contains(unifiedRepairHint, want) {
			t.Fatalf("hint missing %q", want)
		}
	}
}
```

- [ ] **Step 2: 跑红** — `go test ./internal/cli/ -run 'UnifiedLayoutDegraded|UnifiedRepairHint'`。
- [ ] **Step 3: 实现**(上述代码;两处守卫插入;收尾行分支化——unified 分支把成功行放进 if 内,legacy 保原样,`postSetupAutostart` 调用位置不动)。
- [ ] **Step 4: 跑绿** — `go build ./... && go test ./internal/cli/ && go vet ./...` + linux/windows 交叉编译。
- [ ] **Step 5: Commit**

```bash
git add internal/cli/
git commit -m "fix(cli): 损坏统一布局时 setup/update 拒绝回退 legacy(防覆盖 bridge)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2: `bx doctor` 接入 VPN/Tailscale 共存检测

**Files:**
- Modify: `internal/cli/cli.go`(doctorAction :1515-1526、collectClientDoctor :2194)
- Test: `internal/cli/cli_test.go`(collectClientDoctor 的 JSON 用例仿写)

设计稿(2026-07-12-macos-tunnel-coexistence-check)唯一未落地缺口:`collectPlatformChecks` 只被 leak-check 调用,doctor 完全没有共存视角。插入模式照抄既有 `recoveryDoctorCheck`(:1520-1524):

- doctorAction:在 darwin recovery 块之后、`return nil` 之前追加:

```go
for _, check := range collectPlatformChecks(c.Context) {
	doctorLine(check.Status, check.Name, check.Detail)
	if check.Hint != "" {
		doctorLine("hint", check.Name, check.Hint)
	}
}
```

(非 darwin 下 `collectPlatformChecks` 只返回 terminal-proxy 检查,行为自然收敛;`c.Context` 以现场 doctorAction 取 ctx 的方式为准。)
- collectClientDoctor(JSON 路径):同样把 `collectPlatformChecks(ctx)` 的结果 append 进其 checks 列表(读现场结构后按同名字段填充)。
- **只读不变量**:不新增任何 exec 之外的副作用;沿用 5s 超时的现有实现。

- [ ] **Step 1: 写失败测试** — 找到现有 collectClientDoctor 测试(grep `collectClientDoctor` cli_test.go)仿写:非 darwin 下 JSON checks 至少包含 terminal-proxy 检查名(如 `terminal_proxy` 系,读 platform_check_common.go 确认真实 Name);断言 doctor JSON 里出现该 check(证明 platform checks 进了 doctor)。
- [ ] **Step 2: 跑红**;**Step 3: 实现**;**Step 4: 跑绿** — `go build ./... && go test ./internal/cli/ && go vet ./...`。
- [ ] **Step 5: Commit**

```bash
git add internal/cli/
git commit -m "feat(cli): doctor 接入 VPN/Tailscale 共存检测(复用 leak-check 平台检查)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: `bx server up/install --open-ufw`(协议感知)

**Files:**
- Create: `internal/cli/serverufw.go`、`internal/cli/serverufw_test.go`
- Modify: `internal/cli/cli.go`(serverInstallFlags :447、serverInstallAction 成功尾部 :771 附近、serverUpAction :1338、capabilities 表 server up/install 条目)

**Interfaces:**
- Produces(serverufw.go;先读 serverConfig 结构确认字段名,以现场为准):

```go
// serverUFWRules 由 server 配置推导需要放行的 ufw 规则("443/tcp" 形式)。
// reality 默认附带 hysteria2(tcp+udp 同端口);--tcp-only 只 tcp;hysteria2 只 udp;brook 按 listen 端口 tcp。
func serverUFWRules(cfg serverConfig) []string
func openUFWRules(rules []string) error // 逐条 exec ufw allow <rule>,任一失败即返错
```

- flag:`serverInstallFlags()` 追加 `&cli.BoolFlag{Name: "open-ufw", Usage: "安装后自动执行 ufw allow(reality+hys2 会同时放行 tcp 与 udp)"}`(install 与 up 共用同一 flags,自动双落)。
- serverInstallAction:成功打印防火墙提示(:771-773)之后,`if c.Bool("open-ufw") { openUFWRules(serverUFWRules(cfg)) }`,成功打印已放行规则,失败返错。
- serverUpAction:「已安装直接启动」分支(:1340)也要生效——读盘配置(找现场 load serverConfig 的函数)后同样应用 flag;EnableServer 之后打印。
- capabilities:server up/install 条目的 `Arguments` 加 `"--open-ufw"`、`SafeNotes` 加 `"May change firewall only when --open-ufw is passed."`(与 share/invite 条目 :2107-2151 逐字同款)。

- [ ] **Step 1: 写失败测试**(serverufw_test.go;cfg 构造按现场 serverConfig 字段)

```go
func TestServerUFWRules(t *testing.T) {
	// 用例(字段名以现场 serverConfig 为准,期望值固定):
	// reality 默认(port 443, 附带 hys2)      → ["443/tcp", "443/udp"]
	// reality --tcp-only (port 8443)          → ["8443/tcp"]
	// hysteria2 (port 443)                    → ["443/udp"]
	// brook listen ":9999"                    → ["9999/tcp"]
	// port<=0 时按 443 缺省(与 serverFirewallHintFor 的缺省一致)
}
```

- [ ] **Step 2: 跑红**;**Step 3: 实现**(`openUFWRules` 复用 `openUFW` 的 exec 风格但接受完整 "port/proto" 规则;原 `openUFW(listen)` 保留不动,share/invite 不受影响);**Step 4: 跑绿** — `go build ./... && go test ./internal/cli/ -run 'ServerUFW|Capabilities' && go vet ./...` + 交叉编译。
- [ ] **Step 5: Commit**

```bash
git add internal/cli/
git commit -m "feat(cli): server up/install 支持 --open-ufw(协议感知 tcp/udp)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4: README 验收脚本 + Repair 按钮文案

**Files:**
- Modify: `README.md`(:282 后插段;:359 后补表行)
- Modify: `apps/macos/BxMenu/Sources/BxMenu/main.swift`(:518 `runEmbeddedInstaller` 加 `confirmButton: String` 参数,:522 用之;:505 传 "Install"、:512 传 "Repair")

**Steps:**

- [ ] **Step 1: README** — 在 :282(reconnect-check 块)之后、「日常使用:」之前插入(沿用左右段落的 dry-run 文风):

```markdown
统一安装与统一更新也各有真机验收脚本,默认 dry-run(只在临时目录打包并打印计划,零系统改动),显式 `--execute --yes` 才真正执行:

```bash
bash scripts/darwin-unified-install-check.sh          # 统一安装演练(dry-run)
sudo bash scripts/darwin-unified-install-check.sh --execute --yes
bash scripts/darwin-unified-update-check.sh           # 统一更新+自动回滚演练(dry-run)
sudo bash scripts/darwin-unified-update-check.sh --execute --yes
```
```

命令表 :359 后补两行(`| \`scripts/darwin-unified-install-check.sh\` | 统一安装真机验收(默认 dry-run) |` 与 update-check 同款)。
- [ ] **Step 2: Swift** — 三行改动(签名加参、addButton 用参、两个调用点传值);跑 `bash scripts/test-macos-menu.sh` + `cd apps/macos/BxMenu && swift build` 绿。
- [ ] **Step 3: 自查** — `grep -n "darwin-unified" README.md` 两处都在;`grep -n "confirmButton" apps/macos/BxMenu/Sources/BxMenu/main.swift` 四处(签名+使用+两调用)。
- [ ] **Step 4: Commit**

```bash
git add README.md apps/macos/BxMenu/
git commit -m "docs+ux(macos): README 收录验收脚本;Repair 弹窗按钮改为 Repair

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## 验收(自动即可,无真机门)

四个任务均为纯代码/文档,全量验证命令通过即完成;共存检测与守卫的真机表现并入下次 macOS 上机顺带观察(doctor 多几行输出、损坏布局时 setup 报修复指引)。
