# AI 站可达性检查:leakcheck 的第四段(2026-09-14)

## 1. 动机与边界

**所有者的原话**:「其实我想要的是针对 AI 站的可用性检查」,起点是 cleanip.io 与
ipcheck.ing 那两个站,目标是「用别的 VPN 的用户会慢慢转向 bx」。

**这一期做的是「路通不通」,不是「你能不能用 Claude」。** 后者 bx 无权说 —— 地区
限制可能发生在登录或 API 调用层而不是边缘,而这一期只观测边缘。措辞纪律见 §3.4。

### 1.1 为什么不调第三方(实测决定的,不是偏好)

`cleanip.io/api/v2/<IP>` 是它的 IP 声誉接口,路径里直接带 IP,浏览器里点一下
「Start check」就会调它。**裸调的结果是**:

```
$ curl https://cleanip.io/api/v2/195.133.192.92
{"error":"bot not allowed","ok":false}          [HTTP 403]
```

**服务端专门写了一个错误码说不允许。** 要拿到那份数据只能伪造成浏览器(UA + TLS
指纹 + 可能的 JS 挑战),而这是把一个明确的技术拒绝主动规避掉、并分发给全部用户。
不做。**它还会在某个版本静默坏掉**,而那时用户只看到「这一项没结果」—— 正是本仓库
最忌讳的失效形状。

正规的 IP 声誉 API(`ipinfo.io` 一类)不违反任何东西,但**这一期也不接**:① 声誉分是
**推断**,解释不了、也没法在它错时说清楚,与 leakcheck「每条结论都有可解释判据」的
设计冲突;② 端点要过守卫;③ key 的死结 —— `bx leakcheck` 的立身之本是「拒绝 root、
不读 config、装完就能用」,要用户配 key 就破了它;④ 多一个会挂、会改格式、会收费的
依赖,而 bx 的身份是单文件零依赖。

**留给将来的口子**:若真要接,按 `surface` 那一档处理 —— `Verdict.Info`,标明来源,
**不进任何计数**。

### 1.2 不破坏的前提

`bx leakcheck` **拒绝 root、不读 config、不需要 Guardian、不需要 `bx setup`**。
本期两条路径(§5)都不读 config:「当前路径」是普通拨号,「绕过隧道」绑的物理接口
来自 leakcheck **已经在读**的路由表(`WhoOwnsTheRoute` 那一跳)。

## 2. 实测:2026-09-13 夜,项目所有者的 Mac(经 bx 隧道,出口 195.133.192.92)

**这份数据是整个设计的基础,两次推翻了动笔前的假设。**

| 端点 | 码 | body | 归类 |
|---|---|---|---|
| `api.anthropic.com/v1/messages` | 405 | `{"type":"error","error":{"type":"invalid_request_error","message":"Method Not Allowed"}}` | 可达 |
| `api.anthropic.com/favicon.ico` | 404 | 空 | 可达 |
| `claude.ai/favicon.ico` | **200** | favicon 二进制 | 可达 |
| `claude.ai/` (首页) | **403** | `<title>Just a moment...</title>` + `challenges.cloudflare.com` | **说不出来** |
| `api.openai.com/v1/models` | 401 | `{"error":{"message":"Missing bearer authentication..."}}` | 可达 |
| `chatgpt.com/` 与 `chatgpt.com/favicon.ico` | **403** | HTML(CF 挑战) | **说不出来** |
| `generativelanguage.googleapis.com/v1beta/models` | **403** | `{"error":{"code":403,"message":"Method doesn't allow..."}}` | 可达 |
| `gemini.google.com/` | 200 | HTML | 可达 |

### 2.1 被推翻的第一个假设:403 不是地区限制

动笔前判断 `claude.ai` 的 403 是「出口被拦」。**body 证伪**:它是 Cloudflare 的 JS
人机挑战(`Just a moment...`)。**带浏览器 UA 重试仍是 403** —— 不是 UA 检测,是 JS
挑战或 TLS 指纹。

**后果如果没发现**:这个功能会把一台**完全正常的机器**说成用不了 —— 所有者当时
正用着 Claude Code,而 curl 拿到 403。与 `guardian_dns` 把关闭态报成故障、
Tailscale advisory 把正常机器降级成 Needs Attention 是同一形状。

### 2.2 被推翻的第二个假设:网页可达性 bx 做不到

因为 §2.1,一度写下「网页可达性 bx 做不到,只能测 API」。**`claude.ai/favicon.ico`
返回 200 证伪了它** —— **favicon 路径不挂 CF bot 防护**(cleanip 用它正是这个原因,
它的网络请求里是 `claude.ai/favicon.ico?_t=...`)。

### 2.3 决定判据形状的一条:光看状态码会判反

