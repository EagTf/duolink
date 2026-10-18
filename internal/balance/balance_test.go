package balance

import (
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/eagtf/duolink/internal/config"
	"github.com/eagtf/duolink/internal/netif"
)

func testUplinks() (eth, wifi *netif.Adapter, ups []*netif.Adapter) {
	eth = &netif.Adapter{Name: "Ethernet", IPv4: netip.MustParsePrefix("192.168.2.10/24")}
	wifi = &netif.Adapter{Name: "Wi-Fi", IPv4: netip.MustParsePrefix("192.168.1.20/24")}
	return eth, wifi, []*netif.Adapter{eth, wifi}
}

func noSticky() *config.Config {
	c := config.Default()
	c.Sticky.Enabled = false
	c.AutoWeights = false // these tests set weights by hand
	return &c
}

// pickN picks n flows, each to a different destination, and returns the
// uplinks' first letters, e.g. "EWEW".
func pickN(b *Balancer, n int) string {
	var s strings.Builder
	for i := 0; i < n; i++ {
		dst := netip.AddrFrom4([4]byte{203, 0, 113, byte(i)})
		if a := b.Pick(dst); a != nil {
			s.WriteByte(a.Name[0])
		} else {
			s.WriteByte('-')
		}
	}
	return s.String()
}

func TestPickWeighted(t *testing.T) {
	eth, wifi, ups := testUplinks()
	cases := []struct {
		w    map[*netif.Adapter]int
		want string
	}{
		{map[*netif.Adapter]int{eth: 1, wifi: 1}, "EWEWEW"},
		{map[*netif.Adapter]int{eth: 2, wifi: 1}, "EWEEWE"},
		{map[*netif.Adapter]int{eth: 1, wifi: 0}, "EEEEEE"},
	}
	for _, c := range cases {
		if got := pickN(New(ups, c.w, noSticky()), 6); got != c.want {
			t.Errorf("weights %d:%d: got %s, want %s", c.w[eth], c.w[wifi], got, c.want)
		}
	}
}

func TestPickSkipsDown(t *testing.T) {
	eth, wifi, ups := testUplinks()
	b := New(ups, map[*netif.Adapter]int{eth: 1, wifi: 1}, noSticky())
	b.SetHealthy(wifi, false)
	if got := pickN(b, 4); got != "EEEE" {
		t.Errorf("wifi down: got %s, want EEEE", got)
	}
	// Everything down: fall back to all weighted links rather than none.
	b.SetHealthy(eth, false)
	if got := pickN(b, 4); strings.Contains(got, "-") || !strings.Contains(got, "W") {
		t.Errorf("all down: got %s, want both links used", got)
	}
}

func TestSticky(t *testing.T) {
	eth, wifi, ups := testUplinks()
	cfg := config.Default() // sticky on, 10 minutes
	b := New(ups, map[*netif.Adapter]int{eth: 1, wifi: 1}, &cfg)
	clock := time.Unix(1_000_000, 0)
	b.now = func() time.Time { return clock }

	game := netip.MustParseAddr("198.51.100.7")
	first := b.Pick(game)
	for i := 0; i < 5; i++ {
		if got := b.Pick(game); got != first {
			t.Fatalf("flow %d to the same destination moved from %s to %s", i, first.Name, got.Name)
		}
	}
	// Other destinations are still balanced across both lines.
	if got := pickN(b, 4); !strings.Contains(got, "E") || !strings.Contains(got, "W") {
		t.Errorf("new destinations not balanced: %s", got)
	}

	// Each new flow restarts the timer: 9 min later it still sticks...
	clock = clock.Add(9 * time.Minute)
	if b.Pick(game) != first {
		t.Error("expired too early")
	}
	// ...and an expired entry is swept from the table.
	clock = clock.Add(11 * time.Minute)
	b.expireSticky()
	if _, ok := b.sticky[game]; ok {
		t.Error("entry not expired after 11 idle minutes")
	}

	// If the sticky line goes down, the destination moves and sticks to the other one.
	b.Pick(game)
	stuck := b.sticky[game].a
	other := eth
	if stuck == eth {
		other = wifi
	}
	b.SetHealthy(stuck, false)
	if got := b.Pick(game); got != other {
		t.Fatalf("sticky line down: got %s, want %s", got.Name, other.Name)
	}
	b.SetHealthy(stuck, true)
	if got := b.Pick(game); got != other {
		t.Errorf("after recovery the destination should stay on %s, got %s", other.Name, got.Name)
	}
}

func TestResolveWeights(t *testing.T) {
	eth, wifi, ups := testUplinks()
	spec, err := config.ParseWeightSpec("eth=2, wifi=1")
	if err != nil {
		t.Fatal(err)
	}
	w, err := ResolveWeights(spec, ups)
	if err != nil || w[eth] != 2 || w[wifi] != 1 {
		t.Fatalf("got %v, %v", w, err)
	}
	if w, _ := ResolveWeights(nil, ups); w[eth] != 1 || w[wifi] != 1 {
		t.Errorf("default weights: got %v", w)
	}
	if w, _ := ResolveWeights(map[string]int{"wifi": 1}, ups); w[eth] != 0 {
		t.Errorf("unnamed uplink should get 0, got %d", w[eth])
	}
	for _, bad := range []map[string]int{{"eth": -1}, {"eth": 0, "wifi": 0}, {"phone": 1}, {"eth": 1, "ethernet": 1}} {
		if _, err := ResolveWeights(bad, ups); err == nil {
			t.Errorf("%v: expected an error", bad)
		}
	}
}
