package main

import (
	"flag"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testUplinks() (eth, wifi *Adapter, ups []*Adapter) {
	eth = &Adapter{Name: "Ethernet", IPv4: netip.MustParsePrefix("192.168.2.10/24")}
	wifi = &Adapter{Name: "Wi-Fi", IPv4: netip.MustParsePrefix("192.168.1.20/24")}
	return eth, wifi, []*Adapter{eth, wifi}
}

func noSticky() *Config {
	c := defaultConfig()
	c.Sticky.Enabled = false
	return &c
}

// pickN picks n flows, each to a different destination, and returns the
// uplinks' first letters, e.g. "EWEW".
func pickN(b *balancer, n int) string {
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
		w    map[*Adapter]int
		want string
	}{
		{map[*Adapter]int{eth: 1, wifi: 1}, "EWEWEW"},
		{map[*Adapter]int{eth: 2, wifi: 1}, "EWEEWE"},
		{map[*Adapter]int{eth: 1, wifi: 0}, "EEEEEE"},
	}
	for _, c := range cases {
		if got := pickN(newBalancer(ups, c.w, noSticky()), 6); got != c.want {
			t.Errorf("weights %d:%d: got %s, want %s", c.w[eth], c.w[wifi], got, c.want)
		}
	}
}

func TestPickSkipsDown(t *testing.T) {
	eth, wifi, ups := testUplinks()
	b := newBalancer(ups, map[*Adapter]int{eth: 1, wifi: 1}, noSticky())
	b.setHealthy(wifi, false)
	if got := pickN(b, 4); got != "EEEE" {
		t.Errorf("wifi down: got %s, want EEEE", got)
	}
	// Everything down: fall back to all weighted links rather than none.
	b.setHealthy(eth, false)
	if got := pickN(b, 4); strings.Contains(got, "-") || !strings.Contains(got, "W") {
		t.Errorf("all down: got %s, want both links used", got)
	}
}

func TestSticky(t *testing.T) {
	eth, wifi, ups := testUplinks()
	cfg := defaultConfig() // sticky on, 10 minutes
	b := newBalancer(ups, map[*Adapter]int{eth: 1, wifi: 1}, &cfg)
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
	b.setHealthy(stuck, false)
	if got := b.Pick(game); got != other {
		t.Fatalf("sticky line down: got %s, want %s", got.Name, other.Name)
	}
	b.setHealthy(stuck, true)
	if got := b.Pick(game); got != other {
		t.Errorf("after recovery the destination should stay on %s, got %s", other.Name, got.Name)
	}
}

func TestResolveWeights(t *testing.T) {
	eth, wifi, ups := testUplinks()
	spec, err := parseWeightSpec("eth=2, wifi=1")
	if err != nil {
		t.Fatal(err)
	}
	w, err := resolveWeights(spec, ups)
	if err != nil || w[eth] != 2 || w[wifi] != 1 {
		t.Fatalf("got %v, %v", w, err)
	}
	if w, _ := resolveWeights(nil, ups); w[eth] != 1 || w[wifi] != 1 {
		t.Errorf("default weights: got %v", w)
	}
	if w, _ := resolveWeights(map[string]int{"wifi": 1}, ups); w[eth] != 0 {
		t.Errorf("unnamed uplink should get 0, got %d", w[eth])
	}
	for _, bad := range []string{"eth", "eth=x", "=1"} {
		if _, err := parseWeightSpec(bad); err == nil {
			t.Errorf("%q: expected a parse error", bad)
		}
	}
	for _, bad := range []map[string]int{{"eth": -1}, {"eth": 0, "wifi": 0}, {"phone": 1}, {"eth": 1, "ethernet": 1}} {
		if _, err := resolveWeights(bad, ups); err == nil {
			t.Errorf("%v: expected an error", bad)
		}
	}
}

func TestConfigLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "duolink.json")
	write := func(s string) {
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Missing file: defaults.
	c, found, err := loadConfig(path)
	if err != nil || found || !c.Sticky.Enabled || c.Health.IntervalSeconds != 15 {
		t.Fatalf("missing file: %+v %v %v", c, found, err)
	}

	// Partial file: only the given fields change.
	write(`{"sticky": {"minutes": 3}, "weights": {"eth": 2}}`)
	c, found, err = loadConfig(path)
	if err != nil || !found || c.Sticky.Minutes != 3 || !c.Sticky.Enabled || c.Weights["eth"] != 2 || c.Health.TimeoutSeconds != 3 {
		t.Fatalf("partial file: %+v %v %v", c, found, err)
	}

	// Typos and bad values are errors, never silently ignored.
	for _, bad := range []string{
		`{"stickey": {"minutes": 3}}`,
		`{"sticky": {"minutes": 0}}`,
		`{"health": {"targets": ["one.one.one.one:443"]}}`,
		`{"weights": {"eth": 500}}`,
		`{not json`,
	} {
		write(bad)
		if _, _, err := loadConfig(path); err == nil {
			t.Errorf("%s: expected an error", bad)
		}
	}

	// Flags override the file; only flags actually given count.
	write(`{"sticky": {"minutes": 3}, "weights": {"eth": 2}}`)
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	cf := addConfigFlags(fs)
	fs.Parse([]string{"--config", path, "--sticky-minutes", "0"})
	c, _, err = cf.load()
	if err != nil || c.Sticky.Enabled || c.Weights["eth"] != 2 {
		t.Errorf("flag override: %+v %v", c, err)
	}
	fs = flag.NewFlagSet("t", flag.ContinueOnError)
	cf = addConfigFlags(fs)
	fs.Parse([]string{"--config", path, "--weights", "wifi=1"})
	c, _, err = cf.load()
	if err != nil || c.Weights["eth"] != 0 || c.Weights["wifi"] != 1 || c.Sticky.Minutes != 3 {
		t.Errorf("weights override: %+v %v", c, err)
	}
}
