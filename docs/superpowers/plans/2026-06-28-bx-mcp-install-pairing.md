# `bx mcp install` 自描述配对(③ onboarding Slice ③-2)Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 加 `bx mcp install`——纯打印把 bx 接入 agent 的 MCP 配对指令(`claude mcp add` + 通用 JSON 片段,`os.Executable()` 绝对路径),绝不自跑;并让 agent 可发现(Usage/README/capabilities)。

**Architecture:** `mcp` 命令加 `install` 子命令(`bx mcp` 无子命令仍跑 stdio 服务端;urfave/cli v2 父命令无子命令时跑父 Action);`mcpInstallText(bxPath) string` 纯函数构建指令、`mcpInstallAction` 取 `os.Executable()` 后打印。capabilities 加一条 onboarding 条目,README 加一行。

**Tech Stack:** Go 1.26.3;urfave/cli v2.27.7;改 `internal/cli/cli.go`、`internal/cli/cli_test.go`、`README.md`。

## Global Constraints

- 模块 `github.com/getbx/bx`,Go 1.26.3。TDD;提交中文 conventional + 结尾 `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`;master 直接提交。
- **纯打印、零副作用**:`bx mcp install` 不写配置、不执行 `claude mcp add`、不连守护进程、不需权限。
- `bx mcp`(无子命令)行为不变(stdio MCP 服务端,`mcpAction`)。
- 路径用 `os.Executable()` 绝对路径;失败退化裸名 `"bx"`。
- 配对命令固定:`claude mcp add --scope user bx -- <bxPath> mcp`;通用片段 `{"mcpServers": {"bx": {"command": "<bxPath>", "args": ["mcp"]}}}`。

---

### Task 1: bx mcp install 命令 + 纯文本构建器 + 可发现性

**Files:**
- Modify: `internal/cli/cli.go`(`mcpInstallText` 纯函数;`mcpInstallAction`;`mcp` 命令加 `install` 子命令;`capabilities()` 加 onboarding 条目)
- Modify: `internal/cli/cli_test.go`(`TestMCPInstallText`)
- Modify: `README.md`(onboarding 加「让 agent 操作 bx」一段)

**Interfaces:**
- Consumes: 现有 `mcp` 命令(`mcpAction`/`mcpFlags`)、`capabilitiesReport`/`commandCapability`、`os.Executable`。
- Produces:
  - `func mcpInstallText(bxPath string) string`
  - `func mcpInstallAction(c *cli.Context) error`
  - `bx mcp install` 子命令 + capabilities 中 `bx mcp install` 条目。

- [ ] **Step 1: 写失败测试**

在 `internal/cli/cli_test.go` 末尾追加(`strings`/`testing` 该文件应已 import;若缺则补):
```go
func TestMCPInstallText(t *testing.T) {
	out := mcpInstallText("/usr/local/bin/bx")
	for _, want := range []string{
		"claude mcp add --scope user bx -- /usr/local/bin/bx mcp",
		`"command": "/usr/local/bin/bx"`,
		`"args": ["mcp"]`,
		"AI agent",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("mcpInstallText 缺 %q\n--- got ---\n%s", want, out)
		}
	}
}
```

- [ ] **Step 2: 跑红**

Run: `go test ./internal/cli/ -run MCPInstallText 2>&1 | head`
Expected: 编译失败(`undefined: mcpInstallText`)。

- [ ] **Step 3: 加 mcpInstallText + mcpInstallAction(cli.go)**

在 `mcpAction` 函数之后追加:
```go
// mcpInstallText 构建把 bx 接入 agent 的 MCP 配对指令(纯函数,无副作用)。
// bxPath 由调用方用 os.Executable() 解析为绝对路径(robust)。
func mcpInstallText(bxPath string) string {
	return fmt.Sprintf(`把 bx 接入你的 agent(MCP 控制面)——下面指令只需运行一次:

Claude Code:
  claude mcp add --scope user bx -- %s mcp

其他 MCP 客户端(如 Claude Desktop)在 mcpServers 加:
  {"mcpServers": {"bx": {"command": "%s", "args": ["mcp"]}}}

若你是 AI agent:运行上面 Claude Code 命令即可获得 bx 控制面(状态/换传输/重劫持,
commit-confirmed 死手兜底),以业主身份授权、无需 sudo(业主 = 运行 sudo bx setup 的用户)。
`, bxPath, bxPath)
}

// mcpInstallAction 打印配对指令(只打印,不自跑)。
func mcpInstallAction(c *cli.Context) error {
	bxPath, err := os.Executable()
	if err != nil || bxPath == "" {
		bxPath = "bx" // 退化:取不到自身路径时用裸名(假定在 PATH)
	}
	fmt.Fprint(os.Stdout, mcpInstallText(bxPath))
	return nil
}
```
(`fmt`/`os` 在 cli.go 已 import。)

- [ ] **Step 4: mcp 命令加 install 子命令(cli.go)**

