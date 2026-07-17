package guardian

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/getbx/bx/internal/install"
	"github.com/getbx/bx/internal/version"
)

const SocketPath = "/var/run/bx-guard.sock"

type DaemonOptions struct {
	ConfigPath      string
	DNSListen       string
	SocketPath      string
	Handler         http.Handler
	OwnerUID        uint32
	PeerCredentials func(net.Conn) (uint32, bool)
}

type Daemon struct {
	path       string
	listener   net.Listener
	server     *http.Server
	socketInfo os.FileInfo
}

func StartDaemon(ctx context.Context, options DaemonOptions) (*Daemon, error) {
	path := options.SocketPath
	if path == "" {
		path = SocketPath
	}
	if !filepath.IsAbs(path) {
		return nil, errors.New("Guardian socket path must be absolute")
	}
	if options.Handler == nil {
		return nil, errors.New("Guardian HTTP handler required")
	}
	if err := prepareSocketDirectory(filepath.Dir(path), options.OwnerUID); err != nil {
		return nil, err
	}
	if err := removeStaleSocket(path, options.OwnerUID); err != nil {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	closeListener := func() {
		_ = listener.Close()
		_ = os.Remove(path)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		closeListener()
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		closeListener()
		return nil, err
	}
	uid, gotUID := fileOwnerUID(info)
	if !gotUID || uid != options.OwnerUID {
		closeListener()
		return nil, fmt.Errorf("Guardian socket owner is %d, want %d", uid, options.OwnerUID)
	}
	credentials := options.PeerCredentials
	if credentials == nil {
		credentials = localPeerCredentials
	}
	server := &http.Server{
		Handler:           options.Handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
			uid, got := credentials(conn)
			return withPeerCredentials(ctx, uid, got)
		},
	}
	daemon := &Daemon{path: path, listener: listener, server: server, socketInfo: info}
	go func() { _ = server.Serve(listener) }()
	go func() {
		<-ctx.Done()
		_ = daemon.Close()
	}()
	return daemon, nil
}

func (d *Daemon) Close() error {
	_ = d.server.Close()
	err := d.listener.Close()
	if current, statErr := os.Lstat(d.path); statErr == nil && os.SameFile(current, d.socketInfo) {
		_ = os.Remove(d.path)
	}
	return err
}

func prepareSocketDirectory(path string, ownerUID uint32) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("create Guardian socket directory: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("Guardian socket parent %s is not a directory", path)
	}
	uid, gotUID := fileOwnerUID(info)
	if !gotUID || uid != ownerUID {
		return fmt.Errorf("Guardian socket directory owner is %d, want %d", uid, ownerUID)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("Guardian socket directory %s is group/other writable", path)
	}
	return nil
}

func removeStaleSocket(path string, ownerUID uint32) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("refusing to replace non-socket %s", path)
	}
	uid, gotUID := fileOwnerUID(info)
	if !gotUID || uid != ownerUID {
		return fmt.Errorf("refusing to replace socket owned by UID %d", uid)
	}
	connection, dialErr := net.DialTimeout("unix", path, 100*time.Millisecond)
	if dialErr == nil {
		_ = connection.Close()
		return fmt.Errorf("Guardian socket %s is active", path)
	}
	return os.Remove(path)
}

func RunDaemon(ctx context.Context, options DaemonOptions) error {
	if os.Geteuid() != 0 {
		return errors.New("Guardian daemon requires root")
	}
	if runtime.GOOS != "darwin" {
		return errors.New("Guardian daemon is supported only on macOS")
	}
	gateway, err := DiscoverDefaultGateway(ctx)
	if err != nil {
		return err
	}
	runner := NewExecCoreRunner(install.BinPath, options.ConfigPath, options.DNSListen)
	manager, err := NewManager(ManagerOptions{
		Store:          OpenDefaultStore(),
		Runner:         runner,
		Health:         HealthChecker{},
		Barrier:        NewBarrier(nil),
		Restorer:       systemNetworkRestorer{},
		BarrierContext: BarrierContext{Gateway: gateway, BlockIPv6: true},
		CoreVersion:    version.Version,
	})
	if err != nil {
		return err
	}
	options.Handler = NewLocalAPI(manager)
	options.OwnerUID = 0
	daemon, err := StartDaemon(ctx, options)
	if err != nil {
		return err
	}
	defer daemon.Close()
	recoveryCtx, cancelRecovery := context.WithTimeout(context.WithoutCancel(ctx), guardianMutationTimeout)
	_ = manager.Recover(recoveryCtx)
	cancelRecovery()
	<-ctx.Done()
	return nil
}

type systemNetworkRestorer struct{}

func (systemNetworkRestorer) Restore(context.Context) error {
	_, err := install.DisableDNS("")
	return err
}
