package srvgen_test

import (
	"reflect"
	"testing"

	"github.com/getbx/bx/internal/srvgen"
)

// 重启前后要核的端口,从服务端配置里来:reality/trojan 等走 TCP,hysteria2 走 UDP。
func TestListenersComeFromTheServerConfig(t *testing.T) {
	rp := srvgen.RealityParams{UUID: "u", Port: 8444, SNI: "s", PrivateKey: "k", ShortID: "a"}
	hp := srvgen.HysteriaParams{Port: 443, Password: "p"}
	cfg, err := srvgen.CombinedServerConfig(rp, hp)
	if err != nil {
		t.Fatal(err)
	}
	got, err := srvgen.Listeners(cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := []srvgen.Listener{{Proto: "tcp", Port: 8444}, {Proto: "udp", Port: 443}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Listeners = %+v, want %+v", got, want)
	}
	if _, err := srvgen.Listeners([]byte(`{"inbounds":[]}`)); err == nil {
		t.Fatal("a config with no inbounds must be an error: there would be nothing to verify after a restart")
	}
}
