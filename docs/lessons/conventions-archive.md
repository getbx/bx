# 约定 —— 2026-09-23 从根目录 CLAUDE.md 精简时的原文存档

**这是存档,不是现行规矩。** 根目录 `CLAUDE.md` 的「约定」一节在 2026-09-23 精简成规矩本身;
这里逐字保留当时的原文 —— 每条文档守卫的来历与首次扫描结果、并行子代理那次 `git stash`
事故、verify 每一步为什么存在、CI flake 的逐条复盘(socks5 跨族环回、TempDir 与活过测试的
goroutine、挂钟赌调度)、真机与 CI 互相假绿那次。**冲突时以根目录那份为准。**

---

- **CLAUDE.md / README.md 点名的文件必须真的在**(`TestDocumentedFilePathsExist`,
  2026-08-24)。**范围刻意只有这两份,不含 `docs/superpowers/{specs,plans}`** ——
  首次全仓扫描给的结论:文档里共点名 541 个路径、55 个不存在,而**这 55 个无一在
  CLAUDE.md**,全部在 plans 里。计划书是**有日期的意图记录**,它点名的是「将要建
  的文件」;实施走偏或功能后来被删,它的路径失效是预期的,不是谎。把 plans 拉进来
  只会制造 55 条假红,而假红的守卫会被下一个人删掉。
  **同一轮试过、而刻意没做的一条**:「文档里点名的**标识符**是否存在」。噪声太大 ——
  92 个「查不到定义」里绝大多数是 stdlib、Win32/AppKit API、plist 键、域名,以及
  CLAUDE.md 自己明确记述「已删」的东西(`KickControl`/`StatusPanel` 那一类),做不成
  闸门。**但那次扫描本身有产出**:它抓出 CLAUDE.md 的「速率」那一整段描述的是已经
  被换掉的做法(客户端做差、键是 (组,应用名)、判据在 `AppTrafficRateTracker` 里 ——
  三条都不再成立,那个类型已删),已按代码重写。
