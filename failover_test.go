package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// failoverRig is a balancer with a scripted probe and a captured log.
type failoverRig struct {
	b         *balancer
	eth, wifi *uplink
	probeErr  error // what the next probes return
	probes    int
	log       []string
}

func newRig(t *testing.T) *failoverRig {
	eth, wifi, ups := testUplinks()
	r := &failoverRig{b: newBalancer(ups, map[*Adapter]int{eth: 1, wifi: 1}, noSticky())}
	r.eth, r.wifi = r.b.link(eth), r.b.link(wifi)
	r.b.probe = func(context.Context, *Adapter) error { r.probes++; return r.probeErr }
	r.b.logf = func(format string, args ...any) { r.log = append(r.log, fmt.Sprintf(format, args...)) }
	return r
}

// kicked returns the pending probe request for l, or "" if none.
func kicked(l *uplink) string {
	select {
	case why := <-l.kick:
		return why
	default:
		return ""
	}
}

func (r *failoverRig) lastLog() string {
	if len(r.log) == 0 {
		return ""
	}
	return r.log[len(r.log)-1]
}

var (
	errTimeout = &net.OpError{Op: "dial", Net: "tcp4", Err: context.DeadlineExceeded}
	errRefused = &net.OpError{Op: "dial", Net: "tcp4", Err: os.NewSyscallError("connectex", windows.WSAECONNREFUSED)}
	dstA       = netip.MustParseAddrPort("203.0.113.5:443")
	dstA80     = netip.MustParseAddrPort("203.0.113.5:80")
	dstB       = netip.MustParseAddrPort("198.51.100.7:443")
	dstC       = netip.MustParseAddrPort("198.51.100.8:443")
)

