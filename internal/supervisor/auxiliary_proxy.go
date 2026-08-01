package supervisor

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
)

const privateAuxiliaryAddrAttempts = 16

var errPrivateAuxiliaryAddrUnavailable = errors.New("private auxiliary endpoint unavailable")

// auxiliaryProxy owns the configured stable HTTP-proxy listener and forwards
// each accepted connection to the active transport's private HTTP endpoint.
type auxiliaryProxy struct {
	listener net.Listener
	target   atomic.Pointer[string]

	ctx        context.Context
	cancel     context.CancelFunc
	acceptDone chan struct{}
	closeOnce  sync.Once
	wg         sync.WaitGroup

	connMu sync.Mutex
	conns  map[net.Conn]struct{}
}

func startAuxiliaryProxy(listenAddr, target string) (*auxiliaryProxy, error) {
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	proxy := &auxiliaryProxy{
		listener:   listener,
		ctx:        ctx,
		cancel:     cancel,
		acceptDone: make(chan struct{}),
		conns:      make(map[net.Conn]struct{}),
	}
	proxy.SetTarget(target)
	go proxy.accept()
	return proxy, nil
}

func privateAuxiliaryAddr(configured string, enabled bool) (string, error) {
	return privateAuxiliaryAddrWithListen(configured, enabled, net.Listen)
}

func privateAuxiliaryAddrWithListen(
	configured string,
	enabled bool,
	listen func(network, address string) (net.Listener, error),
) (string, error) {
	if configured == "" || !enabled {
		return "", nil
	}
	for range privateAuxiliaryAddrAttempts {
		listener, err := listen("tcp4", "127.0.0.1:0")
		if err != nil {
			continue
		}
		addr := listener.Addr().String()
		if err := listener.Close(); err != nil {
			return "", errPrivateAuxiliaryAddrUnavailable
		}
		if privateAuxiliaryAddrAllowed(configured, addr) {
			return addr, nil
		}
	}
	return "", errPrivateAuxiliaryAddrUnavailable
}

func privateAuxiliaryAddrAllowed(configured, candidate string) bool {
	_, configuredPort, configuredErr := net.SplitHostPort(strings.TrimSpace(configured))
	_, candidatePort, candidateErr := net.SplitHostPort(strings.TrimSpace(candidate))
	if configuredErr != nil || candidateErr != nil || configuredPort == "0" {
		return true
	}
	return configuredPort != candidatePort
}

func (p *auxiliaryProxy) SetTarget(target string) {
	p.target.Store(&target)
}

func (p *auxiliaryProxy) accept() {
	defer close(p.acceptDone)
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			if p.ctx.Err() != nil {
				return
			}
			continue
		}
		p.track(conn)
		p.wg.Add(1)
		go p.forward(conn)
	}
}

func (p *auxiliaryProxy) forward(client net.Conn) {
	defer p.wg.Done()
	defer p.untrackAndClose(client)

	target := p.target.Load()
	if target == nil || *target == "" {
		return
	}
	upstream, err := (&net.Dialer{}).DialContext(p.ctx, "tcp", *target)
	if err != nil {
		return
	}
	p.track(upstream)
	defer p.untrackAndClose(upstream)

	copied := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, client)
		copied <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, upstream)
		copied <- struct{}{}
	}()
	<-copied
	_ = client.Close()
	_ = upstream.Close()
	<-copied
}

func (p *auxiliaryProxy) track(conn net.Conn) {
	p.connMu.Lock()
	p.conns[conn] = struct{}{}
	p.connMu.Unlock()
}

func (p *auxiliaryProxy) untrackAndClose(conn net.Conn) {
	p.connMu.Lock()
	delete(p.conns, conn)
	p.connMu.Unlock()
	_ = conn.Close()
}

func (p *auxiliaryProxy) Close() error {
	var closeErr error
	p.closeOnce.Do(func() {
		p.cancel()
		closeErr = p.listener.Close()
		p.connMu.Lock()
		for conn := range p.conns {
			_ = conn.Close()
		}
		p.connMu.Unlock()
		<-p.acceptDone
		p.wg.Wait()
	})
	return closeErr
}
