package cli

import (
	"testing"
)

func TestServerHostFromLink(t *testing.T) {
	cases := []struct {
		name string
		link string
		want string
	}{
		{
			name: "vless reality link returns host",
			link: "vless://uid@203.0.113.10:443?security=reality&pbk=p&sid=s&sni=www.microsoft.com",
			want: "203.0.113.10",
		},
		{
			name: "brook link returns host from server param",
			link: "brook://server?server=203.0.113.10%3A9999&password=x",
			want: "203.0.113.10",
		},
		{
			name: "garbage string returns empty",
			link: "not-a-link",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := serverHostFromLink(tc.link)
			if got != tc.want {
				t.Errorf("serverHostFromLink(%q) = %q, want %q", tc.link, got, tc.want)
			}
		})
	}
}
