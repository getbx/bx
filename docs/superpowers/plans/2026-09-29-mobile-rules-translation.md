# 手机端第一期:规则翻译层 + 判定一致性守卫(implementation plan)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 一个纯 Go 叶子包,把 `/etc/bx/config.yaml` 的分流意图翻译成 sing-box 1.14 的 `route.rules` + 两个 rule-set 文件,并由三道守卫钉住「同一份配置、同一个域名/IP,手机上的判定 == Mac 上 `route.Explain` 的判定」。

**Architecture:** `internal/singboxrules`(纯判据,只依赖 `config`/`route`/`policy`)提供 `Translate`(config → `Bundle`)与 `Evaluate`(`Bundle` × 目的地 → 出站,按 spec §4 实测过的 sing-box 语义建模)。守卫分三层:① 一致性 —— 走生产路径 `config.Parse → supervisor.BuildRouter → route.Explain`,与 `Evaluate(Translate(cfg))` 逐输入比对(外部测试包);② 真二进制 —— 内嵌的 sing-box 对生成物跑 `check`(schema)与 `rule-set match`(后缀语义);③ 版本 —— 翻译器钉住目标版本,内嵌版本变了就红(spec §4.3)。**没有用户可见命令**(L1 已被所有者撤回,spec §2)。

**Tech Stack:** Go 1.26,`encoding/json`,`net/netip`;测试用 `internal/embedded` 的 sing-box 与 china 列表。

**Spec:** `docs/superpowers/specs/2026-09-17-mobile-client-design.md`(§3 对策、§4 语义对照、§7 约束、§8 分期①)。

## Global Constraints

- 纯判据包:不 import `os`/`os/exec`/`net`/`net/http`;internal 依赖只许 `config`、`route`、`policy`(`purity_test.go` 钉住,verify 的 portable judgment 一步会为 ios/android 交叉编译它)。
- 判定只有一份:域名归网段还是域名走 `policy.RuleCIDR`;后缀归一化与 `route.NewDomainSet` 同一条(去空白、小写、去 `*.`、跳过空行与 `#`)。
- 顺序照 `route.Explain`:域名 = user proxy → user direct → china domain(global 时省略)→ final proxy;IP = user proxy cidr → user direct cidr → 私网(`route.DefaultPrivateCIDRs`)→ china cidr(global 时省略)→ final proxy。
- `egress[]`/`rules[].via` 在手机上没有对应物:**翻译器报错拒绝**,不静默丢(丢了就是「同一地址两边走法不同而两边都不报错」)。
- 出站 tag 三个常量:`proxy` / `direct` / `block`;rule-set tag 两个常量:`bx-china-domain` / `bx-china-cidr`,source 格式(`version: 1`)。
- 目标版本常量 `TargetSingboxVersion = "1.14.2"`,与 `embedded.SingboxVersion()`(去 `v`)相等,否则红。
- 不做:DNS 分流(`dns.split`、`hosts:`,spec §4.3 schema 不稳)、UDP 档(`udp.*`)、kill-switch 语义(§4.4 未实测)。三条在包文档里明写。
- 每条守卫写之前先说出「要让缺陷回来,什么必须改变」;盖测不到的一半用变异证明。

## Review Focus

1. 域名写成大写 / 带尾点(`Example.COM.`):bx 的 `Match` 归一化查询,翻译后的后缀也要小写无尾点;守卫喂这种输入。
2. 同一后缀在 proxy 与 direct 都写了(`zoom.us` 两边都有):bx 先查 proxy ⇒ proxy;翻译后的数组顺序必须给出同一答案。
3. 规则里写裸 IP(`1.2.3.4`)与 IPv6 CIDR:`RuleCIDR` 补 /32、/128;sing-box `ip_cidr` 接受两种写法,守卫喂 v6。
4. china 列表里带 `#` 注释、空行、`*.` 前缀:与 `NewDomainSet` 同一条归一化,否则 rule-set 里多出 `*.a.com` 这种永远匹配不到的条目。
5. `global: true` 时私网仍直连、china 两个 rule-set 都不出现在 rules 里(rule_set 声明可以留着,但**不许**有引用它的 rule)。

---

