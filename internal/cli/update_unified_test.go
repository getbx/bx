package cli

import (
	"regexp"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/guardian"
	"github.com/getbx/bx/internal/release"
	updatepkg "github.com/getbx/bx/internal/update"
)

// 与 guardian.updateTransactionIDPattern(未导出)保持一致的镜像正则,供纯函数测试
// 独立校验 newUpdateTransactionID 的输出格式,不依赖 guardian 内部符号。
var mirrorUpdateTransactionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func TestNewUpdateTransactionID(t *testing.T) {
	id := newUpdateTransactionID(1700000000, []byte{0xde, 0xad, 0xbe, 0xef})
	if !mirrorUpdateTransactionIDPattern.MatchString(id) {
		t.Fatalf("newUpdateTransactionID = %q, does not match transaction id pattern", id)
	}
	if !strings.HasPrefix(id, "update-1700000000-") {
		t.Fatalf("newUpdateTransactionID = %q, want update-<unix>-<hex> shape", id)
	}
	if got := newUpdateTransactionID(1700000000, []byte{0xde, 0xad, 0xbe, 0xef}); got != id {
		t.Fatalf("同输入应确定性:第一次 %q,第二次 %q", id, got)
	}
	if got := newUpdateTransactionID(1700000001, []byte{0xde, 0xad, 0xbe, 0xef}); got == id {
		t.Fatalf("不同 now 应产生不同 id,均得 %q", got)
	}
	if got := newUpdateTransactionID(1700000000, []byte{0x01, 0x02, 0x03, 0x04}); got == id {
		t.Fatalf("不同随机字节应产生不同 id,均得 %q", got)
	}
}

func TestDecideUnifiedUpdateRoute(t *testing.T) {
	cases := []struct {
		name           string
		status         guardian.Status
		statusErr      error
		guardianLoaded bool
		wantRoute      string
		wantErrHas     string
	}{
		{
			name:      "protected routes to guardian",
			status:    guardian.Status{Protection: guardian.ProtectionProtected},
			wantRoute: "guardian",
		},
		{
			name:      "off routes direct",
			status:    guardian.Status{Protection: guardian.ProtectionOff},
			wantRoute: "direct",
		},
		{
			name:       "starting asks to retry later",
			status:     guardian.Status{Protection: guardian.ProtectionStarting},
			wantErrHas: "稍后",
		},
		{
			name:       "recovering asks to retry later",
			status:     guardian.Status{Protection: guardian.ProtectionRecovering},
			wantErrHas: "稍后",
		},
		{
			name:       "blocked points at doctor",
			status:     guardian.Status{Protection: guardian.ProtectionBlocked},
			wantErrHas: "doctor",
		},
		{
			name:       "needs_attention points at doctor",
			status:     guardian.Status{Protection: guardian.ProtectionNeedsAttention},
			wantErrHas: "doctor",
		},
		{
			name:           "status error with guardian loaded is ambiguous",
			statusErr:      errUnavailable,
			guardianLoaded: true,
			wantErrHas:     "doctor",
		},
		{
			name:           "status error without guardian loaded falls back direct",
			statusErr:      errUnavailable,
			guardianLoaded: false,
			wantRoute:      "direct",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			route, err := decideUnifiedUpdateRoute(tc.status, tc.statusErr, tc.guardianLoaded)
			if tc.wantErrHas != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrHas) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErrHas)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if route != tc.wantRoute {
				t.Fatalf("route = %q, want %q", route, tc.wantRoute)
			}
		})
	}
}

var errUnavailable = &guardian.UnavailableError{}

func TestBuildGuardianUpdateRequest(t *testing.T) {
	pkg := updatepkg.MacOSPackage{
		Release: release.Info{Version: "v0.3.0"},
	}
	req := buildGuardianUpdateRequest("update-1700000000-deadbeef", "v0.2.9", pkg, strings.Repeat("ab", 32), "/var/lib/bx/update/staging/update-1700000000-deadbeef/package.tgz")

	normalized, err := guardian.ValidateUpdateRequest(req)
	if err != nil {
		t.Fatalf("built request must pass ValidateUpdateRequest: %v", err)
	}
	if normalized.TransactionID != "update-1700000000-deadbeef" {
		t.Fatalf("transaction id = %q", normalized.TransactionID)
	}
	if normalized.FromVersion != "v0.2.9" {
		t.Fatalf("from version = %q", normalized.FromVersion)
	}
	if normalized.ToVersion != "v0.3.0" {
		t.Fatalf("to version = %q", normalized.ToVersion)
	}
	if normalized.AssetSHA256 != strings.Repeat("ab", 32) {
		t.Fatalf("asset sha256 = %q", normalized.AssetSHA256)
	}
	if normalized.PackagePath != "/var/lib/bx/update/staging/update-1700000000-deadbeef/package.tgz" {
		t.Fatalf("package path = %q", normalized.PackagePath)
	}
	if req.AppPath != "" {
		t.Fatalf("AppPath must stay empty so the server defaults to /Applications/Bx.app, got %q", req.AppPath)
	}
}
