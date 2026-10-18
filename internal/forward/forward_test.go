package forward

import (
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

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

// pumpTCP copies and counts; after the other direction finished, it ends the
// flow once nothing arrives for halfCloseIdle, but not while data still flows.
func TestPumpTCPHalfCloseIdle(t *testing.T) {
	old := halfCloseIdle
	halfCloseIdle = 100 * time.Millisecond
	defer func() { halfCloseIdle = old }()

	appIn, appOut := net.Pipe() // the remote side writes into appOut
	dstR, dstW := net.Pipe()    // pumpTCP writes into dstW
	go io.Copy(io.Discard, dstR)
	var n atomic.Int64
	var half atomic.Bool
	done := make(chan struct{})
	go func() { pumpTCP(dstW, appIn, &n, &half); close(done) }()

	appOut.Write([]byte("hello"))
	// The other direction finishes; the reply still trickles in for a while.
	half.Store(true)
	appIn.SetReadDeadline(time.Now().Add(halfCloseIdle))
	for i := 0; i < 4; i++ {
		time.Sleep(halfCloseIdle / 2)
		if _, err := appOut.Write([]byte("more")); err != nil {
			t.Fatalf("flow ended while data was still arriving: %v", err)
		}
	}
	select {
	case <-done:
	case <-time.After(5 * halfCloseIdle):
		t.Fatal("pumpTCP didn't end after the half-closed flow went idle")
	}
	if got := n.Load(); got != 5+4*4 {
		t.Errorf("counted %d bytes, want %d", got, 5+4*4)
	}
}
