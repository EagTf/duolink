package forward

import "testing"

func TestCloseOutageFlows(t *testing.T) {
	f := &Forwarder{outageFlows: map[uint64]func(){}}
	var closed []string
	untrackA := f.trackOutageFlow(func() { closed = append(closed, "a") })
	f.trackOutageFlow(func() { closed = append(closed, "b") })
	untrackA() // flow a ended by itself before the line came back

	if n := f.CloseOutageFlows(); n != 1 || len(closed) != 1 || closed[0] != "b" {
		t.Errorf("closed %d: %v, want only b", n, closed)
	}
	// Each flow is closed once; the list is empty afterwards.
	if n := f.CloseOutageFlows(); n != 0 {
		t.Errorf("second call closed %d", n)
	}
}
