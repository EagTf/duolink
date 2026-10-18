package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// IP_UNICAST_IF from ws2ipdef.h. x/sys/windows doesn't define it.
const ipUnicastIf = 31

// Dialer returns a net.Dialer whose sockets leave through this adapter only,
// even after duolink has pointed the routing table at its own TUN adapter.
//
// Two things pin a socket to an adapter on Windows, and we use both:
//
//  1. bind() to the adapter's own IPv4 address (Dialer.LocalAddr). This fixes the
//     source address, so replies come back on the right line. On its own it does
//     NOT pick the outgoing interface: Windows still looks up the best route
//     to the destination, which will be our TUN's 0.0.0.0/1 or 128.0.0.0/1 route,
//     and the packet would loop back into duolink.
//
//  2. setsockopt(IPPROTO_IP, IP_UNICAST_IF, ifIndex) before connect(). This
//     tells the stack to send unicast traffic for this socket out of that
//     interface. The route lookup only considers routes on that interface,
//     so it finds the adapter's own default route and gateway. Quirk: for IPv4
//     the index must be passed in network byte order (big-endian). IPv6's
//     IPV6_UNICAST_IF takes host order, but v1 is IPv4-only.
//
// Dialer.Control runs on the raw socket after socket() and before bind/connect,
// which is exactly when the option has to be set.
func (a *Adapter) Dialer() *net.Dialer {
	return &net.Dialer{
		LocalAddr: &net.TCPAddr{IP: a.IPv4.Addr().AsSlice()},
		Control:   a.bindControl,
		Timeout:   10 * time.Second,
	}
}

// DialUDP4 opens a connected UDP socket to addr through this adapter, with the
// same two-part binding as Dialer. Go's Dialer insists LocalAddr matches the
// network, so UDP gets a *net.UDPAddr instead of a *net.TCPAddr.
func (a *Adapter) DialUDP4(ctx context.Context, addr string) (net.Conn, error) {
	d := &net.Dialer{
		LocalAddr: &net.UDPAddr{IP: a.IPv4.Addr().AsSlice()},
		Control:   a.bindControl,
	}
	return d.DialContext(ctx, "udp4", addr)
}

func (a *Adapter) bindControl(network, address string, c syscall.RawConn) error {
	if !strings.HasSuffix(network, "4") {
		return fmt.Errorf("%s: only IPv4 is supported (got %s)", a.Name, network)
	}
	var serr error
	err := c.Control(func(fd uintptr) {
		var idx [4]byte
		binary.BigEndian.PutUint32(idx[:], a.Index)
		serr = windows.Setsockopt(windows.Handle(fd), windows.IPPROTO_IP, ipUnicastIf,
			&idx[0], int32(unsafe.Sizeof(idx)))
	})
	if err != nil {
		return err
	}
	if serr != nil {
		return fmt.Errorf("%s: setsockopt IP_UNICAST_IF(%d): %w", a.Name, a.Index, serr)
	}
	return nil
}

// DialTCP4 dials over IPv4 through this adapter and double-checks that the
// kernel really used the adapter's address as the source.
func (a *Adapter) DialTCP4(ctx context.Context, addr string) (net.Conn, error) {
	c, err := a.Dialer().DialContext(ctx, "tcp4", addr)
	if err != nil {
		return nil, err
	}
	if la, ok := c.LocalAddr().(*net.TCPAddr); ok && !la.IP.Equal(a.IPv4.Addr().AsSlice()) {
		c.Close()
		return nil, fmt.Errorf("%s: socket egressed from %s, not %s", a.Name, la.IP, a.IPv4.Addr())
	}
	return c, nil
}

// publicIP asks api.ipify.org which address our traffic arrives from, going
// through this adapter only. Used by `duolink list --ip`; it's an explicit
// diagnostic, never called in normal operation.
func (a *Adapter) publicIP(ctx context.Context) (string, error) {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return a.DialTCP4(ctx, addr)
		},
		Proxy:             nil, // ignore system proxy settings: we want the raw path
		DisableKeepAlives: true,
	}
	defer tr.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.ipify.org", nil)
	if err != nil {
		return "", err
	}
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("ipify: HTTP %d", resp.StatusCode)
	}
	return strings.TrimSpace(string(body)), nil
}
