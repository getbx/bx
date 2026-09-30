package srvgen

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Listener is one port bx's sing-box must own on the server.
type Listener struct {
	Proto string // tcp | udp
	Port  int
}

// Listeners lists the ports a server config's inbounds listen on — what `bx server harden` and
// `enable-sync` check is free before a restart and owned by bx after it (2026-09-30: a leftover
// hand-made sing-box grabbed the port in the restart window and bx-server crash-looped while
// the command had already printed success).
func Listeners(configBytes []byte) ([]Listener, error) {
	var cfg struct {
		Inbounds []struct {
			Type       string `json:"type"`
			ListenPort int    `json:"listen_port"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(configBytes, &cfg); err != nil {
		return nil, err
	}
	var out []Listener
	for _, in := range cfg.Inbounds {
		if in.ListenPort == 0 {
			continue
		}
		proto := "tcp"
		switch in.Type {
		case "hysteria2", "hysteria", "tuic":
			proto = "udp"
		}
		out = append(out, Listener{Proto: proto, Port: in.ListenPort})
	}
	if len(out) == 0 {
		return nil, errors.New("the server config has no listening inbounds")
	}
	return out, nil
}

func (l Listener) String() string { return fmt.Sprintf("%s/%d", l.Proto, l.Port) }