表里最后两个 **403** 是完全相反的两件事 —— Google 那条是 **API 在正常应答**,
chatgpt 那条是**人机挑战**。**必须同时看状态码与 body 特征。**

## 3. 判据

### 3.1 四态

```
Reachable    服务自己的 JSON 错误(最强) | 404 空 body | 200 且非挑战页
Undetermined CF 挑战特征。判据按**先后顺序**取,前一条命中就不再往下看:
             ① body 含 "challenges.cloudflare.com" 或 "Just a moment"
             ② 403/503 且 body 去掉前导空白后以 "<" 开头(HTML)而非 "{"(JSON)
             ③ 其余认不出的状态码
Unreachable  连不上 / 超时 / TLS 握手失败 / DNS 解析不了
Refused      服务自己的 JSON 里明说地区或国家不支持
```

**零值必须是 `Undetermined`。** 与 `observe.Tristate`、`leakcheck.NotChecked`、
`Section` 零值取 `SectionPath` 同一条纪律:**漏填一个分支的代价是给出一个假答案**,
而这个功能的全部价值就是给真答案。

### 3.2 判据是纯函数

```go
// 住 internal/leakcheck,现有 purity_test.go 自动罩住(禁 net/os/exec)
func JudgeReach(status int, body []byte, dialErr error) ReachState
```

**body 传 `[]byte` 不传 `string`** —— favicon 是二进制,今天的探测脚本就在这上面
撞了 `tr: Illegal byte sequence`。判据只看前若干字节的特征,不做全文解码。

### 3.3 `Refused` 这一档今天没有真机样本

表里没有一行是 `Refused` —— 这台机器的出口在支持区。**它仍然要实现**(不实现的话
地区拒绝会落进 `Undetermined`,而那是我们最想答对的一种),但**它的 fixture 是构造
的,要在代码里标明**;真机见到之后回来把真实 body 补进 golden。

### 3.4 措辞纪律

- 可达 ⇒ 「bx 能到达 `claude.ai`」。**绝不是**「你可以用 Claude」——
  后者是 bx 无权说的话(地区限制可能在登录/调用层,本期只观测边缘)
- 说不出来 ⇒ 「这是 Cloudflare 的人机挑战,**不是你的出口有问题**;浏览器能过,
  命令行过不了」—— 必须主动否掉那句用户会自己脑补的坏消息
- 不可达 ⇒ 只说「这条路到不了 `<host>`」,**不断言对方服务的状态**
  (与 `core_tunnel_unreachable` 那条「只说 bx 观测到什么」同一条)

## 4. 端点清单

| 端点 | 预期信号(2026-09-13 实测) |
|---|---|
| `https://api.anthropic.com/v1/messages` | 405 + 服务 JSON |
| `https://claude.ai/favicon.ico` | 200 |
| `https://api.openai.com/v1/models` | 401 + 服务 JSON |
| `https://generativelanguage.googleapis.com/v1beta/models` | 403 + 服务 JSON |

**只发 GET,不带任何认证,不发 body。** 401/405 恰恰是我们要的信号。

### 4.1 `chatgpt.com` 刻意不进清单

它实测恒为 CF 挑战页 ⇒ **恒 `Undetermined`** ⇒ 一行永远给不出答案的噪声。
与「某一行在真机上恒为未知,该修的是它的构造处,不是回来把 unknown 一起藏掉」
同形。OpenAI 由 `api.openai.com` 代表。

### 4.2 守卫:三关,第三关换语义

沿用现有端点的前两关 —— **常量逐个钉死**、**必须 https**(明文回声在路上可被改写)。

**第三关从「不在 china 直连列表」改成「如实报告在不在」。** 原来那三关是给**回声
端点**设的:它若在 china 列表里就会走直连、报出用户真实 IP。可达性探测走哪条路由
**本功能自己指定**(§5),不依赖分流 —— 所以不是禁止,而是把「这个域名在不在内建
直连列表」**作为该端点结论的一行证据**发出来(与其它结论的 evidence 同一条路),
不单独成一条结论 —— 它解释的是「这次探测经了哪条路」,本身不是好消息也不是坏消息。

### 4.3 每个端点连同预期信号一起记档

清单里每一项带一个「上次实测见到什么」的字段,守卫钉住它非空。**改端点或它的行为
变了,守卫会红一次** —— 那正是回来重测的时刻。

## 5. 两条路径

- **A「当前路径」** — 普通拨号,走系统路由。它经的是什么就测什么:bx / 别人的
  VPN / 裸奔。**这是对「用别的 VPN 的人」也成立的那一半。**
- **B「绕过隧道」** — 绑物理网卡(`IP_BOUND_IF`)。物理接口取自 leakcheck 判
  `WhoOwnsTheRoute` 时**已经读到**的那一跳,不读 config。

两条一比给出:「直连不行、走当前隧道行」/「两边都不行,换台服务器」/「两边都行」。

