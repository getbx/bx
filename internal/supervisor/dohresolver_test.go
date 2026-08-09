package supervisor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestParseDoHJSON(t *testing.T) {
	// 正常 A 记录(含一条 CNAME type=5 应跳过,取 A)
	a := `{"Status":0,"Answer":[{"type":5,"data":"cname.x."},{"type":1,"data":"1.2.3.4"}]}`
	ip, err := parseDoHJSON([]byte(a))
	if err != nil || ip.String() != "1.2.3.4" {
		t.Fatalf("A 记录解析 = %v, %v; want 1.2.3.4", ip, err)
	}
	// AAAA
	aaaa := `{"Status":0,"Answer":[{"type":28,"data":"2400:3200::1"}]}`
	if ip, err := parseDoHJSON([]byte(aaaa)); err != nil || ip.String() != "2400:3200::1" {
		t.Fatalf("AAAA 解析 = %v, %v", ip, err)
	}
	// NXDOMAIN(Status!=0)应报错
	if _, err := parseDoHJSON([]byte(`{"Status":3,"Answer":[]}`)); err == nil {
		t.Fatal("Status=3(NXDOMAIN)应报错")
	}
	// 无 A/AAAA 记录应报错
	if _, err := parseDoHJSON([]byte(`{"Status":0,"Answer":[{"type":5,"data":"x."}]}`)); err == nil {
		t.Fatal("无 A/AAAA 应报错")
	}
	// 坏 JSON
	if _, err := parseDoHJSON([]byte(`not json`)); err == nil {
		t.Fatal("坏 JSON 应报错")
	}
}

func TestDoHResolverResolvesOverHTTP(t *testing.T) {
	var gotName, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotName = r.URL.Query().Get("name")
		gotAccept = r.Header.Get("Accept")
		_, _ = w.Write([]byte(`{"Status":0,"Answer":[{"type":1,"data":"203.0.113.7"}]}`))
	}))
	defer srv.Close()

	r := &dohResolver{url: srv.URL + "/resolve", client: srv.Client()}
	ip, err := r.Resolve(context.Background(), "www.baidu.com")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ip.String() != "203.0.113.7" {
		t.Fatalf("ip = %s, want 203.0.113.7", ip)
	}
	if gotName != "www.baidu.com" {
		t.Fatalf("DoH 请求 name=%q", gotName)
	}
	if !strings.Contains(gotAccept, "dns-json") {
		t.Fatalf("应带 Accept: application/dns-json, got %q", gotAccept)
	}
}

func TestNewResolverDispatchesScheme(t *testing.T) {
	// https:// → DoH;裸 IP → 明文 UDP(向后兼容)
	if _, ok := newResolver("https://223.5.5.5/resolve", nil).(*dohResolver); !ok {
		t.Fatal("https:// 应派发到 dohResolver")
	}
	if _, ok := newResolver("223.5.5.5", nil).(*dnsResolver); !ok {
		t.Fatal("裸 IP 应派发到 dnsResolver(明文 UDP)")
	}
}

// DoH 此前只取首条记录。服务器域名有多条 A 记录时,bypass 只覆盖其中一条,
// 而隧道子进程可能连到没被覆盖的那条 —— 成环。
func TestParseDoHJSONAllReturnsEveryAddress(t *testing.T) {
	body := []byte(`{"Status":0,"Answer":[
		{"type":1,"data":"1.1.1.1"},
		{"type":5,"data":"cname.example."},
		{"type":1,"data":"2.2.2.2"}]}`)
	got, err := parseDoHJSONAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != netip.MustParseAddr("1.1.1.1") || got[1] != netip.MustParseAddr("2.2.2.2") {
		t.Fatalf("多条 A 记录必须全部返回(只取首条会让 bypass 漏掉其余 = 成环), got %v", got)
	}
}

func TestParseDoHJSONAllRejectsFailedStatus(t *testing.T) {
	if _, err := parseDoHJSONAll([]byte(`{"Status":3}`)); err == nil {
		t.Fatal("NXDOMAIN 必须报错")
	}
}

func TestParseDoHJSONAllRejectsAnswerWithoutAddresses(t *testing.T) {
	if _, err := parseDoHJSONAll([]byte(`{"Status":0,"Answer":[{"type":5,"data":"x."}]}`)); err == nil {
		t.Fatal("没有 A/AAAA 必须报错")
	}
}

// DoH 的查询串写死 type=A,只会拿到 A 记录。ResolveAll 必须问同一条路,
// 且把拿到的全部交出去。
func TestDoHResolverResolveAllReturnsEveryAddress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/dns-json")
		_, _ = w.Write([]byte(`{"Status":0,"Answer":[{"type":1,"data":"1.1.1.1"},{"type":1,"data":"2.2.2.2"}]}`))
	}))
	defer srv.Close()
	r := newDoHResolver(srv.URL, nil)

	got, err := r.ResolveAll(context.Background(), "h.example")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("ResolveAll 必须给出全部地址, got %v", got)
	}
	// Resolve 的既有语义(首条)不变。
	one, err := r.Resolve(context.Background(), "h.example")
	if err != nil || one != netip.MustParseAddr("1.1.1.1") {
		t.Fatalf("Resolve 仍取首条, got %v err=%v", one, err)
	}
}
