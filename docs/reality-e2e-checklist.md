# REALITY 整机 e2e checklist(`bx up`)

> 目标:验证 REALITY 传输在**整机透明代理**下全链路正确(出口 / kill-switch / 中国分流 / SSH 不断 / IPv6 堵死 / 还原)。需 root + 动真实网络 → 人工执行。
> 协议层握手已在 socks 层验过(见提交历史 `docs(reality)`),本表覆盖的是 TUN + 整机路由那一段。

## ⚠️ 安全前提(远程主机务必读)
- `bx up` 劫持整机默认路由,**bypass 配错会切断 SSH 把自己锁在外面**。
- **远程主机:先用死手前台试跑,别直接 `bx up`**。`bx run --test-timeout=3m` 到点自动还原一切,即便锁死 3 分钟后自愈。确认无误再 `bx up`。
- `bypass` 必须含**管理网 / SSH 源网段**(reality 服务器自身的 bypass 由 bx 自动加,不用管)。

## 0. 服务端(一次性)
```bash
# 在 VPS 上,sing-box REALITY 服务端落 ufw / 安全组放行的【高端口】(别用 443!见末尾坑①)
sing-box generate reality-keypair   # 记 PrivateKey / PublicKey
sing-box generate uuid              # 记 uuid
openssl rand -hex 4                 # short_id
# 写服务端配置:listen_port=<高端口>,server_name + handshake.server=www.apple.com(稳定 TLS1.3 站),
#   private_key / short_id / uuid 如上;前台或 systemd 起。确认 ufw 放行该端口。
```
构造客户端链接:
```
vless://<uuid>@<VPS_IP>:<高端口>?security=reality&pbk=<PublicKey>&sid=<short_id>&sni=www.apple.com&flow=xtls-rprx-vision&fp=chrome
```

## 1. 客户端配置(`bx setup` 或手写)
`bx setup` 现已接受 `vless://`(换壳成 `bx://` 写入 config,连通检测走 reality 引擎):
```bash
sudo bx setup 'vless://<uuid>@<VPS_IP>:<高端口>?security=reality&pbk=...&sid=...&sni=www.apple.com&flow=xtls-rprx-vision&fp=chrome'
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
- [ ] **整机出口变更**:`curl -s https://api.ipify.org` → 返回 **VPS_IP**(不带任何代理参数,证明整机生效)
- [ ] **确实经隧道**:与停 bx 后的直连出口不同
- [ ] **kill-switch / 不泄漏**:停掉服务端 → `curl https://api.ipify.org` **失败/超时**,绝不回落真实 IP;`bx status` 显示隧道 unhealthy(代理决策 Block)
- [ ] **中国分流**(global=false 时):`curl -s https://www.baidu.com` 正常,且 cn 站出口为本地 / 国内、非 VPS
- [ ] **私网 / SSH 不受影响**:当前 SSH 会话不断;`ping <内网网关>` 通
- [ ] **DNS 无泄漏**:海外域名经 fake-IP 正常解析,无明文 DNS 外泄
- [ ] **IPv6 fail-closed**:`curl -6 -s https://api64.ipify.org` **应失败**(全局 v6 被 `unreachable` 堵);`ip -6 route show table 100` 见 unreachable 默认路由
- [ ] **UDP / QUIC**:按 `udp.mode` 行为符合预期(block / proxy / direct-realtime)
- [ ] **运行期热切换**(可选,已在 socks 层验过):经控制面 `set_transport` 换 brook↔reality 不断流

## 5. 还原 + 复核
```bash
sudo bx down
ip rule ; ip route ; ip -6 route show table 100   # 回到基线:bx 的 pref 100/150/200 规则与 table 100 清空
curl -s https://api.ipify.org                      # 恢复直连(本地出口)
bx status                                          # 已停
```

## 已知坑(验证踩出)
1. **服务端别用 443** —— 路径对 443 的 DPI 干扰 + 服务端 ufw 白名单,会让 TCP 能连但 TLS 载荷被黑洞(连接成功、服务端零接收)。落已放行的高端口(同 brook 9999)。
2. **借用 SNI 站若改 TLS 会握手失败** —— 如 www.microsoft.com 近期更新 TLS;用稳定的 www.apple.com / www.cloudflare.com 等(先在服务端 `openssl s_client -tls1_3` 验该站可达且支持 TLS1.3)。
3. **`bx run --test-timeout`** 是远程实测的保命绳,`bx up`(systemd 持久)无死手 —— 远程务必先 run 后 up。
