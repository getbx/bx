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

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/install"
	"github.com/getbx/bx/internal/version"
)

const SocketPath = "/var/run/bx-guard.sock"

const (
	guardianRecoveryRetryInitial = 100 * time.Millisecond
	guardianRecoveryRetryMax     = 5 * time.Second
)

type DaemonOptions struct {
	ConfigPath             string
	DNSListen              string
	SocketPath             string
	Handler                http.Handler
	OwnerUID               uint32
	LocalAPIOwnerUID       uint32
	PeerCredentials        func(net.Conn) (uint32, bool)
	networkObserver        daemonNetworkObserver
	networkObserverDesired func() DesiredState
}

type Daemon struct {
	path             string
	listener         net.Listener
	server           *http.Server
	socketInfo       os.FileInfo
	mutations        mutationLifecycle
	recoveries       recoveryLifecycle
	observer         *daemonNetworkObserverLifecycle
	shutdownMu       sync.Mutex
	shutdownStarted  bool
	shutdownComplete bool
	shutdownErr      error
}

type mutationLifecycle interface {
	beginShutdown()
	waitForMutations(context.Context) error
}

type recoveringController interface {
	Controller
	PathRecoveryController
	Recover(context.Context) error
}

type daemonStarter func(context.Context, DaemonOptions) (*Daemon, error)

type daemonNetworkObserver interface {
	Run(context.Context)
}

type DaemonShutdownError struct {
	Stage string
	Err   error
}

func (e *DaemonShutdownError) Error() string {
	return fmt.Sprintf("Guardian shutdown %s: %v", e.Stage, e.Err)
}

