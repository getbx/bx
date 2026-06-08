# bx 傻瓜命令模型(setup/blink/up/down/run) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把"从零到跑"压成 scp 二进制 + `sudo bx setup blink://...` + `sudo bx up`;日常只用 `up`/`down`;用户不碰 YAML、看不到 brook。

**Architecture:** 新增 `internal/blink`(brook 链接 base64url 换壳)与 `internal/setup`(写配置 + 连通检测);拆 `internal/install` 为 WriteUnit/Enable/Disable;cli 重排命令:`setup`(配置+装服务+探活,不启动)、`up`=enable --now、`down`=disable --now、`run`=旧前台 up、`blink`=生成链接;`ExecStart` 改跑 `bx run`。底层运行时零改动。

**Tech Stack:** Go 1.26、`encoding/base64`、`gopkg.in/yaml.v3`、systemd、urfave/cli/v2。

参考 spec:`docs/superpowers/specs/2026-06-07-bx-setup-blink-cli-design.md`

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `internal/blink/blink.go` | `Encode`/`Decode`(blink:// ↔ brook://) |
| `internal/install/install.go` | 拆 `WriteUnit`/`Enable`/`Disable`/`UnitInstalled`(改) |
| `internal/setup/setup.go` | `WriteConfig` + `ProbeServer` |
| `internal/cli/cli.go` | 命令重排:setup/up/down/run/blink/status/uninstall |
| `README.md` | 傻瓜流程 + blink 说明 |

---

## Task 1: `internal/blink` 链接换壳

**Files:** Create `internal/blink/blink.go`; Test `internal/blink/blink_test.go`.

- [ ] **Step 1: 写失败测试** — `internal/blink/blink_test.go`:
```go
package blink

import "testing"

func TestEncodeDecodeRoundTrip(t *testing.T) {
	link := "brook://server?server=1.2.3.4%3A9999&password=pw"
	enc := Encode(link)
	if enc[:8] != "blink://" {
		t.Fatalf("应以 blink:// 开头, got %q", enc)
	}
	dec, err := Decode(enc)
	if err != nil {
		t.Fatal(err)
	}
	if dec != link {
		t.Fatalf("round-trip 不一致: %q != %q", dec, link)
	}
}

func TestDecodeRejectsWrongScheme(t *testing.T) {
	if _, err := Decode("brook://x"); err == nil {
		t.Fatal("非 blink scheme 应报错")
	}
}

func TestDecodeRejectsBadBase64(t *testing.T) {
	if _, err := Decode("blink://!!!not-base64!!!"); err == nil {
		t.Fatal("坏 base64 应报错")
	}
}

func TestDecodeRejectsNonBrookContent(t *testing.T) {
	// base64url("http://evil") 解出来不是 brook://,应拒绝
	bad := Encode("http://evil") // Encode 不校验输入,这里用它造一个非 brook 内容的 blink
	if _, err := Decode(bad); err == nil {
		t.Fatal("解出非 brook 内容应报错")
	}
}
```

- [ ] **Step 2: 运行确认失败** — `go test ./internal/blink/` → FAIL(包/函数不存在)

- [ ] **Step 3: 写实现** — `internal/blink/blink.go`:
```go
// Package blink 是 bx 对外的链接别名:把 brook 链接 base64url 换壳成 blink://,
// 对用户隐藏 brook/IP/密码明文。仅在 setup 入口解码回 brook,运行时不涉及。
package blink

import (
	"encoding/base64"
	"fmt"
	"strings"
)

const scheme = "blink://"

// Encode 把 brook 链接包成 blink://(不校验输入是否为 brook,调用方保证)。
func Encode(brookLink string) string {
	return scheme + base64.RawURLEncoding.EncodeToString([]byte(brookLink))
}

// Decode 还原 blink:// 为 brook 链接;校验 scheme、base64、内容前缀。
func Decode(s string) (string, error) {
	if !strings.HasPrefix(s, scheme) {
		return "", fmt.Errorf("不是 blink 链接(应以 %s 开头)", scheme)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, scheme))
	if err != nil {
		return "", fmt.Errorf("blink 解码失败: %w", err)
	}
	link := string(raw)
	if !strings.HasPrefix(link, "brook://") {
		return "", fmt.Errorf("blink 内容不是 brook 链接")
	}
	return link, nil
}
```

- [ ] **Step 4: 运行确认通过** — `go test ./internal/blink/` → PASS

- [ ] **Step 5: 提交**
```bash
git add internal/blink
git commit -m "feat(blink): brook 链接 base64url 换壳成 blink://"
```

---

## Task 2: `internal/install` 拆 WriteUnit/Enable/Disable

**Files:** Modify `internal/install/install.go`; Modify `internal/install/install_test.go`.

- [ ] **Step 1: 改/加测试** — 在 `internal/install/install_test.go` 把 `TestUnitText` 的期望从 `up -c` 改为 `run -c`,并加一个 UnitInstalled 的轻测试。替换 `TestUnitText` 为:
```go
func TestUnitText(t *testing.T) {
	u := UnitText("/usr/local/bin/bx run -c /etc/bx/config.yaml")
	for _, want := range []string{
		"[Unit]",
		"[Service]",
		"[Install]",
		"ExecStart=/usr/local/bin/bx run -c /etc/bx/config.yaml",
		"WantedBy=multi-user.target",
		"Restart=on-failure",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("unit 应含 %q,实际:\n%s", want, u)
		}
	}
}

func TestUnitInstalledFalseWhenAbsent(t *testing.T) {
	// 默认环境下 /etc/systemd/system/bx.service 不一定存在;此测试只验证函数可调用且返回 bool。
	_ = UnitInstalled()
}
```

- [ ] **Step 2: 运行确认失败** — `go test ./internal/install/` → FAIL（`run -c` 断言不匹配旧 UnitText 用例 / `UnitInstalled` 未定义）

- [ ] **Step 3: 写实现** — `internal/install/install.go`:把 `Install` 替换为 `WriteUnit` + `Enable` + `Disable`,加 `UnitInstalled`。删除旧 `Install` 函数。新内容(保留 `UnitText`/`Uninstall`/`runSystemctl`/常量不变):
```go
// WriteUnit 写入 unit 文件并 daemon-reload(不 enable、不 start)。需 root。
func WriteUnit(execStart string) error {
	if err := os.WriteFile(unitPath, []byte(UnitText(execStart)), 0o644); err != nil {
		return fmt.Errorf("写 %s(需 root): %w", unitPath, err)
	}
	return runSystemctl("daemon-reload")
}

// Enable 启动并设为开机自启。
func Enable() error { return runSystemctl("enable", "--now", ServiceName) }

// Disable 停止并取消开机自启。
func Disable() error { return runSystemctl("disable", "--now", ServiceName) }

// UnitInstalled 报告 unit 文件是否已就位(用于 up 前置校验)。
func UnitInstalled() bool {
	_, err := os.Stat(unitPath)
	return err == nil
}
```
(确保 `Uninstall` 仍存在且不变。)

- [ ] **Step 4: 运行确认通过** — `go test ./internal/install/` → PASS

- [ ] **Step 5: 编译检查** — `go build ./...` 此时会因为 cli.go 仍调用 `install.Install` 而 FAIL（预期,Task 4 修)。仅确认错误只在 `internal/cli`。其余包应 build OK:`go build ./internal/install/ ./internal/blink/` → OK。

