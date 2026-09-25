# bx 安装教程:从服务器到电脑

从零到整机受保护要四步:在一台 VPS 上装好 bx server,拿到一条 `bx://` 链接;在电脑上装 bx;把链接贴给电脑端;打开保护并验证出口。

## 准备什么

你需要两台机器:一台境外 VPS 当出口,一台要保护的电脑。两边用的是同一个 `bx` 程序。

| | 要求 |
| --- | --- |
| VPS | Linux(amd64 或 arm64,Ubuntu 24.04 实测过);能以 root 或带 sudo 的用户 ssh 登录;公网 IP |
| VPS 端口 | 默认 443,TCP 和 UDP 都要进得来(TCP 给 REALITY,UDP 给 hysteria2) |
| 电脑 | macOS(Apple 芯片或 Intel)、Windows 10/11、或 Linux(amd64 / arm64) |
| 下载 | [GitHub Releases](https://github.com/getbx/bx/releases) |

云厂商的**安全组 / 防火墙面板**要你自己在网页上放行 443 的 TCP 和 UDP。VPS 系统里的 ufw 由 bx 放行(方式 A 加 `--open-ufw`,方式 B 自动)。

## 第一步:装服务器

装完服务器,你会拿到一整条可以直接复制的客户端命令,形如 `sudo bx setup --udp 'bx://…' 'bx://…'`。**把它原样存好**,第三步要用。两种装法任选一种。

### 方式 A:ssh 进 VPS,在 VPS 上装(最直接)

1. 登录 VPS:`ssh root@<VPS的IP>`
2. 下载并解压(ARM 的 VPS 把 `amd64` 换成 `arm64`):

```bash
curl -LO https://github.com/getbx/bx/releases/latest/download/bx_linux_amd64.tar.gz
tar -xzf bx_linux_amd64.tar.gz
chmod +x bx
```

3. 一条命令装好并启动:

```bash
sudo ./bx server up --open-ufw
```

它会自动生成密钥、探测公网 IP、装系统服务、放行 ufw(系统里没有 ufw 就去掉 `--open-ufw`,否则会报错),默认装 REALITY(TCP,抗探测)+ hysteria2(UDP,加速)。最后打印客户端命令。

4. 确认在跑:`bx server status`

### 方式 B:在自己电脑上一条命令装到 VPS

适合电脑上已经装好 bx(先做完第二步)。bx 走你自己的 ssh,**不经手密码或密钥**:

```bash
bx server deploy root@<VPS的IP> --name myvps
```

VPS 会自己去 GitHub 下载并校验 bx(比从你电脑传过去快得多),装好后打印客户端命令;带 `--name` 且用 sudo 跑时,还会把这台服务器加进本机清单(不会切换你当前的出口)。非 root 用户登录也行,bx 会自动用 sudo。

### 常用选项

| 选项 | 作用 |
| --- | --- |
| `--port 8443` | 换监听端口(443 被干扰时) |
| `--sni <域名>` | REALITY 借用的真实站点,默认 `www.cloudflare.com`;**不要用 microsoft**(证书过大会握手失败) |
| `--protocol hysteria2` / `brook` | 只要速度档 / 简单兜底 |

以后要重新拿链接:在 VPS 上 `sudo bx server link --host <VPS的IP>`。

## 第二步:装电脑端

这一步只装程序,**不会打开保护,也不改网络设置**。

### macOS

1. 下载 `bx-macos-arm64.dmg`(Apple 芯片)或 `bx-macos-amd64.dmg`(Intel)。
2. 打开 dmg,把 **Bx.app** 拖进 **Applications**,双击打开。
3. 系统说「**无法验证开发者**」:打开 **系统设置 → 隐私与安全性**,找到 bx 那一条,点 **仍要打开**。bx 还没有 Apple 开发者签名,所以每个人首装都会遇到。
4. 它会问 **Install bx?**,确认并输入一次电脑密码。装好后菜单栏出现 bx 图标,终端里也有了 `bx` 命令。

> 如果看到的是「**已损坏,应移到废纸篓**」,那是另一回事:包在传输中被改动过。**不要放行,也不要照网上说的跑 `xattr`**,重新下载。

### Windows

1. 下载 `bx-setup.exe`,双击安装(装到 `C:\Program Files\bx`,装完自动起托盘图标)。
2. SmartScreen 说「**Windows 已保护你的电脑**」:点 **更多信息 → 仍要运行**(同样是还没签名)。

不想装的话有便携版 `bx_windows_amd64.zip`,解压就是一个 `bx.exe`。

### Linux

跟 VPS 上一样下载解压即可,不用单独安装 —— 第三步的 `bx setup` 会把自己装进 `/usr/local/bin/bx`。

```bash
curl -LO https://github.com/getbx/bx/releases/latest/download/bx_linux_amd64.tar.gz
tar -xzf bx_linux_amd64.tar.gz && chmod +x bx
```

## 第三步:接上服务器并打开保护

把第一步存下的那一整条命令**原样**贴进电脑的终端。它有两条链接:`--udp` 后面那条给 UDP 加速,**最后那条才是主链接**,别拆开贴。`setup` 只写配置、测一次连通,不改网络;`up` 才真正接管整机流量。

| 电脑 | 写配置 | 打开保护 |
| --- | --- | --- |
| macOS | 打开「终端」,贴 `sudo bx setup --udp '…' '…'`(或用菜单,见下) | 菜单栏 bx 图标里拨开 **Protection**,或 `sudo bx up` |
| Linux | 在解压目录贴,把 `bx` 改成 `./bx`:`sudo ./bx setup --udp '…' '…'` | `sudo bx up` |
| Windows | **管理员** PowerShell,去掉 `sudo`,在 bx.exe 所在目录(安装版是 `C:\Program Files\bx`)运行:`.\bx.exe setup --udp '…' '…'` | `.\bx.exe up`,或托盘里点「连接」 |

打开之后网络会停一两秒,然后整机流量走隧道。以后开机会自动打开,直到你关掉它。

**Mac 也可以不开终端**:菜单栏 bx 图标里的「Set Up bx…」,把那整条命令原样贴进去即可(v0.4.9 起;更早的版本只收一条链接,那时要贴**最后那条**主链接,会少 UDP 加速)。Windows 托盘的「从剪贴板设置」只收一条链接,贴**最后那条**。

**关于模式**:默认是**全局模式**,所有流量(含国内网站)都走隧道。想让国内网站直连,见最后一节。

## 第四步:确认真的在保护

三件事都对上才算装好了(Windows 把 `bx` 换成 `.\bx.exe`)。

1. **状态**:`bx status` 应显示 `Status  Protected`,`Tunnel  ● healthy`。
2. **出口 IP**:`curl https://icanhazip.com` 打印的应是**你 VPS 的 IP**,不是你宽带的 IP。验出口别用 ifconfig.me 或 ipify 这类在国内直连列表里的网站,它们在分流模式下会直连,报出你的真实 IP。
3. **泄漏检测**:`bx leakcheck`(不要 sudo)会打开一个只在本机的页面,点一下开始,它把浏览器那半(WebRTC、IPv6、DNS)和本机那半(路由表)对起来看有没有漏。

**装好之后把浏览器、聊天软件这类常开的应用完全退出再打开一次**(Mac 上是 Cmd+Q)。保护打开之前就建好的连接,系统不会把它们挪进隧道,它们会继续用真实 IP 直到自己关掉。bx 新版会在 `bx status` 里用红字点名这类应用。

## 日常使用

| 要做什么 | 怎么做 |
| --- | --- |
| 关掉 / 打开保护 | Mac 菜单栏的开关;或 `sudo bx down` / `sudo bx up` |
| 看状态、查问题 | `bx status`;`bx doctor` 逐项体检并给出处理办法 |
| 升级 | `sudo bx update`,或 Mac 菜单里的 **Update bx…**;保护开着也能升,失败会自动回滚 |
| 让某个网站直连 | `sudo bx direct add '*.example.com'`;改完立刻生效 |
| 查某个网站走哪条路 | `bx explain example.com` |
| 国内网站全部直连(分流模式) | 把 `/etc/bx/config.yaml` 里的 `global: true` 改成 `false`,再 `sudo bx down && sudo bx up` |
| 多台服务器 | `bx server list` 看清单,`sudo bx server use <名字>` 切换;Mac 菜单里的 **Servers…** 窗口也能加、换、测 |
| 手机也用这台 VPS | 在 VPS 上 `sudo bx server share phone --qr`,用 sing-box / Hiddify / Shadowrocket 扫码 |
| 卸载电脑端 | `sudo bx down`,再 `sudo bx uninstall`(保留 `/etc/bx` 里的配置,方便重装) |
| 卸载服务器 | 在 VPS 上 `sudo bx server uninstall` |

几条要知道的:

- **隧道断了就断网,不会退回直连**。这是设计:宁可打不开网页,也不用真实 IP 出去。`bx status` 会说出连不上哪台服务器。
- **关掉保护期间打开的连接,重新打开保护后仍走真实 IP**,直到应用自己关掉它。开关过一次之后,把浏览器这类应用重开一次。
- 酒店、咖啡店 Wi-Fi 要先登录网页时,用 Mac 菜单里的 **Open Wi-Fi Sign-In Page**,登录完隧道会自己连上。
- 服务器端口打不通时,先查云厂商安全组是否放行了 443 的 TCP 和 UDP。
