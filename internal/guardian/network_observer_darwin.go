//go:build darwin

package guardian

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/net/route"
	"golang.org/x/sys/unix"
)

type darwinRouteEventSource struct{}

func (darwinRouteEventSource) Events(ctx context.Context) (<-chan struct{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fd, err := openDarwinRouteSocket(defaultDarwinRouteSocketOps())
	if err != nil {
		return nil, fmt.Errorf("open routing socket: %w", err)
	}

	events := make(chan struct{}, 1)
	go readDarwinRouteEvents(ctx, fd, events)
	return events, nil
}

type darwinRouteSocketOps struct {
	forkLock    sync.Locker
	socket      func(int, int, int) (int, error)
	closeOnExec func(int) error
	close       func(int) error
}

func defaultDarwinRouteSocketOps() darwinRouteSocketOps {
	return darwinRouteSocketOps{
		forkLock:    syscall.ForkLock.RLocker(),
		socket:      unix.Socket,
		closeOnExec: setDarwinRouteSocketCloseOnExec,
		close:       unix.Close,
	}
}

func openDarwinRouteSocket(ops darwinRouteSocketOps) (int, error) {
	if ops.forkLock == nil || ops.socket == nil || ops.closeOnExec == nil || ops.close == nil {
		return -1, fmt.Errorf("routing socket opener is incomplete")
	}
	ops.forkLock.Lock()
	defer ops.forkLock.Unlock()
	fd, err := ops.socket(unix.AF_ROUTE, unix.SOCK_RAW, unix.AF_UNSPEC)
	if err != nil {
		return -1, err
	}
	if err := ops.closeOnExec(fd); err != nil {
		_ = ops.close(fd)
		return -1, fmt.Errorf("set routing socket close-on-exec: %w", err)
	}
	return fd, nil
}

func setDarwinRouteSocketCloseOnExec(fd int) error {
	_, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, unix.FD_CLOEXEC)
	return err
}

func readDarwinRouteEvents(ctx context.Context, fd int, events chan<- struct{}) {
	defer close(events)
	var closeOnce sync.Once
	closeFD := func() {
		closeOnce.Do(func() {
			_ = unix.Close(fd)
		})
	}
	defer closeFD()

	readDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			closeFD()
		case <-readDone:
		}
	}()
	defer close(readDone)

	buffer := make([]byte, 64<<10)
	for {
		n, err := unix.Read(fd, buffer)
		if err != nil {
			return
		}
		if n == 0 || !relevantDarwinRouteEvent(buffer[:n]) {
			continue
		}
		select {
		case events <- struct{}{}:
		default:
		}
	}
}

func relevantDarwinRouteEvent(data []byte) bool {
	messages, err := route.ParseRIB(route.RIBTypeRoute, data)
	if err != nil {
		return false
	}
	for _, message := range messages {
		switch typed := message.(type) {
		case *route.RouteMessage:
			switch typed.Type {
			case unix.RTM_ADD, unix.RTM_DELETE, unix.RTM_CHANGE, unix.RTM_REDIRECT, unix.RTM_LOSING:
				return true
			}
		case *route.InterfaceMessage, *route.InterfaceAddrMessage:
			return true
		}
	}
	return false
}

type darwinUnderlayGenerationSource struct{}

func (darwinUnderlayGenerationSource) Current(ctx context.Context) (string, error) {
	output, err := (darwinRunner{}).Output(ctx, Command{
		Name: darwinRoutePath,
		Args: []string{"-n", "get", "default"},
	})
	if err != nil {
		return "", fmt.Errorf("observe default route: %w", err)
	}
	gateway, interfaceName, err := parseDarwinObserverDefaultRoute(output)
	if err != nil {
		return "", err
	}
	prefixes, err := darwinObserverInterfacePrefixes(interfaceName)
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, len(prefixes)+2)
	parts = append(parts, interfaceName, gateway.String())
	parts = append(parts, prefixes...)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:8]), nil
}

func parseDarwinObserverDefaultRoute(output []byte) (netip.Addr, string, error) {
	var gatewayText, interfaceName string
	for _, line := range strings.Split(string(output), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "gateway":
			gatewayText = strings.TrimSpace(value)
		case "interface":
			interfaceName = strings.TrimSpace(value)
		}
	}
	gateway, err := netip.ParseAddr(gatewayText)
	if err != nil || !gateway.Unmap().Is4() {
		return netip.Addr{}, "", fmt.Errorf("observe default route: invalid gateway %q", gatewayText)
	}
	if interfaceName == "" || strings.HasPrefix(strings.ToLower(interfaceName), "utun") || strings.HasPrefix(strings.ToLower(interfaceName), "lo") {
		return netip.Addr{}, "", fmt.Errorf("observe default route: invalid physical interface %q", interfaceName)
	}
	return gateway.Unmap(), interfaceName, nil
}

func darwinObserverInterfacePrefixes(interfaceName string) ([]string, error) {
	iface, err := net.InterfaceByName(interfaceName)
	if err != nil {
		return nil, fmt.Errorf("observe interface %q: %w", interfaceName, err)
	}
	addresses, err := iface.Addrs()
	if err != nil {
		return nil, fmt.Errorf("observe interface %q addresses: %w", interfaceName, err)
	}
	prefixes := make([]string, 0, len(addresses))
	seen := make(map[string]struct{}, len(addresses))
	for _, address := range addresses {
		prefix, err := darwinObserverPrefix(address)
		if err != nil {
			return nil, fmt.Errorf("observe interface %q address: %w", interfaceName, err)
		}
		text := prefix.Masked().String()
		if _, ok := seen[text]; ok {
			continue
		}
		seen[text] = struct{}{}
		prefixes = append(prefixes, text)
	}
	sort.Strings(prefixes)
	return prefixes, nil
}

func darwinObserverPrefix(address net.Addr) (netip.Prefix, error) {
	ipNet, ok := address.(*net.IPNet)
	if !ok || ipNet == nil {
		return netip.Prefix{}, fmt.Errorf("unsupported address %q", address)
	}
	ones, bits := ipNet.Mask.Size()
	if ones < 0 || bits == 0 {
		return netip.Prefix{}, fmt.Errorf("invalid address mask %q", address)
	}
	ip, ok := netip.AddrFromSlice(ipNet.IP)
	if !ok {
		return netip.Prefix{}, fmt.Errorf("invalid address %q", address)
	}
	if ip.Is4In6() {
		ip = ip.Unmap()
		if bits == 128 {
			if ones < 96 {
				return netip.Prefix{}, fmt.Errorf("invalid mapped IPv4 mask %q", address)
			}
			ones -= 96
			bits = 32
		}
	}
	if ip.BitLen() != bits {
		return netip.Prefix{}, fmt.Errorf("address family and mask differ for %q", address)
	}
	return netip.PrefixFrom(ip, ones), nil
}

func newPlatformNetworkObserver(recoveries networkRecoveryRequester) daemonNetworkObserver {
	return newNetworkObserver(
		darwinRouteEventSource{},
		darwinUnderlayGenerationSource{},
		recoveries,
		networkObserverConfig{},
	)
}