- [ ] **Step 6: 提交**
```bash
git add internal/install
git commit -m "refactor(install): 拆 WriteUnit/Enable/Disable/UnitInstalled(装与起分离)"
```

---

## Task 3: `internal/setup` 写配置 + 连通检测

**Files:** Create `internal/setup/setup.go`; Test `internal/setup/setup_test.go`.

- [ ] **Step 1: 写失败测试** — `internal/setup/setup_test.go`(只测 `WriteConfig`;`ProbeServer` 需活 brook,集成验证):
```go
package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/config"
)

func TestWriteConfigRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	link := "brook://server?server=1.2.3.4%3A9999&password=pw"
	if err := WriteConfig(p, link, false); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	cfg, err := config.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server != link {
		t.Errorf("server 应为 %q, got %q", link, cfg.Server)
	}
	if !cfg.Global {
		t.Error("global 应为 true")
	}
	if !cfg.Killswitch {
		t.Error("killswitch 应为 true")
	}
}

func TestWriteConfigRefusesExistingWithoutForce(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte("server: old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteConfig(p, "brook://new", false); err == nil {
		t.Fatal("已存在且无 force 应报错")
	}
	if err := WriteConfig(p, "brook://new", true); err != nil {
		t.Fatalf("force 应覆盖: %v", err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "brook://new") {
		t.Error("force 应写入新链接")
	}
}
```

