package balance

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/eagtf/duolink/internal/config"
	"github.com/eagtf/duolink/internal/netif"
)

// adapter builds a usable uplink as netif.List would report it.
func adapter(name string, luid uint64, index uint32, ip string, metric uint32) *netif.Adapter {
	p := netip.MustParsePrefix(ip)
	gw := p.Masked().Addr().Next() // x.x.x.1
	return &netif.Adapter{Name: name, LUID: luid, Index: index, IPv4: p, Gateway: gw, Up: true, Metric: metric}
}

type hotRig struct {
	b         *Balancer
	eth, wifi *netif.Adapter
	log       []string
}

func newHotRig(t *testing.T, weights map[string]int) *hotRig {
	t.Helper()
	eth := adapter("Ethernet", 1, 13, "192.168.2.10/24", 35)
	wifi := adapter("Wi-Fi", 2, 16, "192.168.1.20/24", 40)
	cfg := config.Default()
	cfg.Weights = weights
	ups := []*netif.Adapter{eth, wifi}
	w, err := ResolveWeights(weights, ups)
	if err != nil {
		t.Fatal(err)
	}
	r := &hotRig{b: New(ups, w, &cfg), eth: eth, wifi: wifi}
	r.b.logf = func(format string, args ...any) { r.log = append(r.log, fmt.Sprintf(format, args...)) }
	r.b.probe = func(context.Context, *netif.Adapter) error { return nil }
	return r
}

