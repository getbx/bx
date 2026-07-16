package toolkeys

import "testing"

func TestCanonicalOrigin(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"https://API.Example.com", "https://api.example.com", true},
		{"https://api.example.com:443", "https://api.example.com", true},
		{"https://api.example.com:8443", "https://api.example.com:8443", true},
		{"https://b\u00fccher.example", "https://xn--bcher-kva.example", true},
		{"http://api.example.com", "", false},
		{"https://api.example.com/v1", "", false},
		{"https://user@api.example.com", "", false},
		{"https://127.0.0.1", "", false},
		{"https://localhost", "", false},
		{"https://*.example.com", "", false},
	}
	for _, tt := range tests {
		got, err := CanonicalOrigin(tt.in)
		if tt.ok && (err != nil || got != tt.want) {
			t.Fatalf("CanonicalOrigin(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
		if !tt.ok && err == nil {
			t.Fatalf("CanonicalOrigin(%q) succeeded: %q", tt.in, got)
		}
	}
}

func TestAuthHintValidate(t *testing.T) {
	good := []AuthHint{{Type: AuthBearer}, {Type: AuthHeader, Name: "X-API-Key"}, {Type: AuthQuery, Name: "api_key"}}
	for _, hint := range good {
		if err := hint.Validate(); err != nil {
			t.Fatalf("%+v: %v", hint, err)
		}
	}
	bad := []AuthHint{
		{Type: "raw", Name: "Authorization: Bearer {secret}"},
		{Type: AuthHeader, Name: "Host"},
		{Type: AuthHeader, Name: "X-Key\r\nInjected"},
		{Type: AuthQuery, Name: ""},
	}
	for _, hint := range bad {
		if err := hint.Validate(); err == nil {
			t.Fatalf("%+v accepted", hint)
		}
	}
}
