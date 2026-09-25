# macOS

## 菜单栏

macOS 用户优先使用统一安装包(`Bx.app`)。安装后菜单栏图标常驻显示保护状态。菜单第一行是一个**开关**(`Protection`,控制中心那种):开保护、关保护都拨它,没有单独的 Start / Turn Off 文字项——同一个动作在两个状态里有两个名字,用户得先读一遍才知道现在是开是关。开关下面那行暗色小字是连接摘要(服务器名 · 延迟),出问题时改写成原因并标红。往下是 `Reconnect`,四扇窗口 `Routing Rules…` / `Servers…` / `Traffic by App…` / `Check for Leaks…`(前三扇只在这一版 Guardian 声明了对应能力时出现;`Check for Leaks…` 每个状态都在,保护关着时它照样有用),`Troubleshoot ▸` 子菜单(`Check for Problems`、`Open Logs`、装的是哪一版、`Uninstall bx…`),最后 `Quit bx…`(⌘Q)。有新版时顶部另加一行 `Update bx…`;没装 / 没配置的状态给的是 `Install bx…` / `Set Up bx...`。诊断值(DNS、直连解析、UDP 中继)**只在出问题时才占一行**——正常时天天一个样的东西不是信息。网络变化后自动安全恢复;恢复时可能短暂断网但绝不回落直连。`bx reconnect` 仅用于 troubleshooting,不是日常网络切换步骤。命令行仍然保留,用于自动化、远程诊断和高级维护。

## macOS 安装包

macOS release 包是统一安装:一份 `bx-macos-<arch>.tar.gz` 只含 `Bx.app`(内嵌 `Contents/Resources/{bx-cli,bx-bridge,release.json}`)、`install.sh`、`uninstall.sh`、`README.txt`,不再有顶层裸 `bx` 二进制。

安装(两种方式任选其一):

1. 把 `Bx.app` 拖到 `/Applications`,双击打开,菜单栏点 `Install bx…`(一次管理员授权)。
2. 运行包内 `./install.sh`(等价的命令行方式,内部对 `Bx.app/Contents/Resources/bx-cli app-install` 发起同一次 sudo 安装)。

> **远程 / 自动化(非交互 SSH)提示**:非登录 shell 不跑 `path_helper`,PATH 常不含 `/usr/local/bin`,`sudo bx …` 会报 `command not found`——用绝对路径 `sudo /usr/local/bin/bx …` 即可,不是安装失败。另:覆盖安装到一台**已经装过 bx**(Guardian 服务已加载,无论保护是否开启)的机器上要先确认(见下),而非交互 SSH 没有终端可问——此时安装会**报错中止**(绝不假装装好),确认要升级就跑 `./install.sh --yes`。

全新安装只做落位和铺路,不启动保护、不修改 DNS 或路由:

- `Bx.app` 落到 `/Applications/Bx.app`(唯一产品位置)
- 运行时装到 `/Library/Application Support/bx/runtime/<version>/`(root 拥有,按版本存放,升级靠切换 `current`)
- 稳定命令行入口 `/usr/local/bin/bx`(bridge,定位并 exec 到 `runtime/current` 里同版本的 CLI)——因此终端 `bx --version` 永远和 App 版本一致
- Guardian 保护服务的 plist 就绪但不 enable、不启动
- 菜单栏登录项指向 `/Applications/Bx.app`

装到一台**已经装过 bx**(Guardian 服务已加载)的机器上(覆盖安装/升级)则不同:安装会先问你一次。

- **保护开着**:在**屏障下**切换(2026-09-25 起)——先装一道只放行到服务器的阻断路由,再停旧 Guardian、换文件、起新 Guardian,最后把屏障交给新 Guardian、由它起 Core 并在健康后拆掉屏障。期间断网几秒,**但没有任何流量从隧道外出去**。任何一步失败都停在屏障后面,出路是重跑同一条命令、`sudo bx up`(会把没做完的切换做完),或 `sudo bx down`(明确不要保护、拿回网络)。
- **保护关着**:停服务 → 换文件 → 重启 Guardian 服务,不碰网络。

只换文件不重启进程,跑着的仍是旧版本(2026-08-08 真机事故),所以 Guardian 一定会被换掉。菜单里的 `Install bx…` / `Repair bx…` 走同一条路(确认框在菜单里弹)。

