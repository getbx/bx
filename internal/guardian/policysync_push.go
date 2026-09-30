package guardian

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/policysync"
	"github.com/getbx/bx/internal/socks5"
	"github.com/getbx/bx/internal/supervisor"
)

// policyPusher 把 Mac 的分流规则加密后存到用户自己的 VPS(规则同步第三步;设计
// docs/superpowers/specs/2026-09-29-policy-sync-via-own-server-design.md)。
//
//   - **只经隧道**:拨号走 Core 那条隧道的 socks5,目的地是 VPS 回环上的 sync-store;保护没开
//     就一个请求都不发(绕开隧道去连 VPS 就是一次隧道外的明文连接)。
//   - **不变就不发**:每分钟 stat 一次配置,mtime 变了才算摘要,摘要变了才发 —— 菜单、
//     `bx direct add`、手改文件,三种写法都被同一个判据接住,不用在每个写入点接线。
//   - **存储说成功才记账**:推送失败什么都不记,下一轮再试。
type policyPusher struct {
	configPath string
	statePath  string
	protected  func() bool
	dial       func(ctx context.Context, network, addr string) (net.Conn, error)
	now        func() time.Time

	lastMod time.Time

	// 推送失败之后的退避(1m, 2m, 4m … 封顶 1h):一台没开 sync-store 的服务器是没用手机的人的
	// **常态**,此前每分钟重试一次 = 每分钟一条隧道连接 + Guardian 日志一行失败。
	failures int
	retryAt  time.Time
	lastErr  string
	// wasProtected:上一次看的时候保护开着没有。守护进程没接 poke,「保护刚打开」由这里自己看出来。
	wasProtected bool
}

const policySyncMaxBackoff = time.Hour

// poked 是保护刚打开:立刻再查一次(连同退避一起清掉 —— 用户刚开了保护,正是该试的时候)。
func (p *policyPusher) poked() {
	p.lastMod = time.Time{}
	p.retryAt = time.Time{}
}

type pushState struct {
	// Digest per blob id: switching servers changes the id, so the new server gets a push.
	Pushed map[string]string `json:"pushed"`
}

func newPolicyPusher(configPath string, protected func() bool) *policyPusher {
	return &policyPusher{
		configPath: configPath,
		statePath:  "/var/lib/bx/policy-sync.json",
		protected:  protected,
		dial:       dialThroughTunnel,
		now:        time.Now,
	}
}

// Run checks once a minute (a stat; nothing else unless the file changed) and on every
// transition into protection, via poke.
func (p *policyPusher) Run(ctx context.Context, poke <-chan struct{}) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		if err := p.pushIfChanged(ctx); err != nil {
			log.Printf("guardian_policy_sync_failed err=%v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-poke:
			p.poked()
		}
	}
}

func (p *policyPusher) pushIfChanged(ctx context.Context) error {
	if p.protected == nil || !p.protected() {
		p.wasProtected = false
		return nil
	}
	if !p.wasProtected {
		p.wasProtected = true
		p.poked()
	}
	if !p.retryAt.IsZero() && p.now().Before(p.retryAt) {
		return nil // backing off after a failed push: dial nothing, log nothing
	}
	info, err := os.Stat(p.configPath)
	if err != nil {
		return nil // no config yet: nothing to sync
	}
	if !p.lastMod.IsZero() && info.ModTime().Equal(p.lastMod) {
		return nil // the minute tick's usual case: one stat, nothing else
	}
	raw, err := os.ReadFile(p.configPath)
	if err != nil {
		return err
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		return nil // a config Core cannot use either; the doctor says so
	}
	keys, err := policysync.Derive(cfg.Server)
	if err != nil {
		return nil // not a reality link: this server cannot key a sync
	}
	pol := policyFromConfig(cfg)
	digest := policyDigest(pol)
	state := p.loadState()
	if state.Pushed[keys.BlobID] == digest {
		p.lastMod = info.ModTime()
		return nil
	}
	pol.Version = p.now().Unix()
	pol.UpdatedAt = p.now().UTC().Format(time.RFC3339)
	blob, err := policysync.Seal(keys, pol)
	if err != nil {
		return err
	}
	if err := p.put(ctx, keys.BlobID, blob); err != nil {
		return p.failed(err)
	}
	p.failures, p.retryAt, p.lastErr = 0, time.Time{}, ""
	state.Pushed[keys.BlobID] = digest
	p.saveState(state)
	p.lastMod = info.ModTime()
	log.Printf("guardian_policy_sync_pushed blob=%s… version=%d direct=%d proxy=%d", keys.BlobID[:8], pol.Version, len(pol.Direct), len(pol.Proxy))
	return nil
}

// failed schedules the next try and says whether this failure is news: the first one, or a
// different one from last time. Repeats return nil so Run logs nothing.
func (p *policyPusher) failed(err error) error {
	p.failures++
	backoff := time.Minute << (p.failures - 1)
	if p.failures > 7 || backoff > policySyncMaxBackoff {
		backoff = policySyncMaxBackoff
	}
	p.retryAt = p.now().Add(backoff)
	msg := err.Error()
	if msg == p.lastErr {
		return nil
	}
	p.lastErr = msg
	return err
}

func (p *policyPusher) put(ctx context.Context, id string, blob []byte) error {
	client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{DialContext: p.dial}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://"+policysync.StoreAddr+"/v1/blob/"+id, bytes.NewReader(blob))
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("the server's sync store is not reachable through the tunnel (on the server: sudo bx server enable-sync): %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("the server's sync store answered %s", resp.Status)
	}
	return nil
}

func policyFromConfig(cfg *config.Config) policysync.Policy {
	p := policysync.Policy{Global: cfg.Global, Direct: []string{}, Proxy: []string{}}
	for _, r := range cfg.Rules {
		p.Direct = append(p.Direct, r.Direct...)
		p.Proxy = append(p.Proxy, r.Proxy...)
	}
	return p
}

func policyDigest(p policysync.Policy) string {
	b, _ := json.Marshal(struct {
		Global bool
		Direct []string
		Proxy  []string
	}{p.Global, p.Direct, p.Proxy})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (p *policyPusher) loadState() pushState {
	s := pushState{Pushed: map[string]string{}}
	if raw, err := os.ReadFile(p.statePath); err == nil {
		_ = json.Unmarshal(raw, &s)
	}
	if s.Pushed == nil {
		s.Pushed = map[string]string{}
	}
	return s
}

func (p *policyPusher) saveState(s pushState) {
	b, _ := json.Marshal(s)
	_ = os.WriteFile(p.statePath, b, 0o600)
}

// dialThroughTunnel reaches the VPS loopback via Core's tunnel socks5 — never via the TUN,
// which would treat 127.0.0.1 as this Mac's own loopback.
func dialThroughTunnel(ctx context.Context, network, addr string) (net.Conn, error) {
	state, err := supervisor.FetchRuntimeStateContext(ctx, supervisor.SockPath)
	if err != nil {
		return nil, fmt.Errorf("Core is not answering: %w", err)
	}
	if state.SocksAddr == "" {
		return nil, fmt.Errorf("Core reported no tunnel socks address")
	}
	d, err := socks5.NewDialer(state.SocksAddr, &net.Dialer{Timeout: 10 * time.Second})
	if err != nil {
		return nil, err
	}
	return d.DialContext(ctx, network, addr)
}
