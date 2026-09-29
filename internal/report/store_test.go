package report

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func bundle(sig string, at time.Time) Bundle {
	return Bundle{
		Schema: 1, InstallID: strings.Repeat("ab", 16), BXVersion: "v0.4.16", OS: "darwin",
		OccurredAt: at.UTC().Format(time.RFC3339), Signature: sig, Failure: Failure{Code: "x"},
	}
}

// 本地留档兼队列:落盘 0600、文件名带时刻与签名;Pending 只列没发过的;MarkSent / MarkRejected
// 改名不删(「万一用户事后检查」的落点);保留最近 N 份,多的按时间删最旧。
func TestStoreKeepsRedactedReportsAndTracksWhatWasSent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "reports")
	st := NewStore(dir, 3)
	t0 := time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)
	name, err := st.Put(bundle("attention:core_health_failed", t0), t0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(name, "20260929T010000Z-attention_core_health_failed") || !strings.HasSuffix(name, ".json") {
		t.Fatalf("file name = %q", name)
	}
	st0, err := os.Stat(filepath.Join(dir, name))
	if err != nil || st0.Mode().Perm() != 0o600 {
		t.Fatalf("report must be 0600, got %v %v", st0, err)
	}
	pending, err := st.Pending()
	if err != nil || len(pending) != 1 || pending[0].Name != name {
		t.Fatalf("pending = %v err %v", pending, err)
	}
	if err := st.MarkSent(name); err != nil {
		t.Fatal(err)
	}
	if pending, _ := st.Pending(); len(pending) != 0 {
		t.Fatalf("sent reports must leave the queue, got %v", pending)
	}
	all, err := st.List()
	if err != nil || len(all) != 1 || all[0].State != "sent" {
		t.Fatalf("List must still show it as sent, got %v err %v", all, err)
	}
	// 保留 3 份:再放 4 份,最旧的那份(已发的那份)被删。
	for i := 1; i <= 4; i++ {
		if _, err := st.Put(bundle("recovery:x:y", t0.Add(time.Duration(i)*time.Minute)), t0.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	all, _ = st.List()
	if len(all) != 3 {
		t.Fatalf("retention must cap at 3, got %d", len(all))
	}
	for _, r := range all {
		if r.State == "sent" {
			t.Fatal("the oldest (sent) report must be the one evicted")
		}
	}
}

// 限频判据是纯函数:同签名 6 小时内只报一次,每天最多 10 份;记账文件在同一目录。
func TestStoreRateLimitsBySignatureAndPerDay(t *testing.T) {
	st := NewStore(filepath.Join(t.TempDir(), "reports"), 50)
	t0 := time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)
	if !st.Allow("attention:a", t0) {
		t.Fatal("first report of a signature must be allowed")
	}
	if st.Allow("attention:a", t0.Add(5*time.Hour)) {
		t.Fatal("same signature within 6h must be suppressed")
	}
	if !st.Allow("attention:a", t0.Add(7*time.Hour)) {
		t.Fatal("same signature after 6h is allowed again")
	}
	n := 0
	for i := 0; i < 20; i++ {
		if st.Allow("sig"+string(rune('a'+i)), t0.Add(time.Duration(i)*time.Minute)) {
			n++
		}
	}
	if n != 8 { // 已用 2(a 两次),一天封顶 10
		t.Fatalf("per-day cap: expected 8 more allowed, got %d", n)
	}
	if !st.Allow("late", t0.Add(25*time.Hour)) {
		t.Fatal("a new day resets the cap")
	}
}