func (r *hotRig) logHas(s string) bool {
	for _, l := range r.log {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

func (r *hotRig) line(name string) *uplink {
	for _, l := range r.b.links {
		if l.a.Name == name {
			return l
		}
	}
	return nil
}

func TestOutageDestinationsRebalancedOnRecovery(t *testing.T) {
	r := newHotRig(t, nil)
	before := netip.MustParseAddr("203.0.113.1")
	during := netip.MustParseAddr("203.0.113.2")

	r.b.Pick(before) // both lines UP: a normal sticky entry
	r.b.SetHealthy(r.wifi, false)
	if got := r.b.Pick(during); got != r.eth {
		t.Fatalf("with Wi-Fi DOWN the flow should go to Ethernet, got %v", got.Name)
	}
	if !r.b.sticky[during].duringOutage || r.b.sticky[before].duringOutage {
		t.Fatal("only the entry made during the outage should be flagged")
	}

	// Wi-Fi comes back: its probe succeeds.
	r.b.check(context.Background(), r.b.link(r.wifi), "")
	if _, ok := r.b.sticky[during]; ok {
		t.Error("the destination that moved during the outage should be forgotten")
	}
	if _, ok := r.b.sticky[before]; !ok {
		t.Error("a destination from before the outage should keep its line")
	}
	if !r.logHas("rebalancing 1 destinations") {
		t.Errorf("log: %q", r.log)
	}
}

func TestMovedAdapterBecomesNewLine(t *testing.T) {
	r := newHotRig(t, map[string]int{"Ethernet": 1, "Wi-Fi": 3})
	old := r.b.link(r.wifi)
	moved := adapter("Wi-Fi", 2, 22, "192.168.1.30/24", 40) // same LUID, new index and address

	r.b.applyAdapters([]*netif.Adapter{r.eth, moved})

	nl := r.line("Wi-Fi")
	if nl == nil || nl == old || nl.a != moved {
		t.Fatalf("Wi-Fi should be a new line at the new address, got %+v", nl)
	}
	if nl.weight != 3 {
		t.Errorf("weight = %d, want the old line's 3", nl.weight)
	}
	if nl.healthy.Load() {
		t.Error("a moved line must stay DOWN until a probe succeeds")
	}
	if why := kicked(nl); why != "new address" {
		t.Errorf("probe request = %q", why)
	}
	if !r.logHas("now at 192.168.1.30, index 22 (was 192.168.1.20, index 16)") {
		t.Errorf("log: %q", r.log)
	}
	// The loop guard follows the address.
	if !r.b.IsUplinkIP(netip.MustParseAddr("192.168.1.30")) || r.b.IsUplinkIP(netip.MustParseAddr("192.168.1.20")) {
		t.Error("IsUplinkIP should know the new address and forget the old one")
	}
	if len(r.b.Lines()) != 2 {
		t.Errorf("lines = %d, want 2", len(r.b.Lines()))
	}
}

func TestRemovedAdapterDroppedAndNewOneAdded(t *testing.T) {
	r := newHotRig(t, nil)
	// The old USB Wi-Fi adapter is gone; a new one appears.
	newWifi := adapter("Wi-Fi 2", 7, 30, "192.168.1.40/24", 40)
	r.b.applyAdapters([]*netif.Adapter{r.eth, newWifi})

	if r.line("Wi-Fi") != nil {
		t.Error("the removed adapter should no longer be a line")
	}
	nl := r.line("Wi-Fi 2")
	if nl == nil || nl.weight != 1 || nl.healthy.Load() {
		t.Fatalf("new adapter: %+v", nl)
	}
	if why := kicked(nl); why != "new line" {
		t.Errorf("probe request = %q", why)
	}
	if !r.logHas("Wi-Fi: adapter removed; no longer a line") || !r.logHas("Wi-Fi 2: new line (weight 1); probing it") {
		t.Errorf("log: %q", r.log)
	}

	// Its first good probe brings it UP and it gets flows.
	r.b.check(context.Background(), nl, "new line")
	if !nl.healthy.Load() {
		t.Fatal("new line not UP after a good probe")
	}
	got := map[string]bool{}
	for i := 0; i < 4; i++ {
		got[r.b.Pick(netip.AddrFrom4([4]byte{203, 0, 113, byte(10 + i)})).Name] = true
	}
	if !got["Wi-Fi 2"] || !got["Ethernet"] {
		t.Errorf("new flows should use both lines, got %v", got)
	}
}

func TestNewAdapterWeightFromConfig(t *testing.T) {
	ups := []*netif.Adapter{
		adapter("Ethernet", 1, 13, "192.168.2.10/24", 35),
		adapter("Wi-Fi 2", 7, 30, "192.168.1.40/24", 40),
	}
	cases := []struct {
		spec map[string]int
		want int
	}{
		{nil, 1},                           // no weights: everyone 1
		{map[string]int{"Ethernet": 2}, 0}, // weights given, this line unnamed
		{map[string]int{"Wi-Fi": 3}, 3},    // the old name still matches by prefix
		{map[string]int{"wifi2": 2, "eth": 1}, 2},
	}
	for _, c := range cases {
		if got := weightFor(c.spec, ups[1], ups); got != c.want {
			t.Errorf("weightFor(%v) = %d, want %d", c.spec, got, c.want)
		}
	}
}

func TestPinFollowsReplacedLine(t *testing.T) {
	r := newHotRig(t, nil)
	pin := Rule{Kind: RulePin, Pin: "wifi"}
	if got := r.b.PickFor(netip.MustParseAddr("203.0.113.9"), pin); got != r.wifi {
		t.Fatalf("pinned to %v", got.Name)
	}
	moved := adapter("Wi-Fi", 2, 22, "192.168.1.30/24", 40)
	r.b.applyAdapters([]*netif.Adapter{r.eth, moved})
	// Until the moved line is probed, the pin falls back to balancing.
	if got := r.b.PickFor(netip.MustParseAddr("203.0.113.9"), pin); got != r.eth {
		t.Errorf("pinned line not ready: got %v, want Ethernet", got.Name)
	}
	r.b.check(context.Background(), r.line("Wi-Fi"), "new address")
	if got := r.b.PickFor(netip.MustParseAddr("203.0.113.9"), pin); got != moved {
		t.Errorf("pin should follow the line to its new address, got %v", got)
	}
}

// Health loops start for lines that appear after Run, and stop for lines
// that are replaced.
func TestHealthLoopsFollowLines(t *testing.T) {
	r := newHotRig(t, nil)
	probed := make(chan string, 10)
	r.b.probe = func(_ context.Context, a *netif.Adapter) error { probed <- a.Name; return nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.b.Run(ctx)

	r.b.applyAdapters([]*netif.Adapter{r.eth, r.wifi, adapter("Phone", 9, 40, "192.168.42.2/24", 50)})
	select {
	case name := <-probed:
		if name != "Phone" {
			t.Errorf("first probe was for %s, want the new line", name)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the new line's health loop didn't probe it")
	}
	if l := r.line("Phone"); l == nil || !l.healthy.Load() {
		// give the loop a moment to apply the result
		time.Sleep(50 * time.Millisecond)
		if l = r.line("Phone"); l == nil || !l.healthy.Load() {
			t.Error("new line not UP after its probe")
		}
	}
}