func TestDialOutcome(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{nil, dialLineOK},
		{errRefused, dialLineOK},
		{&net.OpError{Op: "dial", Err: os.NewSyscallError("connectex", windows.WSAECONNRESET)}, dialLineOK},
		{fmt.Errorf("dial: %w", context.Canceled), dialIgnored},
		{errTimeout, dialLineFailed},
		{&net.OpError{Op: "dial", Err: os.NewSyscallError("connectex", windows.WSAENETUNREACH)}, dialLineFailed},
		{errors.New("something else"), dialLineFailed},
	}
	for _, c := range cases {
		if got := dialOutcome(c.err); got != c.want {
			t.Errorf("dialOutcome(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}

func TestFailedDialsToOneDestinationNeverProbe(t *testing.T) {
	r := newRig(t)
	for i := 0; i < 5; i++ {
		r.b.noteDial(r.eth.a, dstA, errTimeout)
		r.b.noteDial(r.eth.a, dstA80, errTimeout) // same IP, other port: still one destination
	}
	if why := kicked(r.eth); why != "" {
		t.Errorf("retries to one dead server triggered a probe: %q", why)
	}
}

func TestFailedDialsToTwoDestinationsProbe(t *testing.T) {
	r := newRig(t)
	r.b.noteDial(r.eth.a, dstA, errTimeout)
	if kicked(r.eth) != "" {
		t.Fatal("one failure already triggered a probe")
	}
	r.b.noteDial(r.eth.a, dstB, errTimeout)
	why := kicked(r.eth)
	if !strings.Contains(why, "2 failed dials") || !strings.Contains(why, dstA.String()) || !strings.Contains(why, dstB.String()) {
		t.Fatalf("probe reason = %q", why)
	}
	if kicked(r.wifi) != "" {
		t.Error("the other line was probed too")
	}
	// The dials alone don't change the state; only the probe does.
	if !r.eth.healthy.Load() {
		t.Error("line went DOWN before any probe")
	}
}

func TestDialSuccessOrRefusalResetsTheCount(t *testing.T) {
	r := newRig(t)
	r.b.noteDial(r.eth.a, dstA, errTimeout)
	r.b.noteDial(r.eth.a, dstB, nil) // a working dial proves the line
	r.b.noteDial(r.eth.a, dstC, errTimeout)
	if why := kicked(r.eth); why != "" {
		t.Errorf("success in between should reset the count, got probe %q", why)
	}

	r.b.noteDial(r.eth.a, dstB, errRefused) // refused = remote answered = line works
	r.b.noteDial(r.eth.a, dstA, errTimeout)
	if why := kicked(r.eth); why != "" {
		t.Errorf("a refused connection should count as proof of life, got probe %q", why)
	}

	r.b.noteDial(r.eth.a, dstB, context.Canceled) // ignored: neither resets nor counts
	r.b.noteDial(r.eth.a, dstC, errTimeout)
	if why := kicked(r.eth); why == "" {
		t.Error("cancelled dial should be ignored, not reset the count")
	}
}

func TestDialsFailButProbeSucceedsStaysUp(t *testing.T) {
	r := newRig(t)
	r.b.noteDial(r.eth.a, dstA, errTimeout)
	r.b.noteDial(r.eth.a, dstB, errTimeout)
	r.b.check(context.Background(), r.eth, kicked(r.eth))
	if r.probes != 1 {
		t.Fatalf("probes = %d, want 1", r.probes)
	}
	if !r.eth.healthy.Load() {
		t.Fatal("line went DOWN although the probe succeeded")
	}
	if !strings.Contains(r.lastLog(), "stays UP") {
		t.Errorf("log = %q, want a 'stays UP' note", r.lastLog())
	}
}

func TestDialsFailAndProbeFailsGoesDown(t *testing.T) {
	r := newRig(t)
	r.probeErr = errors.New("1.1.1.1:443: i/o timeout")
	r.b.noteDial(r.eth.a, dstA, errTimeout)
	r.b.noteDial(r.eth.a, dstB, errTimeout)
	r.b.check(context.Background(), r.eth, kicked(r.eth))
	if r.eth.healthy.Load() {
		t.Fatal("line stayed UP although the probe failed")
	}
	l := r.lastLog()
	if !strings.Contains(l, "Ethernet is DOWN") || !strings.Contains(l, "2 failed dials") || !strings.Contains(l, "probe failed") {
		t.Errorf("log = %q", l)
	}
	if got := r.b.Pick(dstC.Addr()); got != r.wifi.a {
		t.Errorf("new flow went to %v, want Wi-Fi", got.Name)
	}

	// A later successful probe brings it back, as before.
	r.probeErr = nil
	r.b.check(context.Background(), r.eth, "")
	if !r.eth.healthy.Load() || !strings.Contains(r.lastLog(), "back UP") {
		t.Errorf("not back UP after a good probe: %q", r.lastLog())
	}
}

func TestAdapterDisconnectIsImmediate(t *testing.T) {
	r := newRig(t)
	r.b.setAdapterIssue(r.eth, "adapter disconnected")
	if r.eth.healthy.Load() {
		t.Fatal("line still UP after its adapter disconnected")
	}
	if r.probes != 0 {
		t.Error("a disconnect should not need a probe")
	}
	if l := r.lastLog(); !strings.Contains(l, "Ethernet is DOWN (adapter disconnected)") {
		t.Errorf("log = %q", l)
	}
	n := len(r.log)
	r.b.setAdapterIssue(r.eth, "adapter disconnected") // repeated event, same state
	if len(r.log) != n {
		t.Errorf("repeated event logged again: %q", r.lastLog())
	}

	// Reconnect: a probe is requested, but the line stays DOWN until it succeeds.
	r.b.setAdapterIssue(r.eth, "")
	why := kicked(r.eth)
	if why != "adapter reconnected" {
		t.Fatalf("reconnect kicked %q", why)
	}
	if r.eth.healthy.Load() {
		t.Fatal("line came back UP without a probe")
	}
	r.probeErr = errors.New("no route")
	r.b.check(context.Background(), r.eth, why)
	if r.eth.healthy.Load() {
		t.Fatal("line UP although the probe after reconnect failed")
	}
	r.probeErr = nil
	r.b.check(context.Background(), r.eth, "")
	if !r.eth.healthy.Load() {
		t.Fatal("line not UP after a good probe")
	}
}

func TestAdapterIssueWhileAlreadyDownIsLogged(t *testing.T) {
	r := newRig(t)
	r.eth.healthy.Store(false)
	r.b.setAdapterIssue(r.eth, "address changed from 192.168.2.10 to 192.168.2.30; restart duolink to use it")
	if l := r.lastLog(); !strings.Contains(l, "address changed") {
		t.Errorf("log = %q", l)
	}
}

func TestAdapterIssue(t *testing.T) {
	orig := &Adapter{Index: 12, IPv4: netip.MustParsePrefix("192.168.2.10/24"), Gateway: netip.MustParseAddr("192.168.2.1"), Up: true}
	mod := func(f func(a *Adapter)) *Adapter {
		c := *orig
		f(&c)
		return &c
	}
	cases := []struct {
		cur  *Adapter
		want string // substring; "" = no issue
	}{
		{mod(func(*Adapter) {}), ""},
		{nil, "removed"},
		{mod(func(a *Adapter) { a.Up = false }), "disconnected"},
		{mod(func(a *Adapter) { a.IPv4 = netip.Prefix{} }), "lost its IPv4 address"},
		{mod(func(a *Adapter) { a.IPv4 = netip.MustParsePrefix("169.254.7.7/16") }), "lost its IPv4 address"},
		{mod(func(a *Adapter) { a.IPv4 = netip.MustParsePrefix("192.168.2.30/24") }), "address changed"},
		{mod(func(a *Adapter) { a.Index = 40 }), "index changed"},
		{mod(func(a *Adapter) { a.Gateway = netip.Addr{} }), "lost its default gateway"},
	}
	for _, c := range cases {
		got := adapterIssue(orig, c.cur)
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("adapterIssue(%+v) = %q, want %q", c.cur, got, c.want)
		}
	}
}

// The subscriptions only listen (no admin, nothing changes), so this checks
// the real Win32 calls and the callback signature without touching the system.
func TestWatchAdaptersSubscribes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	if err := watchAdapters(ctx, func() {}); err != nil {
		t.Fatal(err)
	}
	cancel()
}
