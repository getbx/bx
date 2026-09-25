# 服务端:bx server

## 安装

和客户端一样简单——**一条命令**:

```bash
sudo ./bx server up --open-ufw   # 装好(默认 REALITY+hysteria2、自动探测公网IP)并启动;系统里没有 ufw 就去掉 --open-ufw
bx server status           # 看状态
sudo bx server down        # 停
```

`bx server up` 自动:生成 x25519 密钥/UUID/证书、探测公网 IP、写配置、装系统服务、**启动**,
并打印客户端**一键命令**(`sudo bx setup --udp '<hys2>' '<reality>'`,flag 在链接之前、**最后一条才是主链接**,整条原样贴到电脑上;主 reality 隐蔽 TCP + hysteria2 加速 UDP,按类分流)。
全程内嵌静态 sing-box,**无需手搭、零配置**。SNI 默认借 `www.cloudflare.com`(装机时自动体检证书大小;
别用 microsoft——证书过大会让 reality 握手失败),内置 `flow=xtls-rprx-vision`/`fp=chrome` 等 2026 推荐默认。

需要别的:`--protocol hysteria2`(纯速度档)、`--protocol brook`(简单兜底)、`--tcp-only`(reality 不带 hys2)、
`--host <域名>`(自定义)、`--port`、`--sni`。协议怎么选见 [multi-transport-guide.md](multi-transport-guide.md)。
分享给普通用户优先用用户管理层:`sudo bx user invite <name>` / `sudo bx user list` / `sudo bx user revoke <name>`。

之后也可以随时重新生成链接:

```bash
sudo bx server link --host <VPS_IP或域名>
```

分享给其他人:

```bash
sudo bx user invite alice
sudo bx user list
sudo bx user revoke alice
```

如果 VPS 使用 ufw,创建分享时可显式放行端口:

```bash
sudo bx user invite alice --open-ufw
```

也可以启动一个只监听本机的极简 Web UI:

```bash
sudo bx server ui --host <VPS_IP或域名>
```

然后从自己的电脑通过 SSH 隧道访问:

```bash
ssh -L 8787:127.0.0.1:8787 <VPS>
```

浏览器打开 `http://127.0.0.1:8787`。
