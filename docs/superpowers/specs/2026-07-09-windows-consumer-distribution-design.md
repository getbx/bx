# Windows 消费级分发 + 小白可用 设计(整体方案)

> 目标:让 bx 在 Windows 上**方便分发、小白可用**——这是产品理念。真机端到端已跑通
> (见 CLAUDE.md「Windows 真机 e2e」),但当前上手门槛对非技术用户偏高:散落文件、只有
> 命令行、手动提权、企业 MITM 网络要手动配置。本文定**整体产品形态**并拆成三个可独立交付
> 的子项目;每个子项目再各自出 spec → plan → 实现 → 真机验。

## 0. 现状痛点(真机联调实测)

| 坎 | 现状 | 小白影响 |
|---|---|---|
| 散落文件 | `bx.exe` + `wintun.dll`(+ reality 要 `sing-box.exe`)必须同目录 | 少放一个就报错,不懂为什么 |
| 只有命令行 | 全靠管理员 PowerShell 敲 `bx run/setup/up` | 不会用命令行 = 用不了 |
| 手动提权 | 需右键「以管理员身份运行」 | 不提权直接失败,报错看不懂 |
| 企业 MITM | 公司网络 TLS 拦截 → github 下载 sing-box 失败 | reality/vless 用不了,需手动配 `singbox_bin` |
| 无安装包 | 没有 .exe/.msi 安装向导、没有卸载入口 | 不符合 Windows 用户直觉 |

## 1. 整体产品形态:一个自包含的 `bx.exe`

**一个文件承载全部能力**(与「静态单文件、零依赖」理念一致,与 Linux/darwin 已内嵌
brook+sing-box 的做法平级):

```
bx.exe (~80MB)
├── CLI            现有:run / setup / up / down / status / restart …
├── Windows 服务    现有:svc.Run handler(SCM 握手)+ svc/mgr 管理
├── 托盘 UI         新增:Go systray——连/断、贴链接、状态、日志、退出
└── 内嵌资产        新增:wintun.dll + sing-box + brook(零下载、零散落文件)
```

**小白路径**:双击 `bx.exe` → UAC 弹窗提权 → 托盘图标出现 → 点「设置」贴 `bx://` 链接 →
点「连接」→ 整机接管。全程不碰命令行 / yaml / dll / 企业网络下载。

**关键决策**(已确认):
- **托盘形态 = Go 原生 systray**——一个 exe 同时是 CLI + 服务 + 托盘,一种语言、好维护,
  与「单文件」一致(不选独立 C#/WinUI:多技术栈、多进程、打包重)。
- **内嵌范围 = 全内嵌**(wintun.dll + sing-box + brook)——对齐 Linux/darwin,真单文件、
  零下载。**顺带彻底消掉企业 MITM 挡下载的坑**(内嵌了不用下载)。
- **提权 = exe 嵌 UAC manifest**——双击自动弹提权,不用教用户右键。

四个坎一次全消:散落文件→全内嵌;命令行→托盘;MITM→免下载;手动提权→UAC manifest。

## 2. 拆成三个子项目(按序,各自独立 spec/plan/实现/真机验)

### ① 内嵌 Windows 二进制(基础,先做)

把 `wintun.dll` + `sing-box` + `brook` 内嵌进 `bx.exe`,首次运行释放到 DataDir/exe 旁
(复用 `provision.Ensure*` 模式)。CI 加 windows 的**自建静态 sing-box**(同 linux 的
`CGO_ENABLED=0 -tags with_utls,with_quic`)+ windows brook,按 GOOS/GOARCH 条件 embed。

- **为什么先做**:打好「自包含」地基——零下载、零散落文件、MITM 坑消失;托盘/安装包才有意义。
- **顺带收拾**:上一轮加的「wintun.dll 必须随行 + `SelfInstall` 拷 dll」(`sidefiles_windows.go`)
  改为**内嵌 + 运行时释放**,`bx run` 前台跑也不再要求同目录放 dll;`EnsureSingbox`/`EnsureBrook`
  的 windows 下载兜底退居其次(内嵌优先,download 仅无内嵌 arch 兜底,同 linux/darwin)。
- **交付验收**:`bx run`/`up` 在**只有一个 bx.exe**(无任何随行文件)的目录跑通,reality
  隧道健康、整机出口==VPS,且**在企业 MITM 网络也不触发任何 github 下载**。

### ② Windows 托盘 App(核心体验)

`bx.exe` 无参双击(或 `bx.exe tray`)启动 Go systray 托盘:
- 连接/断开开关(底层调 `up`/`down` 或直接 supervisor)。
- 「设置」:贴 `bx://` 链接(等价 `bx setup`),校验 + 写配置。
- 状态:节点 / 延迟 / 出口 / 传输(读控制面 socket,同 `bx status`)。
- 看日志、退出。对标 macOS `apps/macos/BxMenu` 的交互与克制(不做复杂控制面板)。

- **底层复用**:现有 CLI + 控制面 unix socket + 服务层,不重造。托盘只是 UI 壳。
- **交付验收**:全程不开命令行,双击→贴链接→连接→看状态→断开,真机跑通。

### ③ UAC 自动提权 + 打包分发(收口)

- exe 嵌 UAC manifest(`requireAdministrator` 或 `highestAvailable`)——双击自动弹提权。
  需区分:托盘/服务安装要提权,纯 `bx status` 只读不必(若 manifest 强制提权会打扰,
  设计时权衡:可能托盘走 manifest、CLI 保持按需)。
- CI release 出**两个产物**:便携 `bx.exe`(直接跑)+ 薄安装包 `bx-setup.exe`(Inno Setup:
  开始菜单快捷方式、Add/Remove Programs 卸载项、装完自动起托盘 + 装服务)。
- **交付验收**:小白拿到 `bx-setup.exe`,双击装、开始菜单能启动、能从「添加/删除程序」卸载。

**顺序**:① 地基(自包含)→ ② 托盘(体验)→ ③ 提权+安装包(分发收口)。

## 3. 不做(YAGNI)

- 不做独立 .NET/WinUI GUI(多技术栈、多进程)。
- 不做复杂控制面板 UI(对齐 macOS 的克制:托盘只给必要入口)。
- 不做自动更新 UI(现有 `bx update` CLI 已够;托盘可后续加一个「检查更新」入口,不在本轮)。
- 不改 Linux/darwin 的现有分发(macOS 菜单栏 App + 包已有;本设计仅补 Windows 侧)。

## 4. 跨平台一致性

- 内嵌资产:Windows 加入 `internal/embedded` 的条件 embed 矩阵(同 brook/singbox 现有
  `embedded_*.go` 平台覆盖),CI `embed-*.yml` 跟上。
- 托盘:Windows 独有(Go systray);macOS 已有 Swift 菜单栏 App;Linux 无(服务器/CLI 导向)。
- 命令模型不变:托盘与安装包都是现有 `setup`/`up`/`down`/`status` 之上的壳。

---

**下一步**:本整体设计通过后,先 brainstorm **子项目①(内嵌 Windows 二进制)**,出其独立
spec → plan → 实现 → 真机验;再依次②③。
