package toolkeys

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAuditRecordsMetadataOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	a, err := OpenAudit(path, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	err = a.Record(AuditEntry{Time: time.Now(), CredentialID: "cred", Label: "Example", Origin: "https://api.example.com", Method: "POST", Path: "/v1/run", Status: 200, Surface: "mcp"})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("info=%v err=%v", info, err)
	}
	if err := a.Record(AuditEntry{Time: time.Now(), Path: "/v1/run?secret=no"}); err == nil {
		t.Fatal("query path accepted")
	}
}
