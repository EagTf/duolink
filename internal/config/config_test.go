package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "duolink.json")
	write := func(s string) {
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Missing file: defaults.
	c, found, err := Load(path)
	if err != nil || found || !c.Sticky.Enabled || c.Health.IntervalSeconds != 15 {
		t.Fatalf("missing file: %+v %v %v", c, found, err)
	}

	// Partial file: only the given fields change.
	write(`{"sticky": {"minutes": 3}, "weights": {"eth": 2}}`)
	c, found, err = Load(path)
	if err != nil || !found || c.Sticky.Minutes != 3 || !c.Sticky.Enabled || c.Weights["eth"] != 2 || c.Health.TimeoutSeconds != 5 || c.Health.FailuresBeforeDown != 2 {
		t.Fatalf("partial file: %+v %v %v", c, found, err)
	}

	// Typos and bad values are errors, never silently ignored.
	for _, bad := range []string{
		`{"stickey": {"minutes": 3}}`,
		`{"sticky": {"minutes": 0}}`,
		`{"health": {"targets": ["one.one.one.one:443"]}}`,
		`{"weights": {"eth": 500}}`,
		`{"health": {"failures_before_down": 0}}`,
		`{not json`,
	} {
		write(bad)
		if _, _, err := Load(path); err == nil {
			t.Errorf("%s: expected an error", bad)
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
		c, _, err := Load(path)
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

func TestParseWeightSpec(t *testing.T) {
	w, err := ParseWeightSpec("eth=2, wifi=1")
	if err != nil || w["eth"] != 2 || w["wifi"] != 1 {
		t.Fatalf("got %v, %v", w, err)
	}
	for _, bad := range []string{"eth", "eth=x", "=1"} {
		if _, err := ParseWeightSpec(bad); err == nil {
			t.Errorf("%q: expected a parse error", bad)
		}
	}
}

func TestSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "duolink.json")
	c := Default()
	c.Weights = map[string]int{"Ethernet": 2, "Wi-Fi": 1}
	c.Sticky.Minutes = 20
	c.Processes.Pin = map[string]string{"discord.exe": "eth"}
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	got, found, err := Load(path)
	if err != nil || !found {
		t.Fatalf("Load after Save: %v %v", found, err)
	}
	if got.Weights["Ethernet"] != 2 || got.Sticky.Minutes != 20 || got.Processes.Pin["discord.exe"] != "eth" {
		t.Errorf("round trip lost values: %+v", got)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("temporary file left behind")
	}
}

func TestSaveRejectsInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "duolink.json")
	if err := os.WriteFile(path, []byte(`{"sticky": {"minutes": 7}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	c := Default()
	c.Sticky.Minutes = 0 // invalid while enabled
	if err := Save(path, c); err == nil {
		t.Fatal("Save accepted an invalid config")
	}
	// The existing file is untouched.
	if b, _ := os.ReadFile(path); string(b) != `{"sticky": {"minutes": 7}}` {
		t.Errorf("file changed: %s", b)
	}
}
