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
	if !f.StickyOn || f.StickyMin != "10" || f.Timeout != "5" || f.Failures != "2" || len(f.Rules) != 1 || f.Rules[0] != (ruleRow{Exe: "steam.exe", Kind: ruleExempt}) {
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

// Automatic weights are the default; the checkbox decides, and weights set
// by hand turn it off (they win, as when loading the file).
func TestFormAutoWeights(t *testing.T) {
	f := formFromConfig(config.Default(), lines)
	if !f.AutoWeights {
		t.Fatal("defaults should show automatic weights")
	}
	f.Weights = []string{"5", "oops"} // ignored while automatic
	c, err := f.toConfig()
	if err != nil || !c.AutoWeights || len(c.Weights) != 0 || !c.AutoWeighted() {
		t.Errorf("automatic: saved %v, auto %v, err %v", c.Weights, c.AutoWeights, err)
	}

	f.AutoWeights = false
	f.Weights = []string{"1", "1"}
	c, err = f.toConfig()
	if err != nil || c.AutoWeights || len(c.Weights) != 0 || c.AutoWeighted() {
		t.Errorf("by hand, all 1: saved %v, auto %v, err %v", c.Weights, c.AutoWeights, err)
	}
	if formFromConfig(c, lines).AutoWeights {
		t.Error("automatic weights turned off should stay off")
	}

	hand := config.Default()
	hand.Weights = map[string]int{"eth": 2, "wifi": 1}
	if formFromConfig(hand, lines).AutoWeights {
		t.Error("weights set by hand win over automatic weights")
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

func TestFormErrors(t *testing.T) {
	base := formFromConfig(config.Default(), lines)
	cases := []struct {
		edit func(f *form)
		want string // part of the error
	}{
		{func(f *form) { f.StickyMin = "ten" }, "Stickiness minutes"},
		{func(f *form) { f.AutoWeights = false; f.Weights[1] = "" }, "Wi-Fi weight"},
		{func(f *form) { f.Failures = "0" }, "DOWN after (misses): must be 1..10"},
		{func(f *form) { f.Targets = "one.one.one.one:443" }, "Targets:"},
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

func TestFormReconnectSetting(t *testing.T) {
	f := formFromConfig(config.Default(), lines)
	if !f.Reconnect {
		t.Fatal("reconnect after outage should default to on")
	}
	f.Reconnect = false
	c, err := f.toConfig()
	if err != nil || c.Processes.ReconnectAfterOutage {
		t.Errorf("turning it off was lost: %v %v", c.Processes.ReconnectAfterOutage, err)
	}
	f.Reconnect = true
	f.Rules = append(f.Rules, ruleRow{Exe: "game.exe", Kind: rulePin, Line: "Ethernet"})
	c, _ = f.toConfig()
	if !c.Processes.ReconnectAfterOutage || c.Processes.Pin["game.exe"] != "Ethernet" {
		t.Errorf("rules and the setting must both survive: %+v", c.Processes)
	}
}
