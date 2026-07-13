# 子项目②:Windows 托盘 App(设计)

> 属「Windows 消费级分发 + 小白可用」整体方案(`2026-07-09-windows-consumer-distribution-design.md`)
> 的第二块——**核心体验**。地基(①内嵌二进制,已完成)让 `bx.exe` 自包含;本子项目给它一个
> 图形入口:`bx.exe tray` 启动系统托盘,小白点图标即可连/断/设置/看状态,全程不碰命令行。
> 对标 macOS `apps/macos/BxMenu`(Swift 菜单栏 App)的克制——不是控制面板,只给必要入口。

## 1. 目标与形态

一个 `bx tray` 子命令(windows-only)启动系统托盘图标:**非提权常驻、开机自启**。它是**现有 CLI +
控制面 + Windows 服务之上的薄 UI 壳**,不重造隧道/服务逻辑。真正的保护跑在 Windows 服务(LocalSystem,
子项目⑤已实现),托盘只是「监控 + 开关」。

## 2. 提权模型(已定:非提权托盘 + 按动作提权)

- 托盘进程本体**非提权**常驻——只轮询 `bx status` 显示状态,不弹 UAC(开机自启友好)。
- 只有点「连接 / 断开 / 设置 / 重启」这类**改动系统**的动作时,才 `ShellExecuteW` verb `runas` 拉起
  一个**提权**的 `bx.exe <up|down|setup|restart>` 子进程执行(仅此时弹 UAC)。
- 动作前 MessageBox 确认(对标 BxMenu「接管系统流量前确认」;断开也确认)。

## 3. 状态检测(全非提权)

轮询(如每 3s)合成托盘态,来源按可靠性叠加:
- **Windows 服务状态**(只读 SCM,`SC_MANAGER_CONNECT`,非管理员可读——子项目⑤的 `openServiceRead` 已是只读):
  服务 Running → 保护中;Stopped/Disabled → 已关闭;服务不存在 → 未安装。
- **config 是否存在**(`C:\ProgramData\bx\config.yaml`):不存在 → 未配置(Setup Required)。
- **控制面 socket `bx status --json`**(best-effort):socket 可读则取延迟/出口/传输/健康,丰富 tooltip;
  不可读(权限/未跑)则退化为仅服务态。
- 合成 5 态:**未安装 / 未配置 / 已关闭 / 保护中(+延迟/出口)/ 需注意**(服务应在跑但隧道不健康)。
- **纯映射函数**(可测):`func trayStateFrom(svcRunning bool, configExists bool, report *StatusReport) TrayState`。

## 4. 菜单(极简,对标 BxMenu)

按当前态动态生成:
- 顶部**状态行**(禁用项):如「● 保护中 · 延迟 401ms · 出口 1.2.3.4」/「○ 已关闭」/「⚠ 需注意」/「未配置」。
- **连接**(态=已关闭):确认 → 提权 `bx up`。**断开**(态=保护中):确认 → 提权 `bx down`。二选一按态显示。
- **从剪贴板设置…**:读系统剪贴板 → 校验是否 `bx://`/`vless://`/`brook://` 等受支持前缀 → 提权 `bx setup <link>`;
  成功后 MessageBox 问「立即连接?」→ 是则提权 `bx up`。剪贴板不是合法链接 → 提示「先复制 bx:// 链接再点此」。
- **打开状态**:MessageBox 显示 `bx status`(文本;非提权读控制面/服务态)。
- **查看日志**:`notepad C:\ProgramData\bx\service.log`(子项目⑤已把服务日志落此文件)。
- **重启保护**(仅态=需注意时出现):确认 → 提权 `bx restart`。
- **退出**:关托盘进程(服务继续跑,保护不受影响)。

## 5. 技术

- **托盘库** = `fyne.io/systray`(getlantern/systray 的维护 fork)。windows 实现走 Win32、**零 cgo**,
  bx 仍 `CGO_ENABLED=0`;只在 `//go:build windows` 下引用(同 winipcfg/winfw,不连累其他平台编译)。
