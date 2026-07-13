package tray

import "testing"

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
