// Package policy applies the narrow, safety-reviewed domain policy surface.
package policy

import (
	"fmt"
	"strings"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/route"
	"gopkg.in/yaml.v3"
)

var riskyDirect = route.NewDomainSet([]string{
	"aliyuncs.com", "myqcloud.com", "bcebos.com", "qiniucdn.com", "qbox.me", "clouddn.com", "upaiyun.com", "myhuaweicloud.com",
	"amazonaws.com", "cloudfront.net", "core.windows.net", "googleapis.com", "r2.dev", "workers.dev", "pages.dev", "github.io", "vercel.app", "netlify.app", "b-cdn.net",
})

type Request struct {
	Mode      string
	Add       []string
	Remove    []string
	AllowRisk bool
}

func norm(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// DirectRisk reports whether a direct rule would cover a public cloud or
// open-subdomain platform that an unrelated party could use for de-anonymizing
// traffic.
func DirectRisk(domain string) bool { return riskyDirect.Match(norm(domain)) }

func mapping(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func removeMapping(n *yaml.Node, key string) bool {
	if n == nil || n.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			n.Content = append(n.Content[:i], n.Content[i+2:]...)
			return true
		}
	}
	return false
}

// Apply validates and edits rules without rewriting unrelated YAML. Adding one
// mode removes the same domains from the opposite mode, preventing ambiguity.
func Apply(in []byte, req Request) ([]byte, bool, error) {
	return apply(in, req, true)
}

// Edit provides the same canonical YAML edit semantics to trusted local CLI
// callers that already validate their surrounding configuration and risk gate.
func Edit(in []byte, req Request) ([]byte, bool, error) {
	return apply(in, req, false)
}

func apply(in []byte, req Request, validateConfig bool) ([]byte, bool, error) {
	if req.Mode != "direct" && req.Mode != "proxy" {
		return nil, false, fmt.Errorf("mode must be direct or proxy")
	}
	if len(req.Add) == 0 && len(req.Remove) == 0 {
		return nil, false, fmt.Errorf("add or remove is required")
	}
	if validateConfig {
		if _, err := config.Parse(in); err != nil {
			return nil, false, err
		}
	}
	for _, d := range req.Add {
		if norm(d) == "" {
			return nil, false, fmt.Errorf("domain is empty")
		}
		if req.Mode == "direct" && riskyDirect.Match(norm(d)) && !req.AllowRisk {
			return nil, false, fmt.Errorf("direct policy for %q is risky; require allow_risk", d)
		}
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(in, &doc); err != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, false, fmt.Errorf("invalid YAML config")
	}
	root := doc.Content[0]
	rules := mapping(root, "rules")
	if rules == nil {
		rules = &yaml.Node{Kind: yaml.SequenceNode}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "rules"}, rules)
	}
	if rules.Kind != yaml.SequenceNode {
		return nil, false, fmt.Errorf("rules must be a sequence")
	}
	opp := "direct"
	if req.Mode == "direct" {
		opp = "proxy"
	}
	remove := map[string]bool{}
	for _, d := range req.Remove {
		remove[norm(d)] = true
	}
	add := map[string]bool{}
	for _, d := range req.Add {
		add[norm(d)] = true
	}
	changed := false
	removeFrom := func(elem *yaml.Node, field string, want map[string]bool) {
		seq := mapping(elem, field)
		if seq == nil || seq.Kind != yaml.SequenceNode {
			return
		}
		kept := seq.Content[:0]
		for _, item := range seq.Content {
			if want[norm(item.Value)] {
				changed = true
				continue
			}
			kept = append(kept, item)
		}
		seq.Content = kept
		if len(seq.Content) == 0 {
			removeMapping(elem, field)
		}
	}
	for _, elem := range rules.Content {
		if elem != nil && elem.Kind == yaml.MappingNode {
			removeFrom(elem, req.Mode, remove)
			removeFrom(elem, opp, add)
		}
	}
	keptRules := rules.Content[:0]
	for _, elem := range rules.Content {
		if elem != nil && elem.Kind == yaml.MappingNode && len(elem.Content) == 0 {
			continue
		}
		keptRules = append(keptRules, elem)
	}
	rules.Content = keptRules
	existing := map[string]bool{}
	for _, elem := range rules.Content {
		if seq := mapping(elem, req.Mode); seq != nil && seq.Kind == yaml.SequenceNode {
			for _, item := range seq.Content {
				existing[norm(item.Value)] = true
			}
		}
	}
	if len(req.Add) > 0 {
		if len(rules.Content) == 0 {
			rules.Content = append(rules.Content, &yaml.Node{Kind: yaml.MappingNode})
		}
		first := rules.Content[0]
		if first.Kind != yaml.MappingNode {
			return nil, false, fmt.Errorf("rules entries must be mappings")
		}
		seq := mapping(first, req.Mode)
		if seq == nil {
			seq = &yaml.Node{Kind: yaml.SequenceNode}
			first.Content = append(first.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: req.Mode}, seq)
		}
		if seq.Kind != yaml.SequenceNode {
			return nil, false, fmt.Errorf("%s must be a sequence", req.Mode)
		}
		for _, d := range req.Add {
			if !existing[norm(d)] {
				seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: strings.TrimSpace(d)})
				existing[norm(d)] = true
				changed = true
			}
		}
	}
	if !changed {
		return in, false, nil
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, false, err
	}
	if validateConfig {
		if _, err := config.Parse(out); err != nil {
			return nil, false, fmt.Errorf("edited config is invalid: %w", err)
		}
	}
	return out, true, nil
}
