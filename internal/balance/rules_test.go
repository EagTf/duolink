package balance

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/eagtf/duolink/internal/config"
	"github.com/eagtf/duolink/internal/netif"
)

// rulesRig: Ethernet (metric 35) and Wi-Fi (metric 45), both weight 1,
// stickiness on.
func rulesRig(t *testing.T, pc config.ProcessConfig) (*Balancer, *Rules, *netif.Adapter, *netif.Adapter) {
	t.Helper()
	eth, wifi, ups := testUplinks()
	eth.Metric, wifi.Metric = 35, 45
	weights := map[*netif.Adapter]int{eth: 1, wifi: 1}
	cfg := config.Default()
	r, err := ResolveRules(pc, ups, weights)
	if err != nil {
		t.Fatal(err)
	}
	return New(ups, weights, &cfg), r, eth, wifi
}

func TestResolveRules(t *testing.T) {
	_, r, eth, wifi := rulesRig(t, config.ProcessConfig{
		StickyExempt: []string{"Steam.exe"},
		Pin:          map[string]string{"Game.EXE": "wifi"},
		Exclude:      []string{"backup.exe"},
	})
	if got := r.ForExe("steam.exe"); got.Kind != RuleStickyExempt {
		t.Errorf("steam.exe: %+v", got)
	}
	if got := r.ForExe("game.exe"); got.Kind != RulePin || got.Line != wifi {
		t.Errorf("game.exe should be pinned to Wi-Fi (names are case-insensitive): %+v", got)
	}
	if got := r.ForExe("backup.exe"); got.Kind != RuleExclude || got.Line != eth {
		t.Errorf("backup.exe should be excluded → Ethernet (lowest metric): %+v", got)
	}
	if got := r.ForExe("chrome.exe"); got.Kind != RuleNone {
		t.Errorf("unlisted exe got a rule: %+v", got)
	}
	if got := r.ForExe(""); got.Kind != RuleNone {
		t.Errorf("unknown owner got a rule: %+v", got)
	}
	if d := r.Describe(); !strings.Contains(d, "game.exe pinned → Wi-Fi") || !strings.Contains(d, "backup.exe excluded → Ethernet") {
		t.Errorf("describe() = %q", d)
	}
}

func TestResolveRulesErrors(t *testing.T) {
	eth, wifi, ups := testUplinks()
	weights := map[*netif.Adapter]int{eth: 1, wifi: 0}
	for _, pc := range []config.ProcessConfig{
		{Pin: map[string]string{"game.exe": "phone"}}, // no such line
		{Pin: map[string]string{"game.exe": "wifi"}},  // weight 0
	} {
		if _, err := ResolveRules(pc, ups, weights); err == nil {
			t.Errorf("%+v: expected an error", pc)
		}
	}
	// Exclude's primary line skips weight-0 lines even if their metric is lower.
	eth.Metric, wifi.Metric = 50, 10
	r, err := ResolveRules(config.ProcessConfig{Exclude: []string{"a.exe"}}, ups, weights)
	if err != nil || r.ForExe("a.exe").Line != eth {
		t.Errorf("primary should be the weighted line: %+v %v", r.ForExe("a.exe"), err)
	}
}

func TestPickPinned(t *testing.T) {
	b, r, eth, wifi := rulesRig(t, config.ProcessConfig{Pin: map[string]string{"game.exe": "wifi"}})
	rule := r.ForExe("game.exe")
	for i := 0; i < 4; i++ {
		dst := netip.AddrFrom4([4]byte{203, 0, 113, byte(i)})
		if got := b.PickFor(dst, rule); got != wifi {
			t.Fatalf("pinned flow %d went to %s", i, got.Name)
		}
	}
	// Pinned line DOWN: fall back to the other line.
	b.SetHealthy(wifi, false)
	if got := b.PickFor(netip.MustParseAddr("203.0.113.50"), rule); got != eth {
		t.Errorf("pinned line down: got %s, want Ethernet", got.Name)
	}
	b.SetHealthy(wifi, true)
	if got := b.PickFor(netip.MustParseAddr("203.0.113.51"), rule); got != wifi {
		t.Errorf("pinned line back: got %s, want Wi-Fi", got.Name)
	}
	if len(b.sticky) != 0 {
		t.Errorf("pinned flows wrote %d sticky entries", len(b.sticky))
	}
}

func TestPickExcluded(t *testing.T) {
	b, r, eth, wifi := rulesRig(t, config.ProcessConfig{Exclude: []string{"backup.exe"}})
	rule := r.ForExe("backup.exe")
	for i := 0; i < 4; i++ {
		if got := b.PickFor(netip.AddrFrom4([4]byte{203, 0, 113, byte(i)}), rule); got != eth {
			t.Fatalf("excluded flow %d went to %s, want Ethernet", i, got.Name)
		}
	}
	b.SetHealthy(eth, false)
	if got := b.PickFor(netip.MustParseAddr("203.0.113.9"), rule); got != wifi {
		t.Errorf("primary down: got %s, want Wi-Fi", got.Name)
	}
}

func TestPickStickyExempt(t *testing.T) {
	b, r, _, _ := rulesRig(t, config.ProcessConfig{StickyExempt: []string{"steam.exe"}})
	cdn := netip.MustParseAddr("203.0.113.80")

	// Many flows from steam.exe to the same CDN IP spread over both lines...
	var s strings.Builder
	for i := 0; i < 4; i++ {
		s.WriteByte(b.PickFor(cdn, r.ForExe("steam.exe")).Name[0])
	}
	if got := s.String(); got != "EWEW" {
		t.Errorf("sticky-exempt flows to one IP: %s, want EWEW", got)
	}
	if len(b.sticky) != 0 {
		t.Error("sticky-exempt flows wrote to the sticky table")
	}

	// ...while another app's flows to that IP still stick.
	first := b.Pick(cdn)
	for i := 0; i < 3; i++ {
		if b.Pick(cdn) != first {
			t.Fatal("a flow without a rule didn't stick")
		}
	}
}
