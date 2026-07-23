# Windows 托盘"开机自启"开关设计

- 日期:2026-07-23
- 范围:**纯 Windows**(Go + SCM + HKCU + systray)。macOS/Linux 的 `bx autostart` 留后续。
- 前置:子项目②托盘(`internal/tray`,`bx tray`)、服务层(`internal/install/service_windows.go`)、盾牌图标 4 态托盘(2026-07-22)均已在仓库。

## 背景与动机

Windows 上 bx 现在:
- **服务开机自启**由 `bx up`(设 `StartAutomatic` + 启动)与 `bx down`(停 + 设 `StartDisabled`)**耦合控制**——想只改"是否开机自启"、不动当前运行状态,没有入口。
- **托盘图标登录自启**由托盘自己在 `pollLoop` 里 `sync.Once` 无脑注册 HKCU Run,**不可关**。
- 小白想要的是一个直观的**"开机自启"开关**。

本设计加一个托盘可勾选项 + 一条 `bx autostart` 命令,把"开机自启"做成用户可控的独立开关。

## 关键决策(brainstorm 结论)

1. **合并成一个开关**:一个可见勾选项同时控**服务自启 + 托盘登录自启**,两者一起开/关。避免"服务自启了但图标没起 → 小白懵"的错位状态(俩绑一起就到不了那个状态)。
2. **"自启"与"连接/断开"正交解耦**:`up/down` 只管"现在开不开",自启开关只管"开机自不自动",互不干扰、最可预测。代价:改 `up/down` 现有语义。
3. **关 = Manual,不是 Disabled**:自启"关"设服务 `StartType=StartManual`(仍能手动/`up` 启动),**不是** `StartDisabled`(会导致停了再也起不来)。修正现有 `down` 用 Disabled 的问题。
4. **恢复靠"打开 bx"、非命令行**:关掉自启后下次登录无图标无保护是**用户主动选择**;要回来只需**打开 bx**(开始菜单 `bx tray`,或双击 exe),不需命令行。故让无参 `bx.exe` 也直接启动托盘。

## 语义模型

| 概念 | 含义 | 由谁控制 | 提权 |
|---|---|---|---|
| 连接/断开(runtime) | 服务**现在**跑不跑 | `bx up`/`down`、托盘"连接/断开" | 是(SCM Start/Stop) |
| **自启(boot)** | 开机**自不自动**保护 + 图标 | `bx autostart on\|off`、托盘"开机自启"勾选 | 是(改 StartType + HKCU) |

- 自启 **ON** = 服务 `StartType=StartAutomatic` **+** 托盘登录自启 HKCU Run 写入。
- 自启 **OFF** = 服务 `StartType=StartManual` **+** 托盘登录自启 HKCU Run 删除。
- **两个方向永远一起动**(SetAutostart 一次改两处),用户到不了"服务自启开、图标自启关"的错位态。
- 权威信号是**服务 StartType**(`AutostartEnabled()` 读它);HKCU 跟随它,不独立判定。

## 组件改动

### `internal/install`(服务 + HKCU 的自启治理)

新增两个跨调用方复用的函数(windows 实现在 `service_windows.go` 或新 `autostart_windows.go`;非 windows 桩返回 `errUnsupported` 或 best-effort):

```
// SetAutostart 原子设置"开机自启"两处:服务 StartType + 托盘登录自启 HKCU Run。
// enabled=true → StartAutomatic + 写 HKCU Run("bx" = `"<BinPath>" tray`);
// enabled=false → StartManual + 删 HKCU Run。需提权(改 SCM 配置)。
func SetAutostart(enabled bool) error

// AutostartEnabled 报告服务是否开机自启(StartType==StartAutomatic)。非提权只读。
func AutostartEnabled() (bool, error)
```

- HKCU Run 值用 `install.BinPath`(`C:\Program Files\bx\bx.exe`)+ ` tray`。写的是**当前用户** hive(提权进程仍是同一用户 SID)。
- `SetAutostart(false)` 用 `StartManual`(非 `StartDisabled`),保证服务停了仍可手动/`up` 起。

### `internal/install/service_windows.go`(解耦 up/down)

- `windowsEnableService`(`bx up`)→ **仅 `s.Start()`**,不再设 `StartAutomatic`。
- `windowsDisableService`(`bx down`)→ **仅 `stopAndWait`**,不再设 `StartDisabled`。
- **默认自启由 `setup` 负责**(见下),使"开箱即自启"的好默认不丢。
- `bx up` 的 Start 在 `StartManual` 上可正常启动(Manual 可手动起);故解耦后 up 仍能拉起服务。

### `internal/cli`(setup 默认 + autostart 命令 + 无参启托盘)

- **`setupAction`(windows)**:装完服务后调用 `install.SetAutostart(true)`,保持"装好即开机自启"。(setup 本就不 start;仅设自启。)
- **新命令 `bx autostart <on|off|status>`**(`autostart_windows.go` 承载 action;非 windows 返回"暂不支持"):
  - `on` → `install.SetAutostart(true)`;`off` → `install.SetAutostart(false)`;`status` → 打印 `AutostartEnabled()`。
  - `on`/`off` 需提权(改 SCM);由托盘经 per-action UAC 拉起。