- **散文里点名的测试必须真的存在**(`TestEveryTestNameMentionedInProseExists`,
  **范围含 CLAUDE.md 本身 + `apps/` 下的 Swift**,
  `internal/cli/testnamerefs_test.go`,2026-08-24)。这个仓库最常复发的失效不是代码
  错,是**关于代码的陈述**错:注释写着「由 `TestXxx` 钉住」而 `TestXxx` 早已改名或
  删除。下一个人读到那句话,会**据此不再去检查那件事**。首次全仓扫描一次性抓出
  **7 个失效引用**;其中一个点名的测试**压根不存在**,而它描述的那件事(常驻安全
  告警的 hint 必须是真敲得动的命令 —— 它已经错过两次:一次指向不存在的
  `bx direct remove`、一次漏了 `sudo`)在 supervisor 那一侧**确实无人守**,那条守卫
  已按注释描述的样子补上。判据对**前缀**宽容(`TestFoo*` 这类通配写法很常见,
  宁可放过一个也不制造假红);**刻意退场**的测试登记进 `retiredTestNames` 并写明
  被什么接手了,另有反向断言钉住「退场的名字不许又变回真测试」。
  **这条守卫自己犯过它要抓的那个错**:反向断言原先排在前缀匹配之后,于是永远
  不可达 —— 变异实测随手加一个同名空测试整条守卫照样绿。**一条在最需要它时恰好
  不可达的断言,与没有这条断言完全一样,而它看起来更让人放心。**
  **2026-09-02 把 CLAUDE.md 拉进同一条守卫**(不是加第二份):它点名了 41 个测试
  而此前**一个守卫都没有** —— 而它恰恰是下一个人(或下一个 agent)开工前唯一会
  通读的东西。首次全量扫描**是干净的**:5 个查不到的里两个是散文占位符
  (`TestFoo`/`TestXxx`,按构造被正则的最短长度排除,不是碰巧),另外三个正是
  「读源码的守卫:三种处置」那张表里明写已退役的,早登记在 `retiredTestNames`。
  测试**顺带改了名**(原 `…MentionedInAComment…`):一条叫「注释」的守卫会让人
  以为 CLAUDE.md 不在保护范围内,而那正是它自己要消灭的那种陈述。
  **2026-09-12 再扩到 `apps/` 下的 Swift**(仍不含 `docs/superpowers/{specs,plans}`,
  理由同 `TestDocumentedFilePathsExist`:计划书是有日期的意图记录,失效是预期的,
  拉进来只会制造 55 条假红)。起因是一次审计在 `main.swift` 里抓到一条失效引用,
  而守卫**在结构上看不见它**;而菜单恰恰是全仓测试覆盖最薄、读源码守卫最多的
  一块 —— 「由 `TestXxx` 钉住」在那里**最承重、也最不容易被发现失效**。Swift 只
  贡献引用不贡献定义(那边的「测试」是 `@main` 结构体加一串 `expect(...)`,没有
  `Test…` 开头的函数名)。**够不着要扫的源码时必须 `t.Fatal`**:两道下限,走不进
  `apps/` 一道、走进去了却一个 `.swift` 都没见着一道 —— 一条安静地扫了零个文件的
  守卫,与没有这条守卫在输出上完全一样,而它看起来更让人放心。首次全量扫描
  **是干净的**(Swift 里 9 处点名全部指向真实存在的 Go 测试)。
  **同一轮收窄了 `retiredTestNames` 那个逃生口。** 它是给**历史记述**用的
  (「那条已退场,由 X 接手」),而审计发现它正在被一句**现在时**的断言吃着:
  `internal/stats/outcome.go` 写着「由 <某条已退场的测试> 钉住」,名字在名单里,
  于是全绿 —— 名字一旦进名单,就从「必须真的存在」变成了「随便怎么用都行」。
  现在多一道:退场的名字**同一句话里紧跟着**断言词(钉住/钉死/守着/守住/盯着/
  pinned by),而相邻一行又没有任何退场字样,就红。**判定粒度是「一句话」不是
  「一段」,这是变异实测逼出来的**:第一版免责窗口开到 ±3 行,把出事那天的原话
  写回去照样全绿 —— 那一段在事后被改对时补上了「当初那条守卫因此退场」,窗口够宽
  就把新写回去的假话一并赦免了,而**一段同时讲着「它退场了」和「由它钉住」的文字
  恰恰是最不该赦免的那一段**。网仍然刻意窄(动词在名字前面、被折到下一行、同义
  改写、块注释、英文只认一种写法,都看不见),理由是本仓库那条老纪律:一条会误报
  的闸门比没有闸门更糟。真实的 12 处退场记述一条都不红。
- **CLAUDE.md 与 `docs/lessons/` 的分家规则(2026-09-13 定)**:CLAUDE.md 只放**判据**
  ——「改这块之前必须知道什么」「哪条不变量不许动」「什么是已知缺口」;**过程**
  (某次事故的逐轮复盘、某个功能的施工日志、某条守卫当初怎么被攻破的)进
  `docs/lessons/`。**判据不许只存在于 lessons 里**:那边是给「想知道当初怎么换来的」
  的人看的,而 CLAUDE.md 是每次会话都加载、下一个人开工前唯一会通读的那一份。
  定这条规则是因为它长到了 200k 字符,其中一个 markdown 列表项独占 79KB(22%)
  —— **通读不了的东西等于没写**。
  **`docs/lessons/` 与 CLAUDE.md 受同两条守卫保护**(`TestDocumentedFilePathsExist`
  与 `TestEveryTestNameMentionedInProseExists`,2026-09-13 扩的范围),因为搬迁本身
  会制造盲区:那些原文点名的测试与路径,搬出去之后若无人看管,**同样会被读到、却
  不再会被证伪**。它与 `docs/superpowers/{specs,plans}` 的区别是**时态** —— 计划书
  写的是「将要建的东西」,失效是预期的;lessons 写的是已经发生的事实。
  **推论:真机验收的逐条结果进 lessons,CLAUDE.md 只留「已验 / 未验」那一行状态加
  指针。** 否则它会随每一次验收单调增长 —— 2026-09-13 那次拆分刚把它压到 148k,
  补三条实测证据就又吃掉 1.6k。**但「未验」那一半必须留在 CLAUDE.md**:它是待办,
  不是历史。
