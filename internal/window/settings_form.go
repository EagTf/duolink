package window

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/eagtf/duolink/internal/config"
	"github.com/eagtf/duolink/internal/netif"
)

// maxFormLines is how many uplinks get a weight field.
const maxFormLines = 4

// form is the settings window's fields as text, the way the user sees and
// types them. formFromConfig and toConfig convert to and from duolink.json.
type form struct {
	Lines   []string // uplink names, one weight field each
	Weights []string

	StickyOn  bool
	StickyMin string

	Interval, DownInterval, Timeout, Failures string
	Targets                                   string // "1.1.1.1:443, 8.8.8.8:443"

	UDPIdle string

	Rules     []ruleRow // per-app rules, one per program
	Reconnect bool      // processes.reconnect_after_outage
}

// formFromConfig fills the form from c, with one weight field per line in
// lines (the uplinks connected now). Weights in c are matched to lines the
// same way duolink does ("eth" → Ethernet); with no weights in c every line
// shows 1, otherwise lines c doesn't name show 0.
func formFromConfig(c config.Config, lines []string) form {
	if len(lines) > maxFormLines {
		lines = lines[:maxFormLines]
	}
	f := form{
		Lines:        lines,
		StickyOn:     c.Sticky.Enabled,
		StickyMin:    strconv.Itoa(c.Sticky.Minutes),
		Interval:     strconv.Itoa(c.Health.IntervalSeconds),
		DownInterval: strconv.Itoa(c.Health.DownIntervalSeconds),
		Timeout:      strconv.Itoa(c.Health.TimeoutSeconds),
		Failures:     strconv.Itoa(c.Health.FailuresBeforeDown),
		Targets:      strings.Join(c.Health.Targets, ", "),
		UDPIdle:      strconv.Itoa(c.UDPIdleSeconds),
		Rules:        rowsFromConfig(c.Processes, lines),
		Reconnect:    c.Processes.ReconnectAfterOutage,
	}
	adapters := make([]*netif.Adapter, len(lines))
	for i, name := range lines {
		adapters[i] = &netif.Adapter{Name: name}
	}
	weight := map[*netif.Adapter]int{}
	for key, w := range c.Weights {
		if a, err := netif.Find(adapters, key); err == nil {
			weight[a] = w
		}
	}
	for _, a := range adapters {
		w, ok := weight[a]
		if !ok && len(c.Weights) == 0 {
			w = 1
		}
		f.Weights = append(f.Weights, strconv.Itoa(w))
	}
	return f
}

// toConfig parses the form into a config and validates it. Errors name the
// field, so they can be shown as is.
func (f form) toConfig() (config.Config, error) {
	c := config.Default()
	var errs []error
	num := func(field, s string) int {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %q is not a whole number", field, s))
		}
		return n
	}

	// Weights: all 1 means "every line equally", saved as no weights at
	// all, so it keeps working when a line is renamed or replaced (a new
	// Wi-Fi adapter). Anything else is saved per line, by full name.
	c.Weights = map[string]int{}
	allOne := true
	for i, line := range f.Lines {
		w := num(line+" weight", f.Weights[i])
		c.Weights[line] = w
		allOne = allOne && w == 1
	}
	if allOne {
		c.Weights = map[string]int{}
	}

	c.Sticky.Enabled = f.StickyOn
	c.Sticky.Minutes = num("Stickiness minutes", f.StickyMin)
	c.Health.IntervalSeconds = num("Check every", f.Interval)
	c.Health.DownIntervalSeconds = num("While DOWN, check every", f.DownInterval)
	c.Health.TimeoutSeconds = num("Timeout", f.Timeout)
	c.Health.FailuresBeforeDown = num("DOWN after misses", f.Failures)
	c.Health.Targets = splitList(f.Targets)
	c.UDPIdleSeconds = num("UDP idle", f.UDPIdle)

	c.Processes = rowsToConfig(f.Rules)
	c.Processes.ReconnectAfterOutage = f.Reconnect // not part of the rule list

	if len(errs) > 0 {
		return c, errors.Join(errs...)
	}
	if err := c.Validate(); err != nil {
		return c, friendly(err)
	}
	return c, nil
}

// friendly rewrites config validation errors, which name duolink.json's
// keys, in terms of this window's fields.
func friendly(err error) error {
	return errors.New(fieldNames.Replace(err.Error()))
}

var fieldNames = strings.NewReplacer(
	// The steam.exe hint explains the file format; in the window it's simpler.
	`(steam.exe is in sticky_exempt by default; set "sticky_exempt": [] to remove it)`, `(remove it from one of the two lists)`,
	"health.down_interval_seconds", "While DOWN, check every",
	"health.interval_seconds", "Check each line every",
	"health.timeout_seconds", "Timeout",
	"health.failures_before_down", "DOWN after (misses)",
	"health.targets", "Targets",
	"sticky.minutes", "Stickiness minutes",
	"udp_idle_seconds", "UDP flow ends after",
	"processes.sticky_exempt", "Balance every connection",
	"processes.exclude", "Never balance",
	"processes.pin.", "Pin ",
	"processes.pin", "Pin",
	"processes:", "Per-app rules:",
	"sticky_exempt", `"Balance every connection"`,
	" exclude", ` "Never balance"`,
	" pin", ` "Pin to a line"`,
	"weights.", "Weight for ",
)

// splitList splits on commas, spaces and line breaks.
func splitList(s string) []string {
	out := []string{}
	for _, f := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\r' || r == '\n'
	}) {
		out = append(out, f)
	}
	return out
}