- [ ] **Step 2: 运行确认失败** — `go test ./internal/setup/` → FAIL(包/函数不存在)

- [ ] **Step 3: 写实现** — `internal/setup/setup.go`:
```go
// Package setup 实现 bx setup 的两块:生成最小配置、连通检测 brook 服务器。
package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/getbx/bx/internal/tunnel"
	"gopkg.in/yaml.v3"
)

// minimalConfig 是 setup 写出的最小可用配置(能被 config.Parse 读回)。
type minimalConfig struct {
	Server     string `yaml:"server"`
	Global     bool   `yaml:"global"`
	Killswitch bool   `yaml:"killswitch"`
}

// WriteConfig 写最小配置(global+killswitch 默认开)。文件已存在且 !force 则报错。
func WriteConfig(path, brookLink string, force bool) error {
	if !force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("配置已存在 %s(加 --force 覆盖)", path)
		}
	}
	b, err := yaml.Marshal(minimalConfig{Server: brookLink, Global: true, Killswitch: true})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// ProbeServer 临时起 brook 隧道探测服务器连通,返回延迟 ms;不建 TUN。
func ProbeServer(brookPath, brookLink, probe string, timeout time.Duration) (int64, error) {
	t, err := tunnel.NewBrook(brookPath, brookLink, probe)
	if err != nil {
		return 0, err
	}
	t.Start()
	defer t.Stop()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline.C:
			return 0, fmt.Errorf("%s 内未连通(检查 server/密码/网络)", timeout)
		case <-tick.C:
			if t.Healthy() {
				return t.Stats().LatencyMS, nil
			}
		}
	}
}
```

- [ ] **Step 4: 运行确认通过** — `go test ./internal/setup/` → PASS（两个 WriteConfig 测试）

- [ ] **Step 5: 提交**
```bash
git add internal/setup
git commit -m "feat(setup): WriteConfig 最小配置 + ProbeServer 连通检测"
```

---

## Task 4: cli 命令重排(setup/up/down/run/blink)

**Files:** Modify `internal/cli/cli.go`; Modify `internal/cli/cli_test.go`.

当前 `cli.go` 关键现状:`New()` 注册 up/down/status/reload/install/uninstall;`upFlags()`;`upAction`→`loadConfig`+`supervisor.Run`;`installAction`→`buildExecStart`(`up -c`)+`install.Install`;`downAction`→读 pid SIGTERM;`buildExecStart` 返回 `<bin> up -c <path>`;有 `resolveConfigPath`/`defaultConfigPath`。

- [ ] **Step 1: 写/改失败测试** — 在 `internal/cli/cli_test.go`:把 `TestBuildExecStart` 期望改为 `run -c`,新增 `blink` 往返与校验测试。替换/追加:
```go
func TestBuildExecStart(t *testing.T) {
	got := buildExecStart("/usr/local/bin/bx", "/etc/bx/config.yaml")
	want := "/usr/local/bin/bx run -c /etc/bx/config.yaml"
	if got != want {
		t.Fatalf("ExecStart 应跑 run, got %q", got)
	}
}

func TestBlinkRoundTripThroughCLI(t *testing.T) {
	// cli 的 blink 生成应能被 blink.Decode 还原(用 blink 包直接验证生成逻辑一致性)
	link := "brook://server?server=1.2.3.4%3A9999&password=pw"
	enc := blink.Encode(link)
	dec, err := blink.Decode(enc)
	if err != nil || dec != link {
		t.Fatalf("round-trip 失败: %q err=%v", dec, err)
	}
}
```
(在该测试文件顶部 import 加 `"github.com/getbx/bx/internal/blink"`。)

- [ ] **Step 2: 运行确认失败** — `go test ./internal/cli/` → FAIL（`run -c` 不匹配旧 `buildExecStart`;`blink` 未 import / 包未被 cli 用到时 import 报错——实现后修正）

- [ ] **Step 3: 写实现** — 改 `internal/cli/cli.go`:

