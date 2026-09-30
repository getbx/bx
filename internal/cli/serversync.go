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
			if err := install.WriteSyncStoreUnit(bin + " server sync-store"); err != nil {
				return err
			}
			if err := install.EnableSyncStore(); err != nil {
				return fmt.Errorf("the sync store unit is written but did not start: %w", err)
			}
			msg, err := hardenServerConfig(serverSingboxPath, install.RestartServer)
			if err != nil {
				return fmt.Errorf("the sync store is running, but refreshing the server's routing failed: %w", err)
			}
			fmt.Println(msg)
			fmt.Printf("✓ Rule sync is on: the store listens on %s and is reachable only through your tunnel.\n", policysync.StoreAddr)
			return nil
		},
	}
}