- **无参 `bx.exe`(windows)→ 启动托盘**:root Action 在 windows 下调 `trayAction`(带 `freeConsole` 隐藏黑框),使双击 exe 回到图标;`bx help`/`bx -h` 仍出帮助。非 windows 保持原帮助行为。

### `internal/tray`(勾选项 + 读态 + 去掉无脑自注册)

- `state.go`:`TrayMenu` 加 `Autostart menuItem`(始终 `Visible=true`,是持久勾选项,不随态显隐);记录一个"是否勾选"的布尔由调用方从 `AutostartEnabled()` 取。
- `tray_windows.go`:
  - `onReady` 用 `systray.AddMenuItemCheckbox("开机自启", "开机自动保护 + 显示图标", checked)`;点击 → `confirm("开机自启", …)` → 提权 `bx autostart on/off`(按当前勾选态取反)。
  - `pollLoop` 每轮读 `install.AutostartEnabled()`(非提权),`Check()`/`Uncheck()` 同步勾选态。
  - **删除** `pollLoop` 里 `autostartOnce.Do(setAutostart)` 的无脑注册;`setAutostart` 由 `install.SetAutostart` 取代(删除或不再调用 tray 内旧 `setAutostart`)。
- 提权动作、`confirm`、`elevateRun("autostart on"/"autostart off")` 复用现有入口。

## 不变量 / 不改动

- `winres.json` 的 `execution-level` 恒 `as invoker`(不改);autostart 改 SCM 靠 per-action UAC。
- 不碰隧道/路由/数据面。macOS/Linux 不改(`bx autostart` 在非 windows 返回"暂不支持",不误动 systemd/launchd)。
- kill-switch/fakeip 等数据面不变量与本特性无关。

## 边界与坑

- **提权写 HKCU**:`bx autostart` 提权后仍是同用户 hive,HKCU Run 写当前用户可行;多用户机器只影响当前用户(消费级单用户 OK)。
- **非提权读 StartType**:托盘读 `AutostartEnabled()` 需要 `SERVICE_QUERY_CONFIG`(一般对 Authenticated Users 放行,与现有非提权读 `ServiceState("is-active")` 同款);**真机需确认**读得到。
- **`down` 语义变化**:解耦后 `down` 只停、不再禁自启 → 停了仍会开机自起(除非关自启开关)。托盘"断开"tooltip/文案讲清"仅现在关,不改开机行为";README 同步。
- **无参启托盘的取舍**:终端里敲 `bx`(无参)将启动托盘而非出帮助;CLI 用户用 `bx help` 或 `bx <子命令>`。可逆的小改动,spec review 若有异议可撤。

## 测试

- **纯逻辑单测(Linux 可跑)**:
  - `bx autostart` 参数解析:把 `on`/`off`/`status`/无效值 → 动作意图(如 `parseAutostartArg(s string) (want *bool, isStatus bool, err error)`)抽成纯函数并测(`on`→want=true、`off`→want=false、`status`→isStatus、其它→err),不触 SCM。
  - (勾选态同步 `enabled?Check():Uncheck()` 太薄不值得造纯函数;由 windows 交叉编译 + 真机验覆盖,不为它写恒等测试。)
- **windows 交叉编译**:amd64/arm64 `go build ./...` + `go vet`。
- **真机待验(交互桌面)**:① 勾"开机自启"→ `sc qc bx` 显示 AUTO_START + HKCU Run 有 `bx` 项 → 重启验证服务起 + 图标出现;② 取消勾选 → START_TYPE=DEMAND(Manual)+ HKCU 无 `bx` → 重启验证服务不起、无图标,打开 bx 回图标;③ 非提权读 StartType 成功(勾选态正确显示);④ `down` 后重启仍自起(印证解耦)。

## 交付清单(供 writing-plans)

1. `install.SetAutostart(bool)` + `AutostartEnabled() bool`(windows,TDD 纯逻辑部分 + 真机验 SCM/HKCU)。
2. 解耦 `windowsEnableService`/`windowsDisableService`(去掉 StartType 改动)。
3. `setupAction`(windows)装完调 `SetAutostart(true)`。
4. `bx autostart <on|off|status>` 命令 + 参数分派(TDD)。
5. 无参 `bx.exe`(windows)启动托盘。
6. 托盘 `AddMenuItemCheckbox` + 点击提权 + poll 同步勾选态 + 删无脑自注册。
7. windows amd64/arm64 交叉编译 + vet + 单测全绿。

## 关键决策记录

- **一个开关控两处自启**:根治"保护中但无图标"的错位;用户到不了该状态。
- **正交解耦 up/down**:runtime 与 boot 两件事各管各的,可预测;代价是 `down` 语义变化,文档讲清。
- **关=Manual 非 Disabled**:停了仍可手动起,修正现有 Disabled 缺陷。
- **无参 exe 启托盘**:支撑"打开 bx 即回图标"的恢复模型,免命令行。
- **纯 Windows**:范围收窄;`bx autostart` 非 windows 明确"暂不支持",不误动其他平台自启。
