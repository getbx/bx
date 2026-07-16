package toolkeys

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type Daemon struct {
	path     string
	listener net.Listener
	server   *http.Server
}

func StartDaemon(ctx context.Context, path string, handler http.Handler) (*Daemon, error) {
	if path == "" {
		return nil, fmt.Errorf("toolkeys socket path required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("refusing to replace non-socket %s", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o660); err != nil {
		_ = listener.Close()
		return nil, err
	}
	d := &Daemon{path: path, listener: listener, server: &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}}
	go func() { _ = d.server.Serve(listener) }()
	go func() { <-ctx.Done(); _ = d.Close() }()
	return d, nil
}
func (d *Daemon) SocketPath() string { return d.path }
func (d *Daemon) Close() error {
	_ = d.server.Close()
	err := d.listener.Close()
	_ = os.Remove(d.path)
	return err
}

func localHTTPClient(path string) *http.Client {
	return &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", path)
	}}}
}
