package supervisor

import (
	"errors"
	"sync"
	"testing"
)

// fakeServerSwapper 与 mutator_test.go 里的 fakeSwapper 同构(currentLink/swapTo),
// 但另起名字以避免同包内类型重名——本文件需要 failOn(按目标链接触发失败)与
// history(记录 swapTo 调用顺序),既有 fakeSwapper 用的是 swapErr(任何调用都失败)。
type fakeServerSwapper struct {
	link    string
	failOn  string // swapTo 到这个链接时报错
	history []string
}

func (f *fakeServerSwapper) currentLink() string { return f.link }
func (f *fakeServerSwapper) swapTo(link string) error {
	f.history = append(f.history, link)
	if link == f.failOn {
		return errors.New("建不起来")
	}
	f.link = link
	return nil
}

func TestSetServerSwapsBothSlots(t *testing.T) {
	main := &fakeServerSwapper{link: "vless://hk"}
	udp := &fakeServerSwapper{link: "hysteria2://hk"}
	m := &liveMutator{swap: main, udpSwap: udp}

	apply, _, err := m.SetServer("vless://tokyo", "hysteria2://tokyo")
	if err != nil {
		t.Fatal(err)
	}
	if err := apply(); err != nil {
		t.Fatal(err)
	}
	if main.link != "vless://tokyo" || udp.link != "hysteria2://tokyo" {
		t.Fatalf("两个槽都要换, got main=%q udp=%q", main.link, udp.link)
	}
}

// 半切状态是个**合法但没人要**的配置:bx 本来就支持 TCP/UDP 走不同主机,所以
// 它不报错、还能用、status 也显绿 —— 只是出口不是用户以为的那台。
func TestSetServerRollsBackMainWhenUDPFails(t *testing.T) {
	main := &fakeServerSwapper{link: "vless://hk"}
	udp := &fakeServerSwapper{link: "hysteria2://hk", failOn: "hysteria2://tokyo"}
	m := &liveMutator{swap: main, udpSwap: udp}

	apply, _, err := m.SetServer("vless://tokyo", "hysteria2://tokyo")
	if err != nil {
		t.Fatal(err)
	}
	if err := apply(); err == nil {
		t.Fatal("UDP 换失败时整个 apply 必须报错")
	}
	if main.link != "vless://hk" {
		t.Fatalf("UDP 失败必须把主传输也换回去, got %q", main.link)
	}
	if udp.link != "hysteria2://hk" {
		t.Fatalf("UDP 槽必须留在原处, got %q", udp.link)
	}
}

