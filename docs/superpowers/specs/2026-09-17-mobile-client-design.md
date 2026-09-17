# bx 手机端:设计准备(2026-09-17)

**状态:未决定要做,也不着急。** 这份文件是**准备** —— 把已经测到的事实、必须
现在划的架构线、以及"桌面开发时别把路堵死"的约束写下来,免得将来动手的人重新推
一遍(或者推错)。

**所有带数字的结论都是实测的,方法写在旁边。** 这份文件里没有"应该是"。

---

## 1. 为什么会有这个念头

桌面用 bx、手机逼用户选别人的客户端,**不是一个能自洽的产品故事** —— 尤其当 bx 的
全部身份是"不撒谎、fail-closed、说不知道就说不知道",然后把手机那半交给一个闭源
付费 app。项目所有者的原话:「自己的 app 更放心」。

而手机**更**需要 VPN:它被带着到处跑、连咖啡馆和酒店 Wi-Fi、占了绝大部分使用时间。
那台 Mac 反而是最安全的一台(固定网络)。

## 2. 三层,成本差一个数量级

| | 是什么 | 手机上拿到什么 |
|---|---|---|
| **L0** | 今天就有:`bx server share --qr` 扫进 SFI/NekoBox | 能翻墙。**策略不跟着走** |
| **L1** | `bx export --format sing-box` | 能翻墙 **+ 和桌面同一份策略** |
| **L2** | 自己的 iOS/Android 客户端 | 上面全部 + bx 的诊断 + 不用信第三方 |

**L1 不是 L2 的替代品,是 L2 的第一个组件** —— 那层翻译两边都要,而且是风险最高、
却唯一能在桌面上完整验证的部分。

## 3. 必须现在划的线:判据会分裂成两份

手机上传输与路由**都会是上游的 libbox**(sing-box 官方的移动端嵌入路径,已经为
iOS 的内存上限调过;自己写是几个月)。于是:

```
桌面:  域名 → route.Router.Decide     → DIRECT / PROXY / BLOCK
手机:  域名 → sing-box 的 route rules → direct / proxy / block
```

**同一个问题、两套实现。** 这正是本仓库最贵的那条教训(`internal/udpsource`、
`barriercidr`、`dirsync`、`elevate` 四个叶子包都是为它长出来的;2026-09-16 那个坏了
五周的 `swift build` 也是它)。

漂了的表现是:**同一个域名,你 Mac 上直连、手机上走隧道**,而两边都不报错。
对一个泄漏检测工具,这是最难堪的一种失效。

### 对策:翻译只有一条路,再加一条判定一致性的守卫

```
config.yaml ──► 唯一的翻译器 ──► sing-box route rules
                     │
                     └─► 守卫:同一份配置、N 个域名,
                         route.Explain 的判定 必须 == 生成的规则的判定
```

先例就在仓库里:`internal/supervisor/ruleprecedence_test.go` 用**真 Router** 去背书
`rulereview` 的推断,两者独立成文、分歧当场被抓。

**这条守卫完全在 Go 里、完全在桌面上跑**,不需要任何移动端工具链 —— 所以它可以
在决定做不做 L2 之前就存在。

## 4. 已实测的语义对照(sing-box 1.14.0,仓库内嵌那份)

方法:最小配置 + 两个 `block` 出站,经 socks5h 发请求,读 sing-box 自己的日志看
它选了哪个出站。

### 4.1 域名匹配:**1:1 对上,没有陷阱**

bx 的 `route.NewDomainSet` 去掉 `*.` 存后缀,`Match` 逐级往父域找。
实测 sing-box `domain_suffix: ["a.com"]`:

| 域名 | sing-box | bx | |
|---|---|---|---|
| `a.com` | MATCHED | 命中 | ✅ |
| `x.a.com` | MATCHED | 命中 | ✅ |
| `y.x.a.com` | MATCHED | 命中 | ✅ |
| `evila.com` | UNMATCHED | 不命中 | ✅ |
| `xa.com` | UNMATCHED | 不命中 | ✅ |

**原先预计的陷阱不存在**:`domain_suffix` 在 1.14 是**按标签边界**匹配,不是裸字符串
后缀。不需要拆成 `domain` + `domain_suffix` 两份去模拟。

### 4.2 规则顺序:**首个命中者胜,与 bx 同构**

两条同样匹配的规则并排,实测第一条赢;没命中的落 `final`。
bx 的顺序是「先查 proxy、再 direct、再 china、最后默认」,直接映射成数组顺序:

```
私网 / server bypass  →  proxy 规则  →  direct 规则  →  china 列表  →  final
```

### 4.3 DNS 分流:字段在,但**schema 跨版本有破坏性变更**

`dns.rules[].server` 能表达 `dns.split`。但实测 sing-box **1.14.0 移除了 1.12 之前的
DNS server 格式**(旧格式直接 FATAL,不是 warning)。

> 翻译层必须**钉住目标 sing-box 版本**,并且在版本变时有守卫会红。
> 手机端跟的 libbox 版本与桌面内嵌的 sing-box 版本**会不一样**,这是个真实的漂移面。

### 4.4 仍未验(动手前要补)