### Task 1: 包骨架、纯度守卫、版本钉

**Files:**
- Create: `internal/singboxrules/doc.go`、`internal/singboxrules/version.go`
- Test: `internal/singboxrules/purity_test.go`、`internal/singboxrules/version_test.go`

**Interfaces:**
- Produces: `const TargetSingboxVersion = "1.14.2"`;`const OutboundProxy/OutboundDirect/OutboundBlock`;`const RuleSetChinaDomain = "bx-china-domain"`,`RuleSetChinaCIDR = "bx-china-cidr"`。

- [ ] **Step 1: 写版本守卫(红)** —— `version_test.go`:`strings.TrimPrefix(embedded.SingboxVersion(), "v") == TargetSingboxVersion`,内嵌版本为空时 `t.Fatal`。
- [ ] **Step 2: 跑红** `go test ./internal/singboxrules/` —— 期待 undefined: TargetSingboxVersion。
- [ ] **Step 3: 写 `version.go` 与 `doc.go`**(包文档写明三条不做的与为什么钉版本)。
- [ ] **Step 4: 写 `purity_test.go`**(照 `internal/rulereview/purity_test.go`,allowed = config/route/policy)。
- [ ] **Step 5: 跑绿;commit** `feat(singboxrules): 包骨架、纯度守卫、目标 sing-box 版本钉`。

### Task 2: `Translate`:config → Bundle

**Files:**
- Create: `internal/singboxrules/translate.go`
- Test: `internal/singboxrules/translate_test.go`

**Interfaces:**
- Produces:
  ```go
  type Lists struct{ ChinaDomain, ChinaCIDR []string } // 与 supervisor.BuildRouter 吃的同一形状
  type Rule struct {
      DomainSuffix []string `json:"domain_suffix,omitempty"`
      IPCIDR       []string `json:"ip_cidr,omitempty"`
      RuleSet      []string `json:"rule_set,omitempty"`
      Outbound     string   `json:"outbound"`
  }
  type RuleSetRef struct{ Tag, Type, Format, Path string } // type=local format=source path=<tag>.json
  type Route struct { Rules []Rule; RuleSet []RuleSetRef; Final string }
  type RuleSetFile struct { Version int; Rules []Rule }   // rules 里只有 domain_suffix 或 ip_cidr
  type Bundle struct { Route Route; RuleSets map[string]RuleSetFile }
  var ErrUnsupportedEgress = errors.New("...")
  func Translate(cfg *config.Config, lists Lists) (Bundle, error)
  func (b Bundle) RouteJSON() ([]byte, error)                 // {"route":{...}}
  func (b Bundle) RuleSetJSON(tag string) ([]byte, error)
  ```
- [ ] **Step 1: 写测试(红)**:① 域名规则进两条 `domain_suffix` rule,顺序 proxy 在 direct 前,`*.` 去掉、小写;② CIDR 规则进 `ip_cidr`,裸 IP 补 /32;③ 私网段那条 == `route.DefaultPrivateCIDRs`;④ china 两个 rule_set 在 rules 里各被引用一次且在私网之后;⑤ `global: true` ⇒ 没有任何 rule 引用 china 两个 tag;⑥ `final == "proxy"`;⑦ 配置含 `via` ⇒ `errors.Is(err, ErrUnsupportedEgress)`;⑧ china 列表里的 `#` 与空行被跳过、`*.` 被去掉;⑨ 空 direct/proxy 不产出空 rule(sing-box 对空 rule 报错)。
- [ ] **Step 2: 跑红。**
- [ ] **Step 3: 实现 `translate.go`**(拆规则用 `policy.RuleCIDR`;域名归一化写一个与 `NewDomainSet` 同条的 `normalizeSuffix`)。
- [ ] **Step 4: 跑绿;commit** `feat(singboxrules): Translate —— config 的分流意图翻成 sing-box route.rules + 两个 rule-set`。

### Task 3: `Evaluate`:sing-box 语义的参考实现

**Files:**
- Create: `internal/singboxrules/evaluate.go`
- Test: `internal/singboxrules/evaluate_test.go`