菜单栏 App 是 macOS 的默认体验:它显示当前保护状态、延迟、DNS 接管状态和诊断入口。**状态编码在盾牌的轮廓形态上,不在颜色上**(图标是 template image,由系统按明暗菜单栏自己上色,旁边没有状态点):**实心盾**=已保护,**空心盾**=已关闭 / 未配置 / 未安装,**虚线盾**=正在开或关,**沿中线裂开的盾**=需要注意(隧道不健康、DNS 没接管、Repair Required、恢复失败)。另有极慢的呼吸(保护中)与明显的脉冲(过渡中);系统里开了「减弱动态效果」时四态全静止,所以**形态必须独自可分**,动效只是加强。鼠标停在图标上有 tooltip 说明原因。

**实心盾(已保护)的完整含义**:隧道健康、路由保护到位,**且 DNS 已核实由 bx 接管**——三者缺一不可。DNS 处于未接管(`unmanaged`)或状态不明(`unknown`、旧版 core 未上报)时,菜单栏一律显示**裂盾**、并在开关下面那行写出原因(`DNS not managed` / `DNS status unavailable`),不会因为隧道通就报已保护。同样地,`sudo bx up` 在 DNS 未能接管时会**返回错误**而非静默成功。安全恢复、重连和更新在返回绿色之前都会重新核实 DNS,因此不存在"路由已恢复但 DNS 还漏着"的中间态。

安装后打开菜单栏图标即可。如果显示 `Setup Required`,点击 `Set Up bx...` 粘贴客户端链接,或服务器打印的那整条 `sudo bx setup --udp … …` 命令(v0.4.9 起);配置成功后菜单栏会询问是否立即 `Start Protection`。命令行备用路径是 `sudo bx setup '<client-link>' && sudo bx up`。

把第一行那个 `Protection` 开关拨到关,只停止保护、恢复 bx 管理的 DNS,菜单栏 App 本身继续常驻;拨完菜单不关闭——开关变灰、进度就写在它下面那行,失败则弹回并说明原因。`Quit bx…` 是另一回事:它在停止保护之上再确认关掉菜单栏 App。也可以用命令行 `sudo bx down`。

**更新**:统一安装布局下 `sudo bx update`(等价菜单栏 `Update bx…`)是就地在线更新,覆盖 App+CLI+runtime 三组件,完成后 `bx --version` 与 App 版本一致。行为按当前保护状态分两条路:

- **保护开启**:经 Guardian 安全事务(4 个阶段——1/4 准备并校验新包、2/4 更新中、3/4 重连、4/4 完成),期间网络可能短暂暂停,但全程 fail-closed(DNS 保持接管、绝不回落直连);新版本未通过健康检查会自动回滚到旧版本并保持保护。事务提交后 Guardian 自己退出、由 launchd 以新版本拉起并接管还在跑的 Core(v0.4.6 起的 plist 带 `AbandonProcessGroup`),所以 Guardian 本身也换成新版、不断网。
- **保护关闭**:直接文件级升级(1/2 校验安装、2/2 完成),零网络影响。

`bx update --check` 始终只读,只查有无新版,不下载不安装。`--json` 输出结构化 `UpdateResult`(`from_version`/`to_version`/`phase`/`core_activated`/`rolled_back`/`protection_state`);`rolled_back=true` 时退出码非零。若 App/CLI/runtime 三版本出现不一致,菜单栏会出现 `Repair bx…` 一键修复;更新进行中开关下面那行写的是 `Updating bx…` 而**不是** `Blocked`——两者今天画的是同一个裂盾,把它们分开的只有那句话本身,所以那句话必须说对。菜单栏更新前的确认弹窗文案是「Internet access may pause briefly. bx will reconnect automatically.」。

**卸载**:

```bash
sudo bx uninstall
```

保护仍在运行时 `bx uninstall` 会拒绝并提示先 `sudo bx down`(它自己不会代为停止保护)。卸载会移除 Guardian plist、统一 runtime、`/usr/local/bin/bx`、`/Applications/Bx.app` 和登录项,但保留 `/etc/bx`(连接配置)与 `/var/lib/bx`(运行时数据)。

## 排查:保护起不来时

Guardian 的控制 socket 落在 bx 自有的运行期目录 `/var/run/bx/`(`guardian.sock`,Core 的 `core.sock`/`core.pid` 同目录),而不是共享的 `/var/run` 根——目录本身经权限校验,不受同目录下其它进程占位影响。`sudo bx up`/`bx down` 会自动等 socket 就绪,并对已加载但探测无响应的服务强制 kickstart 一次;仍失败时会打印 Guardian 自身日志尾巴而不是裸 dial 错误。手工排查可以:

