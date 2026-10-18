package procs

import (
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests only read the system's socket tables (no admin needed, nothing
// changes): they open sockets on loopback and check we find our own process.

// tunAddr is the TUN adapter's address (route.TunAddr), where app sockets live.
const tunAddr = "198.18.0.1"

func testExe(t *testing.T) string {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return strings.ToLower(filepath.Base(exe))
}

func TestProcessName(t *testing.T) {
	if got, want := processName(uint32(os.Getpid())), testExe(t); got != want {
		t.Errorf("processName(self) = %q, want %q", got, want)
	}
	if got := processName(0); got != "" {
		t.Errorf("processName(0) = %q, want empty", got)
	}
}

func TestLookupTCP(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	c, err := net.Dial("tcp4", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	src := c.LocalAddr().(*net.TCPAddr).AddrPort()
	src = netip.AddrPortFrom(src.Addr().Unmap(), src.Port())

	p := NewTable()
	if got, want := p.Lookup(false, src), testExe(t); got != want {
		t.Errorf("Lookup(tcp %s) = %q, want %q", src, got, want)
	}
	if got := p.Lookup(false, netip.MustParseAddrPort("127.0.0.1:1")); got != "" {
		t.Errorf("lookup of an unused port = %q, want empty", got)
	}
}

func TestLookupUDP(t *testing.T) {
	// Bound to a specific address...
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	src := c.LocalAddr().(*net.UDPAddr).AddrPort()
	p := NewTable()
	if got, want := p.Lookup(true, netip.AddrPortFrom(src.Addr().Unmap(), src.Port())), testExe(t); got != want {
		t.Errorf("Lookup(udp %s) = %q, want %q", src, got, want)
	}

	// ...and bound to 0.0.0.0, looked up as if seen from the TUN address.
	w, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	port := uint16(w.LocalAddr().(*net.UDPAddr).Port)
	if got, want := p.Lookup(true, netip.AddrPortFrom(netip.MustParseAddr(tunAddr), port)), testExe(t); got != want {
		t.Errorf("Lookup(udp wildcard :%d) = %q, want %q", port, got, want)
	}
}