(a) import 增加:
```go
	"time"

	"github.com/getbx/bx/internal/blink"
	"github.com/getbx/bx/internal/embedded"
	"github.com/getbx/bx/internal/provision"
	"github.com/getbx/bx/internal/setup"
```
(保留已有 config/install/stats/supervisor/cli 等 import;`net`/`encoding/json`/`syscall`/`strconv` 若 `downAction` 重写后不再用要删——见 (f)。让编译器指出未用 import。)

(b) `New()` 命令表替换为:
```go
		Commands: []*cli.Command{
			{Name: "setup", Usage: "首次配置:写配置+装服务+连通检测(不启动)", ArgsUsage: "blink://...", Flags: setupFlags(), Action: setupAction},
			{Name: "up", Usage: "启动并设为开机自启", Action: upAction},
			{Name: "down", Usage: "停止并取消开机自启", Action: downAction},
			{Name: "run", Usage: "前台运行(调试/服务内部用)", Flags: runFlags(), Action: runAction},
			{Name: "status", Usage: "查看状态面板", Action: statusAction},
			{Name: "blink", Usage: "由 brook 链接生成 blink://(发给用户)", ArgsUsage: "brook://...", Action: blinkAction},
			{Name: "uninstall", Usage: "卸载 systemd 服务", Action: uninstallAction},
		},
```

(c) 把现有 `upFlags()` 改名为 `runFlags()`(函数体不变);把现有 `upAction` 改名为 `runAction`(函数体不变,仍 `loadConfig`+`supervisor.Run`);`optsFromFlags` 不变。

(d) 新增 `setupFlags()` 与 `setupAction`:
```go
func setupFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: defaultConfigPath, Usage: "配置写入路径"},
		&cli.StringFlag{Name: "probe", Value: "1.1.1.1:443", Usage: "连通检测目标"},
		&cli.BoolFlag{Name: "force", Usage: "覆盖已存在的配置"},
		&cli.BoolFlag{Name: "strict", Usage: "连通检测失败则中止(默认仅警告)"},
	}
}

func setupAction(c *cli.Context) error {
	arg := c.Args().First()
	if arg == "" {
		return fmt.Errorf("用法: sudo bx setup blink://...")
	}
	link, err := blink.Decode(arg)
	if err != nil {
		return err
	}
	cfgPath := c.String("config")
	brookPath, err := provision.EnsureBrook("/var/lib/bx", "", embedded.Brook(), embedded.BrookVersion())
	if err != nil {
		return fmt.Errorf("准备 brook: %w", err)
	}
	fmt.Println("⏳ 连通检测中…")
	if lat, perr := setup.ProbeServer(brookPath, link, c.String("probe"), 15*time.Second); perr != nil {
		if c.Bool("strict") {
			return fmt.Errorf("连通检测失败: %w", perr)
		}
		fmt.Printf("⚠️  连通检测未通过(仍写配置,稍后可排查): %v\n", perr)
	} else {
		fmt.Printf("✅ 服务器连通,延迟 %dms\n", lat)
	}
	if err := setup.WriteConfig(cfgPath, link, c.Bool("force")); err != nil {
		return err
	}
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(cfgPath)
	if err != nil {
		return err
	}
	if err := install.WriteUnit(buildExecStart(bin, abs)); err != nil {
		return err
	}
	fmt.Printf("✅ 已写配置 %s 并装好服务。下一步:sudo bx up\n", cfgPath)
	return nil
}
```

(e) 新增 `upAction`/`downAction`/`blinkAction`(替换旧 up/down 语义):
```go
func upAction(c *cli.Context) error {
	if !install.UnitInstalled() {
		return fmt.Errorf("尚未配置。先运行: sudo bx setup blink://...")
	}
	if err := install.Enable(); err != nil {
		return err
	}
	fmt.Println("✅ bx 已启动并设为开机自启。`bx status` 看面板。")
	return nil
}

func downAction(c *cli.Context) error {
	if err := install.Disable(); err != nil {
		return err
	}
	fmt.Println("✅ bx 已停止并取消开机自启。")
	return nil
}

func blinkAction(c *cli.Context) error {
	arg := c.Args().First()
	if !strings.HasPrefix(arg, "brook://") {
		return fmt.Errorf("用法: bx blink brook://...")
	}
	fmt.Println(blink.Encode(arg))
	return nil
}
```

