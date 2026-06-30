# REALITY 服务端搭建 runbook(给 bx 的 `vless://` 传输用)

> 目标:在你自己的 VPS 上立一个 **sing-box VLESS-REALITY 服务端**,产出一条 `vless://` 链接,
> 直接喂给 `bx setup vless://…`。客户端侧 sing-box 由 bx **内嵌**,服务端这份是你要自己跑的。
> 整机验证见 [reality-e2e-checklist.md](reality-e2e-checklist.md)。

> ## ⭐ 最简:`bx server install` 已自动化这整篇
>
> 现在不用手搭了——bx 服务端内置 reality 生成器(`internal/srvgen`),一条命令搞定密钥/UUID/shortID/
> 借用 SNI / sing-box 配置 / systemd 单元 / 客户端链接,内置 2026 推荐默认(`flow=xtls-rprx-vision`、
> `fp=chrome`、SNI 默认 `www.cloudflare.com`):
>
> ```bash
> sudo bx server install --protocol reality --host <VPS_IP或域名> [--sni www.apple.com]
> sudo bx server start
> # 末尾打印的 bx:// 链接 → 客户端 sudo bx setup <bx://…>
> ```
>
> 装机时还会**实连所选 SNI 体检**(TLS1.3 + X25519 + 证书链不过大),坏 SNI 当场警告。
> 下面的手搭 runbook 仅供理解原理 / 自定义场景(如复用已有 sing-box、特殊端口策略)。

## 0. 选端口和 SNI(先定,踩过的坑)

- **端口:别用 443。** 两个真实约束(实测):
  - 服务端 **ufw / 云安全组**必须放行该端口;
  - 路径上对 **443 有 DPI 干扰**,以及部分运营商对非标端口 SYN 黑洞(TCP 能连但 TLS 载荷被丢)。
  - → 落一个**已放行的高端口**(如 `9998`、`8388` 之类),和现网 brook 的 `9999` 同类。下面用 `9998` 举例。
- **借用 SNI(`server_name`/`handshake.server`):挑稳定 TLS1.3 + X25519、且证书链够小的站。**
  - **别用 `www.microsoft.com`**——根因坐实(2026-06-30 真机):**它证书链过大(完整链 ~5879B),
    超出 REALITY 借壳中继证书的承受 → 握手必失败**(服务端报 `processed invalid connection`)。
    这不是网络/密钥问题——换 SNI 即通(真机:microsoft 全挂、`www.cloudflare.com` VPS loopback +
    Mudi 跨主机跨 GFW 全通,出口==VPS)。
  - **挑证书链 < ~4.5KB 的站**:实测 `www.cloudflare.com` 2505B、`www.apple.com` 3230B、
    `dl.google.com` 3543B、`addons.mozilla.org` 4085B(均工作);`www.microsoft.com` 5879B(挂)。
  - 在 **VPS 上**验候选:支持 TLS1.3 且证书链不大——
    ```bash
    # TLS1.3 + X25519?
    openssl s_client -connect www.cloudflare.com:443 -servername www.cloudflare.com -tls1_3 </dev/null 2>/dev/null | grep -iE "TLSv1.3|X25519"
    # 证书链字节(粗估,越小越稳):
    openssl s_client -connect www.cloudflare.com:443 -servername www.cloudflare.com -tls1_3 -showcerts </dev/null 2>/dev/null | awk '/BEGIN CERT/,/END CERT/' | wc -c
    ```
  - `bx server install --protocol reality` 会**自动**做这套体检(`srvgen.CheckRealitySNI`),坏 SNI 当场警告。

## 1. 装 sing-box(服务端,glibc VPS 直接用官方包)

服务端是普通 glibc Ubuntu/Debian,官方二进制即可(musl 静态那套是 bx 客户端在 OpenWrt 上才需要的)：

```bash
curl -fsSL https://sing-box.app/install.sh | bash
sing-box version
```

## 2. 生成密钥

```bash
sing-box generate uuid                 # → VLESS UUID
sing-box generate reality-keypair      # → PrivateKey(进服务端) / PublicKey(进 vless 链接)
sing-box generate rand --hex 8         # → short_id(16 hex)
```
记下:`UUID`、`PrivateKey`、`PublicKey`、`SHORT_ID`。

## 3. 服务端配置

`/etc/sing-box/config.json`(最小:只一个 VLESS-REALITY 入站 + direct 出站):

```json
{
  "log": { "level": "info", "timestamp": true },
  "inbounds": [{
    "type": "vless",
    "tag": "vless-reality-in",
    "listen": "::",
    "listen_port": 9998,
    "users": [{ "name": "primary", "uuid": "REPLACE_UUID", "flow": "xtls-rprx-vision" }],
    "tls": {
      "enabled": true,
      "server_name": "www.apple.com",
      "reality": {
        "enabled": true,
        "handshake": { "server": "www.apple.com", "server_port": 443 },
        "private_key": "REPLACE_PRIVATE_KEY",
        "short_id": ["REPLACE_SHORT_ID"]
      }
    }
  }],
  "outbounds": [{ "type": "direct", "tag": "direct" }]
}
```

校验:
```bash
sing-box check -c /etc/sing-box/config.json && echo OK
```

## 4. 开机自启 + 防火墙

```bash
# systemd(官方 install.sh 已装 sing-box.service)
systemctl enable --now sing-box
systemctl status sing-box --no-pager | head -5

# 放行端口(ufw 例;云厂商还要在安全组同步放行)
ufw allow 9998/tcp
ufw status | grep 9998
```

## 5. 拼 `vless://` 链接 → 交给 bx

```
vless://<UUID>@<VPS_IP>:9998?security=reality&pbk=<PublicKey>&sid=<SHORT_ID>&sni=www.apple.com&flow=xtls-rprx-vision&fp=chrome
```
必填:`uuid` / `host` / `port` / `security=reality` / `pbk` / `sid` / `sni`(`flow` 默认 `xtls-rprx-vision`、`fp` 默认 `chrome`)。

客户端上(bx 已内嵌 sing-box,无需在客户端装):
```bash
sudo bx setup 'vless://…'   # 写配置 + 连通检测(走 reality 引擎),不启动
sudo bx up                  # 启动 + 开机自启
bx status                   # 隧道 healthy、传输=reality
```

## 6. 验证

服务端:
```bash
journalctl -u sing-box -f          # 客户端连上后应见 inbound connection 日志
```
客户端(整机起来前可先单测协议层,见 e2e checklist 的 socks 法):出口 IP 应变成 VPS_IP。

## 踩坑速查
- **服务端日志零连接、客户端超时** → 多半是端口被 ufw/安全组挡或被路径 DPI/运营商黑洞:换已放行高端口,别用 443。
- **握手失败但端口通**(服务端 `processed invalid connection` / 客户端 EOF)→ **头号嫌疑是借用 SNI 证书过大**
  (`www.microsoft.com` ~5879B 必挂),不是密钥/网络。换证书链 <4.5KB 的站(`www.cloudflare.com` 等);
  别误归因 sing-box #4023 同机问题或网络 MITM(本项目两个都误判过——reality 同机 loopback 用好 SNI 照样通)。
- **在 OpenWrt/路由器上测连通**:BusyBox `ash` **不支持 `/dev/tcp`**,用 `curl`/`nc`,否则全是假阴性。
- **`bx setup vless://` 报"不是支持的客户端链接"** → 旧版 bx;本仓库已支持 vless 换壳(`normalizeClientLink` + `blink` 传输无关)。
