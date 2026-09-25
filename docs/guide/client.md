# 客户端:Linux / Windows / agent

## Linux

```bash
sudo ./bx setup --udp '<UDP 链接>' '<主链接>'   # 服务器打印的那整条命令,原样贴
sudo bx up
```

Linux 客户端直接使用这组命令;只有一条链接时就是 `sudo ./bx setup '<链接>'`。完整步骤见 [install-tutorial.md](install-tutorial.md)。

## Windows

Windows 的 `bx.exe` 是**自包含单文件**——wintun.dll、sing-box、brook 全部内嵌,首次运行自动释放,不需要任何随行文件、不联网下载。从 [Releases](https://github.com/getbx/bx/releases) 有两种拿法:

- **安装包(推荐,小白)**:`bx-setup.exe` —— 双击装到 `C:\Program Files\bx`、进开始菜单、可从「添加/删除程序」卸载,装完自动起托盘。
- **便携版**:`bx_windows_amd64.zip` / `bx_windows_arm64.zip` —— 解压即用的单个 `bx.exe`(CLI + Windows 服务 + 托盘,全内嵌)。

> ⚠️ **首次运行 SmartScreen 提示**:产物暂未做代码签名,Windows 会弹「Windows 已保护你的电脑 / 未知发行者」。点 **更多信息 → 仍要运行** 即可。(有证书后会补签名,提示消失。)

**图形用法(推荐)**:双击托盘图标(安装包已自动启动;便携版双击 `bx.exe` 或 `bx.exe tray`)→ 复制你的 `bx://` 链接 → 托盘「从剪贴板设置」(此时弹 UAC 提权)→「连接」→ 整机接管。托盘常驻非提权、只在改动系统的动作时逐次提权;状态、日志、断开、重启都在托盘里。

**命令行用法(高级 / 自动化)**,以**管理员** PowerShell 运行:

```powershell
.\bx.exe setup "<client-link>"   # 写配置 + 装到 C:\Program Files\bx + 建 Windows 服务
.\bx.exe up                       # 启动服务并设为开机自启(整机接管)
.\bx.exe status                   # 看状态
.\bx.exe down                     # 停并取消自启
```

配置写到 `C:\ProgramData\bx\config.yaml`。首次真机联调建议先 `bx run --test-timeout 2m`(前台 + 死手,到点自动还原,防路由改错断网)。

> **受限网络 / 企业 TLS 拦截(MITM)**:release 版已内嵌 sing-box/brook,正常**不触发任何下载**。仅当你从源码自建、或用了没有内嵌二进制的架构时,bx 才会从 GitHub 下载 sing-box/brook;若所在网络对 HTTPS 做 TLS 拦截(企业代理自签根 CA),下载会因证书不受信而失败(`x509: certificate signed by unknown authority`)——这是 bx 供应链校验在**正确拒绝** MITM 证书,不是 bug。此时手动准备一份本地 sing-box 可执行(与 bx 版本匹配),在配置里用绝对路径指过去即可绕开下载:
>
> ```yaml
> singbox_bin: C:\path\to\sing-box.exe   # 直接用本地 sing-box,不联网下载(brook 传输对应 brook: <path>)
> ```

## 让你的 agent 操作 bx(AI-native,可选)

`bx setup` / `bx up` 跑通后,把控制面接给你的 agent:让你的 agent 运行

    bx mcp install

并照打印的 `claude mcp add` 指令做(**只打印、不自跑**)。之后 agent 就能查状态、
安全重连、换传输和重劫持——以**业主**身份授权、**无需 sudo**
(业主 = 运行 `sudo bx setup` 的用户)。工具权限与安全流程见[Agent Tools](agent-tools.md)。

`setup` 会安装系统服务,`up` 会启动并接管流量,`down` 会停止保护。

> **多传输(容灾 + 加速)**:bx 支持 **brook / REALITY / hysteria2 / trojan / shadowsocks / vmess 六种引擎**平级,
> 直接甩别处的分享链接即可用(裸链接建议先 `bx blink <link>` 换壳)。但**六种不是一个层次**——按当今封锁/检测态势分档:
>
> - 🟢 **主力**:**REALITY**(TCP,最隐蔽,2026 实测 98-99% 突破)+ **hysteria2**(UDP/QUIC 速度档,建议配 salamander 混淆)。
> - 🟡 **兼容**:trojan / vmess / shadowsocks / brook——接住已有节点,但 2025 起强 DPI 下 trojan/vmess/ss 检出 80-95%,慎用于强封锁。
>
> 推荐组合 = **REALITY(TCP)+ hysteria2(UDP)+ brook 兜底**,即"按类分流 + 容灾",既安全又有速度。
> `bx setup` 贴兼容档链接会提示弱点并建议 server 端换 REALITY(不止 GFW——Claude/OpenAI/Google 等也对弱协议出口 IP 做风控)。
> 全程 fail-closed 不泄漏。详见 [multi-transport-guide.md](multi-transport-guide.md)。
