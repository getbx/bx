package bxkit

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/getbx/bx/internal/blink"
	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/mobileconfig"
	"github.com/getbx/bx/internal/singboxout"
	"github.com/getbx/bx/internal/singboxrules"
)

type configured struct {
	Config     string            `json:"config"`
	RuleSets   map[string]string `json:"rule_sets"`
	ServerHost string            `json:"server_host"`
}

// Configure turns a pasted server link (bare vless:// or a bx:// envelope, possibly a bundle)
// into the complete libbox configuration the Packet Tunnel extension runs, plus the rule-set
// files it references. Routing is the desktop's `bx setup` default: split (China direct,
// everything else through the tunnel), from the lists the app bundles.
//
// The link is a credential: it goes into the returned config and nowhere else; the caller
// stores it (Keychain) and the config (app group) itself.
func Configure(link, chinaDomain, chinaCIDR string) (string, error) {
	links, err := candidateLinks(strings.TrimSpace(link))
	if err != nil {
		return "", err
	}
	var proxy map[string]any
	var firstErr error
	for _, l := range links {
		out, err := singboxout.Outbound(l, singboxrules.OutboundProxy)
		if err == nil {
			proxy = out
			break
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	if proxy == nil {
		return "", firstErr
	}
	cfg := &config.Config{} // the desktop default: no user rules, not global
	files, err := mobileconfig.Build(cfg, singboxrules.Lists{
		ChinaDomain: strings.Split(chinaDomain, "\n"),
		ChinaCIDR:   strings.Split(chinaCIDR, "\n"),
	}, proxy)
	if err != nil {
		return "", err
	}
	c := configured{Config: string(files.Config), RuleSets: map[string]string{}, ServerHost: fmt.Sprint(proxy["server"])}
	for name, body := range files.RuleSets {
		c.RuleSets[name] = string(body)
	}
	b, err := json.Marshal(c)
	return string(b), err
}

// DefaultPolicy is the policy Explain uses on a phone configured by Configure: the same
// routing intent, so the two can never disagree.
func DefaultPolicy() string {
	return `{"global":false,"direct":[],"proxy":[]}`
}

func candidateLinks(link string) ([]string, error) {
	if link == "" {
		return nil, errors.New("empty link")
	}
	if strings.HasPrefix(link, "bx://") || strings.HasPrefix(link, "blink://") {
		return blink.DecodeAll(link)
	}
	return []string{link}, nil
}
