package guardian

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"syscall"
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
	path         string
	listener     net.Listener
	server       *http.Server
	socketInfo   os.FileInfo
	mutations    mutationLifecycle
	recoveries   recoveryLifecycle
	shutdownOnce sync.Once
	shutdownDone chan struct{}
	shutdownErr  error
}

type mutationLifecycle interface {
	beginShutdown()
	waitForMutations(context.Context) error
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
	mutations, _ := options.Handler.(mutationLifecycle)
	recoveries, _ := options.Handler.(recoveryLifecycle)
	daemon := &Daemon{
		path: path, listener: listener, server: server, socketInfo: info,
		mutations: mutations, shutdownDone: make(chan struct{}),
		recoveries: recoveries,
	}
	go func() { _ = server.Serve(listener) }()
	go func() {
		<-ctx.Done()
		_ = daemon.Close()
	}()
	return daemon, nil
}

func (d *Daemon) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), guardianMutationTimeout)
	defer cancel()
	return d.Shutdown(ctx)
}

func (d *Daemon) Shutdown(ctx context.Context) error {
	d.shutdownOnce.Do(func() {
		if d.recoveries != nil {
			d.recoveries.beginRecoveryShutdown()
		}
		if d.mutations != nil {
			d.mutations.beginShutdown()
		}
		shutdownErr := d.server.Shutdown(ctx)
		var mutationErr error
		if d.mutations != nil {
			mutationErr = d.mutations.waitForMutations(ctx)
		}
		var recoveryErr error
		if d.recoveries != nil {
			recoveryErr = d.recoveries.waitForRecoveries(ctx)
		}
		if shutdownErr != nil || mutationErr != nil || recoveryErr != nil {
			_ = d.server.Close()
		}
		listenerErr := d.listener.Close()
		if errors.Is(listenerErr, net.ErrClosed) {
			listenerErr = nil
		}
		var removeErr error
		if current, statErr := os.Lstat(d.path); statErr == nil && os.SameFile(current, d.socketInfo) {
			removeErr = os.Remove(d.path)
		} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			removeErr = statErr
		}
		d.shutdownErr = errors.Join(shutdownErr, mutationErr, recoveryErr, listenerErr, removeErr)
		close(d.shutdownDone)
	})
	<-d.shutdownDone
	return d.shutdownErr
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
	return removeStaleSocketWithDial(path, ownerUID, func(ctx context.Context, path string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	})
}

func removeStaleSocketWithDial(path string, ownerUID uint32, dial func(context.Context, string) (net.Conn, error)) error {
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
	dialCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	connection, dialErr := dial(dialCtx, path)
	if dialErr == nil {
		_ = connection.Close()
		return fmt.Errorf("Guardian socket %s is active", path)
	}
	if !errors.Is(dialErr, syscall.ECONNREFUSED) {
		return fmt.Errorf("cannot prove Guardian socket %s is stale: %w", path, dialErr)
	}
	current, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("recheck stale Guardian socket %s: %w", path, err)
	}
	if !os.SameFile(info, current) {
		return fmt.Errorf("Guardian socket %s changed during stale check", path)
	}
	return os.Remove(path)
}

func RunDaemon(ctx context.Context, options DaemonOptions) error {
	if err := requireDaemonPlatform(); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return errors.New("Guardian daemon requires root")
	}
	gateway, err := discoverDaemonGateway(ctx)
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
		Legacy:         systemLegacyCoreLifecycle{},
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
	recoveryCtx, cancelRecovery := context.WithTimeout(ctx, guardianMutationTimeout)
	_ = manager.Recover(recoveryCtx)
	cancelRecovery()
	<-ctx.Done()
	return daemon.Close()
}

type systemNetworkRestorer struct {
	disableDNS func(context.Context, string) (install.DNSStatus, error)
}

type systemLegacyCoreLifecycle struct {
	stop   func(context.Context) error
	remove func() error
}

func (l systemLegacyCoreLifecycle) Stop(ctx context.Context) error {
	stop := l.stop
	if stop == nil {
		stop = install.BootoutLegacyCoreUnit
	}
	return stop(ctx)
}

func (l systemLegacyCoreLifecycle) Remove() error {
	remove := l.remove
	if remove == nil {
		remove = install.RemoveLegacyCoreUnit
	}
	return remove()
}

func (r systemNetworkRestorer) Restore(ctx context.Context) error {
	disableDNS := r.disableDNS
	if disableDNS == nil {
		disableDNS = install.DisableDNSContext
	}
	_, err := disableDNS(ctx, "")
	return err
}
