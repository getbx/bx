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
			p.lastMod = time.Time{} // force a digest check
		}
	}
}

func (p *policyPusher) pushIfChanged(ctx context.Context) error {
	if p.protected == nil || !p.protected() {
		return nil
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
		return err
	}
	state.Pushed[keys.BlobID] = digest
	p.saveState(state)
	p.lastMod = info.ModTime()
	log.Printf("guardian_policy_sync_pushed blob=%s… version=%d direct=%d proxy=%d", keys.BlobID[:8], pol.Version, len(pol.Direct), len(pol.Proxy))
	return nil
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
