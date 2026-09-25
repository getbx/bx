# 命令

| 命令 | 作用 |
|---|---|
| `sudo bx server install --host <host>` | VPS 首次安装 bx server |
| `sudo bx server start` | 启动 bx server 并设为开机自启 |
| `sudo bx server stop` | 停止 bx server 并取消开机自启 |
| `sudo bx server link --host <host>` | 生成客户端链接 |
| `sudo bx invite [name]` | 生成给普通用户的安装/配置邀请 |
| `sudo bx user list` | 查看用户 |
| `sudo bx user show <name>` | 查看一个用户 |
| `sudo bx user invite <name>` | 生成或复显用户邀请 |
| `sudo bx user revoke <name>` | 撤销用户 |
| `sudo bx server share <name> --host <host>` | 创建一个独立分享链接 |
| `sudo bx server shares` | 查看已分享的链接 |
| `sudo bx server shares --json` | 以 JSON 查看已分享的链接 |
| `sudo bx server revoke <name>` | 撤销一个分享 |
| `sudo bx server rotate --host <host>` | 轮换 server 密码并生成新的客户端链接 |
| `sudo bx server logs` | 查看服务端日志 |
| `sudo bx server ui --host <host>` | 启动只监听本机的极简 Web UI |
| `sudo bx server uninstall` | 卸载 bx server 服务 |
| `sudo bx setup <client-link>` | 客户端首次配置 |
| `sudo bx server deploy <user@host>` | 从本机把 bx server 装到一台裸 VPS(走系统 ssh,**bx 不经手凭据**);加 `--name <名字>` 装好后自动加进本机清单(**不会切换当前出口**) |
| `bx server list` | 列出已配置的服务器:出口主机、UDP 出口、以及观测到的峰值吞吐(带年龄) |
| `bx server list --test` | 顺便逐台测延迟与可达性。**它会往隧道外面发包**,所以是显式的一下,不是默认行为 |
| `sudo bx server use <name>` | 换到清单里的另一台:武装 → 等新隧道健康 → 确认;起不来就地回滚 |
| `sudo bx server rm <name>` | 从清单里删掉一台(不许删当前在用的那台) |
| `sudo bx up` | 启动客户端并设为开机自启 |
| `sudo bx down` | 停止客户端并取消开机自启 |
| `sudo bx reconnect` | 安全重连传输:替代传输健康后切换,不中断 TUN、路由或 DNS |
| `bx update --check --json` | 只读检查已签名 release,供菜单栏或自动化读取 |
| `sudo bx update` | 校验已签名 release 并原子替换 CLI;macOS 统一安装(Bx.app)下走统一在线更新(保护开启经 Guardian fail-closed 事务、失败自动回滚,保护关闭直接文件级升级) |
| `sudo bx direct add <domain>` | 将域名加入直连白名单(**TCP 与 UDP 都生效**)。**同名**的 proxy 规则会被一并清掉(`zoom.us` 与 `*.zoom.us` 算同一条);但被一条**更宽**的 proxy 规则盖住时**直接拒绝、不写**,并点名该删哪一行——`Explain` 先查 proxy 且没有「更具体优先」,写下去就是一条永远不命中的死规则,所以这道门刻意没有 `--force`。命中公有云/CDN 风险名单会拒绝,那一道**有** `--force` |
| `sudo bx direct rm <domain>` | 从直连白名单移除域名 |
| `sudo bx proxy add <domain>` | 强制域名走隧道,会与 direct 规则互斥清理 |
| `sudo bx proxy rm <domain>` | 从强制隧道列表移除域名 |
| `bx preset ls` | 列出内置应用可用性预设 |
| `bx preset show <name>` | 查看预设将加入的直连域名 |
| `sudo bx preset apply <name>` | 显式应用预设并在运行时热加载 |
| `bx egress add <name> <socks5>` | 加一个具名出口(mesh 用不了时的退路;地址必须是 loopback) |
| `bx egress route <name> <cidr>` | 把一个网段交给该出口(**白名单**,只有写出来的走它) |
| `bx egress ls` / `bx egress rm <name>` | 查看 / 删除(rm 连同它的网段一起删) |
| `bx dns status` | 查看 macOS DNS 接管状态 |
| `sudo bx dns on` | 手动将 macOS 系统 DNS 切到 bx |
| `sudo bx dns off` | 恢复 bx 保存的 macOS 原始 DNS |
| `bx status` | 查看客户端状态面板 |
| `bx capabilities` | 输出机器可读能力清单 |
| `bx doctor` | 诊断客户端配置、服务状态和链接连通性 |
| `bx doctor --json` | 输出客户端机器可读诊断 |
| `bx leakcheck` | **泄漏检测（推荐）**：开本地页面，把浏览器那半（WebRTC/出口/指纹）与本机那半（路由/DNS）对起来。bx 关着、别的 VPN 在跑时照样能用。**还会从本机探测几个 AI 站**（Anthropic / Claude / OpenAI / Google AI），回答「这条路能不能到达它们」—— 那不是安全问题，所以单独一段、单独计数，`--no-reach` 可关 |
| `bx leak-check --json` | 非交互的机器可读检查（不开页面；供 MCP 与脚本） |
| `bx leak-check --network --json --expected-ip <ip>` | 主动探测 IPv4/IPv6/DNS 出口并判断是否符合预期 |
| `bx explain <域名或 IP>` | **「这个目标在我这台机器上会怎么走」** —— 先答本机视角(解析到什么、进不进 TUN、绑网卡的程序会走哪),再答 bx 的判定(命中哪条规则、本次与累计各失败多少次)。bx 没在跑也能用 |
| `bx apps` | 按应用看分流:哪个应用的流量走隧道、直连还是被拦(采样一个窗口,默认 6 秒) |
| `bx leakcheck --no-reach` | 同上,但**不探测那几个 AI 站**(默认会探;见下) |
| `bx observe --json --duration 30s --scenario video` | 观察短窗口内连接、分流、UDP 阻断和流量变化 |
| `bx logs` | 查看客户端日志 |
| `bx logs --json` | 输出 agent 可读的客户端日志文本、错误和提示 |
| `scripts/package-macos-menu.sh` | 打包 macOS 菜单栏 App 到 `dist.noindex/macos/Bx.app`(`.noindex` 后缀让 Spotlight 不去索引构建产物,否则 `mdfind` 会把它当成一个装好的 Bx.app) |
| `scripts/package-macos-release.sh` | 生成 macOS release 目录和 `.tar.gz` |
| `scripts/verify-macos-release.sh` | 验证 macOS release 目录、压缩包和 SHA256SUMS |
| `scripts/darwin-unified-install-check.sh` | 统一安装真机验收(默认 dry-run) |
| `scripts/darwin-unified-update-check.sh` | 统一更新真机验收(默认 dry-run) |
| `scripts/open-privacy-checks.sh` | 打开第三方浏览器指纹参考页(默认 dry-run;不采集、不判断) |
| `scripts/install-macos-menu.sh install` | 安装并启动 macOS 菜单栏 App,不启动 protection、不修改网络配置 |
| `scripts/install-macos-menu.sh status` | 查看 macOS 菜单栏 App 安装和运行状态 |
| `scripts/install-macos-menu.sh uninstall` | 移除 macOS 菜单栏 App 和登录项,不关闭 protection |
| `sudo bx run` | 前台运行,用于调试 |
| `sudo bx uninstall` | 卸载客户端服务 |
| `sudo bx server doctor` | 诊断服务端配置、监听端口和服务状态 |
| `sudo bx server doctor --json` | 输出服务端机器可读诊断 |
