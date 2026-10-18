package balance

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/eagtf/duolink/internal/config"
	"github.com/eagtf/duolink/internal/netif"
)

func dst(i int) netip.Addr { return netip.AddrFrom4([4]byte{203, 0, 113, byte(i)}) }

// New flows go to the line with fewer open flows, and a finished flow frees
// its line at once.
func TestLeastBusy(t *testing.T) {
	eth, wifi, ups := testUplinks()
	b := New(ups, map[*netif.Adapter]int{eth: 1, wifi: 1}, noSticky())
	exempt := Rule{Kind: RuleStickyExempt}

	// Three long flows open on Ethernet (pinned there): the next flows all go
	// to Wi-Fi until it catches up.
	var open []*Flow
	for i := 0; i < 3; i++ {
		open = append(open, b.PickFlow(dst(i), Rule{Kind: RulePin, Pin: "eth"}, nil))
	}
	var got []string
	var wifiFlows []*Flow
	for i := 0; i < 3; i++ {
		f := b.PickFlow(dst(10+i), exempt, nil)
		got = append(got, f.Adapter.Name)
		wifiFlows = append(wifiFlows, f)
	}
	if fmt.Sprint(got) != "[Wi-Fi Wi-Fi Wi-Fi]" {
		t.Errorf("with 3 flows open on Ethernet, next picks = %v; want Wi-Fi three times", got)
	}
	// Now 3:3. Ethernet's flows end (Done twice must count once).
	for _, f := range open {
		f.Done()
		f.Done()
	}
	if f := b.PickFlow(dst(20), exempt, nil); f.Adapter != eth {
		t.Errorf("Ethernet is idle now, got %s", f.Adapter.Name)
	}
	if n := b.link(eth).active.Load(); n != 1 {
		t.Errorf("Ethernet open flows = %d, want 1", n)
	}
	for _, f := range wifiFlows {
		f.Done()
	}
}

// With weights 2:1 the first line carries about twice as many open flows.
func TestLeastBusyWeighted(t *testing.T) {
	eth, wifi, ups := testUplinks()
	b := New(ups, map[*netif.Adapter]int{eth: 2, wifi: 1}, noSticky())
	n := map[*netif.Adapter]int{}
	for i := 0; i < 30; i++ {
		n[b.PickFlow(dst(i), Rule{}, nil).Adapter]++ // all stay open
	}
	if n[eth] != 20 || n[wifi] != 10 {
		t.Errorf("open flows Ethernet %d, Wi-Fi %d; want 20 and 10", n[eth], n[wifi])
	}
}

// A retried flow skips the lines it already tried, also a sticky one, and
// the sticky entry moves to the line that worked.
func TestPickFlowSkip(t *testing.T) {
	eth, wifi, ups := testUplinks()
	cfg := config.Default() // sticky on
	b := New(ups, map[*netif.Adapter]int{eth: 1, wifi: 1}, &cfg)
	game := dst(1)
	first := b.PickFlow(game, Rule{}, nil)
	first.Done()
	other := wifi
	if first.Adapter == wifi {
		other = eth
	}
	if f := b.PickFlow(game, Rule{}, []*netif.Adapter{first.Adapter}); f.Adapter != other {
		t.Fatalf("skipping %s: got %v", first.Adapter.Name, f.Adapter)
	}
	if got := b.Pick(game); got != other {
		t.Errorf("sticky entry should have moved to %s, got %s", other.Name, got.Name)
	}
	if f := b.PickFlow(game, Rule{}, ups); f.Adapter != nil {
		t.Errorf("every line skipped: got %s, want none", f.Adapter.Name)
	}
	// A pin whose line was tried falls back to the other line.
	pin := Rule{Kind: RulePin, Pin: "eth"}
	if f := b.PickFlow(game, pin, []*netif.Adapter{eth}); f.Adapter != wifi {
		t.Errorf("pinned to a tried line: got %v, want Wi-Fi", f.Adapter)
	}
}

// A dial that finds the line broken sidelines it for a while and probes it;
// a good probe ends the cooldown. It isn't an outage.
func TestBrokenDialCooldown(t *testing.T) {
	eth, wifi, ups := testUplinks()
	b := New(ups, map[*netif.Adapter]int{eth: 1, wifi: 1}, noSticky())
	var log []string
	b.logf = func(format string, args ...any) { log = append(log, fmt.Sprintf(format, args...)) }
	b.probe = func(context.Context, *netif.Adapter) error { return nil }
	now := time.Unix(1000, 0)
	b.now = func() time.Time { return now }

	unreach := fmt.Errorf("dial tcp: %w", windows.WSAENETUNREACH)
	b.NoteDial(wifi, netip.MustParseAddrPort("203.0.113.5:443"), unreach)
	b.NoteDial(wifi, netip.MustParseAddrPort("203.0.113.6:443"), unreach)
	if len(log) != 1 {
		t.Errorf("want one log line per cooldown, got %q", log)
	}
	if why := kicked(b.link(wifi)); why == "" {
		t.Error("a broken dial should kick a probe")
	}
	if got := pickN(b, 4); got != "EEEE" {
		t.Errorf("Wi-Fi cooling down: picks %s, want EEEE", got)
	}
	if f := b.PickFlow(dst(50), Rule{Kind: RuleStickyExempt}, nil); f.DuringOutage {
		t.Error("a cooldown alone isn't an outage")
	} else {
		f.Done()
	}
	// Ethernet broken too: rather both than nothing.
	b.NoteDial(eth, netip.MustParseAddrPort("203.0.113.7:443"), unreach)
	if got := pickN(b, 2); got != "EW" && got != "WE" {
		t.Errorf("both cooling down: picks %s, want both lines", got)
	}
	// The cooldown ends by time, or at once with a good probe.
	now = now.Add(lineCooldown)
	b.check(context.Background(), b.link(eth), "")
	b.check(context.Background(), b.link(wifi), "dial failed")
	if got := pickN(b, 2); got != "EW" && got != "WE" {
		t.Errorf("after the cooldown: picks %s, want both lines", got)
	}
}

func TestDialErrorClasses(t *testing.T) {
	for _, c := range []struct {
		err           error
		retry, broken bool
	}{
		{windows.WSAENETUNREACH, true, true},
		{windows.WSAENETDOWN, true, true},
		{windows.WSAEADDRNOTAVAIL, true, true},
		{windows.WSAEHOSTUNREACH, true, false},
		{windows.WSAETIMEDOUT, true, false},
		{context.DeadlineExceeded, true, false},
		{windows.WSAECONNREFUSED, false, false},
		{windows.WSAECONNRESET, false, false},
		{context.Canceled, false, false},
	} {
		err := fmt.Errorf("dial tcp4: %w", c.err)
		if RetryOnOtherLine(err) != c.retry || LineBroken(err) != c.broken {
			t.Errorf("%v: retry %v broken %v, want %v %v", c.err, RetryOnOtherLine(err), LineBroken(err), c.retry, c.broken)
		}
	}
	if LineBroken(errors.New("anything else")) {
		t.Error("unknown errors aren't a broken line")
	}
}