- **TDD**:先写失败测试→跑红→最小实现→跑绿→提交。纯逻辑测试免 root(用 `t.TempDir()`,不碰真实路由/设备)。
- **绝不并行派两个会写盘的子代理进同一个 checkout。** 2026-08-17 实测的代价:两个
  实施代理按「路径不相交」并行(一个改 `internal/socks5`,一个改
  `internal/cli`/`internal/rulereview`),结果**一方为隔离自己而 `git stash`,把另一方
  五个进行中的未提交文件整批卷走** —— 受害者看到的现象是「文件回到一个我从未提交过
  的 HEAD,而两个我根本没碰过的文件显示为已修改」,只能从零重做。
  **路径不相交挡得住 git 冲突,挡不住两件事**:① `verify.sh` 是全树的,任何一方跑全量
  都会读到另一方的半成品(已害得一个代理吃过一次假红);② **`git stash` 是全树操作,
  完全不受路径保护**。
  只读的 review 代理可以并行。要真并行写,就得各自一个 worktree。
  **控制器的判断错误也记在这里**:当时依据 stash 那一方「零数据丢失、逐字节还原」的
  报告下了「没出事」的结论 —— 那只在它自己视角内成立,它不知道自己卷走了同伴的工作。
  **一方的报告不是全局事实**,尤其当那一方恰好是肇事者。
