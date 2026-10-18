package main

import (
	"fmt"
	"sort"
	"strings"
)

type ruleKind int

const (
	ruleNone         ruleKind = iota
	ruleStickyExempt          // balanced per flow, stickiness ignored
	rulePin                   // always rule.line while it's usable
	ruleExclude               // always the primary line while it's usable
)

func (k ruleKind) String() string {
	return [...]string{"", "sticky-exempt", "pinned", "excluded"}[k]
}

// flowRule is what the process rules say about one flow. The zero value
// means "no rule": normal balancing with stickiness.
type flowRule struct {
	kind ruleKind
	line *Adapter // pin target, or the primary line for exclude
}

// procRules maps exe names to rules, resolved against the uplinks at startup.
type procRules struct {
	byExe map[string]flowRule
}

// resolveRules checks the configured rules against the actual uplinks.
// The primary line for "exclude" is the weighted line with the lowest
// interface metric: the one Windows itself would send this traffic through
// without duolink. Pins must point at a line with a non-zero weight, since
// lines with weight 0 aren't health-checked.
func resolveRules(pc ProcessConfig, ups []*Adapter, weights map[*Adapter]int) (*procRules, error) {
	var primary *Adapter
	for _, a := range ups {
		if weights[a] > 0 && (primary == nil || a.Metric < primary.Metric) {
			primary = a
		}
	}
	r := &procRules{byExe: map[string]flowRule{}}
	for _, e := range pc.StickyExempt {
		r.byExe[strings.ToLower(e)] = flowRule{kind: ruleStickyExempt}
	}
	for e, name := range pc.Pin {
		a, err := findAdapter(ups, name)
		if err != nil {
			return nil, fmt.Errorf("processes.pin.%s: %w", e, err)
		}
		if weights[a] == 0 {
			return nil, fmt.Errorf("processes.pin.%s: %s has weight 0, so it isn't used; give it a weight or pin elsewhere", e, a.Name)
		}
		r.byExe[strings.ToLower(e)] = flowRule{kind: rulePin, line: a}
	}
	for _, e := range pc.Exclude {
		r.byExe[strings.ToLower(e)] = flowRule{kind: ruleExclude, line: primary}
	}
	return r, nil
}

func (r *procRules) empty() bool { return len(r.byExe) == 0 }

// forExe returns the rule for a lower-case exe name ("" = unknown → no rule).
func (r *procRules) forExe(exe string) flowRule { return r.byExe[exe] }

// describe returns a one-line summary for the startup output.
func (r *procRules) describe() string {
	if r.empty() {
		return "none"
	}
	var parts []string
	for _, exe := range r.exes() {
		rule := r.byExe[exe]
		s := exe + " " + rule.kind.String()
		if rule.line != nil {
			s += " → " + rule.line.Name
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ", ")
}

func (r *procRules) exes() []string {
	var out []string
	for exe := range r.byExe {
		out = append(out, exe)
	}
	sort.Strings(out)
	return out
}
