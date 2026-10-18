// Package config loads and validates duolink.json.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config is duolink.json. Every field has a default, so the file only needs the
// settings you want to change. Unknown fields are an error, to catch typos.
type Config struct {
	// Flow share per uplink, e.g. {"eth": 1, "wifi": 1}. Keys match adapter
	// names like --weights does. Empty = every usable uplink gets 1.
	Weights map[string]int `json:"weights"`
	// With no Weights set, each line's share follows its measured speed
	// (see AutoWeighted). Weights set by hand always win.
	AutoWeights bool `json:"auto_weights"`

	Sticky    StickyConfig  `json:"sticky"`
	Health    HealthConfig  `json:"health"`
	Processes ProcessConfig `json:"processes"`

	// A UDP flow ends after this long with no packets in either direction.
	UDPIdleSeconds int `json:"udp_idle_seconds"`
}

// StickyConfig: new flows to a destination IP seen in the last Minutes go out
// the same line as before, so a site or game server sees one public IP.
// The timer restarts on every new flow to that destination.
type StickyConfig struct {
	Enabled bool `json:"enabled"`
	Minutes int  `json:"minutes"`
}

// ProcessConfig holds per-app rules, keyed by exe file name (case-insensitive,
// e.g. "steam.exe"). Each exe may appear in only one of them.
type ProcessConfig struct {
	// Balanced per flow even with stickiness on: for apps that download over
	// many connections to the same few servers.
	StickyExempt []string `json:"sticky_exempt"`
	// exe → line (matched like weights: "eth", "wifi"). Always that line;
	// balanced normally while it's DOWN.
	Pin map[string]string `json:"pin"`
	// Not balanced: always the line Windows itself would use (lowest
	// interface metric), or another one while that's DOWN.
	Exclude []string `json:"exclude"`
	// When a line comes back after an outage, close the connections that
	// StickyExempt apps opened during it, so they reconnect over all lines.
	// For download apps (Steam) that keep reusing their connections: without
	// this their download stays on the line that survived.
	ReconnectAfterOutage bool `json:"reconnect_after_outage"`
}

type HealthConfig struct {
	IntervalSeconds     int      `json:"interval_seconds"`      // probe interval while a line is up
	DownIntervalSeconds int      `json:"down_interval_seconds"` // ...and while it's down
	TimeoutSeconds      int      `json:"timeout_seconds"`       // per probe (targets are tried in parallel)
	Targets             []string `json:"targets"`               // ip:port, TCP connect only
	// Regular probes that must fail in a row before a line goes DOWN. A
	// single miss is common on a saturated line (router buffers full,
	// latency spikes), so one isn't enough. 1 = the first failure.
	FailuresBeforeDown int `json:"failures_before_down"`
}

// Default returns the built-in settings that duolink.json is applied on top of.
func Default() Config {
	return Config{
		Weights:     map[string]int{},
		AutoWeights: true,
		Sticky:      StickyConfig{Enabled: true, Minutes: 10},
		Health: HealthConfig{
			IntervalSeconds:     15,
			DownIntervalSeconds: 4,
			TimeoutSeconds:      5,
			Targets:             []string{"1.1.1.1:443", "8.8.8.8:443"},
			FailuresBeforeDown:  2,
		},
		Processes: ProcessConfig{
			StickyExempt:         []string{"steam.exe"},
			Pin:                  map[string]string{},
			Exclude:              []string{},
			ReconnectAfterOutage: true,
		},
		UDPIdleSeconds: 60,
	}
}

// StickyTTL is how long a destination stays on its line; 0 = stickiness off.
func (c *Config) StickyTTL() time.Duration {
	if !c.Sticky.Enabled {
		return 0
	}
	return time.Duration(c.Sticky.Minutes) * time.Minute
}

// AutoWeighted reports whether line shares follow measured speed: automatic
// weights are on and no weights were set by hand.
func (c *Config) AutoWeighted() bool { return c.AutoWeights && len(c.Weights) == 0 }

