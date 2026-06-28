# `bx mcp install` 自描述配对(③ onboarding Slice ③-2)设计

Status: APPROVED-FOR-PLANNING(brainstormed 2026-06-28)。

## 背景与定位

③-1(业主 uid 授权)已交付:业主(及其以业主身份跑的 agent)免 root 操作 bx。③-2 是 onboarding 收口——但**用户重定方向**:不做人工 `bx pair` 替用户改 agent 配置;而是让 **bx 可发现 + 自描述**,由 **agent 自己装 MCP**。即:agent 知道 bx 在、知道 bx 有 MCP 控制面、agent 自己跑 `claude mcp add` 接上。onboarding 也 agent 驱动,贯彻"AI-native"。

用户进一步定:`bx mcp install` **只打印指令、绝不自跑**(最透明、零副作用)。

## 目标 / 非目标

**目标**:`bx mcp install` 打印把 bx 接入 agent 的精确配对指令(含 `os.Executable()` 解析的绝对路径)——Claude Code 的 `claude mcp add` 命令行 + 通用 MCP 客户端 JSON 片段 + 一行 agent 面向说明。**纯打印、无任何副作用**(不写配置、不执行 `claude mcp add`)。加可发现性:命令 Usage、README onboarding 一行、`bx capabilities` 提及。纯函数 `mcpInstallText` 便于单测。

**非目标**:执行 `claude mcp add`(用户定只打印);手改 Claude Desktop JSON 配置;`bx mcp` 服务端行为(不变);darwin LOCAL_PEERCRED(业主授权 Linux 生效)。

## 架构

### 命令 `bx mcp install`(cli)

`bx mcp`(无子命令)仍跑 stdio MCP 服务端(现 `mcpAction`);新增子命令 `install` 走 `mcpInstallAction`,只打印 `mcpInstallText(bxPath)` 到 stdout。cli 结构在 plan 落地(子命令 vs 顶层命令的取舍为实现细节;`bx mcp` 默认仍是服务端)。

### 纯文本构建器(可单测)

```go
// mcpInstallText 构建把 bx 接入 agent 的配对指令(纯函数,无副作用)。
// bxPath 由 os.Executable() 解析(绝对路径,robust)。
func mcpInstallText(bxPath string) string
```
输出内容(中文 + 可直接复制/agent 可执行):
1. **Claude Code**:`claude mcp add --scope user bx -- <bxPath> mcp`(`--scope user`:跨该用户所有 Claude Code 项目可用)。
2. **通用 MCP 客户端(如 Claude Desktop)**:`{"mcpServers": {"bx": {"command": "<bxPath>", "args": ["mcp"]}}}`。
3. **agent 面向一行**:「若你是 AI agent:运行上面命令即可获得 bx 控制面(状态/换传输/重劫持,commit-confirmed),以业主身份授权、无需 sudo。」

### 命令 Action

```go
func mcpInstallAction(c *cli.Context) error {
    bxPath, err := os.Executable()
    if err != nil || bxPath == "" {
        bxPath = "bx" // 退化:os.Executable 失败时用裸名(PATH 中)
    }
    fmt.Fprint(os.Stdout, mcpInstallText(bxPath))
    return nil
}
```

### 可发现性

- 命令 `Usage`:「打印把 bx 接入你的 agent 的 MCP 配对指令(只打印,不自跑)」。
- `README.md` onboarding 段加一行:「让你的 agent 运行 `bx mcp install` 并照做,即可让它操作 bx(以业主身份,无需 sudo)。」
- `bx capabilities`(`capabilitiesReport`):加一处指向 `bx mcp install` 的 onboarding 提示(机器可读,便于 agent 发现)。

## 数据流 / 错误处理

`bx mcp install` →(无守护进程依赖、无 socket、无权限要求,任何用户可跑)→ `os.Executable()` 取自身路径 → `mcpInstallText` 拼指令 → 打印 stdout → exit 0。`os.Executable` 失败 → 退化裸名 `bx`(假定在 PATH)。无失败路径(纯打印)。

## 测试策略(全 Mac 原生)

- `mcpInstallText("/usr/local/bin/bx")`:断言含 `claude mcp add --scope user bx -- /usr/local/bin/bx mcp` 整行、含 JSON 片段 `"command": "/usr/local/bin/bx"` 与 `"args": ["mcp"]`、含 agent 面向说明关键字。
- `mcpInstallAction`:可经 `bx mcp install` 端到端跑一次(集成性,或仅靠 mcpInstallText 纯测 + 手验命令注册)。
- 回归:既有 cli/mcp 测不受影响;`bx mcp`(服务端)行为不变;两平台编译。

## 决策记录

- 方向:bx 可发现 + 自描述,**agent 自装 MCP**(非人工 `bx pair`)——贯彻 AI-native onboarding。
- `bx mcp install` **只打印、不自跑**(用户定;最透明零副作用)。
- 打印两形:Claude Code `claude mcp add` + 通用 MCP JSON 片段;绝对路径经 `os.Executable()`。
- 跑为任何用户、无 sudo、无守护进程依赖(纯打印)。
- 业主授权(③-1)使 agent 以业主身份免 sudo 操作 bx;本片只负责"让 agent 知道怎么接"。

## 范围自检

单一可实现小增量(`mcpInstallText` 纯函数 + `bx mcp install` 命令 + 可发现性 README/Usage/capabilities + 单测),全 Mac 可测、纯打印零副作用。适合一份小 plan(1-2 任务)。
