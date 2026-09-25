# 配置

## 具名出口(把某些网段交给一条已有的隧道)

**先说什么时候不该用它**:能用 Tailscale 就用 Tailscale —— 在内网那台(或
VPS)上开 subnet router 即可,而 **bx 不挡它**(私网地址不绑物理网卡、走系统
路由表;subnet router 通告的 /16 也比 bx 那条 /8 更具体)。那条路一次配置、
所有设备受益,bx 一行都不用改。

只有在 mesh 用不了时(内网穿不出去)才用这个:

命令(不必手改 YAML):

```
bx egress add office 127.0.0.1:1080     # ssh -D 1080 -J <vps> <内网那台> 开的
bx egress route office 10.84.0.0/16     # 白名单:只有写出来的网段走它
bx egress ls
bx egress rm office                     # 连同交给它的网段一起删
sudo bx down && sudo bx up              # 改动在重连后生效
```

写进配置长这样:

```yaml
egress:
  - name: office
    socks5: 127.0.0.1:1080

rules:
  - via: office
    cidr: ['10.84.0.0/16']
```

几条定死的语义:

- **白名单,只有白名单。** 没写出来的网段一个字都不受影响 —— docker、LAN、
  SSH 全照旧走「私网恒直连」。
- **压过「私网恒直连」**,这正是它存在的理由:`10.84.3.239` 落在 `10/8` 里,
  不压过就会被送去本地网卡。
- **出口不通时阻断,绝不回落直连。** 理由不是防泄漏,是**防连错机器**:
  回落之后,在别人家 Wi-Fi 上打开那个地址会连到那个网络里同 IP 的另一台机器。
- **不受 kill-switch 约束**:出口在 loopback,与主隧道无关,主隧道挂了不该
  连累它。
- **socks5 只收 loopback 地址**,且加载期校验 —— 非 loopback 会被 bx 自己抓走。
- 第一版**只按网段**匹配,不按域名(内网域名多半只有内网 DNS 答得出,那是
  `dns.split` 的活)。


客户端默认配置路径:

```text
/etc/bx/config.yaml
```

服务端默认配置路径:

```text
/etc/bx/server.yaml
```

通常不需要手写配置。`bx server install` 和 `bx setup` 会自动生成需要的文件。

客户端支持的常用配置(下面是**键的说明**,不是 `bx setup` 写出来的那一份——它只写
`server`(或 `transports`)、`global: true`、`killswitch: true`,加上 sudo 下的
`owner_uid` 与 `--udp` 给的 `udp.transport`;其余键一个不写,靠 `config.Parse` 填默认值):

```yaml
server: "bx://..."
killswitch: true
global: true                  # setup 写的就是 true;改成 false 才走 china 分流
dns:
  china: 223.5.5.5
  fakeip_cidr: 198.18.0.0/15
  split:                        # 内网域名交给内网 DNS 解析,并强制直连
    - domains: ["*.corp.example"]
      servers: ["10.0.13.23", "10.0.13.24"]
bypass:
  - 10.0.0.0/16
rules:
  - direct: ["*.corp.internal", "10.0.0.0/8"]
  - proxy: ["*.openai.com"]
```

说明:

- `killswitch: true`:隧道不健康时阻断代理连接。
- `global: true`:除内网和用户直连规则外,所有流量都走 bx 隧道。
- `bypass`:路由层绕过 bx 的网段,适合管理网、SSH、内网。
- 私网、Docker、loopback、link-local 默认内建直连,通常无需手动配置。
- `transports: [link1, link2, ...]`(替代 `server:`):多传输自动容灾,有序优先级,主挂自动切备。
- `udp.transport: "hysteria2://..."`:按类分流——UDP/QUIC 走它加速、TCP 走主传输。它挂了 UDP 自动回落主传输(同一台 VPS、同一条加密隧道,不泄漏,只是没了加速档);主传输也挂才 fail-closed 阻断。
- `dns.split`:**内网域名的解析交给内网 DNS**(公网 DNS 答不出它们)。命中的域名
  不分配假 IP、转发到 `servers` 拿真实地址,并把拿到的地址**注册成强制直连** ——
  所以内网访问不会绕进隧道。`servers` 可以写多台(AD 域控通常成对),语义是
  **并发查、先到先用**,一台挂了不拖慢另一台;单台写法 `server: 10.0.13.23` 仍然
  支持,但**两个键不能同时写**(加载期报错)。无端口时补 `:53`。
  一轮查询的总预算是 2 秒:离开内网时这些域名会在 2 秒内明确失败,而不是卡住。
  **改完要 `bx down && bx up`**(split 不热重载,只有 direct/proxy 规则热生效)。
- 多传输/分流详见 [multi-transport-guide.md](multi-transport-guide.md)。

## 路由器模式(mode: router)

把 bx 装在网关/路由器上,只代理 **LAN 客户端的转发流量**;路由器自身流量一律不碰
(源地址策略路由),因此 Tailscale、管理流量、上游不受影响。

```yaml
mode: router
killswitch: true
router:
  lan_cidrs: [192.168.8.0/24]   # 要代理的 LAN 网段;留空则自动探测 br-* 私网桥
```

- 只有「源在 `lan_cidrs` 内」的转发流量被劫进 bx;路由器发起的流量走正常路由直连。
- **Fail-closed**:LAN 流量只能经 bx 出去;bx/隧道一挂即丢弃,绝不泄露真实 IP(路由层 blackhole + 防火墙)。
- 防泄露:强制 LAN DNS 走 bx fake-IP;封 LAN IPv6 转发(防 WebRTC/ICE v6 泄露);UDP(含 STUN)走代理或 block,不直连。
- 部署前用 `bx router-plan -c /etc/bx/config.yaml` 预览将下发的 `ip` + `nft` 命令(不改系统)。
- 目前需要 OpenWrt fw4(nftables)。完整上线步骤见 [router-mode-deploy-runbook.md](router-mode-deploy-runbook.md)。
