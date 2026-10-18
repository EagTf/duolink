package netif

import (
	"context"
	"net/netip"
	"strings"
	"testing"
)

func TestOverlapWarnings(t *testing.T) {
	ad := func(name, ip, gw string) *Adapter {
		return &Adapter{Name: name, IPv4: netip.MustParsePrefix(ip), Gateway: netip.MustParseAddr(gw)}
	}
	cases := []struct {
		a, b *Adapter
		want string // substring of the warning, "" for none
	}{
		{ad("eth", "192.168.2.10/24", "192.168.2.1"), ad("wifi", "192.168.1.20/24", "192.168.1.1"), ""},
		{ad("eth", "192.168.1.10/24", "192.168.1.1"), ad("wifi", "192.168.1.20/24", "192.168.1.1"), "same gateway"},
		{ad("eth", "192.168.1.10/24", "192.168.1.1"), ad("wifi", "192.168.1.20/24", "192.168.1.254"), "overlapping subnets"},
		{ad("eth", "10.0.0.5/8", "10.0.0.1"), ad("wifi", "10.1.2.3/24", "10.1.2.1"), "overlapping subnets"},
	}
	for _, c := range cases {
		got := strings.Join(OverlapWarnings([]*Adapter{c.a, c.b}), "\n")
		if c.want == "" && got != "" || !strings.Contains(got, c.want) {
			t.Errorf("%s / %s: got %q, want %q", c.a.IPv4, c.b.IPv4, got, c.want)
		}
	}
}

func TestFind(t *testing.T) {
	eth := &Adapter{Name: "Ethernet"}
	eth2 := &Adapter{Name: "Ethernet 2"}
	wifi := &Adapter{Name: "Wi-Fi"}
	ups := []*Adapter{eth, eth2, wifi}
	for key, want := range map[string]*Adapter{"wifi": wifi, "WI-FI": wifi, "ethernet": eth, "ethernet2": eth2} {
		if got, err := Find(ups, key); err != nil || got != want {
			t.Errorf("Find(%q) = %v, %v", key, got, err)
		}
	}
	for _, bad := range []string{"eth", "phone"} { // ambiguous, no match
		if _, err := Find(ups, bad); err == nil {
			t.Errorf("Find(%q): expected an error", bad)
		}
	}
}

func TestIsTunName(t *testing.T) {
	for name, want := range map[string]bool{
		"duolink": true, "duolink 2": true,
		"Ethernet": false, "duolinkx": false,
	} {
		if got := IsTunName(name); got != want {
			t.Errorf("IsTunName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestIssue(t *testing.T) {
	orig := &Adapter{Index: 12, IPv4: netip.MustParsePrefix("192.168.2.10/24"), Gateway: netip.MustParseAddr("192.168.2.1"), Up: true}
	mod := func(f func(a *Adapter)) *Adapter {
		c := *orig
		f(&c)
		return &c
	}
	cases := []struct {
		cur  *Adapter
		want string // substring; "" = no issue
	}{
		{mod(func(*Adapter) {}), ""},
		{nil, "removed"},
		{mod(func(a *Adapter) { a.Up = false }), "disconnected"},
		{mod(func(a *Adapter) { a.IPv4 = netip.Prefix{} }), "lost its IPv4 address"},
		{mod(func(a *Adapter) { a.IPv4 = netip.MustParsePrefix("169.254.7.7/16") }), "lost its IPv4 address"},
		{mod(func(a *Adapter) { a.IPv4 = netip.MustParsePrefix("192.168.2.30/24") }), "address changed"},
		{mod(func(a *Adapter) { a.Index = 40 }), "index changed"},
		{mod(func(a *Adapter) { a.Gateway = netip.Addr{} }), "lost its default gateway"},
	}
	for _, c := range cases {
		got := Issue(orig, c.cur)
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("Issue(%+v) = %q, want %q", c.cur, got, c.want)
		}
	}
}

// The subscriptions only listen (no admin, nothing changes), so this checks
// the real Win32 calls and the callback signature without touching the system.
func TestWatchSubscribes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	if err := Watch(ctx, func() {}); err != nil {
		t.Fatal(err)
	}
	cancel()
}
