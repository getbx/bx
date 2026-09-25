# REALITY 整机 e2e checklist(`bx up`)

> 目标:验证 REALITY 传输在**整机透明代理**下全链路正确(出口 / kill-switch / 中国分流 / SSH 不断 / IPv6 堵死 / 还原)。需 root + 动真实网络 → 人工执行。
> 协议层握手已在 socks 层验过(见提交历史 `docs(reality)`),本表覆盖的是 TUN + 整机路由那一段。

## ⚠️ 安全前提(远程主机务必读)
- `bx up` 劫持整机默认路由,**bypass 配错会切断 SSH 把自己锁在外面**。
- **远程主机:先用死手前台试跑,别直接 `bx up`**。`bx run --test-timeout=3m` 到点自动还原一切,即便锁死 3 分钟后自愈。确认无误再 `bx up`。
- `bypass` 必须含**管理网 / SSH 源网段**(reality 服务器自身的 bypass 由 bx 自动加,不用管)。

## 0. 服务端(一次性)
> 完整步骤见 [reality-server-setup.md](../guide/reality-server-setup.md);下面是速记。
```bash
# 在 VPS 上,sing-box REALITY 服务端落 443(默认;443 被占才换一个已放行的高端口,见末尾坑①)
sing-box generate reality-keypair   # 记 PrivateKey / PublicKey
sing-box generate uuid              # 记 uuid
openssl rand -hex 4                 # short_id
# 写服务端配置:listen_port=<端口>,server_name + handshake.server=www.apple.com(稳定 TLS1.3 站),
#   private_key / short_id / uuid 如上;前台或 systemd 起。确认 ufw / 安全组放行该端口。
```
构造客户端链接:
```
vless://<uuid>@<VPS_IP>:<端口>?security=reality&pbk=<PublicKey>&sid=<short_id>&sni=www.apple.com&flow=xtls-rprx-vision&fp=chrome
```

## 1. 客户端配置(`bx setup` 或手写)
`bx setup` 现已接受 `vless://`(换壳成 `bx://` 写入 config,连通检测走 reality 引擎):
```bash
sudo bx setup 'vless://<uuid>@<VPS_IP>:<端口>?security=reality&pbk=...&sid=...&sni=www.apple.com&flow=xtls-rprx-vision&fp=chrome'
# 装服务 + 写 /etc/bx/config.yaml + 连通检测(不启动)
```
然后按需补 `bypass`(保 SSH):
```yaml
# /etc/bx/config.yaml 追加 / 确认
killswitch: true
global: true                 # 或 false 走中国分流(分流验证更全)
bypass:
  - 192.168.0.0/16           # ← 换成你的管理网 / SSH 源段,保命
```
连通自检(读 config,reality-aware):
```bash
sudo bx doctor               # probe 应 ok + 显示延迟;内嵌 sing-box 被拉起探测
```

## 2. 死手保护下前台试跑(远程必做)
```bash
sudo bx run --test-timeout=3m   # 前台;3 分钟后自动还原。立刻另开一个 SSH 会话跑 §4 验证
```

## 3. 正式启动(§2 通过后)
```bash
sudo bx up
bx status                       # 隧道 healthy、传输=reality、延迟正常
```

## 4. 验证项(逐条勾)
> **⚠️ 出口探测的域名不能随便挑,这里踩过三次。** 下面统一用 `ipv4.icanhazip.com` /
> `ipv6.icanhazip.com`,理由只有一条、而且是可复验的:它们**不在 bx 内嵌的 china 直连
> 列表里**。`api.ipify.org` / `api64.ipify.org` **在**(`ipify.org` 是 `china_domain.txt`
> 第 6045 行,而 `route.DomainSet` 是后缀匹配),于是 `global: false` 时 bx 会**正确地**
> 把它直连出去 —— 两个检查项因此都反过来:出口那条报出本地 ISP IP(以为 bx 坏了)、
> kill-switch 那条在服务端停掉之后**照样通**(以为 kill-switch 坏了)。同样在列表里而
> 不能用于出口探测的还有 `ifconfig.me` / `ifconfig.co` / `ipapi.co`。
> 换域名之前请照 `internal/leakcheck/endpoints_test.go` 的
> `TestEchoEndpointsAreNotOnTheChinaDirectList` 那个办法比一遍:拿**真实内嵌列表** +
> 生产那份 `route.NewDomainSet`,别凭记忆。

- [ ] **整机出口变更**:`curl -s https://ipv4.icanhazip.com` → 返回 **VPS_IP**(不带任何代理参数,证明整机生效)
- [ ] **确实经隧道**:与停 bx 后的直连出口不同
- [ ] **kill-switch / 不泄漏**:停掉服务端 → `curl https://ipv4.icanhazip.com` **失败/超时**,绝不回落真实 IP;`bx status` 显示隧道 unhealthy(代理决策 Block)
- [ ] **中国分流**(global=false 时):`curl -s https://www.baidu.com` 正常,且 cn 站出口为本地 / 国内、非 VPS
- [ ] **私网 / SSH 不受影响**:当前 SSH 会话不断;`ping <内网网关>` 通
- [ ] **DNS 无泄漏**:海外域名经 fake-IP 正常解析,无明文 DNS 外泄
- [ ] **IPv6 fail-closed**:`curl -6 -s https://ipv6.icanhazip.com` **应失败**(全局 v6 被 `unreachable` 堵);`ip -6 route show table 100` 见 unreachable 默认路由
- [ ] **UDP / QUIC**:按 `udp.mode` 行为符合预期(block / proxy / direct-realtime)
- [ ] **运行期热切换**(可选,已在 socks 层验过):经控制面 `set_transport` 换 brook↔reality 不断流

## 5. 还原 + 复核
```bash
sudo bx down
ip rule ; ip route ; ip -6 route show table 100   # 回到基线:bx 的 pref 100/150/200 规则与 table 100 清空
curl -s https://ipv4.icanhazip.com                 # 恢复直连(本地出口)
bx status                                          # 已停
```

## 已知坑(验证踩出)
1. **reality 就该用 443** —— 「服务端别用 443」是 brook 明文时代的 folklore,[reality-server-setup.md](../guide/reality-server-setup.md) 已明确撤回,`srvgen` 的默认端口就是 443:reality 伪装的是访问真站的 HTTPS,落到 9998 这种怪端口反而更可疑。当年疑似「443 挂」的真因是坑 2(SNI 证书链过大),与端口无关。**唯一真实约束是防火墙**:ufw / 云安全组必须放行 443;443 已被真 web 服务占着,才换一个已放行的高端口。
2. **借用 SNI 挑证书链够小的站** —— **别用 `www.microsoft.com`**:它的完整证书链 ~5879B,超出 REALITY 借壳中继证书的承受,握手必失败(服务端报 `processed invalid connection`),而这**不是**网络或密钥问题,换 SNI 即通。实测可用:`www.cloudflare.com` 2505B(`srvgen` 默认)、`www.apple.com` 3230B、`dl.google.com` 3543B、`addons.mozilla.org` 4085B。先在服务端 `openssl s_client -tls1_3` 验该站可达、支持 TLS1.3 + X25519。
3. **`bx run --test-timeout`** 是远程实测的保命绳,`bx up`(systemd 持久)无死手 —— 远程务必先 run 后 up。