// UDPIdle is the UDP idle timeout as a duration.
func (c *Config) UDPIdle() time.Duration {
	return time.Duration(c.UDPIdleSeconds) * time.Second
}

// Validate reports every invalid setting at once.
func (c *Config) Validate() error {
	var errs []error
	check := func(ok bool, format string, args ...any) {
		if !ok {
			errs = append(errs, fmt.Errorf(format, args...))
		}
	}
	for k, v := range c.Weights {
		check(v >= 0 && v <= 100, "weights.%s: must be 0..100, got %d", k, v)
	}
	if c.Sticky.Enabled {
		check(c.Sticky.Minutes >= 1 && c.Sticky.Minutes <= 1440, "sticky.minutes: must be 1..1440, got %d", c.Sticky.Minutes)
	}
	h := c.Health
	check(h.IntervalSeconds >= 1, "health.interval_seconds: must be at least 1")
	check(h.DownIntervalSeconds >= 1, "health.down_interval_seconds: must be at least 1")
	check(h.TimeoutSeconds >= 1, "health.timeout_seconds: must be at least 1")
	check(h.FailuresBeforeDown >= 1 && h.FailuresBeforeDown <= 10, "health.failures_before_down: must be 1..10, got %d", h.FailuresBeforeDown)
	check(len(h.Targets) > 0, "health.targets: need at least one ip:port")
	for _, t := range h.Targets {
		ap, err := netip.ParseAddrPort(t)
		check(err == nil && ap.Addr().Is4(), "health.targets: %q is not an IPv4 ip:port", t)
	}
	check(c.UDPIdleSeconds >= 5, "udp_idle_seconds: must be at least 5")

	seen := map[string]string{} // exe → list it's in
	addExe := func(list, exe string) {
		e := strings.ToLower(exe)
		check(strings.HasSuffix(e, ".exe") && !strings.ContainsAny(e, `\/`),
			"processes.%s: %q must be an exe file name like steam.exe", list, exe)
		if prev, dup := seen[e]; dup {
			hint := ""
			if prev == "sticky_exempt" && e == "steam.exe" {
				hint = ` (steam.exe is in sticky_exempt by default; set "sticky_exempt": [] to remove it)`
			}
			check(false, "processes: %s is in both %s and %s; use one%s", exe, prev, list, hint)
		}
		seen[e] = list
	}
	p := c.Processes
	for _, e := range p.StickyExempt {
		addExe("sticky_exempt", e)
	}
	for e, line := range p.Pin {
		addExe("pin", e)
		check(line != "", "processes.pin.%s: needs a line name, e.g. \"eth\"", e)
	}
	for _, e := range p.Exclude {
		addExe("exclude", e)
	}
	return errors.Join(errs...)
}

// DefaultPath is duolink.json next to duolink.exe, like wintun.dll.
func DefaultPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "duolink.json"
	}
	return filepath.Join(filepath.Dir(exe), "duolink.json")
}

// Load reads path on top of the defaults. A missing file is fine (defaults
// only, found = false); a broken one is an error, never silently ignored.
func Load(path string) (c Config, found bool, err error) {
	c = Default()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, false, nil
	}
	if err != nil {
		return c, false, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, true, fmt.Errorf("%s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return c, true, fmt.Errorf("%s:\n%w", path, err)
	}
	return c, true, nil
}

// ParseWeightSpec turns the --weights text "eth=2,wifi=1" into a name → weight
// map, the same shape as "weights" in duolink.json.
func ParseWeightSpec(spec string) (map[string]int, error) {
	w := map[string]int{}
	for _, part := range strings.Split(spec, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("bad weight %q: want name=number, e.g. eth=1", part)
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("bad weight %q: %q is not a number", part, v)
		}
		w[k] = n
	}
	return w, nil
}

// Save writes c to path as indented JSON (the format `duolink config --init`
// writes). It validates first, and writes to a temporary file that then
// replaces path, so a failure never leaves a half-written duolink.json.
func Save(path string, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
