package toolkeys

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"golang.org/x/net/idna"
)

func CanonicalOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("origin must be a bare HTTPS origin")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || host == "localhost" || strings.Contains(host, "*") || net.ParseIP(host) != nil {
		return "", fmt.Errorf("origin host is not allowed")
	}
	host, err = idna.Lookup.ToASCII(host)
	if err != nil {
		return "", fmt.Errorf("canonicalize origin host: %w", err)
	}
	port := u.Port()
	if port == "" || port == "443" {
		return "https://" + host, nil
	}
	return "https://" + net.JoinHostPort(host, port), nil
}
