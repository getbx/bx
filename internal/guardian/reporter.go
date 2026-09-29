package guardian

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/getbx/bx/internal/config"

	"github.com/getbx/bx/internal/doctor"
	"github.com/getbx/bx/internal/report"
)

// DefaultReportsEndpoint 是维护者的收集端(tools/reports-collector,Cloudflare Worker)。
// 不挂任何个人域名;配置 `reports_endpoint:` 可覆盖,`reports: off` 全关。
const DefaultReportsEndpoint = "https://bx-reports.example.invalid/v1/reports"

const (
	reportsDir           = "/var/lib/bx/reports"
	reportsInstallIDPath = "/var/lib/bx/install-id"
	reportLogTailLines   = 200
	reportDoctorBudget   = 10 * time.Second
	reportFlushInterval  = 5 * time.Minute
	reportMaxBackoff     = time.Hour
)

// ReporterOptions 是 Reporter 的全部输入。**Protected 是承重的**:只在保护开着(经隧道)时
// 才发,保护关着、隧道不健康时只入队 —— 直连 `*.workers.dev` 的 SNI 就是一条「这台机器
// 装了 bx」的明文证据,而报告本来就不急。
type ReporterOptions struct {
	Enabled     bool
	Endpoint    string
	Dir         string
	InstallID   string
	Version     string
	OS          string
	Arch        string
	OSVersion   string
	Protected   func() bool
	Doctor      func(ctx context.Context) doctor.Report
	LogTail     func(n int) []string
	HTTPTimeout time.Duration
}

// Reporter 在几类失败上攒一份脱敏包、落本地、经隧道送到收集端。
// 设计:docs/superpowers/specs/2026-09-29-bx-problem-reports-design.md
type Reporter struct {
	opts   ReporterOptions
	store  *report.Store
	client *http.Client

	mu     sync.Mutex
	recent []report.Transition
	kick   chan struct{}
}

func NewReporter(opts ReporterOptions) *Reporter {
	if opts.Endpoint == "" {
		opts.Endpoint = DefaultReportsEndpoint
	}
	if opts.HTTPTimeout <= 0 {
		opts.HTTPTimeout = 15 * time.Second
	}
	if opts.OS == "" {
		opts.OS = runtime.GOOS
	}
	if opts.Arch == "" {
		opts.Arch = runtime.GOARCH
	}
	return &Reporter{
		opts:   opts,
		store:  report.NewStore(opts.Dir, 50),
		client: &http.Client{Timeout: opts.HTTPTimeout}, // 普通拨号:要的正是经隧道
		kick:   make(chan struct{}, 1),
	}
}

// Transition 记一次保护状态的转换(最近 10 次进报告)。
func (r *Reporter) Transition(state, code string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recent = append(r.recent, report.Transition{At: time.Now().UTC().Format(time.RFC3339), State: state, Code: code})
	if len(r.recent) > 10 {
		r.recent = r.recent[len(r.recent)-10:]
	}
}

