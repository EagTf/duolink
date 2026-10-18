package window

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/eagtf/duolink/internal/config"
	"github.com/eagtf/duolink/internal/netif"
)

// ruleKind is what duolink does with one program's connections.
type ruleKind int

const (
	ruleExempt  ruleKind = iota // balance every connection (sticky_exempt)
	ruleExclude                 // never balance (exclude)
	rulePin                     // always one line (pin)
)

// ruleRow is one program's rule in the settings window's list.
type ruleRow struct {
	Exe  string
	Kind ruleKind
	Line string // pin target: a line name, or the configured key if no line matches
}

func (r ruleRow) label() string {
	switch r.Kind {
	case ruleExclude:
		return r.Exe + ": never balance (Windows' line)"
	case rulePin:
		return r.Exe + ": pinned to " + r.Line
	}
	return r.Exe + ": balance every connection"
}

// ruleOption is one choice in the rule dropdown.
type ruleOption struct {
	Label string
	Kind  ruleKind
	Line  string
}

// ruleOptions lists the dropdown's choices: the two plain rules, then "Pin
// to" each connected line, then any pin target in rows that isn't connected
// right now (so editing such a rule doesn't silently change it).
func ruleOptions(lines []string, rows []ruleRow) []ruleOption {
	opts := []ruleOption{
		{Label: "Balance every connection", Kind: ruleExempt},
		{Label: "Never balance (Windows' line)", Kind: ruleExclude},
	}
	seen := map[string]bool{}
	for _, l := range lines {
		opts = append(opts, ruleOption{Label: "Pin to " + l, Kind: rulePin, Line: l})
		seen[l] = true
	}
	for _, r := range rows {
		if r.Kind == rulePin && !seen[r.Line] {
			opts = append(opts, ruleOption{Label: "Pin to " + r.Line + " (not connected)", Kind: rulePin, Line: r.Line})
			seen[r.Line] = true
		}
	}
	return opts
}

// optionIndex finds the dropdown choice that matches row, or 0.
func optionIndex(opts []ruleOption, row ruleRow) int {
	for i, o := range opts {
		if o.Kind == row.Kind && o.Line == row.Line {
			return i
		}
	}
	return 0
}

// rowsFromConfig lists the configured rules, sorted by program. A pin's
// key ("eth") is shown as the line it matches now ("Ethernet"), or as is
// if no connected line matches.
func rowsFromConfig(pc config.ProcessConfig, lines []string) []ruleRow {
	ads := make([]*netif.Adapter, len(lines))
	for i, l := range lines {
		ads[i] = &netif.Adapter{Name: l}
	}
	var rows []ruleRow
	for _, e := range pc.StickyExempt {
		rows = append(rows, ruleRow{Exe: e, Kind: ruleExempt})
	}
	for _, e := range pc.Exclude {
		rows = append(rows, ruleRow{Exe: e, Kind: ruleExclude})
	}
	for e, key := range pc.Pin {
		line := key
		if a, err := netif.Find(ads, key); err == nil {
			line = a.Name
		}
		rows = append(rows, ruleRow{Exe: e, Kind: rulePin, Line: line})
	}
	sort.Slice(rows, func(i, j int) bool { return strings.ToLower(rows[i].Exe) < strings.ToLower(rows[j].Exe) })
	return rows
}

// rowsToConfig turns the list back into duolink.json's three lists.
func rowsToConfig(rows []ruleRow) config.ProcessConfig {
	pc := config.ProcessConfig{StickyExempt: []string{}, Exclude: []string{}, Pin: map[string]string{}}
	for _, r := range rows {
		switch r.Kind {
		case ruleExempt:
			pc.StickyExempt = append(pc.StickyExempt, r.Exe)
		case ruleExclude:
			pc.Exclude = append(pc.Exclude, r.Exe)
		case rulePin:
			pc.Pin[r.Exe] = r.Line
		}
	}
	return pc
}

// exeName turns what the user typed or picked into an exe file name: a full
// path becomes its file name, and the name must end in .exe.
func exeName(s string) (string, error) {
	s = strings.Trim(strings.TrimSpace(s), `"`)
	if s == "" {
		return "", errors.New("Program: pick one with Browse… or type its file name, like game.exe")
	}
	s = filepath.Base(s)
	if !strings.HasSuffix(strings.ToLower(s), ".exe") {
		return "", fmt.Errorf("Program: %q should be a program file name ending in .exe, like game.exe", s)
	}
	return s, nil
}

// upsertRule adds r, or replaces the rule for the same program (names are
// case-insensitive), so a program never has two rules. It returns the list
// and the index of r in it, sorted by program.
func upsertRule(rows []ruleRow, r ruleRow) ([]ruleRow, int) {
	out := make([]ruleRow, 0, len(rows)+1)
	for _, x := range rows {
		if !strings.EqualFold(x.Exe, r.Exe) {
			out = append(out, x)
		}
	}
	out = append(out, r)
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Exe) < strings.ToLower(out[j].Exe) })
	for i, x := range out {
		if x.Exe == r.Exe {
			return out, i
		}
	}
	return out, 0
}