- **剪贴板读 / MessageBox / ShellExecute** 走 `golang.org/x/sys/windows` 原生(OpenClipboard+GetClipboardData、
  user32 `MessageBoxW`、shell32 `ShellExecuteW`)——不加额外依赖。
- **图标**:内嵌 3 个 `.ico`(protected 绿 / off 灰 / attention 红),`//go:embed`;systray `SetIcon` 按态切换。
- **开机自启**:注册 `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` = `"<exe路径>" tray`
  (`golang.org/x/sys/windows/registry`,首次 tray 运行时幂等注册;菜单不做开关,YAGNI)。
- **启动入口**:`bx.exe tray` 子命令(安装包/开始菜单快捷方式指向它,见子项目③)。托盘进程无控制台窗口
  (windows GUI 子系统 vs console 子系统的取舍见「风险」)。

## 6. 可测逻辑(TDD)

windows-only UI 代码不可 Linux 单测,但抽出纯逻辑 TDD:
- `trayStateFrom(...)`:服务态 + config 存在 + report → 5 态映射(含「需注意」= running 但 unhealthy)。
- `parseSetupLink(clipboard string) (link string, ok bool)`:trim、校验受支持前缀(bx://blink://vless://…),
  拒空/非链接。复用现有 `setup`/`blink` 的链接识别逻辑(`resolveConfigLinks`/`tunnel.Kind`),不另造。
- 菜单标签/可见性由态决定的纯函数(如 `menuItemsFor(state) []item`)。

## 7. 测试与验收

- **纯逻辑单测**(免真机、跨平台):`trayStateFrom`、`parseSetupLink`、`menuItemsFor`。
- **交叉编译**:`GOOS=windows GOARCH=amd64/arm64 go build`(含 systray + tray 代码);linux/darwin 不受影响
  (tray 全 windows-tag)。
- **真机验收**(030-SJWJ-GSR-B):`bx.exe tray` → 托盘出图标;复制 reality bx:// → 「从剪贴板设置」→ UAC →
  setup 成功 → 「连接」→ UAC → 保护中、图标变绿、tooltip 显延迟/出口==VPS;「打开状态」看面板;
  「断开」→ UAC → 图标变灰、出口回直连;「退出」关托盘服务仍跑;重启机器验证自启图标出现。

## 8. 不做(YAGNI)

- 不做完整控制面板 GUI(对标 BxMenu 的克制)。
- 不做 Win32 输入对话框(设置走剪贴板,已定)。
- 不做托盘内的传输切换/direct-proxy 白名单编辑/流量图表(CLI 已有;托盘只给必要入口)。
- 不做自启开关菜单项(首次注册即可;要关自启走系统设置/CLI)。
- 不做自动更新 UI(`bx update` CLI 已有)。

## 9. 风险

- **控制台 vs GUI 子系统**:`bx.exe` 现是 console 子系统(CLI 用),从 Explorer 双击 `bx.exe tray` 会闪一个
  黑框。托盘理想是 GUI 子系统(无黑框)。但同一个 exe 不能既是 console 又是 GUI 子系统。取舍:
  (a) 托盘进程用 `-ldflags -H=windowsgui` 单独构建?——但那样整个 bx.exe 变 GUI 子系统,CLI 输出没了。
  (b) 托盘用 `FreeConsole()`(启动时立即释放控制台)隐藏黑框——保 console 子系统 + CLI 可用,托盘启动瞬间
  的黑框用 FreeConsole 消除。**倾向 (b)**:`bx tray` 首行 `windows.FreeConsole()`。实现时真机验证黑框是否消除。
- **控制面 socket 非管理员可读性**:若 socket ACL 不允许非提权托盘读 `bx status`,状态退化为仅服务态
  (Running/Stopped)——仍够用(protected/off),延迟/出口细节缺失。实现时真机确认;必要时子项目⑤的
  socket 创建放宽 ACL(留作实现期决定,不在本 spec 强定)。
- **fyne.io/systray 依赖体积/维护**:成熟库,windows 零 cgo;加一条 module require(同 winipcfg 已开的先例)。

---

**下一步**:本 spec 通过后进 writing-plans 出分步实现计划(纯逻辑 TDD + windows 交叉编译 + 真机验收),
再 subagent-driven 执行。