- **验证命令**:`bash scripts/verify.sh`(全量)或 `--quick`(改一行时)。
  **步数这里不写了** —— 原文写着「全量 14 步」,而实测是 17 步,与拆除台账那条
  「九处 defer / 11 处」同一个形状:一个没人会去核的数,过一阵就变成假的。
  要知道有哪几步就跑一次看横幅;要知道 `--quick` 跳了什么,横幅也会逐条报出来
  (race / 交叉编译 / windows 测试 typecheck / integration 测试 typecheck /
  纯判据可移植性,共 5 步 —— **此前后三步在 `--quick` 下一个字都不报**,横幅却说
  「跳过 2 步」,2026-09-17 补上)。
  **2026-09-13 加的 windows typecheck 那步值得单说**:那圈交叉编译用的 `go build` **从不编译 `_test.go`**,
  而 Windows 那半的行为断言只在 CI 的 windows runner 上跑 —— 实测把一个 `*_windows_test.go`
  里的常量改成不存在的名字,`go vet ./...` 与 `GOOS=windows go build ./...` **两条都通过**,
  推上去才红。现在多一步:只 vet 那些含 windows-tagged 测试的包(vet 会 typecheck 测试文件),
  清单从 `git ls-files` 现取、一个文件都找不到时响亮失败。**刻意不写成 `GOOS=windows go vet ./...`**
  —— `internal/tray` 有一条先于此存在的 unsafe.Pointer 告警,拉进来就是一道恒红的闸门。
  **2026-09-17 补的 integration typecheck 是同一条盲区的另一半,但上限更低**:那九个
  `//go:build integration && linux` 的 netns 台子既不被 `go build` 编(不编测试文件)、
  也不被 `go test ./...` 编(缺 tag),本机**一个字都看不见**,只有 CI 那条
  `sudo go test -tags integration ./...` 会红。这一步只 typecheck,**不跑** ——
  同一天就有一条真实的断言(换服务器被拒绝时答复里那句话)随文案改英文而失效,
  而 vet 对它一个字都说不出来。它拦得住「改了个名字、台子编不过了」,拦不住
  「编得过、断言不再成立」;后者今天仍然只有 CI 那条腿证得了。
  **判据一律是退出码,不是字符串匹配。** 它的存在是因为 2026-08-11 那轮里同一个根因栽了六次:
  `go test … | grep …; git commit` 用 `;` 串联(测试红了照样提交)、变异验证 grep `^failed` 而套件
  打印的是 `FAIL:`(「没转红」被误判成守卫失效)、`head -5` 查 `set -e` 而注释头十几行、`grep -c` 数
  「出现次数」而它数的是行数、替换串带了不存在的前导 tab 而 `str.replace` 匹配不上时不报错。
  **别再手敲那一串命令**;`verify.sh` 自己也验过五个方向都会失败,漏一道闸门由 `TestVerifyScriptCoversEveryGate` 钉住。
  **一个会偶发红的闸门比没有闸门更糟**,因为它训练人去重跑
  **2026-09-14 的 release run 连着红两次,两次是不同的测试、不同的病因,而且
  `scripts/verify.sh` 在本机(macOS)全绿 —— 那一整类平台差异它结构上覆盖不到,
  因为它跑的是这台 Mac,而 CI 的 build job 跑 Linux。两条都记下来:**
  ① `TestManagerUpStartsCoreDespiteUnremovableDeadCoreRecord` —— **确定性的,已修**。
  它 `release` 那个假进程,于是 manager 把它当**意外退出**走 `handleUnexpectedExit`:
  写状态、可能再起一个 Core,而那些全落在 `t.TempDir()` 里,与 TempDir 自己的
  `RemoveAll` 抢同一个目录(`unlinkat …: directory not empty` —— 删完内容正要
  rmdir 时又被写进来)。修法是**不 release**:这个测试到 Up 成功就该结束,再模拟
  一次退出不属于它。**只同步 runner 那条 goroutine 不够(试过),manager 的 monitor
  是另一根。** 形状与 socks5 那次同源(活过测试函数的 goroutine),只是那次碰的是
  `t.Errorf`,这次碰的是文件。**在 Colima 的 linux 容器里复现与验证**(本机复现不出来)。
  ② `TestManagerUpdateReservesDeadlineForTargetCleanup` —— **仍是潜在 flake,未修**。
  整个 `Update` 只给 500ms,而 v2 的健康检查无限阻塞、先吃掉大半,剩给「回滚后等
  v1 健康」的余量在 CI 慢机器上不够(`previous_core_health_failed`);本机与本地
  linux 容器各跑 20~30 次都全过。**正确修法不是把 500ms 调大** —— 要先弄清 `Update`
  内部怎么在健康检查与清理之间分预算(那正是这条测试要证明的东西),否则调大只是
  把同一个竞态推远一点。
  **2026-09-17 又红一次,同一个码,而这次值得记的是它落在哪条腿上**:v0.4.1 的
  release run 在 `build` 里红,而 **ci.yml 在同一个 commit 上二十分钟前刚 7/7 绿过**
  (含同一个包的 `go test ./...`)—— 于是它不是回归,是那个竞态本身。**两次已知的
  CI 失败都落在 release.yml,一次都没落在 ci.yml。** 两个样本不足以断言「只在发版
  时发生」(两条腿跑的是同一条命令,更可能只是概率),记下来是因为**它挡住的恰好
  是最不想被挡住的那件事**:发版流水线红了就没有 release 资产,而重跑一条已知会
  偶发红的闸门,正是本节那句「一个会偶发红的闸门比没有闸门更糟」警告的东西。谁
  下次动这条测试,先去比 `release.yml` 与 `ci.yml` 两条腿的 runner 规格与并发度 ——
  那是今天还没查的一格。

  **2026-09-18 第三次,而这一条是确定性写法造成的、已修,判据可复用**:
  `TestManagerDNSContextFailureUsesBoundedBarrierCleanupContext/ensure/deadline`
  在 ubuntu 那条腿上红成「DNS context failure did not leave a proven barrier」,
  而真因与屏障无关。它要证明的是「DNS 那一跳拿到的 context 死掉时,屏障清理必须
  另起一个活的 context」—— 那要求 context **在 DNS 那一跳**死掉;它用的却是
  `context.WithTimeout(…, 40ms)`,给的是「在某个绝对时刻死掉」。**两者只在
  `Up` 能在预算内走到 DNS 时才等价**,而那取决于机器有多忙:预算先到期时 `Up`
  失败在更早的一步,那条路不装恢复屏障,断言于是红在屏障上 —— 报的不是它守的
  那件事。**形状叫得出名字:拿挂钟去指定「哪一步」该失败,就是在赌调度。**
  兄弟子测试 "canceled" 从来没这个问题(它的 `cancel()` 由 `fail` 自己调,
  时点就是那一跳);修法是照它,换一个由调用方决定何时到期、`Err()` 仍报
  `DeadlineExceeded` 的 context 替身,错误形状一个字没变。**把 40ms 调大不算修。**
  顺带补的那条断言值得照抄:`Up` 没走到 DNS 就失败时**当场说出来**,别让它伪装
  成屏障问题 —— 「断言被满足/被违反,但是因为别的理由」是记档在案的第五种守卫
  失效写法。复现方式:把那个预算改成 1ns,得到 CI 那句一字不差的话。
 —— 而重跑正是「判据是
  退出码」这条纪律唯一的解毒方式。2026-08-17 抓到并修掉一个:`internal/socks5` 的
  `TestDialerUDPAssociateRelaysDatagrams` 在 1500 次里失败 4 次,根因是 UDP ASSOCIATE
  的客户端 socket 绑的是**双栈通配** `[::]`,而 relay 是 IPv4 —— 服务端明明写成功了
  (14 字节、err=nil、28µs),客户端两秒收不到。**内核层面为什么会漏投这个跨族环回包
  至今未查清**,但修法不依赖它:一个 SOCKS5 客户端只跟一个 relay 说话,socket 就该绑在
  **relay 所在的地址族**上(`78edafa`,改后 0/1500)。守卫钉的是**修法的机制**而不是那个
  flake ——「IPv4 relay ⇒ 本地址是 IPv4」是确定性的,而 1/375 的失败率跑一遍抓不到。
  **同一个文件里第二个、独立的间歇失败源也已修(2026-08-17)**:`serveTCP`/`serveUDP` 是
  活过测试函数的 goroutine,而它们在里面调 `t.Errorf`(以及 `t.Helper()`,同样是测试
  完成后不该调的 `*testing.T` 方法)—— 测试返回之后再调 `t.Errorf` 会让 Go panic
  (`Log in goroutine after Test… has completed`)。当时只修了被点名的那一处丢弃
  `WriteTo` 错误的地方(经 `t.Cleanup` 排空的 channel);现在把同一套机制推广到两个
  goroutine 里全部诊断点(读握手/版本/方法/请求/地址、写方法回复/写 ASSOCIATE 回复、
  解析/构造 UDP 数据报……一律经 `s.reportf` 排队成 `error`),并加一个 `sync.WaitGroup`
  让 `t.Cleanup` **先等两个 goroutine 真正退出、再排空 channel 逐条 `t.Errorf`**——
  否则会有「goroutine 还没来得及把错误塞进 channel,Cleanup 已经查过一遍」的竞态,
  origin 那版靠 `select+default` 单次不阻塞查询本就吃这个亏。`t.Helper()` 从两个
  goroutine 里整个删掉:它们不再直接调 `t.Errorf`,标记 helper 帧对它们已没有意义。
  **教训是通用的、留着**:任何活过测试函数的 goroutine,一旦持有 `*testing.T` 并调用
  它的任何方法(不止 `Errorf`/`Fatalf`,`Helper`/`Log` 同样算),就是一颗定时炸弹 ——
  正确的形状始终是「goroutine 只把错误递给一个 channel,由测试(或 `t.Cleanup`)
  自己的 goroutine 在还没标记完成时把它转成 `t.Errorf`」,一份机制,别为下一个诊断点
  另开一条路。
  两处 grep 参与判据是**必要**的并已注明:`test-macos-menu.sh` 提前 `exit 0` 时退出码仍是 0(只有收尾
  横幅抓得住),`gofumpt -l` 输出文件名而退出码恒 0。
