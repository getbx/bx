package toolkeys

import (
	"bytes"
	"testing"
)

func TestRedactResponseRemovesExactSecretsAndSensitiveJSON(t *testing.T) {
	input := []byte(`{"ok":true,"access_token":"new-token","nested":{"api_key":"key-value"},"note":"stored-secret"}`)
	got, err := RedactResponse(input, "application/json", []string{"stored-secret"})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"stored-secret", "new-token", "key-value"} {
		if bytes.Contains(got, []byte(secret)) {
			t.Fatalf("response leaked %q: %s", secret, got)
		}
	}
	if !bytes.Contains(got, []byte("redacted")) {
		t.Fatalf("missing redaction: %s", got)
	}
}

func TestRedactResponseRedactsTextWithoutParsing(t *testing.T) {
	got, err := RedactResponse([]byte("error: stored-secret"), "text/plain", []string{"stored-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "error: <redacted>" {
		t.Fatalf("got %q", got)
	}
}