### 5.1 ⚠️ 待所有者拍板:B 默认跑不跑

**本 spec 暂定「默认跑」**,理由是不跑就少一半价值(单条路径答不出「是不是隧道的
问题」)。

**代价必须写清楚**:B 会从物理网卡直接发 4 个 GET 到 Anthropic / OpenAI / Google,
**暴露用户的真实 IP 给这三家**。对一个泄漏检测工具这是要慎重的新行为 ——
leakcheck 今天的探测(icanhazip / cloudflare trace)走的都是**当前路径**,
绑物理网卡发请求是本期新增的。

**无论默认哪边,两件事必须做**:① 界面上明说「这一步从物理网卡直接发,不经任何
隧道」;② 给关掉的开关。改成「默认不跑、`--bypass` 才跑」同样说得通,由所有者定。

## 6. 第四段 `SectionReach`

### 6.1 为什么不塞进现有三段

三段是 `path`(流量去哪儿)· `identity`(会不会被单独认出来)· `surface`(网站看得到
什么)。**可达性哪一段都不是**,而且**极性不同**:path/identity 的坏消息是「你泄漏
了」,可达性的坏消息是「你用不了」—— **后者根本不是安全问题**。

硬塞进 `path` 的 `AnomalyCount` 就是让「Claude 连不上」被算成一次泄漏,**正好违反
「判据分三段,各自计数,绝不合成一个总数」那条纪律**。

### 6.2 计数与措辞

新计数与 `AnomalyCount` / `IdentityCount` **并排,绝不合成**。措辞列**四态**
(与 §3.1 逐项对上,少一档就是把那一档的结论静默并进别档):
「N 可达 · N 被拒 · N 没查出来 · N 不可达」,**不是**「N 个问题」。
为零的档不打印 —— 但 `Undetermined` 与 `Unreachable` **为零也要打印**,
否则「一条都没查出来」与「查了、全可达」在屏幕上长得一样。

### 6.3 骨架与守卫

`Outline()` 要加对应骨架,ID/顺序/分段与 `Judge()` 逐项对上(现有守卫已钉)。
**「十条结论」那个数字会变 —— `TestOutlineHasTheDocumentedNumberOfConclusions`
与 CLAUDE.md 里那句话必须同批改。** 本仓库为这个数栽过一次(写「八条」而实际十条,
漏了头尾两条)。

## 7. 渲染

- **CLI**(`bx leakcheck --json` 与人读的那份):第四段独立成块
- **页面**:按骨架先摆行、再按 ID 塞结论。**页面对认不出的 ID 是静默丢弃**
  (`if (!row) return;`),所以新 ID 必须同批加进页面骨架,否则那一格永远停在「还在等」
- **探测名是跨语言契约**:若第四段引入新的 probe 名,要进
  `leakserve.TestPageProbeNamesMatchTheGoConstants` 那条双向守卫

## 8. 测试

1. **纯判据 golden**:fixture **来自 §2 那轮真机实测的真实 body 片段** ——
   合成数据造不出「同一个 403 下,服务 JSON 与 CF 挑战页对立」这个形状
2. **端点常量逐个钉死** + https 断言 + 预期信号非空(§4.3)
3. **零值是 `Undetermined`** 单独一条
4. **`chatgpt.com` 不在清单里**单独一条,带上「它恒为挑战页」的理由 ——
   否则下一个人会「顺手补全」把它加回来
5. **第四段进了 `Outline()` 且与 `Judge()` 对得上**(复用现有守卫)
6. **B 路径绑的是物理接口而不是默认路由** —— 判据打在传给拨号器的接口名上,
   不打在「这次调用发生过」(第七种失效写法)

## 9. 不做什么

- 不调任何第三方 API(§1.1),不伪装浏览器去绕 `bot not allowed`
- 不做 IP 声誉 / 风险评分 / 指纹唯一性百分比 —— 没有语料库就没有分母
- 不测速、不 MTR、不做全球延迟 —— 那是「后台定时探测」,与既有边界冲突
- 不判断「你能不能登录/能不能用」,只判断边缘可达(§3.4)
- 不做站点清单的用户自定义(`--target`)—— 今天没有第二个消费方,YAGNI

## 10. 已知边界

- **favicon 200 只证明边缘可达**,不证明能登录、能用。写在界面上
- **`Refused` 无真机样本**(§3.3)
- **CF 的防护会变**:今天 `claude.ai/favicon.ico` 不挂挑战,明天可能挂。
  那一天这一项会从 `Reachable` 变成 `Undetermined` —— **判据仍然是对的**
  (它如实说「没查出来」),但 §4.3 那个记档字段会与实测不符,守卫提醒重测
- **本期只覆盖 darwin**:B 路径要 `IP_BOUND_IF`,与 leakcheck 现有的路由观测同门槛
