// Package bxkit is the part of bx that runs inside the iPhone app (bound with gomobile).
// It carries bx's judgments, not its plumbing: the phone's data plane is libbox, and what
// bx adds there is the answer to "where does this destination go, and why" — computed by
// the same router builder and worded by the same table as `bx explain` on the Mac.
//
// Only strings cross the boundary (gomobile). Design: docs/superpowers/specs/2026-09-17-mobile-client-design.md §8 ③.
package bxkit

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/explainwords"
	"github.com/getbx/bx/internal/route"
	"github.com/getbx/bx/internal/routerbuild"
)

// policy is the phone's copy of the routing intent: no server link, no egress.
type policy struct {
	Global bool     `json:"global"`
	Direct []string `json:"direct"`
	Proxy  []string `json:"proxy"`
}

type answer struct {
	Target  string `json:"target"`
	Kind    string `json:"kind"`    // domain | ip
	Verdict string `json:"verdict"` // tunnel | direct | blocked
	Because string `json:"because"`
	Rule    string `json:"rule,omitempty"`
}

// Explain answers for one target. policyJSON is {"global":bool,"direct":[…],"proxy":[…]};
// chinaDomain/chinaCIDR are the list files' text. Returns the answer as JSON.
func Explain(policyJSON, chinaDomain, chinaCIDR, target string) (string, error) {
	var p policy
	if err := json.Unmarshal([]byte(policyJSON), &p); err != nil {
		return "", fmt.Errorf("policy: %w", err)
	}
	host, err := normalizeTarget(target)
	if err != nil {
		return "", err
	}
	cfg := &config.Config{Global: p.Global, Rules: []config.Rule{{Direct: p.Direct, Proxy: p.Proxy}}}
	router, err := routerbuild.Build(cfg, strings.Split(chinaDomain, "\n"), strings.Split(chinaCIDR, "\n"))
	if err != nil {
		return "", err
	}
	router.GlobalProxy = p.Global

	a := answer{Target: host}
	var dec route.Decision
	var why route.Reason
	if ip, err := netip.ParseAddr(host); err == nil {
		a.Kind = "ip"
		if ip.Is6() && !ip.Is4In6() {
			// The phone rejects every IPv6 destination before any rule (mobileconfig), as the
			// desktop blackholes v6. Saying "tunnel" here would describe a path that does not exist.
			a.Verdict = "blocked"
			a.Because = "IPv6 is blocked on this phone (bx sends apps to IPv4)"
			return marshal(a)
		}
		dec, why = router.ExplainIP(ip)
	} else {
		a.Kind = "domain"
		dec, why = router.Explain(route.Meta{Domain: host})
	}
	switch dec {
	case route.Direct:
		a.Verdict = "direct"
	case route.Proxy:
		a.Verdict = "tunnel"
	default:
		return "", fmt.Errorf("unexpected decision %s", dec)
	}
	a.Because = explainwords.SourceLabel(why.Source.String())
	a.Rule = why.Rule
	return marshal(a)
}

// normalizeTarget accepts what a person pastes: a host, an address, or a URL.
func normalizeTarget(target string) (string, error) {
	t := strings.TrimSpace(target)
	if t == "" {
		return "", errors.New("empty target")
	}
	if strings.Contains(t, "://") {
		u, err := url.Parse(t)
		if err != nil || u.Hostname() == "" {
			return "", fmt.Errorf("cannot read a host from %q", target)
		}
		t = u.Hostname()
	}
	return strings.ToLower(strings.TrimSuffix(t, ".")), nil
}

func marshal(a answer) (string, error) {
	b, err := json.Marshal(a)
	return string(b), err
}