把命令注册里的 `mcp` 行(现 `{Name: "mcp", Usage: "启动 agent 控制面 MCP server(stdio)", Hidden: false, Flags: mcpFlags(), Action: mcpAction},`)改为带子命令:
```go
			{Name: "mcp", Usage: "启动 agent 控制面 MCP server(stdio)", Hidden: false, Flags: mcpFlags(), Action: mcpAction, Subcommands: []*cli.Command{
				{Name: "install", Usage: "打印把 bx 接入你的 agent 的 MCP 配对指令(只打印,不自跑)", Action: mcpInstallAction},
			}},
```
(`bx mcp` 无子命令仍跑 `mcpAction`;`bx mcp install` 跑 `mcpInstallAction`。)

- [ ] **Step 5: capabilities 加 onboarding 条目(cli.go)**

在 `capabilities()` 的 `Commands: []commandCapability{ ... }` 列表里加一条(放末尾或 discovery 附近):
```go
			{
				Command:        "bx mcp install",
				Category:       "onboarding",
				Summary:        "Print the MCP pairing instruction so an agent can register bx's control plane with itself.",
				Stable:         true,
				RequiresRoot:   false,
				ChangesSystem:  false,
				ChangesNetwork: false,
				Outputs:        []string{"text"},
				Examples:       []string{"bx mcp install"},
				SafeNotes:      []string{"Print-only; runs nothing. An AI agent reading the output can run the printed `claude mcp add` to gain bx's control plane, authorized as the machine owner (no sudo)."},
			},
```
(字段名以 `commandCapability` 结构体为准——`Command/Category/Summary/Stable/RequiresRoot/ChangesSystem/ChangesNetwork/Outputs/Examples/SafeNotes`。)

- [ ] **Step 6: README onboarding 一段**

在 `README.md` 的「### 2. 客户端安装 bx」段(`sudo bx up` 之后)插入:
```markdown
#### 让你的 agent 操作 bx(AI-native,可选)

`bx setup` / `bx up` 跑通后,把控制面接给你的 agent:让你的 agent 运行

    bx mcp install

并照打印的 `claude mcp add` 指令做(**只打印、不自跑**)。之后 agent 就能查状态、
换传输(brook↔REALITY 防封)、重劫持等——以**业主**身份授权、**无需 sudo**
(业主 = 运行 `sudo bx setup` 的用户)。
```

- [ ] **Step 7: 跑绿 + 全量 + 手验**

Run:
```bash
go test ./internal/cli/ -run MCPInstallText -v
go build ./... && go vet ./... && go test ./...
GOOS=darwin GOARCH=arm64 go build -o /dev/null ./...
go run . mcp install        # 手验:打印配对指令,含 claude mcp add 与 JSON 片段
go run . capabilities | grep -c 'bx mcp install'   # 手验:capabilities 含该条目(>=1)
```
Expected: `TestMCPInstallText` PASS;全套件绿;两平台编译过;`bx mcp install` 打印含 `claude mcp add --scope user bx -- <path> mcp`;`bx mcp`(无 install)仍是服务端(不在此步跑,避免阻塞)。

- [ ] **Step 8: 提交**

```bash
git add internal/cli/cli.go internal/cli/cli_test.go README.md
git commit -m "feat(cli): bx mcp install —— 打印 agent 配对指令(③-2 自描述配对)

bx mcp install 纯打印把 bx 接入 agent 的 MCP 配对指令(claude mcp add --scope user
bx -- <os.Executable> mcp + 通用 JSON 片段),绝不自跑。mcp 加 install 子命令(bx mcp
仍是服务端);capabilities + README 加可发现性。agent 知道 bx、知道有 MCP、自装即可
以业主身份(③-1)免 sudo 操作 bx。

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review(对照 spec)

**Spec 覆盖:**
- `bx mcp install` 纯打印、不自跑 → Step 3(mcpInstallAction 只 Fprint)+ Step 4(子命令)。
- `mcpInstallText` 纯函数 + 两形输出(claude mcp add + 通用 JSON)+ 绝对路径 + agent 说明 → Step 3。
- `os.Executable()` 取路径、失败退化 `"bx"` → Step 3。
- `bx mcp` 服务端不变 → Step 4(保留 Action: mcpAction)。
- 可发现性:Usage(Step4)+ capabilities 条目(Step5)+ README(Step6)。
- 单测 mcpInstallText(含 claude mcp add 行 + JSON 片段 + agent 说明)→ Step 1。

**占位扫描:** 无 TBD;每步完整代码/命令。Step5 字段名以现有 `commandCapability` 为准(已核对:Command/Category/Summary/Stable/RequiresRoot/ChangesSystem/ChangesNetwork/Outputs/Examples/SafeNotes)。

**类型一致性:** `mcpInstallText(string) string`(Step3)与测试(Step1)、`mcpInstallAction` 调用(Step3)一致;`mcp` 子命令 `*cli.Command{Name:"install", Action: mcpInstallAction}`(Step4)与 urfave/cli v2 一致;capabilities 条目字段(Step5)与 `commandCapability` 结构体一致。
