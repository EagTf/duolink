package window

import (
	"fmt"
	"strings"
	"testing"

	"github.com/eagtf/duolink/internal/engine"
)

func TestFormatRunning(t *testing.T) {
	s := State{
		Running: true,
		Stats: engine.Stats{
			Lines: []engine.LineStats{
				{Name: "Ethernet", State: "UP", Weight: 1, DownMbps: 21.34, UpMbps: 0.8, TCP: 12, UDP: 2, Flows: 140, Sticky: 31},
				{Name: "Wi-Fi", State: "DOWN", Weight: 1, DownMbps: 0, UpMbps: 0, Flows: 9},
			},
			DownMbps: 21.34, UpMbps: 0.8,
			Rules: "rules (flows): steam.exe sticky-exempt 120",
		},
		Events: []string{"15:14:05 Wi-Fi is DOWN (adapter disconnected)"},
	}
	out := Format(s)
	for _, want := range []string{"Down Mbit/s", "Ethernet", "21.3", "Wi-Fi", "DOWN", "Total", "steam.exe sticky-exempt 120", "Wi-Fi is DOWN"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(strings.ReplaceAll(out, "\r\n", ""), "\n") {
		t.Error("edit controls need \r\n line breaks, found a bare \n")
	}
}

func TestFormatStoppedAndEventOrder(t *testing.T) {
	var events []string
	for i := 1; i <= 20; i++ {
		events = append(events, fmt.Sprintf("event %02d", i))
	}
	out := Format(State{Events: events})
	if !strings.Contains(out, "not balancing") || strings.Contains(out, "Down Mbit/s") {
		t.Errorf("stopped state should show no table:\n%s", out)
	}
	// Newest first, and only the last maxEvents.
	if strings.Index(out, "event 20") > strings.Index(out, "event 19") {
		t.Error("newest event should come first")
	}
	if strings.Contains(out, "event 08") || !strings.Contains(out, "event 09") {
		t.Errorf("should list exactly the last %d events:\n%s", maxEvents, out)
	}
}

func TestFormatAutoWeightsAndPCTraffic(t *testing.T) {
	s := State{Running: true, Stats: engine.Stats{
		AutoWeights: true,
		Lines: []engine.LineStats{
			{Name: "Ethernet", State: "UP", Weight: 100, DownMbps: 20, PCDownMbps: 22.5},
			{Name: "Wi-Fi", State: "UP", Weight: 48, DownMbps: 10, PCDownMbps: 10.25},
		},
		DownMbps: 30,
	}}
	out := Format(s)
	for _, want := range []string{"Auto", "PC down", "22.5", "32.8", "fastest line 100"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	s.Stats.AutoWeights = false
	if out := Format(s); strings.Contains(out, "fastest line 100") || !strings.Contains(out, "Weight") {
		t.Errorf("fixed weights should show the Weight column without the auto note:\n%s", out)
	}
}
