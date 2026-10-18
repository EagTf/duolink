package engine

import (
	"context"
	"reflect"
	"testing"

	"github.com/eagtf/duolink/internal/config"
)

// A Start that fails must return the error, not panic, and leave nothing
// behind. Skipped when elevated: there, Start would really create the
// adapter and change routes, which tests must never do.
func TestStartFailsCleanlyWithoutAdmin(t *testing.T) {
	if IsAdmin() {
		t.Skip("elevated: Start would change routing")
	}
	e, err := Start(Options{Config: config.Default()})
	if err == nil || e != nil {
		t.Fatalf("Start without admin = %v, %v; want nil and an error", e, err)
	}
}

func TestStopUndoesInReverseOnce(t *testing.T) {
	var order []string
	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{cancel: cancel}
	for _, step := range []string{"mutex", "adapter", "netstack", "routes"} {
		e.undo = append(e.undo, func() { order = append(order, step) })
	}
	e.Stop()
	e.Stop() // second call does nothing
	if want := []string{"routes", "netstack", "adapter", "mutex"}; !reflect.DeepEqual(order, want) {
		t.Errorf("undo order = %v, want %v", order, want)
	}
	if ctx.Err() == nil {
		t.Error("Stop didn't cancel the background work")
	}
}

func TestStatsAnyDown(t *testing.T) {
	s := Stats{Lines: []LineStats{{State: "UP"}, {State: "off"}}}
	if s.AnyDown() {
		t.Error("UP + off reported as down")
	}
	s.Lines = append(s.Lines, LineStats{State: "DOWN"})
	if !s.AnyDown() {
		t.Error("a DOWN line not reported")
	}
}
