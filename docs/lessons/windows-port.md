# Windows 移植:施工日志(①–⑭)

**这份文件是过程,不是判据。** 从 CLAUDE.md 搬出(2026-09-13):那一节是一个 10k 字符的
单行,记的是十四个子项各自怎么做成的,而 CLAUDE.md 该回答的是「改这块之前必须知道什么」。

**判据留在 CLAUDE.md 的「跨平台待办 / Windows」**;这里是每一步的经过、真机 e2e 的
逐条结果、以及当时踩的坑。Windows 侧的代码今天仍然只由 CI 的交叉编译 + 单测覆盖,
真机 e2e 停在 2026-07-09 那次(`030-SJWJ-GSR-B`,Win10 19044)。

---

**第 1/2 步 + 第 3 步的 OpenTUN/DirectDialer/Hijack 已做**——交叉编译通过(`embedded_other.go` brook 兜底、`paths_windows.go`)+ CI 三平台矩阵(ubuntu/macos/windows runner 跑单测,全绿)。真机环境已就绪(SSH 提权可达,`bx.exe` 已执行)。**第 3 步进度**:① **OpenTUN**——wintun `CreateTUN`→`wgbridge`(照抄 darwin,去掉 utun 命名限制,`bx0` 直用;回填适配器 `LUID` 供 Hijack;运行时需签名 `wintun.dll` 同目录);② **DirectDialer**——`GetBestInterfaceEx` 探物理默认网卡 index → `IP_UNICAST_IF`(v4)/`IPV6_UNICAST_IF`(v6)防环,复用共享 `shouldBindToDevice`「仅公网目的地才绑」。头号坑 IPv4 字节序(`htonl(index)`,MSDN 怪癖)抽到 `unicastif.go` 的 `unicastIfV4Value`(纯逻辑 `bits.ReverseBytes32`)TDD 覆盖;③ **Hijack**——用 **winipcfg**(`wireguard/windows/tunnel/winipcfg`,包级依赖实际只 x/sys,GUI 依赖不编译)拿 TUN 的 LUID 配地址 + split-default(`0.0.0.0/1`+`128.0.0.0/1`)劫进 TUN,server/私网/SSH bypass 经**物理默认网关**(`physicalDefaultRoute` 从 `GetIPForwardTable2` 取 metric 最低的 `0.0.0.0/0`)旁路;**IPv6 fail-closed**(宿主有 v6 时把 `::/1`+`8000::/1` 劫进 TUN;域名维度 v6 已由 DNS `AAAA→NODATA` 堵死,此为字面量 v6 纵深防御,best-effort 不连累 v4)。纯路由计划抽到 `windows_routes.go`(`windowsRoutes`,与 API 无关)TDD 覆盖;teardown **逐条 `DeleteRoute` 对称还原**(不 flush 物理 LUID),配 `--test-timeout` 死手复原;④ **WFP 封 off-TUN :53**(防 Windows smart-multihomed DNS 泄漏)——**vendor** `wireguard/windows/tunnel/firewall`(MIT)进 `internal/winfw`,加薄入口 `winfw.BlockDNSLeak(tunLUID,nil)` 只装三条过滤器 `permitSelf(15)`+`permitTun(14)`+`blockDNS(deny 12)`、**刻意不带 `blockAll`**(bx 分流,china/direct/bypass 合法走物理,全封会打死)。**权重是正确性核心**:`permitTun(14) > blockDNS deny(12)` 让**进-TUN 的 :53 通**(fake-IP 解析靠它)、只封 off-TUN;不可照抄上游 `EnableFirewall` 的 `deny 14 > permitTun 12`(那靠 `restrictToDNSServers` 例外放行隧道 DNS,bx 不依赖特定 DNS IP)。动态会话(`FLAG_DYNAMIC`)进程退出/崩溃**自动清过滤器**,比路由更 fail-safe。⑤ **Windows Service**(`bx up/down/uninstall` 自启)——两侧:进程侧 `internal/cli/service_windows.go` 的 `svc.Run` handler(bx.exe 被 SCM 拉起时必须上报 Running/Stopped,否则 SCM 判超时杀;`isWindowsService()` 门控,控制台 `bx run` 调试照常前台;Stop 时 cancel ctx 触发 `supervisor.Run` 的 defer 全量还原,与信号关机同源),管理侧 `internal/install/service_windows.go` 用 `svc/mgr` 建/起/停/删(`install.WriteUnit/Enable/Disable/Uninstall/ExecStartCmd/ServiceState` 加 `case "windows"`,LocalSystem 跑)。服务 `BinaryPathName` 用带引号命令行(路径含空格),`commandLineFields`(纯逻辑 TDD)双向解析(建服务拆 exepath+args、读回取子命令做 up 防呆)。OS-aware 路径:`defaultConfigPath`=`C:\ProgramData\bx\config.yaml`、`install.BinPath`=`C:\Program Files\bx\bx.exe`(build-tagged `paths_{windows,other}.go`)。这五块 windows-only(winipcfg/winfw/svc/syscall)交叉编译 amd64/arm64 + vet 过,**真机待验(务必带 `bx run --test-timeout 2m`)**。⑥ **setup 端到端集成(2026-07-08)**:OS-aware 路径(`config.DefaultDataDir`=`C:\ProgramData\bx`、`defaultConfigPath`、`install.BinPath`,均 build-tagged;`setupAction` 不再硬编码 `/var/lib/bx`)+ **`EnsureBrook` 补下载兜底**(原来无内嵌时把 nil 当内容写出空文件冒充 brook → 隧道莫名崩;现 `override>内嵌>下载`,windows/无内嵌 arch 按 `defaultBrookURL` 从版本派生官方 release 地址或用 config `brook_url`/`brook_sha256`,`downloadBinary` 与 EnsureSingbox 共享,`.brook-src` 缓存键避免每次重下)。**brook:// 的 `setup→up` 在 Windows 已代码打通**。⑦ **sing-box windows zip 下载兜底(2026-07-08)**:`EnsureSingbox` 补齐 windows 下载——url 空则 `defaultSingboxURL` 从版本派生官方 release 地址(**注意 tag 带 `v` 前缀、资产文件名不带**:`.../download/v1.13.14/sing-box-1.13.14-windows-amd64.zip`),url 以 `.zip` 结尾则下载后 `extractSingbox` 解压取 `sing-box.exe`(忽略子目录,`path.Base` 匹配),裸二进制 url 仍直接落盘;sha256 校验的是下载物(zip)本身;`.singbox-src` 缓存键(sha 或 url)避免重下。**至此 vless/reality/hysteria2/trojan/ss/vmess 六种传输在 Windows 均可 setup→up(代码层)**。⑧ **DNS-into-TUN(2026-07-08)**:Hijack 给 TUN 适配器设哨兵 DNS `1.1.1.1`(`winipcfg LUID.SetDNS(AF_INET)`)——否则 Windows 系统 DNS 常指 LAN 路由器(私网 bypass + 被 WFP 封 off-TUN :53)→ DNS 整个断。哨兵是**会路由进 TUN** 的公网 IPv4(在 `0.0.0.0/1`、非私网 bypass;不变量由 `windns.go`+守卫测试 `TestTunDNSSentinelRoutesIntoTun` 钉死),系统 DNS 查询进 TUN 由 fake-IP handler 应答(`engine.go` 拦 UDP:53 到**任意**目的地);TUN 接口 metric 已 0(最优)使系统优先用它,off-TUN DNS 由 WFP `blockDNS` 封、bx 自身 resolver 由 `permitSelf` 放行(三者权重自洽)。teardown `FlushDNS` + 适配器随 `closeTUN` 销毁,物理 NIC DNS 从不被碰、还原干净。⑨ **真机 e2e 已验(2026-07-09,`030-SJWJ-GSR-B` Win10 19044,SSH 提权联调)**:梯度 `debug-tun`(wintun.dll 加载+适配器+干净移除)→ `run --no-hijack`(reality 隧道 433ms 健康、TUN、经隧道刷 china 列表)→ `run`(全量劫持)全过——**整机出口==203.0.113.123(VPS)**、`0.0.0.0/1` 指向 bx0、DNS `example.com→198.18.0.16`(fake-IP,经 TUN)、WFP-DNS=true、SSH 源 10.84.14.37 经 10/8 旁路全程存活、死手 teardown 后残留 bypass/TUN/WFP 全 0、出口回直连。真机暴露并当场修掉 2 个 bug:**① WFP `permitWireGuardService` 靠服务 SID 放行自身,bx 非服务跑→`ERROR_NO_SUCH_GROUP` 整个 WFP 建不起来**(改 `permitAppID` 按 app-id 放行,`winfw/dnsleak.go`);**② run.go 第 0 步无条件下 brook**,reality-only 也白下(改惰性,尤其公司网络 TLS MITM 挡 github 时不卡无关下载)。**环境注记**:公司网络对 HTTPS 做 TLS MITM(自签根 CA)→ bx 的 github 下载 `x509: unknown authority`(**这是 bx 供应链安全在正确工作**,拒绝 MITM 证书);真机测试用 `singbox_bin` 指本地 sing-box.exe 绕开(override 通路真机坐实)。⑩ **Windows Service e2e 已验(2026-07-09)**:`setup`(自动装 bx.exe 到 `C:\Program Files\bx` + 写 config 到 ProgramData + 建 SCM 服务 DEMAND_START LocalSystem)→ `up`(Enable 设 AUTO_START+Start、svc.Run handler 上报 Running、服务内起隧道+全量劫持、**整机出口==VPS、WFP-DNS=true**)→ `status`(读 `C:\ProgramData\bx\bx.sock` 控制面正常)→ `down`(Stop→svc ctx cancel→`supervisor.Run` defer teardown 还原干净、START_TYPE 转 DISABLED)→ `uninstall`/`sc delete`。真机暴露并修:**① `SelfInstall` 漏装 wintun.dll**——服务以 System32 为 CWD 跑 Program Files\bx.exe,DLL 只在源目录→`Error loading wintun.dll`→服务起了就退;当时的修法是在安装那一步把 wintun.dll 一并复制到 BinPath 同目录。**那条修法今天已经不在了(2026-09-13 核过:`installPlatformSideFiles` 与 `sidefiles_windows.go` 全仓零出现,`install.SelfInstall` 只复制 bx 二进制)** —— 取而代之的是子项目①的内嵌:dll 由 `internal/embedded` 按 GOOS/GOARCH 嵌进 exe,`provision.EnsureWintun` 在 `windowsPlatform.OpenTUN` 的第一句把它按内容 hash 释放到**当前 exe 所在目录**(`.wintun-version` 做缓存键),内嵌为空的 arch 则回落到系统已装的那份。**这两条路解决的不是同一个问题**:复制那条只保证 BinPath 旁边有一份,而服务与便携 exe 可能从别的目录跑;释放那条问的是「我这个进程的 exe 在哪」,所以它对两者都成立。(这条陈旧记述逃过了 `TestDocumentedFilePathsExist`,因为那条守卫的正则要求路径带目录前缀,而 `sidefiles_windows.go` 是个裸文件名。)**② 服务无控制台 stderr 丢弃**——加 `runAsWindowsService` 把 log 落 `C:\ProgramData\bx\service.log` + svc handler 记录 run 退出错误(靠它抓到 dll 错误)。**坑**:服务跑时 wintun.dll 必须在 exe 同目录 —— 这条**加载器的**要求今天仍然成立(wireguard-go 只搜 exe 目录 + System32),只是**满足它的人换了**:不再靠发布时随行,而是 `EnsureWintun` 在开 TUN 时自己写出来;`bx uninstall` 的 Delete 可能被查询句柄挂 pending(同 review #10),`sc delete` 可强删。**Windows 移植网络/服务/供给/DNS 全层代码完整且真机端到端背书。** ⑪ **hysteria2 UDP 档 e2e 已验(2026-07-09)**:config `udp.transport: hysteria2://…` + `udp.mode: proxy`,`bx status` 显示 `传输 reality@… UDP→hysteria2@…`(TCP=reality/UDP=hysteria2 按类分流);整机劫持下 **NTP(UDP:123)经 dialer→UDP 档→hysteria2(QUIC)→VPS→时间服务器往返成功**(`w32tm` 返回真实偏移)、UDP 阻断 0——sing-box hysteria2(`with_quic`)在 Windows 跑通。⑫ **`bx kick`→`bx restart`(2026-07-09,真机促成)**:真机复现 kick 在 Windows 恒 403(非 Linux 无 peer-cred,`peercred_other.go` fail-closed 拒改动类)+ kick 仅热切隧道(数据面卡住修不了),按需精简——删 kick(命令/控制面 `/v0/kick`/`KickControl`),改一条 `bx restart`=全量重启(`install.Restart`),保留自启;`swapTo` 仍供 `runFailover` 容灾用。⚠️ **注意**:Hijack 实现后 `bx run` 一旦隧道健康就直冲全量路由劫持,无 OpenTUN-only 止步点。故加了两个**安全梯度 bring-up 工具**:`bx debug-tun`(只建 wintun 适配器、不起隧道/不碰路由,隔离验证 `wintun.dll`+wgbridge,零系统改动)与 `bx run --no-hijack`(起隧道+TUN+引擎但跳过 Hijack,验证隧道健康+TUN,系统网络零改动)。真机梯度:`debug-tun` → `run --no-hijack` → `run --test-timeout 2m`(+ config bypass SSH/RDP 源)全量。**真机观察项(code-review 提的 PLAUSIBLE)**:server 是**主机名**(非 IP)时,隧道断线重连要重解析主机名——WFP `permitSelf` 只放行 `bx.exe`,brook/sing-box 子进程 app-id 不同,其 off-TUN :53 会被封;理论上靠哨兵 DNS(TUN 内、`permitTun` 放行)+ `staticA` 静态真 IP 答案兜住(子进程走系统 resolver→哨兵→拿真 IP),但真机需实测重连是否卡死;若卡,补 WFP 放行子进程 exe 的 app-id。**施工图见 `docs/superpowers/specs/2026-07-08-windows-tun-design.md`**。⑬ **托盘 App(子项目②,2026-07-12,代码完成+交叉编译验,真机待验)**:新增 `bx tray` 子命令(windows-only)启动 `fyne.io/systray` 系统托盘,小白点图标即可连/断/设置/看状态,全程不碰命令行——对标 macOS `apps/macos/BxMenu` 的克制,是现有 CLI+服务+控制面之上的**薄 UI 壳**,不重造隧道逻辑。**提权模型**:托盘进程**非提权**常驻(只轮询状态、不弹 UAC,开机自启友好),仅点「连/断/设置/重启」这类改动系统的动作时 `ShellExecuteW` verb `runas`(+`SW_HIDE` 隐藏子进程黑框)拉起**提权** `bx.exe <up|down|setup|restart>` 子进程执行(仅此时弹 UAC);动作前 `MessageBox` 确认。**状态检测全非提权**:`install.ServiceState("is-active","bx")`(只读 SCM)+ config 存在性 + spawn `bx status --json`(解析 `server`/`tunnel_healthy`/`latency_ms`/`transport`)合成 5 态(未安装/未配置/已关闭/保护中/需注意),`detectState` 每 3s 刷图标(绿/灰/红 `.ico` 内嵌)+ tooltip。**设置走剪贴板**:`readClipboardText`(user32 LazyDLL,x/sys 无剪贴板)→ `parseSetupLink` 校验受支持前缀(bx/vless/hysteria2/trojan/ss/vmess/brook/blink),**且拒绝内嵌引号/空白**(真链接是 URL-safe base64,挡 `bx setup "<link>"` 参数注入,纵深防御)→ 提权 setup。**自启** HKCU Run(`golang.org/x/sys/windows/registry`,`sync.Once` 幂等)。**黑框**:`freeConsole()`(kernel32 LazyDLL,x/sys 无)消除 console 子系统 exe 双击时的闪窗。包 `internal/tray`:纯逻辑(`state.go`/`status.go`,无 tag,Linux 单测 7 绿)+ windows-only UI/syscall(`tray_windows.go`/`win_windows.go`/`icons_windows.go`,`//go:build windows`);`fyne.io/systray` 只被 windows-tagged 文件 import(`go list -deps` 证 Linux 从不编译它,零连累)。amd64/arm64 交叉编译 + vet 过。**真机待验(Task 6,030-SJWJ-GSR-B)**:GUI 交互(点托盘、批 UAC)需人在机器旁,非 SSH headless 可驱。设计见 `docs/superpowers/specs/2026-07-12-windows-tray-app-design.md`、计划 `docs/superpowers/plans/2026-07-12-windows-tray-app.md`。⑭ **打包分发(子项目③,2026-07-12,代码完成+dev 验,真机待验)**:收口消费级分发。**(A) exe 资源**:`go-winres`(纯 Go,Linux 可跑)从 `winres/winres.json`(真相源)生成 `rsrc_windows_{amd64,arm64}.syso`(提交进仓库根,Go 链接器按 GOOS/GOARCH 自动链入 windows 构建),给 `bx.exe` 嵌 **manifest(`execution-level: as invoker`——绝不 requireAdministrator,保②非提权托盘 + per-action UAC)+ 图标(`winres/icon.png` 绿盾 bx 标)+ 版本信息**;`go generate ./...`(`generate_windows.go` 承载指令)重生成。**dev 端到端验过**:amd64/arm64 build 链入成功、`go-winres extract` 证资源已嵌、linux/darwin 不受影响。**(B) Inno Setup 安装包** `packaging/windows/bx-setup.iss`:`PrivilegesRequired=admin`(装器自提权)装单文件 `bx.exe`(已全内嵌)到 `{autopf}\bx` + 开始菜单快捷方式(`bx.exe tray`)+ 添加/删除程序 + 装完 `postinstall` 起托盘 + 卸载 `[UninstallRun] bx.exe uninstall`(停删服务);固定 `AppId={45A7EBE8-…}`(永不变);**不装服务**(无链接,交托盘 setup)。`.iss` 无法 Linux 编(`iscc` windows-only),CI/真机验。**(C) release 接入 windows**:`release.yml` build job 加 windows amd64/arm64 交叉编译(go-winres 先 `--product-version/--file-version` 覆盖 tag 版本再编)出 `bx_windows_*.zip`,新增 `installer` job(`windows-latest` + choco 装 Inno + `iscc` 出 `bx-setup.exe`)上传 release;`ci.yml` 早已含 windows 交叉编译 + 测试矩阵(`.syso` 守卫自动覆盖,无需改)。**(D) README** 加 Windows 安装(安装包/便携版/托盘)+ **SmartScreen「仍要运行」提示**(无 code signing,现无证书,留待有证书补 signtool 一步),并纠正旧文档「wintun.dll 必须同目录」(①已内嵌)。**关键决策**:manifest `asInvoker`(相对原分发文档「双击→UAC」的有意修正,与②自洽)、不签名、安装包 amd64-only(便携 exe 覆盖 arm64)。**真机待验(Task 5,与②合并)**:双击 `bx-setup.exe`→SmartScreen→装→开始菜单起托盘(带图标)→exe Properties 显版本→剪贴板设置连接→出口==VPS→添加/删除程序卸载清干净。设计 `docs/superpowers/specs/2026-07-12-windows-installer-packaging-design.md`、计划 `docs/superpowers/plans/2026-07-12-windows-installer-packaging.md`。**至此「Windows 消费级分发+小白可用」三子项目(①内嵌②托盘③打包)代码全完成,合并真机验收待用户在 Win 机器旁跑。**

