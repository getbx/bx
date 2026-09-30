package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/urfave/cli/v2"

	"github.com/getbx/bx/internal/install"
	"github.com/getbx/bx/internal/srvgen"
)

// bx server harden:给已经装好的服务器补上 srvgen.HardenedRoute —— 不许经隧道去这台 VPS 的
// 回环、内网与云元数据地址。新装的服务器从一开始就带;这条给 2026-09-29 之前装的那些。
// 只动路由,钥匙与用户一个字节不碰;已经加固过时什么都不写、不重启。
func serverHardenCommand() *cli.Command {
	return &cli.Command{
		Name:   "harden",
		Usage:  "stop people who have your link from reaching this server's own local services and network",
		Action: serverHardenAction,
		Description: "Adds a rule to the server's sing-box config: connections through the tunnel may not go to this\n" +
			"server's loopback address, its private network, or the cloud metadata address (169.254.169.254).\n" +
			"Anyone holding a link to this server (including links made with `bx server share`) could reach those\n" +
			"before. Keys and users are not changed. Servers installed after this version already have the rule.",
	}
}

func serverHardenAction(_ *cli.Context) error {
	msg, err := hardenServerConfig(serverSingboxPath, install.RestartServer)
	if msg != "" {
		fmt.Println(msg)
	}
	return err
}

// hardenServerConfig is the testable half: read, patch, write only if changed, restart only if written.
func hardenServerConfig(path string, restart func() error) (string, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("no sing-box server config at %s: this command is for reality / hysteria2 servers installed with bx server install (a brook server is not covered)", path)
	}
	if err != nil {
		return "", err
	}
	patched, changed, err := srvgen.Harden(raw)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	if !changed {
		return "✓ This server is already hardened; nothing changed.", nil
	}
	if err := os.WriteFile(path, patched, 0o600); err != nil {
		return "", err
	}
	if err := restart(); err != nil {
		return "", fmt.Errorf("the rule was written to %s, but restarting the server failed (it takes effect on the next start): %w", path, err)
	}
	return "✓ Hardened: connections through the tunnel can no longer reach this server's local services, private network or cloud metadata address. Keys and users are unchanged.", nil
}
