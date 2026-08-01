package tray

import (
	"testing"
	"time"
)

func TestParseStatusJSON(t *testing.T) {
	// bx status --json 的关键字段(对齐 internal/stats/render.go 的 json tag:server/tunnel_healthy/latency_ms/transport)
	b := []byte(`{"server":"1.2.3.4","tunnel_healthy":true,"latency_ms":401,"transport":"reality@1.2.3.4"}`)
	d, ok := parseStatusJSON(b)
	if !ok {
		t.Fatal("应解析成功")
	}
	if !d.Healthy || d.LatencyMS != 401 || d.Server != "1.2.3.4" {
		t.Fatalf("字段错: %+v", d)
	}
}

func TestParseStatusJSONBad(t *testing.T) {
	if _, ok := parseStatusJSON([]byte("not json")); ok {
		t.Error("坏 JSON 应 ok=false")
	}
}

func TestParseUpdateCheckJSON(t *testing.T) {
	if avail, ok := parseUpdateCheckJSON([]byte(`{"current":"v0.2.7","latest":"v0.3.0","available":true,"verified":true}`)); !ok || !avail {
		t.Fatalf("有更新应 (true,true), got (%v,%v)", avail, ok)
	}
	if avail, ok := parseUpdateCheckJSON([]byte(`{"available":false,"verified":true}`)); !ok || avail {
		t.Fatalf("无更新应 (false,true), got (%v,%v)", avail, ok)
	}
	if _, ok := parseUpdateCheckJSON([]byte(`not json`)); ok {
		t.Fatal("坏 JSON 应 ok=false")
	}
}

func TestShouldCheckUpdate(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	interval := 6 * time.Hour
	if !shouldCheckUpdate(time.Time{}, now, interval) {
		t.Fatal("零值 lastChecked(从未查过)应 true")
	}
	if !shouldCheckUpdate(now.Add(-7*time.Hour), now, interval) {
		t.Fatal("超过间隔应 true")
	}
	if shouldCheckUpdate(now.Add(-1*time.Hour), now, interval) {
		t.Fatal("间隔内应 false")
	}
}
