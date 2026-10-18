package main

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rulesRig: Ethernet (metric 35) and Wi-Fi (metric 45), both weight 1,
// stickiness on.
func rulesRig(t *testing.T, pc ProcessConfig) (*balancer, *procRules, *Adapter, *Adapter) {
	t.Helper()
	eth, wifi, ups := testUplinks()
	eth.Metric, wifi.Metric = 35, 45
	weights := map[*Adapter]int{eth: 1, wifi: 1}
	cfg := defaultConfig()
	r, err := resolveRules(pc, ups, weights)
	if err != nil {
		t.Fatal(err)
	}
	return newBalancer(ups, weights, &cfg), r, eth, wifi
}

func TestResolveRules(t *testing.T) {
	_, r, eth, wifi := rulesRig(t, ProcessConfig{
		StickyExempt: []string{"Steam.exe"},
		Pin:          map[string]string{"Game.EXE": "wifi"},
		Exclude:      []string{"backup.exe"},
	})
	if got := r.forExe("steam.exe"); got.kind != ruleStickyExempt {
		t.Errorf("steam.exe: %+v", got)
	}
	if got := r.forExe("game.exe"); got.kind != rulePin || got.line != wifi {
		t.Errorf("game.exe should be pinned to Wi-Fi (names are case-insensitive): %+v", got)
	}
	if got := r.forExe("backup.exe"); got.kind != ruleExclude || got.line != eth {
		t.Errorf("backup.exe should be excluded → Ethernet (lowest metric): %+v", got)
	}
	if got := r.forExe("chrome.exe"); got.kind != ruleNone {
		t.Errorf("unlisted exe got a rule: %+v", got)
	}
	if got := r.forExe(""); got.kind != ruleNone {
		t.Errorf("unknown owner got a rule: %+v", got)
	}
	if d := r.describe(); !strings.Contains(d, "game.exe pinned → Wi-Fi") || !strings.Contains(d, "backup.exe excluded → Ethernet") {
		t.Errorf("describe() = %q", d)
	}
}

func TestResolveRulesErrors(t *testing.T) {
	eth, wifi, ups := testUplinks()
	weights := map[*Adapter]int{eth: 1, wifi: 0}
	for _, pc := range []ProcessConfig{
		{Pin: map[string]string{"game.exe": "phone"}}, // no such line
		{Pin: map[string]string{"game.exe": "wifi"}},  // weight 0
	} {
		if _, err := resolveRules(pc, ups, weights); err == nil {
			t.Errorf("%+v: expected an error", pc)
		}
	}
	// Exclude's primary line skips weight-0 lines even if their metric is lower.
	eth.Metric, wifi.Metric = 50, 10
	r, err := resolveRules(ProcessConfig{Exclude: []string{"a.exe"}}, ups, weights)
	if err != nil || r.forExe("a.exe").line != eth {
		t.Errorf("primary should be the weighted line: %+v %v", r.forExe("a.exe"), err)
	}
}

func TestPickPinned(t *testing.T) {
	b, r, eth, wifi := rulesRig(t, ProcessConfig{Pin: map[string]string{"game.exe": "wifi"}})
	rule := r.forExe("game.exe")
	for i := 0; i < 4; i++ {
		dst := netip.AddrFrom4([4]byte{203, 0, 113, byte(i)})
		if got := b.PickFor(dst, rule); got != wifi {
			t.Fatalf("pinned flow %d went to %s", i, got.Name)
		}
	}
	// Pinned line DOWN: fall back to the other line.
	b.setHealthy(wifi, false)
	if got := b.PickFor(netip.MustParseAddr("203.0.113.50"), rule); got != eth {
		t.Errorf("pinned line down: got %s, want Ethernet", got.Name)
	}
	b.setHealthy(wifi, true)
	if got := b.PickFor(netip.MustParseAddr("203.0.113.51"), rule); got != wifi {
		t.Errorf("pinned line back: got %s, want Wi-Fi", got.Name)
	}
	if len(b.sticky) != 0 {
		t.Errorf("pinned flows wrote %d sticky entries", len(b.sticky))
	}
}

func TestPickExcluded(t *testing.T) {
	b, r, eth, wifi := rulesRig(t, ProcessConfig{Exclude: []string{"backup.exe"}})
	rule := r.forExe("backup.exe")
	for i := 0; i < 4; i++ {
		if got := b.PickFor(netip.AddrFrom4([4]byte{203, 0, 113, byte(i)}), rule); got != eth {
			t.Fatalf("excluded flow %d went to %s, want Ethernet", i, got.Name)
		}
	}
	b.setHealthy(eth, false)
	if got := b.PickFor(netip.MustParseAddr("203.0.113.9"), rule); got != wifi {
		t.Errorf("primary down: got %s, want Wi-Fi", got.Name)
	}
}

func TestPickStickyExempt(t *testing.T) {
	b, r, _, _ := rulesRig(t, ProcessConfig{StickyExempt: []string{"steam.exe"}})
	cdn := netip.MustParseAddr("203.0.113.80")

	// Many flows from steam.exe to the same CDN IP spread over both lines...
	var s strings.Builder
	for i := 0; i < 4; i++ {
		s.WriteByte(b.PickFor(cdn, r.forExe("steam.exe")).Name[0])
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

func TestProcessConfigValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "duolink.json")
	load := func(s string) (Config, error) {
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
		c, _, err := loadConfig(path)
		return c, err
	}

	c, err := load(`{}`)
	if err != nil || len(c.Processes.StickyExempt) != 1 || c.Processes.StickyExempt[0] != "steam.exe" {
		t.Fatalf("default sticky_exempt: %+v %v", c.Processes, err)
	}
	c, err = load(`{"processes": {"sticky_exempt": []}}`)
	if err != nil || len(c.Processes.StickyExempt) != 0 {
		t.Errorf("an empty list should replace the default: %+v %v", c.Processes, err)
	}
	c, err = load(`{"processes": {"pin": {"game.exe": "eth"}, "exclude": ["backup.exe"]}}`)
	if err != nil || c.Processes.Pin["game.exe"] != "eth" || c.Processes.Exclude[0] != "backup.exe" {
		t.Errorf("pin/exclude: %+v %v", c.Processes, err)
	}

	for _, bad := range []string{
		`{"processes": {"sticky_exempt": ["steam"]}}`,                                         // not an exe name
		`{"processes": {"exclude": ["C:\\Games\\game.exe"]}}`,                                 // a path
		`{"processes": {"pin": {"game.exe": ""}}}`,                                            // no line
		`{"processes": {"pin": {"steam.exe": "eth"}}}`,                                        // also in default sticky_exempt
		`{"processes": {"sticky_exempt": [], "pin": {"a.exe": "eth"}, "exclude": ["A.exe"]}}`, // two lists, any case
		`{"processes": {"pinned": {}}}`,                                                       // typo
	} {
		if _, err := load(bad); err == nil {
			t.Errorf("%s: expected an error", bad)
		}
	}
}
