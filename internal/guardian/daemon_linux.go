package guardian

// linux 的门自 2026-08-30 起打开。**顺序不许反**(CLAUDE.md 记档的移植纪律):
// 先供货、每一块有 netns 断言背书,门最后开。到位的是 —— procscan_linux
// (/proc 树)、peercred_linux(SO_PEERCRED)、barrier_iproute+barrier_linux
// (pref 120 + table 90 + throw 私网,四条断言打在 `ip route get` 的判决上)、
// dnsmanager_linux(DNSNotNeeded,数据面自己管)、observer 显式 nil,
// 以及 Manager 级的四条台子断言(真 spawn、真屏障、真 procscan)。
//
// **开门不改变 linux 的产品形态**:生产 linux 仍是 systemd 直管 supervisor,
// `bx up` 写的 unit 指向 `bx run`,没有任何东西会去装或拉起 Guardian ——
// 「产品形态不变」这句承诺由**没有调用方**保证,不由这道门保证。它开着,是为了
// 集成台能跑真 RunDaemon。谁要给 linux 产品真的接上 Guardian,从这里开始读:
// docs/superpowers/specs/2026-08-29-guardian-linux-adapter-design.md。
func requireDaemonPlatform() error { return nil }
