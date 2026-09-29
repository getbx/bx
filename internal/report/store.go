package report

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Store 是本地留档兼发送队列:一份报告一个文件,root:wheel 0600(与日志同权限)。
//
//	<occurred_at>-<signature>.json           待发
//	<occurred_at>-<signature>.sent.json      已发(收集端回 202)
//	<occurred_at>-<signature>.rejected.json  收集端拒收(4xx),不再重试
//
// **发过的不删,改名**:「万一用户事后检查」的落点就是这个目录(bx reports 列它)。
// 保留最近 keep 份,多的按时间删最旧,不分状态。限频记账在同目录的 rate.json。
type Store struct {
	dir  string
	keep int
}

func NewStore(dir string, keep int) *Store {
	if keep <= 0 {
		keep = 50
	}
	return &Store{dir: dir, keep: keep}
}

// Entry 是目录里的一份报告。
type Entry struct {
	Name  string // 文件名
	Path  string
	State string // pending / sent / rejected
	At    time.Time
}

const (
	// 同一签名 6 小时内只报一次;每天最多 10 份。
	sameSignatureEvery = 6 * time.Hour
	perDay             = 10
)

// Put 把一份**已脱敏**的报告落盘,返回文件名。目录不存在就建(0700)。
func (s *Store) Put(b Bundle, now time.Time) (string, error) {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s-%s.json", now.UTC().Format("20060102T150405Z"), safeSignature(b.Signature))
	raw, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return "", err
	}
	tmp := filepath.Join(s.dir, "."+name+".tmp")
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, filepath.Join(s.dir, name)); err != nil {
		return "", err
	}
	return name, s.prune()
}

func safeSignature(sig string) string {
	var b strings.Builder
	for _, r := range sig {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

// List 按时间列出全部报告(旧在前)。
func (s *Store) List() ([]Entry, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []Entry
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") || strings.HasPrefix(name, ".") || name == "rate.json" {
			continue
		}
		state := "pending"
		switch {
		case strings.HasSuffix(name, ".sent.json"):
			state = "sent"
		case strings.HasSuffix(name, ".rejected.json"):
			state = "rejected"
		}
		at, _ := time.Parse("20060102T150405Z", strings.SplitN(name, "-", 2)[0])
		out = append(out, Entry{Name: name, Path: filepath.Join(s.dir, name), State: state, At: at})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Pending 只列还没发出去的。
func (s *Store) Pending() ([]Entry, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, e := range all {
		if e.State == "pending" {
			out = append(out, e)
		}
	}
	return out, nil
}

// Read 读一份报告的原文(已脱敏的 JSON)。
func (s *Store) Read(name string) ([]byte, error) {
	if strings.ContainsAny(name, "/\\") || strings.HasPrefix(name, ".") {
		return nil, errors.New("bad report name")
	}
	return os.ReadFile(filepath.Join(s.dir, name))
}

func (s *Store) MarkSent(name string) error     { return s.mark(name, ".sent.json") }
func (s *Store) MarkRejected(name string) error { return s.mark(name, ".rejected.json") }

func (s *Store) mark(name, suffix string) error {
	if !strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".sent.json") || strings.HasSuffix(name, ".rejected.json") {
		return fmt.Errorf("report %q is not pending", name)
	}
	return os.Rename(filepath.Join(s.dir, name), filepath.Join(s.dir, strings.TrimSuffix(name, ".json")+suffix))
}

// prune 保留最近 keep 份,多的删最旧(不分状态:一份两个月前发过的报告不比一份没发的更值得留)。
func (s *Store) prune() error {
	all, err := s.List()
	if err != nil {
		return err
	}
	for len(all) > s.keep {
		if err := os.Remove(all[0].Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		all = all[1:]
	}
	return nil
}

// rateBook 是限频记账:每个签名上次报的时刻,以及按天的计数。
type rateBook struct {
	LastBySignature map[string]time.Time `json:"last_by_signature"`
	Day             string               `json:"day"`
	Count           int                  `json:"count"`
}

func (s *Store) ratePath() string { return filepath.Join(s.dir, "rate.json") }

func (s *Store) loadRate() rateBook {
	var rb rateBook
	if raw, err := os.ReadFile(s.ratePath()); err == nil {
		_ = json.Unmarshal(raw, &rb)
	}
	if rb.LastBySignature == nil {
		rb.LastBySignature = map[string]time.Time{}
	}
	return rb
}

// Allow 回答「这个签名此刻能不能报」,并在能时记账:同签名 6 小时内只一次,每天最多 perDay 份。
// 记不下来(目录不可写)按不允许处理 —— 少报一份比刷屏安全。
func (s *Store) Allow(signature string, now time.Time) bool {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return false
	}
	rb := s.loadRate()
	day := now.UTC().Format("2006-01-02")
	if rb.Day != day {
		rb.Day, rb.Count = day, 0
	}
	if last, ok := rb.LastBySignature[signature]; ok && now.Sub(last) < sameSignatureEvery {
		return false
	}
	if rb.Count >= perDay {
		return false
	}
	rb.LastBySignature[signature] = now
	rb.Count++
	raw, err := json.Marshal(rb)
	if err != nil {
		return false
	}
	return os.WriteFile(s.ratePath(), raw, 0o600) == nil
}
