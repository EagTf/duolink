package window

import (
	"errors"
	"strings"
	"testing"

	"github.com/eagtf/duolink/internal/config"
)

var lines = []string{"Ethernet", "Wi-Fi"}

func TestFormRoundTripDefaults(t *testing.T) {
	f := formFromConfig(config.Default(), lines)
	if f.Weights[0] != "1" || f.Weights[1] != "1" {
		t.Errorf("no weights in config should show 1 each, got %v", f.Weights)
	}
	if !f.StickyOn || f.StickyMin != "10" || f.Timeout != "5" || f.Failures != "2" || f.Exempt != "steam.exe" {
		t.Errorf("defaults not shown: %+v", f)
	}
	c, err := f.toConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Weights) != 0 {
		t.Errorf("all weights 1 should save as no weights, got %v", c.Weights)
	}
	if c.Health.TimeoutSeconds != 5 || len(c.Health.Targets) != 2 || c.Processes.StickyExempt[0] != "steam.exe" {
		t.Errorf("round trip changed values: %+v", c)
	}
}

func TestFormWeightsMatchedByPrefix(t *testing.T) {
	c := config.Default()
	c.Weights = map[string]int{"eth": 2} // as people write them in the file
	f := formFromConfig(c, lines)
	if f.Weights[0] != "2" || f.Weights[1] != "0" {
		t.Errorf("eth=2 should show Ethernet 2, Wi-Fi 0 (unnamed), got %v", f.Weights)
	}
	f.Weights = []string{"3", "1"}
	got, err := f.toConfig()
	if err != nil || got.Weights["Ethernet"] != 3 || got.Weights["Wi-Fi"] != 1 {
		t.Errorf("weights saved as %v, %v", got.Weights, err)
	}
}

func TestFormRules(t *testing.T) {
	f := formFromConfig(config.Default(), lines)
	f.Exempt = "steam.exe\r\n\r\nepicgameslauncher.exe"
	f.Exclude = "backup.exe, sync.exe"
	f.Pin = "discord.exe = eth\r\n  game.exe=wifi  \r\n"
	c, err := f.toConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Processes.StickyExempt) != 2 || len(c.Processes.Exclude) != 2 {
		t.Errorf("lists: %+v", c.Processes)
	}
	if c.Processes.Pin["discord.exe"] != "eth" || c.Processes.Pin["game.exe"] != "wifi" {
		t.Errorf("pins: %v", c.Processes.Pin)
	}
	// And back: pins shown sorted, one per line.
	back := formFromConfig(c, lines)
	if back.Pin != "discord.exe = eth\r\ngame.exe = wifi" {
		t.Errorf("pin text = %q", back.Pin)
	}
}

func TestFormErrors(t *testing.T) {
	base := formFromConfig(config.Default(), lines)
	cases := []struct {
		edit func(f *form)
		want string // part of the error
	}{
		{func(f *form) { f.StickyMin = "ten" }, "Stickiness minutes"},
		{func(f *form) { f.Weights[1] = "" }, "Wi-Fi weight"},
		{func(f *form) { f.Pin = "discord.exe" }, "Pin, line 1"},
		{func(f *form) { f.Failures = "0" }, "DOWN after (misses): must be 1..10"},
		{func(f *form) { f.Targets = "one.one.one.one:443" }, "Targets:"},
		{func(f *form) { f.Exclude = "steam.exe" }, `steam.exe is in both "Balance every connection" and "Never balance"; use one (remove it from one of the two lists)`},
		{func(f *form) { f.Pin = "chrome.exe = " }, "Pin, line 1"},
	}
	for i, c := range cases {
		f := base
		f.Weights = append([]string(nil), base.Weights...)
		c.edit(&f)
		_, err := f.toConfig()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("case %d: error %v, want it to mention %q", i, err, c.want)
		}
	}
}

// Errors from the app's checks against the real lines use the file's key
// names too; the window shows them in its own terms.
func TestFriendlyRealLineErrors(t *testing.T) {
	err := friendly(errors.New(`processes.pin.game.exe: no usable uplink matches "phone" (see ` + "`duolink list`)"))
	if !strings.HasPrefix(err.Error(), `Pin game.exe: no usable uplink matches "phone"`) {
		t.Errorf("got %q", err)
	}
}