// Record 在一次失败上攒包、脱敏、落盘,并踢一下发送循环。关着、限频不过、写不下来都
// 安静返回(记日志):上报是附加功能,绝不许它连累 Guardian 自己的事。
func (r *Reporter) Record(signature string, failure report.Failure) {
	if r == nil || !r.opts.Enabled {
		return
	}
	now := time.Now().UTC()
	if !r.store.Allow(signature, now) {
		return
	}
	b := report.Bundle{
		Schema:       1,
		InstallID:    r.opts.InstallID,
		BXVersion:    r.opts.Version,
		OS:           r.opts.OS,
		Arch:         r.opts.Arch,
		MacOSVersion: r.opts.OSVersion,
		OccurredAt:   now.Format(time.RFC3339),
		Signature:    signature,
		Failure:      failure,
	}
	r.mu.Lock()
	b.Protection = append([]report.Transition(nil), r.recent...)
	r.mu.Unlock()
	if r.opts.Doctor != nil {
		ctx, cancel := context.WithTimeout(context.Background(), reportDoctorBudget)
		rep := r.opts.Doctor(ctx)
		cancel()
		for _, c := range rep.Checks {
			b.Doctor = append(b.Doctor, report.Check{Name: c.Name, Status: c.Status, Detail: c.Detail, Hint: c.Hint})
		}
	}
	if r.opts.LogTail != nil {
		b.LogTail = r.opts.LogTail(reportLogTailLines)
	}
	name, err := r.store.Put(report.Redact(b), now)
	if err != nil {
		log.Printf("guardian_report_store_failed signature=%s err=%v", signature, err)
		return
	}
	log.Printf("guardian_report_stored signature=%s file=%s", signature, name)
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// Run 是发送循环:每 5 分钟(或被 Record 踢一下)看一眼队列;失败按退避,封顶 1 小时。
func (r *Reporter) Run(ctx context.Context) {
	if r == nil || !r.opts.Enabled {
		return
	}
	backoff := reportFlushInterval
	timer := time.NewTimer(backoff)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.kick:
		case <-timer.C:
		}
		if r.flush(ctx) {
			backoff = reportFlushInterval
		} else if backoff < reportMaxBackoff {
			backoff *= 2
		}
		timer.Reset(backoff)
	}
}

// flush 发队列里每一份待发的报告。返回 false 表示这一轮有没送出去的(退避)。
// **保护没开就一份都不发**。
func (r *Reporter) flush(ctx context.Context) bool {
	if r.opts.Protected == nil || !r.opts.Protected() {
		return true
	}
	pending, err := r.store.Pending()
	if err != nil || len(pending) == 0 {
		return true
	}
	clean := true
	for _, e := range pending {
		raw, err := r.store.Read(e.Name)
		if err != nil {
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.opts.Endpoint, bytes.NewReader(raw))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "bx-guardian/"+r.opts.Version)
		resp, err := r.client.Do(req)
		if err != nil {
			log.Printf("guardian_report_send_failed file=%s err=%v", e.Name, err)
			clean = false
			continue
		}
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusAccepted:
			_ = r.store.MarkSent(e.Name)
			log.Printf("guardian_report_sent file=%s", e.Name)
		case resp.StatusCode >= 400 && resp.StatusCode < 500:
			// 收集端拒收(形状不对、限频):重试没有意义,留档但不再发。
			_ = r.store.MarkRejected(e.Name)
			log.Printf("guardian_report_rejected file=%s status=%d", e.Name, resp.StatusCode)
		default:
			log.Printf("guardian_report_send_failed file=%s status=%d", e.Name, resp.StatusCode)
			clean = false
		}
	}
	return clean
}

// loadOrCreateInstallID 读或生成 32 位 hex 的安装 ID(只用于把同一台机器的重复报告归到
// 一起,不带任何能认出人的东西)。读不动就生成新的,绝不返回空。
func loadOrCreateInstallID(path string) string {
	if raw, err := os.ReadFile(path); err == nil {
		s := string(bytes.TrimSpace(raw))
		if len(s) == 32 {
			return s
		}
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000000000000000000000000000"
	}
	id := hex.EncodeToString(b[:])
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte(id+"\n"), 0o644)
	return id
}

// reporterOptionsFor 把配置折成 ReporterOptions(纯函数,RunDaemon 唯一的组装点)。
// 目录、安装 ID、Doctor、LogTail、Protected 由 RunDaemon 补上后三样。
func reporterOptionsFor(cfg *config.Config, version string) ReporterOptions {
	opts := ReporterOptions{
		Enabled:   cfg.ReportsEnabled(),
		Endpoint:  cfg.ReportsEndpoint,
		Dir:       reportsDir,
		Version:   version,
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		OSVersion: osVersionString(),
	}
	if opts.Endpoint == "" {
		opts.Endpoint = DefaultReportsEndpoint
	}
	return opts
}

// osVersionString:macOS 上是 `sw_vers -productVersion` 的答案;别的平台留空(不猜)。
func osVersionString() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	out, err := exec.Command("/usr/bin/sw_vers", "-productVersion").Output()
	if err != nil {
		return ""
	}
	return string(bytes.TrimSpace(out))
}
