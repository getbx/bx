package pfreset

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `pfctl -E` 的输出形如:
//
//	pf enabled
//	Token : 1234567890
//
// 没有 Token 那一行就不是成功,不许拿 0 去 -X(那会释放别人的引用)。
func TestParseEnableTokenReadsTheTokenLineOnly(t *testing.T) {
	tok, err := ParseEnableToken("pf enabled\nToken : 1234567890\n")
	if err != nil || tok != "1234567890" {
		t.Fatalf("token = %q err = %v", tok, err)
	}
	if _, err := ParseEnableToken("pf enabled\n"); err == nil {
		t.Fatal("no Token line must be an error")
	}
	if _, err := ParseEnableToken(""); err == nil {
		t.Fatal("empty output must be an error")
	}
}

// 残留:anchor 里有规则 / token 文件还在 ⇒ 冲掉 + 释放 + 删文件;都没有 ⇒ 一个破坏性的
// pfctl 都不调。
func TestFlushStaleClearsALeftoverAnchorAndToken(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "pf.token")
	if err := os.WriteFile(tokenPath, []byte("42\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls []string
	run := func(_ context.Context, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		calls = append(calls, joined)
		if strings.HasSuffix(joined, "-s rules") {
			return "block return-rst out quick on en0 ...\n", nil
		}
		return "", nil
	}
	flushed, err := FlushStale(context.Background(), tokenPath, run)
	if err != nil || !flushed {
		t.Fatalf("flushed = %v err = %v", flushed, err)
	}
	joined := strings.Join(calls, "|")
	for _, want := range []string{"-a " + Anchor + " -s rules", "-a " + Anchor + " -F all", "-X 42"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("calls %v miss %q", calls, want)
		}
	}
	if _, err := os.Stat(tokenPath); !os.IsNotExist(err) {
		t.Fatal("the token file must be gone after release")
	}

	calls = nil
	run2 := func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		return "", nil
	}
	flushed, err = FlushStale(context.Background(), tokenPath, run2)
	if err != nil || flushed {
		t.Fatalf("a clean machine: flushed = %v err = %v", flushed, err)
	}
	for _, c := range calls {
		if strings.Contains(c, "-F") || strings.Contains(c, "-X") {
			t.Fatalf("nothing to flush must mean no destructive pfctl call, got %v", calls)
		}
	}
}