- **真机绿不等于这条路没问题 —— 真机与 CI 互不替代(2026-09-16 付的学费)。**
  给 Windows 那条腿修三条测试时,测试二进制交叉编译到项目所有者的真机
  (`030-SJWJ-GSR-B`)上跑,七个包全绿,变异对照也做了(修复前红、修复后绿,
  CRLF 那条还当场复现了「找不到函数结尾」)。推上 CI 第一轮却红了 **11 条**:
  `control_client_test.go` 写死 `os.MkdirTemp("/tmp", …)`,而 `/tmp` 在 Windows 上
  解析成**当前盘**的 `\tmp` —— 那台真机恰好有 `C:\tmp`(事后实测确认),runner 上没有。
  **真机比 CI 宽松,于是它在这一族上给了假绿,而我当时已经用它下过「七个包全绿」的结论。**
  分工是确定的,别拿一边的绿去替另一边背书:真机验 CI 验不了的(平台语义、真实文件
  系统、真实网卡、真实网络);CI 验真机验不了的(**干净 checkout** —— `.gitattributes`
  的行尾效果只有它证得了、标准环境、没有任何本地遗留物)。
- **提交信息**:中文 conventional commits,结尾带 `Co-Authored-By: Claude …`。在默认分支直接提交(单人项目)。
- **内嵌资产**:`internal/embedded/assets/brook_linux_{amd64,arm64}`(~30MB)+ `singbox_{linux,darwin}_{amd64,arm64}`(linux ~28MB / darwin ~23MB)是提交进仓库的真二进制,按 GOOS/GOARCH 条件 embed(每构建只嵌匹配的那一个;singbox 经 `embedded_singbox_{amd64,arm64,darwin_amd64,darwin_arm64,other}.go`,**linux+darwin 都内嵌(同 brook 平台覆盖,mac 上 reality/hysteria2 也零依赖即跑)**,windows/其他 arch 走 nil 兜底→下载)。CI `embed-brook.yml`/`embed-singbox.yml` 跟上游 release 自动重嵌。换 arch 要补对应二进制。**缓存键掺内容 hash(已实现)**:`provision.embedCacheKey` = 版本 tag + `sha256(内嵌字节)[:12]`,写进 `.brook-version`/`.singbox-version`;同 tag 重嵌不同字节(如 sing-box 从 `with_utls` 加到 `with_utls,with_quic`)也会失效旧缓存、强制重释放,避免用到陈旧二进制。
  - **sing-box 是「自建静态最小构建」不是官方 release 二进制**:官方 linux 包是 glibc **动态链接 + 56MB 全家桶**(含 tailscale/acme/clash/dhcp,reality 全用不上),违背 bx「静态单文件、零依赖」。故从同一 release tag 源码用 `CGO_ENABLED=0 go build -tags with_utls,with_quic`(REALITY 需 utls;**hysteria2/QUIC 需 with_quic**)自建:**静态**(Alpine/musl 也跑,同 brook)、**~28MB**(官方半体积)、同 revision。CI `embed-singbox.yml` 复刻此构建;改时务必保持 `with_utls,with_quic` 与 `CGO_ENABLED=0`。
- **绝不擅自启动 bx / 改路由**:启动是用户的事(需 root、动真实网络)。改完让用户自己 `bx up`。
  **2026-09-13 真机事故:这条约定被 shell 绕过去了一次,而不是被谁决定绕过去的。** 一个只读
  排查代理在**双引号**的 grep 模式里带了反引号,zsh 把它当命令替换执行,于是真的跑了一次
  `bx up`(Core 没起来、屏障没装、路由与 DNS 未动;只有盘上 `desired` 被翻成 on,因为
  `upLocked` 先写意图再起 Core)。**这个仓库对这个形状格外脆弱:文档里到处是反引号包着的命令**,
  而搜文档是每个代理开工第一件事 —— 一次 `grep -rn "…`bx up`…" CLAUDE.md` 就会真的执行它。
  **规矩:凡是搜索/匹配用的模式一律单引号**(单引号里的反引号不执行,双引号里的会);
  要在双引号里出现反引号就转义。判据不是「代理会不会自觉」——这次自觉的是代理,执行的是 shell。
- gVisor/wireguard 等库的 API 易随版本变——查 `$(go list -m -f '{{.Dir}}' <module>)` 的真实源码,别凭记忆。