---

## ⑮ 修复轮:把 Windows 那条腿从「恒红」修到真的会跑(2026-09-15/16)

**判据在 CLAUDE.md 的「跨平台待办 / Windows」与「约定」两处;这里是经过。**

起点:`master` 自 **2026-07-08** 起没有一次完整绿。Windows 腿 `go test ./...` 168 条失败,
而同期在真机上一次只读扫描抓到的**五条真缺陷**没有一条会被它们中的任何一个抓到。

### 收窄之后剩下的三条,以及各自的根因形状

| 测试 | 根因 | 处置 |
|---|---|---|
| `TestLookupRouteIsExplicitlyUnsupportedWhereItIsNotImplemented` | 守卫里**手抄的 GOOS 清单**只写 darwin/linux,而 `route_lookup_windows.go` 早就落地了 | 换成跟着实现文件走的 `lookupRouteSupported` 常量。新平台漏写它连编译都过不去 |
| `TestSaveReplacesAtomically` | `os.Stat` 在 Windows 上不带文件 ID,`os.SameFile` 才由 `loadFileId` 按**路径**补取 —— 两个 FileInfo 都解析到当时那一个文件,「换没换过」恒答「没换」 | 改用 `(*File).Stat`(走 `GetFileInformationByHandle`,当场填 vol/idxhi/idxlo)。**产品代码一个字没改** |
| `TestTailscaleBypassIsNotGatedByOS` | 锚点 `"\n}\n"` 在 CRLF checkout 下永不匹配 | 加 `.gitattributes` 强制 LF —— **不是给这一条打补丁**,那几个包里有 8 个读源码的测试文件 |

