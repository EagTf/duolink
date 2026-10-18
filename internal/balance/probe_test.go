package balance

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"testing"

	"github.com/eagtf/duolink/internal/config"
	"github.com/eagtf/duolink/internal/netif"
)

// The real Probe, through the loopback adapter (interface 1): nothing leaves
// the PC. Port 1 on loopback is closed, so dialing it fails at once.
func loopback() *netif.Adapter {
	return &netif.Adapter{Name: "loopback", Index: 1, IPv4: netip.MustParsePrefix("127.0.0.1/8")}
}

func TestProbeAnyTargetAnswering(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	h := config.Default().Health
	h.Targets = []string{"127.0.0.1:1", ln.Addr().String()} // one closed, one open
	if err := Probe(context.Background(), loopback(), h); err != nil {
		t.Errorf("one target answering should be enough: %v", err)
	}
}

func TestProbeAllTargetsFail(t *testing.T) {
	h := config.Default().Health
	h.Targets = []string{"127.0.0.1:2", "127.0.0.1:1"}
	err := Probe(context.Background(), loopback(), h)
	if err == nil {
		t.Fatal("expected an error when no target answers")
	}
	// Both targets are reported, in a stable order.
	msg := err.Error()
	if i, j := strings.Index(msg, "127.0.0.1:1"), strings.Index(msg, "127.0.0.1:2"); i < 0 || j < 0 || i > j {
		t.Errorf("error = %q", msg)
	}
}
