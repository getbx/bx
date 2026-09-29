# bx 问题上报:失败时自动、无感地把脱敏报告送到维护者手里(design)

**状态:所有者 2026-09-28/29 定了形状,同日实施完毕(收集端已部署并冒烟;客户端全套有守卫,经隧道那一跳真机未验,见 A14)。** 原话:「有问题肯定发给我,我才能改进
bx 吧」「给用户看一遍,用户大概率不会看。但可以留存到本地……尽量这个过程要无感,用户需要的是
稳定体验,而不是 bug 监察员」。

## 一句话

Guardian 在几类**失败**发生时攒一份脱敏包,先落本地留档,再经隧道 POST 到维护者的收集端
(Cloudflare Worker),收集端按失败签名归并成私有仓库 `getbx/bx-reports` 的 issue。用户不用
做任何事,菜单不常驻任何东西;`reports: off` 能关。

## 为什么不是别的形状

- **不直接建公开 issue**:公开 issue 等于公开名单(「这个人在用翻墙工具、出口在哪」);客户端
  也放不了 token(公开二进制)。token 住在 Worker 的 secret 里,issue 进私有仓库。
- **不发到用户自己的 VPS**:报告要到维护者手里才有用;别人自建的服务器维护者看不到。
- **不定时、不心跳**:只在失败上触发。定时外发是一个很整齐的网络信号,所有者否掉过后台探测。
- **不绕隧道**:只在 `protection_state == protected` 时发,走系统默认路由(经 TUN → 隧道);
  保护关着、隧道不健康时只入队。直连 `*.workers.dev` 的 SNI 就是一条「这台机器装了 bx」的
  明文证据,而报告本来就不急。
- **不弹窗、不让用户看包**:所有者定的。留档在本地,事后能查。

## 触发(只有这几类,Guardian 侧)

| 事件 | 签名 | 来源 |
|---|---|---|
| `needsAttention(code)` | `attention:<code>` | `Manager.needsAttention` |
| Core 起不来且判别出原因 | `corestart:<code>` | corestartfailure 记录 |
| 路径恢复放弃(耗尽) | `recovery:<stage>:<error_code>` | recovery 报告 |
| 更新回滚 | `update:<outcome>` | update 结局码 |
| pf 残留(doctor 报) | `pf_residue` | 起 Core 之前的 FlushStale 结果 |

**限频**:同一签名 6 小时内只报一次(本地记账),每台机器每天最多 10 份;收集端再按
install_id 与来源 IP 限一次。

## 报告的内容(固定结构,`internal/report.Bundle`)

```
schema: 1
install_id      随机 16 字节 hex,首次生成落 /var/lib/bx/install-id(只用于把同一台机器的
                重复报告归到一起;不带任何能认出人的东西)
bx_version / os / arch / macos_version
occurred_at
signature       见上表
protection      最近 5 次 protection_state 转换(码 + 时刻)
failure         code / stage / error_code(全部是码,不带原始错误串)
doctor          doctor.Report(check 名 / 状态 / detail / hint —— detail 经脱敏)
log_tail        Guardian 日志最后 200 行,逐行脱敏
```

**脱敏是构造上的,不是事后 grep**(`report.Redact`,纯函数,守卫拿真形状的夹具):
- 公网 IPv4/IPv6 一律替换成 `<ip-N>`(同一个地址同一个 N);私网、TUN 地址(198.51.100.x)、
  fake-IP 池(198.18/15)保留 —— 它们说明的是拓扑,不是身份。
- `bx://`、`vless://` 等链接与任何 `token=`/`key=` 形状的查询串:整段替换成 `<link>`。
- 主机名:只保留 bx 自己的端点(icanhazip / cloudflare trace / workers.dev)与 `localhost`,
  其余替换成 `<host>`。
- 用户 `bypass:` 与 `rules:` 的内容不进包;应用名不进包。
- 守卫 `TestRedactedBundleCarriesNoAddressOrCredential`:夹具里放服务器 IP、链接、bypass 网段、
  域名,序列化后一个都找不到;**反向断言**保证夹具本身确实含这些(否则守卫在空集合上恒真)。

## 本地留档兼队列

`/var/lib/bx/reports/<occurred_at>-<signature>.json`,root:wheel 0600(与日志同权限),
保留最近 50 份;发送成功改名 `.sent.json`,失败留着下次再发(重启后也继续)。
`bx reports` 列出本地报告,`bx reports show <name>` 看一份。**这就是「万一用户事后检查」的落点**。

## 发送

Guardian 起一个后台循环:每 5 分钟看一眼队列;有待发的且 `protection_state == protected` 就
POST(`Content-Type: application/json`,≤ 64 KB,超时 15 s),`202` 即成功;`4xx` 不重试
(记日志并标 `.rejected.json`);其余保留重试,退避到 1 小时。端点默认
`https://bx-reports.example.invalid/v1/reports`,`reports_endpoint:` 可覆盖,
`reports: off` 全关。**发送用普通 net.Dial,不用 DirectDialer** —— 要的正是经隧道。

## 收集端(`tools/reports-collector/`,Cloudflare Worker)

- `POST /v1/reports`:校验 schema 与大小;按 `install_id` 与 `cf-connecting-ip` 各限一天 20 份
  (KV 计数);存 KV(`report:<ts>:<install_id>`,TTL 90 天)。
- 有 `GITHUB_TOKEN` secret 时:按 `signature` 查 KV 里的 issue 号 —— 没有就在
  `getbx/bx-reports` 建 issue(标题 `[<signature>] bx <version> on <os>`,标签 `auto`、
  `code:<签名首段>`,正文是脱敏包的可读渲染),有就追加一条评论并把标题里的计数 +1。
  GitHub 调用失败不影响 202:报告已在 KV 里,下一份来时会再试。
- 没有 token 也能跑(只存 KV)。`GET /healthz` 回 200。其余路径 404。
- 部署:`scripts/deploy-reports-collector.sh`,用一把只带 Workers Scripts:Edit + KV:Edit 的
  Cloudflare API token(由全局 key 创建,不把全局 key 交给 wrangler)。

## 默认开,怎么告知

`bx setup` 结束时打一句(`reportsNotice`,守卫 `TestReportsNoticeSaysWhereItGoesWhereItStaysAndHowToTurnItOff`):
发去哪(维护者、只经隧道)、留在哪(目录 + `bx reports`)、怎么关(`reports: off`)。
README 的 Notes 段同一句(`verify-macos-release.sh` 钉住)。菜单里不常驻;Troubleshoot ▸ 里一行「Problem reports: N sent」
留给布局层再议。

## 不做

- 不发成功事件、不发使用统计。
- 不带原始错误串(Guardian 的失败码纪律),不带配置文件。
- 不在保护关着时发,哪怕队列积压。
- 不做「用户确认后发」:所有者定的。

## 真机未验

发送经隧道那一跳、Worker 在真实 Cloudflare 上的限频、issue 归并。验收进
`docs/acceptance-pending.md`(A14)。