- `network: ["udp"]` 分流到另一个出站 —— schema 通过,**行为未实测**(socks5h 测不了 UDP)
- **kill-switch**:sing-box 没有 bx 那种"隧道不健康就在拨号前 Block"的语义。
  预期是"不配 fallback 出站 ⇒ 连接直接失败 ⇒ 等效不泄漏",但**这是推理不是实测**,
  而 fail-closed 是 bx 最核心的不变量,不许靠推理。
- `rule_set` 对 12k 条 china 域名的表达与体积

## 5. iOS 的两个真实约束(以及一个我一开始说错了的)

**说错的那个**:我最初拿桌面的内存数(Core 123.8MB + 两个 sing-box 子进程 = 249MB)
去论证 iOS 装不下。**那是无效推理** —— 手机版不会那样构建:libbox 是进程内库,
上游已经为 iOS 的 Network Extension 内存上限调过,官方 SFI 就是这么跑的。

### 5.1 `ios` 满足 `darwin` 构建标签 —— 这是个运行时陷阱

实测(`go list -f '{{.GoFiles}}'`):`GOOS=ios` 的构建**包含** `//go:build darwin`
的文件;`GOOS=android` 包含 `//go:build linux` 的。

后果:`platform_darwin.go` 里那些 `exec.Command("route", …)`、`networksetup`、launchd
**会被原样编进 iOS 构建**。**编得过,运行时全挂** —— iOS 上没有 `/sbin/route`,
更不许起进程。

> 所以在 iOS 上,"交叉编译通过"**格外**不能当作"能用"。
> 真要做 L2,桌面那批 darwin-only 的东西需要显式 `!ios` 约束,或者换包边界。

### 5.2 app extension 不能起进程

bx 桌面架构的第一句话就是「隧道是**可插拔黑盒子进程**」(brook / sing-box 是
`posix_spawn` 出来的子进程)。iOS 的 app extension **不许创建进程** —— 这不是
"port 一下",是推翻那个决定,六种传输全部要改成进程内库。

**这也正是为什么 L2 是"共用判据的第二个产品",不是"移植"。**

## 6. 什么共享、什么不共享(这一条要诚实)

| | 桌面 | 手机 |
|---|---|---|
| 传输(六种引擎) | 子进程 | **libbox,进程内** |
| TUN / 路由 / 分流 | gVisor + `route.Router` + `dialer` | **libbox 自己做,bx 这半一行不用** |
| 规则语义 | `route.Router` | 翻译成 sing-box route rules ← **唯一的共享点,也是唯一会漂的地方** |
| 纯判据(泄漏检测/explain/规则体检/doctor) | Go | **同一份 Go,原样编** |

也就是说:**桌面 bx 最厚的那两层(数据面 + 控制面)在手机上完全不复用。**
bx 贡献的是判断,不是管道。

## 7. 桌面开发时要守住的约束(现在就生效)

**做不做 L2 还没定,但下面这条现在就该守 —— 它的成本接近零,而违反之后很难发现。**

`scripts/verify.sh` 的 `portable judgment (ios+android)` 一步:把带 `purity_test.go`
的那几个包(清单 `git ls-files` 现取)为 ios/android 交叉编译。
**它的力量有明确上限**(见 5.1:编得过≠能用),所以它和另外两道一起才够:

1. 各包自己的 `purity_test.go` —— 按 AST 禁 net/os/exec(本包文件)
2. `TestLeakcheckHasNoTransitiveControlPlaneDependency` —— `go list -deps` 查传递依赖
3. 这一步 —— 交叉编译

三者各守一段。**别指望任何一道单独成立。**

## 8. 分期(如果决定做)

| | 做什么 | 桌面能验完吗 |
|---|---|---|
| ① | 翻译层 + 判定一致性守卫 + `bx export` | ✅ 全部 |
| ② | 最小 iOS Network Extension + libbox,能连上即可,无 UI | ❌ 要真机 |
| ③ | 诊断(六个纯判据包) | 判据 ✅ / 呈现 ❌ |
| ④ | UI | ❌ |

**① 做完就已经拿到 L1 的全部好处**,而且 ② 之后不用重做。

**在 ④ 之前必须先回答一个问题**:iOS 的界面能不能像 2026-09-17 给 macOS 菜单做的那样
**离屏渲染 + 让 agent 自己读**(模拟器 + `xcrun simctl io screenshot`)。
桌面那次证明了"UI 没法自动验证"只是没人试过;手机这边如果不提前确认,
会重演"真机未验清单最长的一段",而且**连一台能让 agent 自己跑的真机都没有**。

## 9. 与此相关的既定边界(别推翻)

- **不建站、不做 WASM、不做引流漏斗** —— 项目所有者已否决,理由是"需要的人会自己装"
- **`leak-check --browser` 已删** —— 理由是"一道安全面有两份实现就有两份要守,
  而只有一份会被想起来"。手机上的泄漏检测要重新开面时,先读那条记述
- **bx 是 GPLv3**,链 libbox(GPLv3)没有许可证障碍(2026-09-17 核过 `LICENSE`)
- **Apple 审核指南 5.4 要求 VPN app 由企业账号提供** —— 个人账号做不了
