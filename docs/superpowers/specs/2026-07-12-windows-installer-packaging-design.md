# 子项目③:UAC manifest + Windows 打包分发(设计)

> 属「Windows 消费级分发 + 小白可用」整体方案(`2026-07-09-windows-consumer-distribution-design.md`)
> 的第三块——**分发收口**。地基(①内嵌二进制)让 `bx.exe` 自包含,②托盘给了图形入口;
> 本子项目给它消费级 Windows 分发的最后一层:exe 自带 UAC-aware manifest + 图标 + 版本信息,
> 产出薄安装包 `bx-setup.exe`(开始菜单、添加/删除程序、装完起托盘),并把 Windows 正式接入
> release 产物(现状:`release.yml` 只出 linux/darwin,Windows 根本没进 release 矩阵)。

## 1. 目标与形态

三部分,各自独立可验:
- **Part A** — 给 `bx.exe` 嵌入 Windows 资源:UAC manifest(`asInvoker`)+ 应用图标 + 版本信息。
- **Part B** — Inno Setup 安装包 `bx-setup.exe`:装 exe、开始菜单快捷方式、添加/删除程序卸载项、装完起托盘。
- **Part C** — `release.yml` 接入 Windows:交叉编译便携 `bx.exe`(amd64+arm64)+ 一个 windows job 出安装包。

**小白路径(③收口后)**:拿到 `bx-setup.exe` → 双击(SmartScreen 提示未知发行者 → 「仍要运行」)→
安装器自提权装到 Program Files + 开始菜单 → 装完托盘图标出现(非提权)→ 复制 `bx://` → 托盘「从剪贴板
设置」(此时 per-action UAC)→ 连接。卸载走「添加/删除程序」。

## 2. 关键决策(已确认)

- **manifest = `asInvoker`(不强制提权)**。这是相对原分发文档「双击→UAC 提权托盘」的**有意修正**:
  子项目②已把托盘做成**非提权常驻 + per-action `runas` 提权**。若 exe 嵌 `requireAdministrator`,
  托盘会以管理员跑、每个 `bx status`/CLI 都弹 UAC,推翻②的设计。故 exe 用 `asInvoker`——
  双击 `bx.exe` 非提权起托盘,改动系统的动作各自弹 UAC(②已实现),安装器 `bx-setup.exe` 自身
  `PrivilegesRequired=admin` 在安装时提权。**与②的 per-action 模型完全自洽。**
- **不做 code signing**(现无证书)。产物无签名,Windows SmartScreen 会提示「未知发行者」;
  文档明确告知用户「仍要运行」的点法。留到有证书再补签名步骤(CI 加一步 signtool,不改结构)。
- **安装包 amd64-only**。便携 `bx.exe` 出 amd64+arm64 两个;安装包只出 amd64(Windows-on-ARM
  消费占比 <1%,arm64 用户用便携 exe)。
- **应用图标 = 专用多分辨率 `bx.ico`**(16/32/48/256,绿盾 bx 标,与托盘 `protected.ico` 同族),
  Explorer/任务栏/安装器统一显示。
- **安装器装完起托盘、不装服务**:安装器没有用户的 `bx://` 链接,故不建服务;服务由托盘
  「从剪贴板设置」→ 提权 `bx setup` 创建(与②一致)。卸载先 `bx.exe uninstall`(停+删服务)再删文件。

## 3. Part A — exe 资源(manifest + 图标 + 版本)

- 工具 **`github.com/tc-hib/go-winres`**(纯 Go,Linux 上可跑,不引入 Windows 依赖)。
- `winres/winres.json` 描述三块资源:
  - **manifest**:`identity`(name=`getbx.bx`, version 占位);`requestedExecutionLevel level=asInvoker uiAccess=false`;
    `dpiAwareness=PerMonitorV2`;`longPathAware=true`。
  - **icon**:`winres/bx.ico`(多分辨率;新建)。绑到默认 icon 资源(exe 图标)。
  - **versioninfo**:ProductName=`bx`、CompanyName=`getbx`、FileDescription=`bx transparent proxy`、
    ProductVersion/FileVersion(占位,release 时 CI 覆盖成 tag)。
- 生成 `rsrc_windows_amd64.syso` + `rsrc_windows_arm64.syso`,**提交进仓库根**(Go 链接器自动把
  匹配 GOOS/GOARCH 的 `.syso` 链入 `main` 包的 windows 构建)。
- `go generate` 指令(如 `//go:generate go run github.com/tc-hib/go-winres make --in winres/winres.json`)
  + `winres/README.md` 记录重生成方式(换图标/版本时 `go generate ./...` 后提交新 .syso)。
- **不变量**:`.syso` 只影响 windows 构建;linux/darwin 的 `go build` 完全不看它。
- **CI 版本覆盖**:release 时用 `go-winres patch`(或 make 时传 `--product-version`/`--file-version`)
  把 PE VersionInfo 写成 tag 版本,与 `-ldflags -X internal/version.Version` 一致。

## 4. Part B — Inno Setup 安装包

