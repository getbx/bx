# 应用可用性预设

默认不为任何 App 放宽直连规则。遇到 Steam 下载、Apple 服务或常见国内 CDN 的可用性问题时,可明确查看并启用对应预设:

```bash
bx preset ls
bx preset show gaming
sudo bx preset apply gaming
```

当前内置 `apple`、`china-cdn`、`gaming`、`tencent`。预设只向客户端配置加入经过筛选的 `direct` 域名规则,并清理同名 `proxy` 规则;运行中的 bx 会热加载,不会直接修改 TUN、路由或 DNS。要撤销某一项,用 `sudo bx direct rm <domain>`。

**一个组 = 一件「让某类应用正常工作」的事,不是一批域名。** 这个区别在你要判断「该不该开」时才显出来:
`apple` 管的是 iCloud 同步与 Game Center 能不能连上,`tencent` 管的是微信/腾讯会议的登录、消息与媒体
(那一组是一次真实排查的固化 —— 白名单里只有 `*.qq.com` 时,腾讯会议整个跑在 `*.tencent.com` 上,
信令和媒体流全部绕到境外再回来,而**发现它花了半小时**)。`gaming` 刻意只含**纯字节**:游戏文件与
更新,商店页面不在其中 —— 它的 HTML 走隧道而图片走直连的话,同一个页面会一半美国一半本地。

**预设里的每一条都过同一道风险门。** `bx direct add` 会拒绝开放平台域(任何人都能在
`*.myqcloud.com`、`*.amazonaws.com` 上注册子域,而 bx 的匹配器是后缀集 —— 一条直连规则覆盖它的
每一个子域,于是陌生人能让你的真实 IP 走到隧道外面);**预设不许绕过那道门**,由
`TestNoPresetShipsARuleItsOwnCommandWouldRefuse` 钉着 —— 一键装进去一条自己的命令会拒绝的规则,
是同一道门给出两个答案。所以 `tencent` 只收固定服务域 `*.im.qcloud.com`,不收对象存储。

macOS 菜单栏的 **Routing Rules** 窗口把这几组画成可勾选的行(半装的组显示成第三态),点 `Show`
能展开看这一组到底管哪些域名;下面另起一段是**你自己加的**规则。

如果只想生成 `.app` 包而不安装:

```bash
scripts/package-macos-menu.sh
open dist.noindex/macos/Bx.app
```

生成可分发 macOS release 包:

```bash
scripts/package-macos-release.sh
```

产物:

```text
dist.noindex/release/bx-macos-arm64/
  Bx.app
  install.sh
  uninstall.sh
  README.txt
dist.noindex/release/bx-macos-arm64.tar.gz
dist.noindex/release/SHA256SUMS
```

`Bx.app/Contents/Resources` 内嵌 `bx-cli`、`bx-bridge` 和 `release.json`(校验用的 sha256 摘要),不再有顶层裸 `bx` 二进制。

发包前可验证产物:

```bash
scripts/verify-macos-release.sh
```

macOS DNS 状态可单独查看或手动修复:

```bash
bx dns status
sudo bx dns on
sudo bx dns off
```

**正常使用不需要这三条命令。**`sudo bx up` 自己会接管 macOS DNS,`sudo bx down` 自己会还原;安全恢复、重连、更新也都会在返回绿色前重新核实 DNS。`bx dns on`/`off` 是**修复工具**,只在诊断出 DNS 状态异常(菜单栏黄色 `DNS not managed`,或 `bx doctor` 的 `guardian_dns` 检查失败)时才用得上,不是启动流程的一环。

macOS launchd 实机验证可先 dry-run:

```bash
scripts/darwin-launchd-smoke.sh
sudo BX_LINK='<client-link>' scripts/darwin-launchd-smoke.sh --execute
```

已运行 bx 的安全重连也可先单独 dry-run。它不启动或停止服务，不改 DNS、路由或配置；明确加 `--execute` 后才会发起一次 `bx reconnect`，并验证 split-default 路由、DNS 接管和隧道健康均保留：

```bash
scripts/darwin-testkit.sh --reconnect-check
sudo scripts/darwin-testkit.sh --reconnect-check --execute
```

统一安装与统一更新也各有真机验收脚本,默认 dry-run(只在临时目录打包并打印计划,零系统改动),显式 `--execute --yes` 才真正执行:

```bash
bash scripts/darwin-unified-install-check.sh          # 统一安装演练(dry-run)
sudo bash scripts/darwin-unified-install-check.sh --execute --yes
bash scripts/darwin-unified-update-check.sh           # 统一更新+自动回滚演练(dry-run)
sudo bash scripts/darwin-unified-update-check.sh --execute --yes
```

日常使用:

```bash
bx status
bx doctor
bx logs
bx dns status
sudo bx down
sudo bx up
```

给脚本或 AI agent 诊断时,使用 JSON 输出:

```bash
bx capabilities
bx doctor --json
sudo bx server doctor --json
sudo bx server shares --json
```

接入 MCP 后,agent 可优先调用 `bx_inspect`、`bx_leak_check`、`bx_logs` 这些只读工具拿结构化诊断,再决定是否需要改动类操作；`bx_reconnect` 是唯一的常规安全恢复动作。

`bx capabilities` 会输出稳定的机器可读能力清单,标明每个入口是否需要 root、是否会修改系统或网络、是否读取敏感配置。上面的 JSON 诊断命令只读取状态并输出机器可解析结果,不会修改系统或网络配置。
