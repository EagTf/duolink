package engine

import (
	"context"
	"errors"
	"fmt"
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
	// It failed before touching anything, so the tray may retry it.
	if !Retryable(err) {
		t.Errorf("a failure before any change should be retryable: %v", err)
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

func TestRetryable(t *testing.T) {
	base := errors.New("need at least 2 usable uplinks")
	r := error(retryable{base})
	if !Retryable(r) || !errors.Is(r, base) || r.Error() != base.Error() {
		t.Errorf("retryable wrapper: Retryable=%v Is=%v msg=%q", Retryable(r), errors.Is(r, base), r)
	}
	if Retryable(base) || Retryable(nil) {
		t.Error("a plain error must not count as retryable")
	}
	if !Retryable(fmt.Errorf("context: %w", r)) {
		t.Error("Retryable must see through wrapping")
	}
}

// Running only opens an existing mutex; it must never create one (that
// would make the next Start think another engine is running).
func TestRunningDoesNotCreateTheMutex(t *testing.T) {
	if Running() {
		t.Skip("a duolink engine is running on this PC")
	}
	if Running() {
		t.Error("Running() created the mutex it was only meant to look for")
	}
}