func (e *DaemonShutdownError) Unwrap() error {
	return e.Err
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
	handler := options.Handler
	var observer *daemonNetworkObserverLifecycle
	if options.networkObserver != nil {
		observer = newDaemonNetworkObserverLifecycle(ctx, options.networkObserver)
		handler = &networkObserverDesiredHandler{
			next:     handler,
			observer: observer,
			desired:  options.networkObserverDesired,
		}
	}
	server := &http.Server{
		Handler:           handler,
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
		mutations: mutations, recoveries: recoveries, observer: observer,
	}
	if observer != nil {
		observer.syncDesired(context.Background(), observerDesiredState(options.networkObserverDesired))
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
	d.shutdownMu.Lock()
	defer d.shutdownMu.Unlock()
	if d.shutdownComplete {
		return d.shutdownErr
	}
	if !d.shutdownStarted {
		d.shutdownStarted = true
		if d.recoveries != nil {
			d.recoveries.beginRecoveryShutdown()
		}
		if d.mutations != nil {
			d.mutations.beginShutdown()
		}
		if d.observer != nil {
			d.observer.beginShutdown()
		}
	}
	if d.observer != nil {
		if err := d.observer.wait(ctx); err != nil {
			return &DaemonShutdownError{Stage: "network_observer_drain", Err: err}
		}
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
	d.shutdownComplete = true
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
	localAPIOwnerUID, err := loadGuardianLocalAPIOwnerUID(options.ConfigPath)
	if err != nil {
		return err
	}
	options.LocalAPIOwnerUID = localAPIOwnerUID
	gateway, err := discoverDaemonGateway(ctx)
	if err != nil {
		return err
	}
	runner := NewExecCoreRunner(install.BinPath, options.ConfigPath, options.DNSListen)
	manager, err := NewManager(ManagerOptions{
		Store:           OpenDefaultStore(),
		Runner:          runner,
		Health:          HealthChecker{},
		Barrier:         NewBarrier(nil),
		Restorer:        systemNetworkRestorer{},
		Legacy:          systemLegacyCoreLifecycle{},
		BarrierContext:  BarrierContext{Gateway: gateway, BlockIPv6: true},
		GatewayProvider: GatewayProviderFunc(DiscoverDefaultGateway),
		CoreVersion:     version.Version,
	})
	if err != nil {
		return err
	}
	daemon, err := startRecoveredDaemon(ctx, options, manager, StartDaemon)
	if err != nil {
		return err
	}
	defer daemon.Close()
	<-ctx.Done()
	return daemon.Close()
}

func startRecoveredDaemon(ctx context.Context, options DaemonOptions, controller recoveringController, start daemonStarter) (*Daemon, error) {
	recoveryCtx, cancelRecovery := context.WithTimeout(ctx, guardianMutationTimeout)
	recoveryErr := controller.Recover(recoveryCtx)
	cancelRecovery()
	options.Handler = NewLocalAPI(controller, LocalAPIOptions{OwnerUID: options.LocalAPIOwnerUID})
	options.OwnerUID = 0
	if options.networkObserver == nil {
		options.networkObserver = newPlatformNetworkObserver(controller)
	}
	options.networkObserverDesired = func() DesiredState {
		return controller.Status().Desired
	}
	daemon, err := start(ctx, options)
	if err != nil {
		return nil, err
	}
	if recoveryErr != nil {
		go retryDaemonRecovery(ctx, controller)
	}
	return daemon, nil
}

type networkObserverDesiredHandler struct {
	next     http.Handler
	observer *daemonNetworkObserverLifecycle
	desired  func() DesiredState
}

func (h *networkObserverDesiredHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.next.ServeHTTP(w, r)
	ctx, cancel := context.WithTimeout(context.Background(), guardianMutationTimeout)
	defer cancel()
	_ = h.observer.syncDesired(ctx, observerDesiredState(h.desired))
}

func observerDesiredState(desired func() DesiredState) DesiredState {
	if desired == nil {
		return DesiredOff
	}
	return desired()
}

type daemonNetworkObserverLifecycle struct {
	mu        sync.Mutex
	root      context.Context
	observer  daemonNetworkObserver
	accepting bool
	target    DesiredState
	cancel    context.CancelFunc
	done      chan struct{}
}

func newDaemonNetworkObserverLifecycle(ctx context.Context, observer daemonNetworkObserver) *daemonNetworkObserverLifecycle {
	return &daemonNetworkObserverLifecycle{
		root:      ctx,
		observer:  observer,
		accepting: true,
		target:    DesiredOff,
	}
}

func (l *daemonNetworkObserverLifecycle) syncDesired(ctx context.Context, desired DesiredState) error {
	l.mu.Lock()
	if !l.accepting {
		l.target = DesiredOff
		cancel := l.cancel
		l.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return nil
	}
	if desired != DesiredOn {
		desired = DesiredOff
	}
	l.target = desired
	if l.target == DesiredOn {
		if l.done == nil {
			l.startLocked()
		}
		l.mu.Unlock()
		return nil
	}
	cancel, done := l.cancel, l.done
	l.mu.Unlock()

	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *daemonNetworkObserverLifecycle) startLocked() {
	runCtx, cancel := context.WithCancel(l.root)
	done := make(chan struct{})
	l.cancel = cancel
	l.done = done
	go l.run(runCtx, done)
}

func (l *daemonNetworkObserverLifecycle) run(ctx context.Context, done chan struct{}) {
	defer func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.done == done {
			close(done)
			l.cancel = nil
			l.done = nil
			if l.accepting && l.target == DesiredOn {
				l.startLocked()
			}
			return
		}
		close(done)
	}()
	l.observer.Run(ctx)
}

func (l *daemonNetworkObserverLifecycle) beginShutdown() {
	l.mu.Lock()
	l.accepting = false
	l.target = DesiredOff
	cancel := l.cancel
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (l *daemonNetworkObserverLifecycle) wait(ctx context.Context) error {
	l.mu.Lock()
	done := l.done
	l.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func loadGuardianLocalAPIOwnerUID(configPath string) (uint32, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return 0, fmt.Errorf("read Guardian configuration: %w", err)
	}
	cfg, err := config.Parse(data)
	if err != nil {
		return 0, fmt.Errorf("parse Guardian configuration: %w", err)
	}
	if uint64(cfg.OwnerUID) > uint64(^uint32(0)) {
		return 0, errors.New("Guardian owner UID is out of range")
	}
	return uint32(cfg.OwnerUID), nil
}

func retryDaemonRecovery(ctx context.Context, controller recoveringController) {
	delay := guardianRecoveryRetryInitial
	for {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
		recoveryCtx, cancel := context.WithTimeout(ctx, guardianMutationTimeout)
		err := controller.Recover(recoveryCtx)
		cancel()
		if err == nil {
			return
		}
		if delay < guardianRecoveryRetryMax/2 {
			delay *= 2
		} else {
			delay = guardianRecoveryRetryMax
		}
	}
}

type systemNetworkRestorer struct {
	disableDNS func(context.Context, string) (install.DNSStatus, error)
}

type systemLegacyCoreLifecycle struct {
	present func(context.Context) (bool, error)
	stop    func(context.Context) error
	remove  func() error
}

func (l systemLegacyCoreLifecycle) Present(ctx context.Context) (bool, error) {
	present := l.present
	if present != nil {
		return present(ctx)
	}
	loaded, err := install.LegacyCoreLoaded()
	if err != nil {
		return false, err
	}
	return install.LegacyCoreInstalled() || loaded, nil
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
