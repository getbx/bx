# REALITY 服务端搭建 runbook(给 bx 的 `vless://` 传输用)

> 目标:在你自己的 VPS 上立一个 **sing-box VLESS-REALITY 服务端**,产出一条 `vless://` 链接,
> 直接喂给 `bx setup vless://…`。客户端侧 sing-box 由 bx **内嵌**,服务端这份是你要自己跑的。
> 整机验证见 [reality-e2e-checklist.md](reality-e2e-checklist.md)。

## 0. 选端口和 SNI(先定,踩过的坑)

- **端口:别用 443。** 两个真实约束(实测):
  - 服务端 **ufw / 云安全组**必须放行该端口;
  - 路径上对 **443 有 DPI 干扰**,以及部分运营商对非标端口 SYN 黑洞(TCP 能连但 TLS 载荷被丢)。
  - → 落一个**已放行的高端口**(如 `9998`、`8388` 之类),和现网 brook 的 `9999` 同类。下面用 `9998` 举例。
- **借用 SNI(`server_name`/`handshake.server`):挑稳定的 TLS1.3 站。**
  - **别用 `www.microsoft.com`**——它改过 TLS,会让 REALITY 借壳握手失败(本项目踩过)。
  - 用 `www.apple.com` / `www.cloudflare.com` / `addons.mozilla.org` 等。先在 **VPS 上**验它可达且支持 TLS1.3:
    ```bash
    openssl s_client -connect www.apple.com:443 -servername www.apple.com -tls1_3 </dev/null 2>/dev/null | grep -i "TLSv1.3"
    ```

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
- **握手失败但端口通** → 借用 SNI 那个站改了 TLS;换 `www.apple.com` 等,先用 `openssl s_client -tls1_3` 验。
- **在 OpenWrt/路由器上测连通**:BusyBox `ash` **不支持 `/dev/tcp`**,用 `curl`/`nc`,否则全是假阴性。
- **`bx setup vless://` 报"不是支持的客户端链接"** → 旧版 bx;本仓库已支持 vless 换壳(`normalizeClientLink` + `blink` 传输无关)。
