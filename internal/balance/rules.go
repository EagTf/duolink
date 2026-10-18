package balance

import (
	"fmt"
	"sort"
	"strings"

	"github.com/eagtf/duolink/internal/config"
	"github.com/eagtf/duolink/internal/netif"
)

type RuleKind int

const (
	RuleNone         RuleKind = iota
	RuleStickyExempt          // balanced per flow, stickiness ignored
	RulePin                   // always the line Rule.Pin names, while it's usable
	RuleExclude               // always the primary line while it's usable
)

func (k RuleKind) String() string {
	return [...]string{"", "sticky-exempt", "pinned", "excluded"}[k]
}

// Rule is what the process rules say about one flow. The zero value
// means "no rule": normal balancing with stickiness.
type Rule struct {
	Kind RuleKind
	// Pin is the configured line name for RulePin ("eth"). It is matched
	// against the current lines at every pick, so a pin follows a line whose
	// adapter was replaced; the balancer resolves "exclude" the same way.
	Pin string
}

// Rules maps exe names to rules, resolved against the uplinks at startup.
type Rules struct {
	byExe   map[string]Rule
	display map[string]string // exe → line name at startup, for Describe
}

// ResolveRules checks the configured rules against the actual uplinks.
// The primary line for "exclude" is the weighted line with the lowest
// interface metric: the one Windows itself would send this traffic through
// without duolink. Pins must point at a line with a non-zero weight, since
// lines with weight 0 aren't health-checked.
func ResolveRules(pc config.ProcessConfig, ups []*netif.Adapter, weights map[*netif.Adapter]int) (*Rules, error) {
	var primary *netif.Adapter
	for _, a := range ups {
		if weights[a] > 0 && (primary == nil || a.Metric < primary.Metric) {
			primary = a
		}
	}
	r := &Rules{byExe: map[string]Rule{}, display: map[string]string{}}
	for _, e := range pc.StickyExempt {
		r.byExe[strings.ToLower(e)] = Rule{Kind: RuleStickyExempt}
	}
	for e, name := range pc.Pin {
		a, err := netif.Find(ups, name)
		if err != nil {
			return nil, fmt.Errorf("processes.pin.%s: %w", e, err)
		}
		if weights[a] == 0 {
			return nil, fmt.Errorf("processes.pin.%s: %s has weight 0, so it isn't used; give it a weight or pin elsewhere", e, a.Name)
		}
		r.byExe[strings.ToLower(e)] = Rule{Kind: RulePin, Pin: name}
		r.display[strings.ToLower(e)] = a.Name
	}
	for _, e := range pc.Exclude {
		r.byExe[strings.ToLower(e)] = Rule{Kind: RuleExclude}
		if primary != nil {
			r.display[strings.ToLower(e)] = primary.Name
		}
	}
	return r, nil
}

func (r *Rules) Empty() bool { return len(r.byExe) == 0 }

// ForExe returns the rule for a lower-case exe name ("" = unknown → no rule).
func (r *Rules) ForExe(exe string) Rule { return r.byExe[exe] }

// Describe returns a one-line summary for the startup output.
func (r *Rules) Describe() string {
	if r.Empty() {
		return "none"
	}
	var parts []string
	for _, exe := range r.Exes() {
		rule := r.byExe[exe]
		s := exe + " " + rule.Kind.String()
		if name := r.display[exe]; name != "" {
			s += " → " + name
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ", ")
}

func (r *Rules) Exes() []string {
	var out []string
	for exe := range r.byExe {
		out = append(out, exe)
	}
	sort.Strings(out)
	return out
}