- `sudo launchctl print system/com.getbx.bx.guard`(看服务是否 loaded、上次退出码)
- `sudo tail -20 /var/log/bx-guard.err.log`(守护进程自身的拒绝原因与失败的完整错误;该日志安装时被设为 `0600` root-only,故要 `sudo`。`bx logs` 看的是 Core 日志,不含这些)

bx 不会与其它全局 VPN 争抢默认路由:如果另一条隧道(如 Tailscale、WireGuard 等)已经占着默认路由,`bx up` 会明确报告是哪个接口占用、无法解析网关,而不是崩溃循环——关掉对方那条全局隧道再试。

## 排查:关不掉怎么办

`sudo bx down` 不依赖 Guardian 还活着、也不依赖网络还通:

- **网络故障(解析不到默认网关)**:降级为纯阻断屏障继续走完拆除,而不是报错拒绝。
- **Guardian 无响应,或响应了却关不掉**(例如断网期间重启后恢复事务被永久阻断):自动改走强制停止,依次做**六**件事——

  1. 先记录"已关闭"(不再开机自启,也让还活着的 Guardian 不再把 core 退出当成崩溃去重装屏障、重启 core);
  2. 请求正在运行的 core 经自己的控制面退出,趁它还有路可走、由它的 defer 还原**它装的路由**;
  3. 停止 Guardian 服务,免得它在背后又把 core 拉起来;
  4. **删除屏障装下的阻断路由**——Guardian 连同它的所有权记录一起没了,再没有别的东西能删它。这一步才是真正让你重新上网的那一步(它删掉覆盖整个公网的那组 reject 路由),所以它**排在还原 DNS 之前**:两步互不依赖,而下一步那几条没有自带超时的外部命令绝不许拖住它;
  5. **还原系统 DNS**(macOS 的 `networksetup` DNS 是 Guardian 接管的,core 只是 127.0.0.1:53 的监听方,不还原就会"路由干净但网页照样打不开");它必须排在停掉 Guardian 之后,否则 Guardian 会把 DNS 抢回去。这一步有超时上限,免得一条卡住的 `networksetup` 让 `bx down` 永远挂着;
  6. 再记录一次关闭意图。便宜、幂等,而且此刻才是权威的:Guardian 已经被 bootout,没有任何东西能覆盖它,一个抢在第 1 步前面的并发 `up` 也留不下 On。

  任一步失败都不会中断后面的步骤,并会把失败原因和下一步一起打印出来(DNS 还原失败会提示手动 `sudo bx dns off`)。

强制停止只停服务、只删 bx 自己装的路由,**不动 `/etc/bx`、`/var/lib/bx` 与任何配置**。它会如实告诉你做了什么,但不替你断言网络已恢复——请用 `bx status` 或打开任意网页确认。若仍不通,`sudo bx uninstall` 会停止全部服务并还原网络(同样保留 `/etc/bx` 配置,便于重装)。

## 开发模式 / 从源码安装菜单栏 App

仓库内 `./bx`(本地 `go build` 产物)照常可以跑测试、`sudo ./bx run` 前台调试;它不会注册生产 Guardian 服务,也不会覆盖或写入统一 runtime(`/Library/Application Support/bx/runtime`)。

只想单独开发/测试菜单栏 App(不涉及统一安装的 CLI/runtime/Guardian)时,可以从仓库源码打包并安装到当前用户:

```bash
cd /path/to/bx
scripts/install-macos-menu.sh install
```

安装后会生成并安装:

- `~/Applications/Bx.app`
- `~/Library/LaunchAgents/com.getbx.bx.menu.plist`
- `~/Library/Logs/bx/menu.log` 和 `menu.err.log`

常用维护命令:

```bash
scripts/install-macos-menu.sh status
scripts/install-macos-menu.sh restart
scripts/install-macos-menu.sh uninstall
```

这条路径只装菜单栏 App 本身,`/usr/local/bin/bx` 仍由统一安装的 bridge 提供。菜单栏比 Guardian 新时,菜单里会多出一行 `Guardian` 附注(`Older build; live Core status and diagnostics archive unavailable`)以及它自己给出的那条补救命令——**它是一条与保护状态并排的附注,不是一个顶掉保护状态的状态**:Protected/Off、开关、Reconnect 一个不少,降级的只有它点名的那一项。按上面「macOS 安装包」的方式重新走一遍安装即可。不要手工拿本地 `./bx` 覆盖 `/usr/local/bin/bx`,那会绕开 bridge,让 `bx --version` 和 App 版本脱节。