`packaging/windows/bx-setup.iss`:
- `[Setup]`:`AppId={{...固定 GUID...}}`、`AppName=bx`、`AppVersion`(CI 传入)、
  `PrivilegesRequired=admin`(安装器自提权)、`DefaultDirName={autopf}\bx`、
  `UninstallDisplayIcon={app}\bx.exe`、`OutputBaseFilename=bx-setup`、`ArchitecturesInstallIn64BitMode=x64`。
- `[Files]`:`Source: bx.exe; DestDir: {app}`(仅一个文件——已全内嵌)。
- `[Icons]`:开始菜单 `{group}\bx` → `{app}\bx.exe` 参数 `tray`;`{group}\卸载 bx` → uninstaller。
- `[Run]`:`Filename: {app}\bx.exe; Parameters: tray; Flags: postinstall nowait skipifsilent`(装完起托盘,非提权由 manifest asInvoker 决定)。
- `[UninstallRun]`:`Filename: {app}\bx.exe; Parameters: uninstall; Flags: runhidden; RunOnceId: bxsvc`
  (卸载前停+删服务;`bx uninstall` 已实现)。
- 添加/删除程序卸载项:Inno 自动生成(靠 AppId/AppName)。
- **验收**:CI `iscc` 编出 `bx-setup.exe`;真机双击装 → 开始菜单起托盘 → 添加/删除程序能卸载、
  卸载后服务/文件清干净。

## 5. Part C — release.yml 接入 Windows

现状 `release.yml`:ubuntu-latest,只 build linux+darwin(amd64/arm64)tar.gz + SHA256SUMS。改为:
- **ubuntu job**(现有,扩):循环加 `windows` amd64+arm64——`CGO_ENABLED=0 GOOS=windows go build`
  (带 .syso 自动链入 + `-ldflags` 版本),产 `bx_windows_{amd64,arm64}.zip`(内含 `bx.exe`)。
  linux/darwin 仍 tar.gz。上传便携产物为 artifact 供下游 windows job 用。
- **windows job**(新增,`runs-on: windows-latest`,needs: ubuntu job):下载 amd64 `bx.exe`
  → 装 Inno Setup(choco 或缓存)→ `iscc /DMyAppVersion=<tag> packaging\windows\bx-setup.iss`
  → 上传 `bx-setup.exe`。
- **汇总**:release 附 `bx_{linux,darwin}_*.tar.gz` + `bx_windows_{amd64,arm64}.zip` + `bx-setup.exe`
  + `SHA256SUMS`(含全部产物)。
- **CI 验证**(非 release,`ci.yml`):加 `GOOS=windows GOARCH=amd64/arm64 go build`(确保 .syso 不破构建);
  `iscc` 语法检查可选(windows runner 上 `iscc /DMyAppVersion=0.0.0-ci` 干跑)。

## 6. 可测 / 验收分层

- **dev 可验(Linux)**:`go-winres make` 生成 .syso;`GOOS=windows GOARCH=amd64/arm64 go build -o /dev/null .`
  成功(带资源);`go build ./... && go test ./...`(linux/darwin 不受影响)。**这是本子项目在
  dev 上唯一能自动验的部分**——资源不破构建。
- **CI-only**:`iscc` 编 `.iss`(Linux 无 `iscc`);release 全流程。
- **真机验收(030-SJWJ-GSR-B)**:双击 `bx-setup.exe` → SmartScreen「仍要运行」→ 装 → 开始菜单
  起托盘(图标带 bx.ico)→ exe Properties 显版本 → 复制 `bx://` 设置连接 → 出口==VPS → 卸载清干净。
  与②的真机验收可合并一次做。

## 7. 不做(YAGNI)

- 不做 code signing(无证书;留到有证书,CI 加 signtool 一步即可)。
- 不做 MSI / winget / Chocolatey(Inno 单包够;winget 可后续投稿)。
- 不做安装器内输链接 / 选传输(装完交给托盘,对齐②克制)。
- arm64 不出安装包(便携 exe 覆盖)。
- 不做自动更新(现有 `bx update` CLI 够)。

## 8. 风险

- **`iscc` 只在 windows**:Linux dev 编不了 `.iss`,靠 CI windows runner + 真机。`.iss` 写错只有
  CI/真机才暴露——故 `.iss` 尽量薄(单文件安装,逻辑最少)。
- **无签名 → SmartScreen**:首次双击 `bx-setup.exe` 会被 SmartScreen 拦「未知发行者」,需用户
  「更多信息 → 仍要运行」。文档(README/release notes)明确写这一步,否则小白会以为是病毒。
- **`.syso` 提交进仓库**:换图标/版本需重生成并提交;`go generate` + README 记录,避免陈旧。
  `.syso` 是二进制,review 时看不了 diff——靠 `winres.json`(文本,可 review)作真相源。
- **go-winres 版本覆盖**:PE VersionInfo 与 `-ldflags` 版本两处需一致;CI 用同一 tag 变量喂两边。

---

**下一步**:本 spec 通过后进 writing-plans 出分步实现计划(Part A 资源 + build 验证 / Part B .iss /
Part C release 接入),再 subagent-driven 执行,末尾与②合并真机验收。