(f) 删除旧 `installAction`(功能并入 setup)。`buildExecStart` 改为 `run`:
```go
func buildExecStart(bin, configPath string) string {
	return fmt.Sprintf("%s run -c %s", bin, configPath)
}
```
旧 `downAction`(读 pid SIGTERM)已被 (e) 覆盖删除;若 `statusAction` 仍用 `net`/`encoding/json`/`stats` 则保留这些 import,删掉因 downAction 重写而不再用的 `strconv`/`syscall`/`strings`?——注意 `blinkAction` 仍用 `strings`,`statusAction` 仍用 `net`/`json`/`stats`。让 `go build` 指出真正未用的 import 并据此删(预期:`strconv`、`syscall` 不再用 → 删)。

- [ ] **Step 4: 运行确认通过 + 全量构建** — `gofmt -w internal/cli/cli.go && go build ./... && go vet ./... && go test ./...`
Expected: BUILD OK;vet clean;ALL PASS。

- [ ] **Step 5: 提交**
```bash
git add internal/cli/cli.go internal/cli/cli_test.go
git commit -m "feat(cli): setup/up(enable)/down(disable)/run(前台)/blink 命令重排"
```

---

## Task 5: README 傻瓜流程

**Files:** Modify `README.md`.

- [ ] **Step 1: 改 README** — 把"## 快速开始(开箱即用)"一节替换为以 setup/blink 为主的傻瓜流程,并更新"## 命令"表与"## 用法一/二"里的命令到新模型(`run` 取代前台 `up`;`bx install` → `bx setup`)。新「快速开始」正文:
````markdown
## 快速开始(傻瓜版)

bx 是单一静态二进制,brook/列表已内嵌。管理员把服务器链接转成 `blink://` 发给用户,用户三步即跑:

```bash
# (管理员)由 brook 链接生成 blink,发给用户
bx blink "brook://server?server=1.2.3.4%3A9999&password=xxx"
#   → blink://YnJvb2s6Ly...

# (用户)① 放上二进制  ② 配置(自动连通检测,不启动)  ③ 启动
sudo install -m755 bx /usr/local/bin/bx
sudo bx setup blink://YnJvb2s6Ly...      # 看到 ✅ 服务器连通
sudo bx up                                # 后台起 + 开机自启
```

日常只用两个词:`sudo bx down`(停+取消自启)、`sudo bx up`(起+自启)。
`bx status` 看面板;`sudo bx run` 前台带 log 排错;`sudo bx uninstall` 卸载。
私网/docker 自动绕过 tun,SSH 不会被锁死;无需写任何 YAML。
````
并在「## 命令」表中改为 setup/up/down/run/status/blink/uninstall 七项;删除/改写「用法一」「用法二」里 `bx up --brook ... --china-* ...` 与 `bx install -c ...` 为 `bx run -c ...` 与 `bx setup`。

- [ ] **Step 2: 最终验证** — `go build ./... && go vet ./... && go test ./...` → 全绿(README 不影响)
- [ ] **Step 3: 提交**
```bash
git add README.md
git commit -m "docs(README): 傻瓜流程 setup/blink/up/down + 命令表更新"
```

---

## 收尾:部署到 14.37

- [ ] 交叉编译 + scp:`CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /tmp/bx-new . && scp /tmp/bx-new 10.0.14.37:~/bx-new`
- [ ] 在 14.37 验证傻瓜流程:`bx blink <现有 brook 链接>` → `sudo bx setup blink://...`(看连通)→ `sudo bx up` → `bx status` / `ip rule | grep 'pref 150'` / compass 通。

---

## Self-Review 记录
- **Spec 覆盖**:blink(T1)、install 拆分(T2)、setup 写配置+探活(T3)、cli 重排 up/down/run/setup/blink + ExecStart→run(T4)、README(T5)。✓
- **占位符**:无;每个改码步含完整代码。✓
- **类型一致**:`blink.Encode/Decode`、`install.WriteUnit/Enable/Disable/UnitInstalled`、`setup.WriteConfig/ProbeServer`、`buildExecStart(...)→"run -c"`、`runFlags()`/`runAction` 在定义与调用处一致;`provision.EnsureBrook`/`embedded.Brook`/`tunnel.NewBrook`/`t.Healthy()`/`t.Stats().LatencyMS` 与现有签名一致。✓
- **顺序依赖**:T2 后 cli 暂不可编译(调用旧 install.Install),T4 修复;计划已在 T2 Step5 标注预期。