**第二条动手前先去 Go 标准库源码里核实了前提**(`stat_windows.go` 的
`newFileStatFromWin32FileAttributeData` vs `newFileStatFromGetFileInformationByHandle`,
`types_windows.go` 的 `sameFile` 比的是哪三个字段),没有凭记忆。

### 真机怎么跑的:那台机器没有 Go 也没有 git

把测试二进制**交叉编译**过去(`GOOS=windows go test -c`),连同包源码一起 scp。
这样不需要在一台公司工作站上装工具链。三轮:

1. **变异对照** —— 同时送 HEAD 版本与修复版本两个二进制:`TestSaveReplacesAtomically`
   修复前 **FAIL**、修复后 **PASS**,在真 Windows 上。
2. **CRLF 复现** —— 把 `tailscale_bypass.go` 转成 CRLF 送过去,当场复现
   「找不到函数结尾」;换回 LF 立刻转绿。这一条**只能这么验**:`.gitattributes` 的
   效果在本机看不见,本机的 checkout 本来就是 LF。
3. **七个包全跑** —— 第一次只带包源码,三条 appattr 守卫红在「扫描范围塌了」;
   补成**完整 `.go` 源码树**之后全绿。那一步顺带排除了路径分隔符问题
   (`filepath.Walk` 在 Windows 上给 `\`,而守卫拿 `/` 拼的路径去比对,是这类
   守卫在 Windows 上最容易出的真错)。

### 然后 CI 抓到了真机漏掉的那一族

推上去第一轮 `test-windows` 红了 **11 条**,全是
`GetFileAttributesEx /tmp: The system cannot find the file specified.` ——
`control_client_test.go` 写死 `os.MkdirTemp("/tmp", …)`,而 `/tmp` 在 Windows 上
解析成**当前盘**的 `\tmp`。

**那台真机恰好有 `C:\tmp`(事后 SSH 实测确认),于是同一批测试在真机上是绿的**,
而我已经拿它下过「七个包全绿」的结论。这条教训进了 CLAUDE.md 的「约定」。

修法**不是**换成 `t.TempDir()`:写死 `/tmp` 原本是有理由的 —— unix socket 的
`sun_path` 只有 104/108 字节,而 `t.TempDir()` 的路径带测试名(子测试再带一层),
超了 `net.Listen` 以 `invalid argument` 失败、**那个错误一个字都不提长度**。
新的 `shortSocketDir` 把两条互相拉扯的约束都写在注释里。
(这几条测的是活路径,不是该跳过的东西:bx 在 Windows 上用的确实是真 AF_UNIX,
见 `paths_windows.go`。)

### 顺带挖出来的:`swift build` 坏了五周,而且是同一个形状

修完跑 `verify.sh`,红在一个我没碰过的地方:`swift build` 报
`cannot find 'GuardianStatus' in scope`。根因是包上挂着的 `BxMenuTestPlugin` ——
它在 `swift build` 时顺带跑菜单包目录下的 `run-swift-tests.sh`(**本轮已删**,
所以这里刻意不写全路径 —— `TestDocumentedFilePathsExist` 会红,而它红得对),而那是
`scripts/test-macos-menu.sh` 的**第二份拷贝**,2026-08-01 的计划书白纸黑字写着
两份编译清单「人工保持一致」。

它漂了:插件那份停在 7 个套件、真正那份长到 32 个(前者是后者的严格子集);
它的 `guardian-client` 套件编 `GuardianClient.swift` 却没带 **11 天后**才出现的
`GuardianStatus.swift`;插件的 `inputFiles` 又是手抄的 10 个路径,SwiftPM 按它建沙箱,
**于是连让脚本读到那个文件都做不到 —— 两份清单各错各的。**

处置选「让守卫失业」:删掉插件、那份脚本、以及只为挂插件而存在的 `SwiftPMTests`
占位 target,加 `TestOnlyOneSwiftTestRunnerExists`。判据钉的是缺陷本身
(**能编 `Tests/` 下文件的脚本有几个**),而不是「插件在不在 `Package.swift` 里」——
后者挡不住换个方式再挂一份。判据还得**剥掉注释**再匹配:`verify.sh` 的注释里同时
写着 `swiftc` 与 `Tests/`,正是在解释这两步的分工。变异实测:放回去即红、撤回即绿。

### 收尾

`master` 在 2026-09-16 恢复 **7/7 全绿**。rebase 到一次 sing-box 自动升级之上时,
逐个核过新加的 `.gitattributes` 有没有碰坏那 150MB 内嵌二进制(大小逐字节一致、
`git check-attr` 报 `binary: set`)—— `* text=auto eol=lf` 撞上内嵌可执行文件,
一次探测失误就是把二进制改坏,而那种坏法在 `git status` 上看不出来。
