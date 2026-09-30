package cli

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/getbx/bx/internal/install"
	"github.com/getbx/bx/internal/policysync"
	"github.com/getbx/bx/internal/syncstore"
)

// 规则同步的服务端一半(设计 docs/superpowers/specs/2026-09-29-policy-sync-via-own-server-design.md)。
// sync-store 只听回环、只存它解不开的密文;enable-sync 把它装成服务,并刷新服务端路由,
// 让「经隧道去 127.0.0.1:<StorePort>」成为唯一放行的回环目的地。

const syncStoreDir = "/var/lib/bx/sync"

func serverSyncStoreCommand() *cli.Command {
	return &cli.Command{
		Name:   "sync-store",
		Usage:  "run the loopback-only store that keeps your encrypted rules for your other devices (started by bx server enable-sync)",
		Hidden: true,
		Action: func(*cli.Context) error {
			srv := &http.Server{
				Addr:              policysync.StoreAddr,
				Handler:           syncstore.Handler(syncStoreDir),
				ReadHeaderTimeout: 10 * time.Second,
				ReadTimeout:       30 * time.Second,
			}
			fmt.Fprintf(os.Stderr, "bx sync store listening on %s (loopback only), data in %s\n", policysync.StoreAddr, syncStoreDir)
			return srv.ListenAndServe()
		},
	}
}

func serverEnableSyncCommand() *cli.Command {
	return &cli.Command{
		Name:  "enable-sync",
		Usage: "let your Mac and iPhone sync their rules through this server (encrypted; nothing new is opened to the Internet)",
		Description: "Installs a small store that listens on this server's loopback address only and keeps one encrypted\n" +
			"blob per link. Devices reach it through their own tunnel; the server cannot read what it keeps.\n" +
			"Also refreshes the server's routing so that store is the only loopback destination the tunnel may reach.",
		Action: func(*cli.Context) error {
			bin, err := os.Executable()
			if err != nil {
				return err
			}
			msg, err := enableSync(
				func() (string, error) { return hardenServerConfig(serverSingboxPath, systemServerHost{}) },
				func() error {
					if err := install.WriteSyncStoreUnit(bin + " server sync-store"); err != nil {
						return err
					}
					return install.EnableSyncStore()
				},
			)
			if msg != "" {
				fmt.Println(msg)
			}
			return err
		},
	}
}

// enableSync 先刷新服务端路由(它会先查、重启后确认、失败放回原配置),再装存储。
// 顺序是承重的:路由那步拒绝时它说「什么都没改」,那句话必须是真的 —— 先装存储就不是了。
// 路由先放行 127.0.0.1:51781 而存储还没起,只是一个暂时连不上的端口,不暴露任何东西。
func enableSync(hardenRoute func() (string, error), installStore func() error) (string, error) {
	msg, err := hardenRoute()
	if err != nil {
		return "", err
	}
	if err := installStore(); err != nil {
		return msg, fmt.Errorf("the server's routing is ready, but the sync store did not start: %w", err)
	}
	return msg + "\n" + fmt.Sprintf("✓ Rule sync is on: the store listens on %s and is reachable only through your tunnel.", policysync.StoreAddr), nil
}
