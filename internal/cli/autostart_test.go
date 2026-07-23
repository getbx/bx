package cli

import "testing"

func TestParseAutostartArg(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }
	cases := []struct {
		arg     string
		want    *bool
		status  bool
		wantErr bool
	}{
		{"on", boolPtr(true), false, false},
		{"off", boolPtr(false), false, false},
		{"status", nil, true, false},
		{"", nil, true, false},
		{"maybe", nil, false, true},
	}
	for _, c := range cases {
		t.Run(c.arg, func(t *testing.T) {
			want, status, err := parseAutostartArg(c.arg)
			if (err != nil) != c.wantErr {
				t.Fatalf("arg %q: err=%v wantErr=%v", c.arg, err, c.wantErr)
			}
			if c.wantErr {
				return
			}
			if status != c.status {
				t.Fatalf("arg %q: status=%v want %v", c.arg, status, c.status)
			}
			if (want == nil) != (c.want == nil) || (want != nil && *want != *c.want) {
				t.Fatalf("arg %q: want=%v expected %v", c.arg, want, c.want)
			}
		})
	}
}