**Interfaces:**
- Produces: `func (b Bundle) Evaluate(dst Destination) Verdict`;`type Destination struct{ Domain string; IP netip.Addr }`;`type Verdict struct{ Outbound string; RuleIndex int }`(RuleIndex = -1 表示落 final)。
- 语义(spec §4.1/§4.2 实测):`domain_suffix` 按标签边界(`a.com` 命中 `a.com`、`x.a.com`,不命中 `xa.com`);首个命中者胜;域名目的地只看 domain 类字段,IP 目的地只看 `ip_cidr`(不解析);`rule_set` 展开成它文件里的规则;都没命中落 `final`。

- [ ] **Step 1: 写测试(红)**:标签边界四例;首个命中者胜;域名不碰 `ip_cidr`、IP 不碰 `domain_suffix`;rule_set 展开;落 final。
- [ ] **Step 2: 跑红。** **Step 3: 实现。** **Step 4: 跑绿;commit** `feat(singboxrules): Evaluate —— 按实测的 sing-box 语义评估一份 Bundle`。

### Task 4: 一致性守卫(走生产路径)

**Files:**
- Test: `internal/singboxrules/consistency_test.go`(package `singboxrules_test`,才 import 得到 `supervisor`)

- [ ] **Step 1: 写守卫**:fixture YAML(proxy `*.zoom.us`/`x.a.com`/`10.9.0.0/16`,direct `zoom.us`/`a.com`/`1.2.3.4`/`2001:db8::/32`,global 两种),china 列表取 `embedded.ChinaDomain()/ChinaCIDR()` 全量(空即 `t.Fatal`)。输入:每条规则的本体/子域/`evil` 前缀/大写尾点;china 列表抽样 300 条 + 子域 + 前缀攻击;IP:私网、china CIDR 抽样、公网、v6。对每个输入:`route.Explain` 的 Decision 映射到 outbound(Direct→direct、Proxy→proxy),与 `Evaluate` 比对,不同即 `t.Errorf` 点名输入与两边依据。
- [ ] **Step 2: 跑绿(它应当一次就绿;若红,那是翻译器的 bug,不是守卫)。**
- [ ] **Step 3: 变异**:把 translate 里 proxy/direct 的顺序对调 ⇒ `zoom.us` 那组红;把 `global` 分支删掉 ⇒ china 抽样红;把 `normalizeSuffix` 的 `*.` 去掉 ⇒ `*.zoom.us` 红。三条各咬中一条后还原。
- [ ] **Step 4: commit** `test(singboxrules): 一致性守卫 —— 同一份配置,route.Explain 与翻译后的规则逐输入相同`。

### Task 5: 真二进制守卫(check + rule-set match)

**Files:**
- Test: `internal/singboxrules/singbox_binary_test.go`(package `singboxrules_test`;`embedded.Singbox()` 为空则 `t.Skip` 并说明)

- [ ] **Step 1**:把内嵌 sing-box 释放到 `t.TempDir()`,写 `RouteJSON()` 合成的完整配置(outbounds:`direct` 直连、`proxy` 与 `block` 用 `block` 类型占位,inbounds 省略)与两个 rule-set 文件,跑 `sing-box check -c`,非零即 `t.Fatalf` 带输出。
- [ ] **Step 2**:对 `bx-china-domain.json` 与一份合成 rule-set,拿一组域名(命中/子域/`evil` 前缀/大写)跑 `sing-box rule-set match`,解析输出的 matched/unmatched,与 `Evaluate` 对同一 rule-set 的答案逐个比对。**这一条是 Evaluate 的语义与真 sing-box 对得上的唯一证据。**
- [ ] **Step 3: 跑绿;commit** `test(singboxrules): 内嵌 sing-box 对生成物跑 check 与 rule-set match`。

### Task 6: 文档与收尾

- [ ] spec §状态改「第一期已做(2026-09-29)」;§4 标题版本改 1.14.2;§4.4 保持未验;§8 表①打勾。
- [ ] `docs/known-gaps.md` 不加(没有已知没修的);`docs/acceptance-pending.md` 不加(全部桌面可验)。
- [ ] `bash scripts/verify.sh`;commit `docs(mobile): 第一期完成 —— 翻译层与三道守卫`;push。
