package guardian

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/getbx/bx/internal/socks5"
	"github.com/getbx/bx/internal/supervisor"
)

const (
	defaultHealthTimeout = 20 * time.Second
	defaultHealthPoll    = 100 * time.Millisecond
	defaultProbeAddr     = "1.1.1.1:443"
)

type HealthTarget struct {
	Version string
	PID     int
	Timeout time.Duration
}

type HealthChecker struct {
	SockPath     string
	PollInterval time.Duration
	ProbeAddr    string

	fetchRuntime func(context.Context, string) (supervisor.RuntimeState, error)
	probe        func(context.Context, string, string) error
}

func (h HealthChecker) Wait(ctx context.Context, target HealthTarget) (supervisor.RuntimeState, error) {
	timeout := target.Timeout
	if timeout <= 0 {
		timeout = defaultHealthTimeout
	}
	poll := h.PollInterval
	if poll <= 0 {
		poll = defaultHealthPoll
	}
	sockPath := h.SockPath
	if sockPath == "" {
		sockPath = supervisor.SockPath
	}
	probeAddr := h.ProbeAddr
	if probeAddr == "" {
		probeAddr = defaultProbeAddr
	}
	fetch := h.fetchRuntime
	if fetch == nil {
		fetch = func(ctx context.Context, sockPath string) (supervisor.RuntimeState, error) {
			return fetchRuntimeState(ctx, sockPath)
		}
	}
	probe := h.probe
	if probe == nil {
		probe = probeSOCKSTransport
	}

	healthCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	var lastState supervisor.RuntimeState
	var lastErr error
	for {
		state, err := fetch(healthCtx, sockPath)
		if err == nil {
			lastState = state
			err = validateRuntimeState(state, target)
			if err == nil {
				err = probe(healthCtx, state.SocksAddr, probeAddr)
			}
		}
		if err == nil {
			return state, nil
		}
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			lastErr = err
		}

		select {
		case <-ctx.Done():
			return lastState, ctx.Err()
		case <-healthCtx.Done():
			if lastErr != nil {
				return lastState, fmt.Errorf("core health check timed out after %s: %w", timeout, lastErr)
			}
			return lastState, healthCtx.Err()
		case <-ticker.C:
		}
	}
}

func fetchRuntimeState(ctx context.Context, sockPath string) (supervisor.RuntimeState, error) {
	type result struct {
		state supervisor.RuntimeState
		err   error
	}
	done := make(chan result, 1)
	go func() {
		state, err := supervisor.FetchRuntimeState(sockPath)
		done <- result{state: state, err: err}
	}()
	select {
	case <-ctx.Done():
		return supervisor.RuntimeState{}, ctx.Err()
	case out := <-done:
		return out.state, out.err
	}
}

func validateRuntimeState(state supervisor.RuntimeState, target HealthTarget) error {
	if state.Version != target.Version {
		return fmt.Errorf("core version %q does not match expected %q", state.Version, target.Version)
	}
	if state.PID != target.PID {
		return fmt.Errorf("core PID %d does not match expected %d", state.PID, target.PID)
	}
	if !state.TunnelHealthy {
		return errors.New("core tunnel is not healthy")
	}
	if !state.DNSListening {
		return errors.New("core DNS listener is not ready")
	}
	if state.TunName == "" {
		return errors.New("core TUN name is missing")
	}
	if !state.RoutesInstalled {
		return errors.New("core routes are not installed")
	}
	if state.UDPRequired && !state.UDPReady {
		return errors.New("required UDP transport is not ready")
	}
	if err := validateRuntimeBypass(state.ServerBypass); err != nil {
		return err
	}
	if _, err := loopbackSOCKSAddr(state.SocksAddr); err != nil {
		return err
	}
	return nil
}

func validateRuntimeBypass(bypasses []string) error {
	if len(bypasses) == 0 {
		return errors.New("core server bypass is missing")
	}
	for _, bypass := range bypasses {
		prefix, err := netip.ParsePrefix(bypass)
		if err != nil || !prefix.Addr().Is4() || prefix.Bits() != 32 || prefix != prefix.Masked() {
			return fmt.Errorf("core server bypass %q is not an exact IPv4 /32", bypass)
		}
	}
	return nil
}

func loopbackSOCKSAddr(value string) (string, error) {
	host, port, err := net.SplitHostPort(value)
	if err != nil || port == "" {
		return "", fmt.Errorf("core SOCKS address %q is invalid", value)
	}
	addr, err := netip.ParseAddr(host)
	if err != nil || !addr.IsLoopback() {
		return "", fmt.Errorf("core SOCKS address %q is not loopback", value)
	}
	return net.JoinHostPort(addr.String(), port), nil
}

func probeSOCKSTransport(ctx context.Context, socksAddr, target string) error {
	loopbackAddr, err := loopbackSOCKSAddr(socksAddr)
	if err != nil {
		return err
	}
	dialer, err := socks5.NewDialer(loopbackAddr, &net.Dialer{Timeout: time.Second})
	if err != nil {
		return fmt.Errorf("build loopback SOCKS probe: %w", err)
	}
	conn, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		return fmt.Errorf("probe Core transport through loopback SOCKS: %w", err)
	}
	return conn.Close()
}
