// Package socks5 provides a small SOCKS5 dialer with UDP ASSOCIATE support.
package socks5

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	"golang.org/x/net/proxy"
)

const (
	socksVersion = 5
	cmdConnect   = 1
	cmdUDP       = 3
	atypIPv4     = 1
	atypDomain   = 3
	atypIPv6     = 4
)

// Dialer speaks TCP CONNECT through x/net/proxy and UDP ASSOCIATE directly.
type Dialer struct {
	addr string
	base *net.Dialer
	tcp  proxy.ContextDialer
}

// NewDialer returns a ContextDialer for a local SOCKS5 server.
func NewDialer(addr string, base *net.Dialer) (*Dialer, error) {
	if base == nil {
		base = &net.Dialer{Timeout: 10 * time.Second}
	}
	pd, err := proxy.SOCKS5("tcp", addr, nil, base)
	if err != nil {
		return nil, err
	}
	tcp, ok := pd.(proxy.ContextDialer)
	if !ok {
		return nil, errors.New("socks5 tcp dialer does not support context")
	}
	return &Dialer{addr: addr, base: base, tcp: tcp}, nil
}

// DialContext supports "tcp" and "udp".
func (d *Dialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	switch network {
	case "udp", "udp4", "udp6":
		return d.dialUDP(ctx, address)
	default:
		return d.tcp.DialContext(ctx, network, address)
	}
}

func (d *Dialer) dialUDP(ctx context.Context, target string) (net.Conn, error) {
	control, err := d.base.DialContext(ctx, "tcp", d.addr)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			control.Close()
		}
	}()
	if deadline, has := ctx.Deadline(); has {
		_ = control.SetDeadline(deadline)
	}
	if err := socksHandshake(control); err != nil {
		return nil, err
	}
	relay, err := requestUDPAssociate(control, d.addr)
	if err != nil {
		return nil, err
	}
	_ = control.SetDeadline(time.Time{})
	pc, err := net.ListenPacket("udp", "")
	if err != nil {
		return nil, err
	}
	relayAddr, err := net.ResolveUDPAddr("udp", relay)
	if err != nil {
		pc.Close()
		return nil, err
	}
	ok = true
	return &udpConn{
		control: control,
		pc:      pc,
		relay:   relayAddr,
		target:  target,
	}, nil
}

func socksHandshake(c net.Conn) error {
	if _, err := c.Write([]byte{socksVersion, 1, 0}); err != nil {
		return err
	}
	var resp [2]byte
	if _, err := io.ReadFull(c, resp[:]); err != nil {
		return err
	}
	if resp[0] != socksVersion || resp[1] != 0 {
		return fmt.Errorf("socks5 no-auth rejected: %v", resp)
	}
	return nil
}

func requestUDPAssociate(c net.Conn, socksAddr string) (string, error) {
	req := []byte{socksVersion, cmdUDP, 0, atypIPv4, 0, 0, 0, 0, 0, 0}
	if _, err := c.Write(req); err != nil {
		return "", err
	}
	host, port, err := readReply(c)
	if err != nil {
		return "", err
	}
	if host == "0.0.0.0" || host == "::" {
		h, _, splitErr := net.SplitHostPort(socksAddr)
		if splitErr == nil {
			host = h
		}
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

func readReply(r io.Reader) (string, int, error) {
	var head [4]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return "", 0, err
	}
	if head[0] != socksVersion {
		return "", 0, fmt.Errorf("bad socks version %d", head[0])
	}
	if head[1] != 0 {
		return "", 0, fmt.Errorf("socks5 reply code %d", head[1])
	}
	return readAddrPort(r, head[3])
}

func readAddrPort(r io.Reader, atyp byte) (string, int, error) {
	var host string
	switch atyp {
	case atypIPv4:
		var b [4]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return "", 0, err
		}
		host = net.IP(b[:]).String()
	case atypIPv6:
		var b [16]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return "", 0, err
		}
		host = net.IP(b[:]).String()
	case atypDomain:
		var l [1]byte
		if _, err := io.ReadFull(r, l[:]); err != nil {
			return "", 0, err
		}
		b := make([]byte, int(l[0]))
		if _, err := io.ReadFull(r, b); err != nil {
			return "", 0, err
		}
		host = string(b)
	default:
		return "", 0, fmt.Errorf("unsupported socks address type %d", atyp)
	}
	var p [2]byte
	if _, err := io.ReadFull(r, p[:]); err != nil {
		return "", 0, err
	}
	return host, int(binary.BigEndian.Uint16(p[:])), nil
}

