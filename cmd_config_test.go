package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// Flags override the file; only flags actually given count.
func TestConfigFlagsOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "duolink.json")
	if err := os.WriteFile(path, []byte(`{"sticky": {"minutes": 3}, "weights": {"eth": 2}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	cf := addConfigFlags(fs)
	fs.Parse([]string{"--config", path, "--sticky-minutes", "0"})
	c, _, err := cf.load()
	if err != nil || c.Sticky.Enabled || c.Weights["eth"] != 2 {
		t.Errorf("sticky override: %+v %v", c, err)
	}

	fs = flag.NewFlagSet("t", flag.ContinueOnError)
	cf = addConfigFlags(fs)
	fs.Parse([]string{"--config", path, "--weights", "wifi=1"})
	c, _, err = cf.load()
	if err != nil || c.Weights["eth"] != 0 || c.Weights["wifi"] != 1 || c.Sticky.Minutes != 3 {
		t.Errorf("weights override: %+v %v", c, err)
	}

	fs = flag.NewFlagSet("t", flag.ContinueOnError)
	cf = addConfigFlags(fs)
	fs.Parse([]string{"--config", path})
	c, _, err = cf.load()
	if err != nil || c.Sticky.Minutes != 3 || c.Weights["eth"] != 2 {
		t.Errorf("no flags should keep the file's values: %+v %v", c, err)
	}
}
