package window

import (
	"fmt"
	"strings"

	"github.com/eagtf/duolink/internal/engine"
)

// State is everything the window shows.
type State struct {
	Status      string       // top line, e.g. "Running" or "Paused"
	ToggleLabel string       // "Pause" or "Start"
	Running     bool         // Stats is current
	Stats       engine.Stats // latest sample while running
	Events      []string     // recent events, oldest first
}

// maxEvents is how many recent events the window lists.
const maxEvents = 12

// Format renders the main text area: the per-line table, totals, per-app
// rule counts and recent events (newest first). Windows edit controls need
// \r\n line breaks.
func Format(s State) string {
	var b strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\r\n", args...) }

	if s.Running && len(s.Stats.Lines) > 0 {
		weight := "Weight"
		if s.Stats.AutoWeights {
			weight = "Auto" // 5..100 from measured speed
		}
		line("%-12s %-5s %6s %12s %10s %5s %5s %8s %7s %8s", "Line", "State", weight, "Down Mbit/s", "Up Mbit/s", "TCP", "UDP", "Flows", "Sticky", "PC down")
		var pc float64
		for _, l := range s.Stats.Lines {
			line("%-12s %-5s %6d %12.1f %10.1f %5d %5d %8d %7d %8.1f", l.Name, l.State, l.Weight, l.DownMbps, l.UpMbps, l.TCP, l.UDP, l.Flows, l.Sticky, l.PCDownMbps)
			pc += l.PCDownMbps
		}
		line("%-12s %-5s %6s %12.1f %10.1f %5s %5s %8s %7s %8.1f", "Total", "", "", s.Stats.DownMbps, s.Stats.UpMbps, "", "", "", "", pc)
		line("")
		line("PC down: the adapter's whole download as Windows counts it, all apps.")
		if s.Stats.AutoWeights {
			line("Auto: shares from measured speed, fastest line 100 (equal until measured).")
		}
		if s.Stats.Rules != "" {
			line("")
			line("%s", s.Stats.Rules)
		}
		if s.Stats.Retried > 0 {
			line("Connections saved by trying the other line: %d", s.Stats.Retried)
		}
	} else {
		line("duolink is not balancing right now.")
	}

	line("")
	line("Recent events")
	if len(s.Events) == 0 {
		line("  (none yet)")
	}
	for i := len(s.Events) - 1; i >= 0 && i >= len(s.Events)-maxEvents; i-- {
		line("  %s", s.Events[i])
	}
	return b.String()
}