type udpConn struct {
	control net.Conn
	pc      net.PacketConn
	relay   net.Addr
	target  string
}

func (c *udpConn) Read(b []byte) (int, error) {
	buf := make([]byte, len(b)+512)
	n, _, err := c.pc.ReadFrom(buf)
	if err != nil {
		return 0, err
	}
	_, payload, err := parseUDPDatagram(buf[:n])
	if err != nil {
		return 0, err
	}
	return copy(b, payload), nil
}

func (c *udpConn) Write(b []byte) (int, error) {
	pkt, err := buildUDPDatagram(c.target, b)
	if err != nil {
		return 0, err
	}
	if _, err := c.pc.WriteTo(pkt, c.relay); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (c *udpConn) Close() error {
	err1 := c.pc.Close()
	err2 := c.control.Close()
	if err1 != nil {
		return err1
	}
	return err2
}

func (c *udpConn) LocalAddr() net.Addr                { return c.pc.LocalAddr() }
func (c *udpConn) RemoteAddr() net.Addr               { return c.relay }
func (c *udpConn) SetDeadline(t time.Time) error      { return setPacketDeadline(c.pc, t) }
func (c *udpConn) SetReadDeadline(t time.Time) error  { return c.pc.SetReadDeadline(t) }
func (c *udpConn) SetWriteDeadline(t time.Time) error { return c.pc.SetWriteDeadline(t) }

func setPacketDeadline(pc net.PacketConn, t time.Time) error {
	if err := pc.SetReadDeadline(t); err != nil {
		return err
	}
	return pc.SetWriteDeadline(t)
}

func buildUDPDatagram(target string, payload []byte) ([]byte, error) {
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 0 || port > 65535 {
		return nil, fmt.Errorf("bad port %q", portStr)
	}
	header := []byte{0, 0, 0}
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			header = append(header, atypIPv4)
			header = append(header, v4...)
		} else {
			header = append(header, atypIPv6)
			header = append(header, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return nil, fmt.Errorf("domain too long")
		}
		header = append(header, atypDomain, byte(len(host)))
		header = append(header, host...)
	}
	header = binary.BigEndian.AppendUint16(header, uint16(port))
	return append(header, payload...), nil
}

func parseUDPDatagram(pkt []byte) (string, []byte, error) {
	if len(pkt) < 4 {
		return "", nil, io.ErrUnexpectedEOF
	}
	if pkt[0] != 0 || pkt[1] != 0 || pkt[2] != 0 {
		return "", nil, fmt.Errorf("unsupported socks5 udp header")
	}
	host, port, off, err := parseAddrPort(pkt, 3)
	if err != nil {
		return "", nil, err
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), pkt[off:], nil
}

func parseAddrPort(pkt []byte, off int) (string, int, int, error) {
	if off >= len(pkt) {
		return "", 0, 0, io.ErrUnexpectedEOF
	}
	atyp := pkt[off]
	off++
	var host string
	switch atyp {
	case atypIPv4:
		if off+4+2 > len(pkt) {
			return "", 0, 0, io.ErrUnexpectedEOF
		}
		host = net.IP(pkt[off : off+4]).String()
		off += 4
	case atypIPv6:
		if off+16+2 > len(pkt) {
			return "", 0, 0, io.ErrUnexpectedEOF
		}
		host = net.IP(pkt[off : off+16]).String()
		off += 16
	case atypDomain:
		if off >= len(pkt) {
			return "", 0, 0, io.ErrUnexpectedEOF
		}
		l := int(pkt[off])
		off++
		if off+l+2 > len(pkt) {
			return "", 0, 0, io.ErrUnexpectedEOF
		}
		host = string(pkt[off : off+l])
		off += l
	default:
		return "", 0, 0, fmt.Errorf("unsupported socks address type %d", atyp)
	}
	port := int(binary.BigEndian.Uint16(pkt[off : off+2]))
	off += 2
	return host, port, off, nil
}