func TestSetServerClearsUDPSlotWhenTargetHasNone(t *testing.T) {
	main := &fakeServerSwapper{link: "vless://hk"}
	udp := &fakeServerSwapper{link: "hysteria2://hk"}
	m := &liveMutator{swap: main, udpSwap: udp}

	apply, _, err := m.SetServer("vless://tokyo", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := apply(); err != nil {
		t.Fatal(err)
	}
	// 目标没有 UDP 专用传输 ⇒ UDP 回落到主传输,而不是留着上一台的。
	// 留着上一台 = UDP 流量还从 hk 出去,而用户以为自己已经在 tokyo。
	if udp.link != "vless://tokyo" {
		t.Fatalf("目标没 udp 时 UDP 槽必须跟随主传输, got %q", udp.link)
	}
}

func TestSetServerUndoRestoresBothSlots(t *testing.T) {
	main := &fakeServerSwapper{link: "vless://hk"}
	udp := &fakeServerSwapper{link: "hysteria2://hk"}
	m := &liveMutator{swap: main, udpSwap: udp}

	apply, undo, _ := m.SetServer("vless://tokyo", "hysteria2://tokyo")
	if err := apply(); err != nil {
		t.Fatal(err)
	}
	if err := undo(); err != nil {
		t.Fatal(err)
	}
	if main.link != "vless://hk" || udp.link != "hysteria2://hk" {
		t.Fatalf("undo 必须把两个槽都还原, got main=%q udp=%q", main.link, udp.link)
	}
}

// serverBypass 在旧代码里是启动时捕获的定值。加**新**服务器时它不含新 IP,
// 而 Rehijack 正是为了把新 IP 装进去才被调用的 —— 读到陈旧值就等于没装,
// 切过去立刻成环。
func TestRehijackReadsBypassAtApplyTime(t *testing.T) {
	fp := &fakePlatform{}
	m := &liveMutator{plat: fp, serverBypass: []string{"1.1.1.1/32"}}
	apply, _, err := m.Rehijack()
	if err != nil {
		t.Fatal(err)
	}
	m.SetServerBypass([]string{"1.1.1.1/32", "2.2.2.2/32"})
	if err := apply(); err != nil {
		t.Fatal(err)
	}
	if len(fp.lastServerBypass) != 2 {
		t.Fatalf("Rehijack 必须用调用时的 bypass 集合(否则新服务器的 IP 装不进去,切过去成环), got %v", fp.lastServerBypass)
	}
}

// serverBypass 上那把锁守的是一条真实可达的并发:commit-confirmed 引擎跑在自己的
// goroutine 上(run.go 的 `go mutEng.Run(ctx)`)、由它执行 Rehijack 的 apply,
// 而 SetServerBypass 来自控制面那条路 —— 后续任务把「切服务器」的 HTTP handler
// 接上之后,两者就确确实实在不同 goroutine 上跑。
//
// 但锁本身没有任何测试盯着。它是那种**去掉之后一切照常绿**的东西:
// 竞态要在真实并发下才暴露,而单测默认不并发。这条补的就是那个空档 ——
// 它只在 `go test -race` 下有意义,而 -race 已经是本项目的固定验证命令之一。
func TestServerBypassIsSafeUnderConcurrentUpdateAndRehijack(t *testing.T) {
	m := &liveMutator{plat: &fakePlatform{}, serverBypass: []string{"1.1.1.1/32"}}
	apply, _, err := m.Rehijack()
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			m.SetServerBypass([]string{"1.1.1.1/32", "2.2.2.2/32"})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			if err := apply(); err != nil {
				t.Errorf("apply 报错: %v", err)
				return
			}
		}
	}()
	wg.Wait()
}

// liveMutator 与刷新、路径恢复必须看同一份集合。以前它自己藏一份,
// 刷新写进去、路径恢复读不到,反之亦然 —— 每多一份冻结拷贝就多一个成环入口。
func TestLiveMutatorReadsSharedBypassStore(t *testing.T) {
	fp := &fakePlatform{}
	store := newBypassStore([]string{"1.1.1.1/32"}, nil)
	m := &liveMutator{plat: fp, store: store}

	store.set([]string{"1.1.1.1/32", "2.2.2.2/32"}, nil, nil)
	apply, _, err := m.Rehijack()
	if err != nil {
		t.Fatal(err)
	}
	if err := apply(); err != nil {
		t.Fatal(err)
	}
	if !containsString(fp.lastServerBypass, "2.2.2.2/32") {
		t.Fatalf("Rehijack 必须拿到 store 里的当前集合, got %v", fp.lastServerBypass)
	}
}

// SetServerBypass 在接了 store 的部署里必须写进 store(而不是写进自己那份影子拷贝,
// 那样路径恢复与后续刷新都看不见)。
func TestLiveMutatorSetServerBypassWritesThroughToStore(t *testing.T) {
	store := newBypassStore([]string{"1.1.1.1/32"}, nil)
	m := &liveMutator{plat: &fakePlatform{}, store: store}
	m.SetServerBypass([]string{"3.3.3.3/32"})
	if !containsString(store.cidrs(), "3.3.3.3/32") {
		t.Fatalf("写进 store 才能被所有消费者看见, got %v", store.cidrs())
	}
}
