// Package forward handles the flows the netstack terminates: it picks an
// uplink for each one, dials out through it and copies bytes both ways.
package forward

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xjasonlyu/tun2socks/v2/core/adapter"
	"gvisor.dev/gvisor/pkg/tcpip/stack"

	"github.com/eagtf/duolink/internal/balance"
	"github.com/eagtf/duolink/internal/netif"
	"github.com/eagtf/duolink/internal/procs"
	"github.com/eagtf/duolink/internal/route"
)

// linkStats are live counters for one uplink, updated by the copy loops.
type linkStats struct {
	tx, rx   atomic.Int64 // bytes sent to / received from the internet
	tcp, udp atomic.Int64 // active flows
	flows    atomic.Int64 // flows ever assigned
}

// Forwarder implements tun2socks' adapter.TransportHandler: the netstack calls
// HandleTCP / HandleUDP once per new flow coming out of the TUN, with a
// net.Conn for the app's side. We dial the real destination through a chosen
// uplink and copy bytes both ways.
type Forwarder struct {
	bal     *balance.Balancer // picks the uplink for each new flow and hears how dials went
	udpIdle time.Duration
	stats   map[*netif.Adapter]*linkStats
	own     map[netip.Addr]bool // our uplinks' IPs; seeing one as a flow source = loop

	loops    atomic.Int64
	loopOnce sync.Once

	// Process rules (milestone 8). procs is nil when no rules are configured,
	// so no lookups happen at all.
	rules     *balance.Rules
	procs     *procs.Table
	ruleMu    sync.Mutex
	ruleFlows map[string]int64 // exe → flows a rule applied to
	unknown   atomic.Int64     // flows whose owning process wasn't found
}

func New(ups []*netif.Adapter, bal *balance.Balancer, rules *balance.Rules, udpIdle time.Duration) *Forwarder {
	f := &Forwarder{bal: bal, rules: rules, ruleFlows: map[string]int64{}, udpIdle: udpIdle, stats: map[*netif.Adapter]*linkStats{}, own: map[netip.Addr]bool{}}
	for _, a := range ups {
		f.stats[a] = &linkStats{}
		f.own[a.IPv4.Addr()] = true
	}
	if rules != nil && !rules.Empty() {
		f.procs = procs.NewTable()
	}
	return f
}

// endpoints returns the flow's app-side source and real destination.
// In the netstack's view "local" is the address the app connected to.
func endpoints(id stack.TransportEndpointID) (src, dst netip.AddrPort) {
	s, _ := netip.AddrFromSlice(id.RemoteAddress.AsSlice())
	d, _ := netip.AddrFromSlice(id.LocalAddress.AsSlice())
	return netip.AddrPortFrom(s, id.RemotePort), netip.AddrPortFrom(d, id.LocalPort)
}

// accept filters flows before dialing. It returns false (and the caller drops
// the flow) for:
//   - routing loops: a flow whose source is one of our uplink IPs is our own
//     outbound socket that escaped its adapter binding and came back into the TUN.
//     Forwarding it would dial again, loop again, and so on.
//   - anything that isn't plain IPv4 unicast: Windows also sends LLMNR, mDNS,
//     SSDP and NetBIOS broadcasts out of every adapter, including ours. Those
//     mean nothing on the internet.
func (f *Forwarder) accept(src, dst netip.AddrPort) bool {
	if f.own[src.Addr()] {
		f.loops.Add(1)
		f.loopOnce.Do(func() {
			fmt.Fprintf(os.Stderr, "ROUTING LOOP: a flow from our own uplink address %s (to %s) came back into the TUN. "+
				"Adapter binding is not working; dropping these flows.\n", src.Addr(), dst)
		})
		return false
	}
	a := dst.Addr()
	return a.Is4() && !a.IsMulticast() && !a.IsLinkLocalUnicast() && !a.IsUnspecified() &&
		a != netip.AddrFrom4([4]byte{255, 255, 255, 255}) && !route.TunPrefix.Contains(a)
}

// ruleFor finds the process that opened this flow and returns its rule.
// src is the app's side of the flow (its socket's local address).
func (f *Forwarder) ruleFor(udp bool, src netip.AddrPort) balance.Rule {
	if f.procs == nil {
		return balance.Rule{}
	}
	exe := f.procs.Lookup(udp, src)
	if exe == "" {
		f.unknown.Add(1)
		return balance.Rule{}
	}
	r := f.rules.ForExe(exe)
	if r.Kind != balance.RuleNone {
		f.ruleMu.Lock()
		f.ruleFlows[exe]++
		f.ruleMu.Unlock()
	}
	return r
}

// ruleSummary is the stats line for process rules, or "" if nothing to show.
func (f *Forwarder) ruleSummary() string {
	if f.procs == nil {
		return ""
	}
	var parts []string
	f.ruleMu.Lock()
	for _, exe := range f.rules.Exes() {
		if n := f.ruleFlows[exe]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %s %d", exe, f.rules.ForExe(exe).Kind, n))
		}
	}
	f.ruleMu.Unlock()
	if n := f.unknown.Load(); n > 0 {
		parts = append(parts, fmt.Sprintf("owner unknown %d", n))
	}
	if len(parts) == 0 {
		return ""
	}
	return "rules (flows): " + strings.Join(parts, ", ")
}

// HandleTCP and HandleUDP must return quickly: gVisor calls the UDP handler
// inline on its packet path, and tun2socks only frees the TCP handshake slot
// once HandleTCP returns. So each flow gets its own goroutine.
func (f *Forwarder) HandleTCP(c adapter.TCPConn) { go f.guard(c, func() { f.tcp(c) }) }
func (f *Forwarder) HandleUDP(c adapter.UDPConn) { go f.guard(c, func() { f.udp(c) }) }

// guard keeps a bug in one flow from killing the process: a panic in a
// goroutine would skip the deferred route cleanup in run.
func (f *Forwarder) guard(c net.Conn, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintln(os.Stderr, "flow panic:", r)
			c.Close()
		}
	}()
	fn()
}

func (f *Forwarder) tcp(c adapter.TCPConn) {
	defer c.Close()
	src, dst := endpoints(c.ID())
	if !f.accept(src, dst) {
		return
	}
	up := f.bal.PickFor(dst.Addr(), f.ruleFor(false, src))
	if up == nil {
		return // no healthy uplink: closing sends a RST to the app
	}
	st := f.stats[up]
	st.flows.Add(1)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	out, err := up.DialTCP4(ctx, dst.String())
	cancel()
	f.bal.NoteDial(up, dst, err)
	if err != nil {
		return
	}
	defer out.Close()
	st.tcp.Add(1)
	defer st.tcp.Add(-1)

	// Copy both directions. When one side finishes sending, pass the
	// half-close (FIN) on with CloseWrite so the other direction can finish.
	done := make(chan struct{})
	go func() {
		io.Copy(countWriter{out, &st.tx}, c)
		closeWrite(out)
		close(done)
	}()
	io.Copy(countWriter{c, &st.rx}, out)
	closeWrite(c)
	<-done
}

func (f *Forwarder) udp(c adapter.UDPConn) {
	defer c.Close()
	src, dst := endpoints(c.ID())
	if !f.accept(src, dst) {
		return
	}
	up := f.bal.PickFor(dst.Addr(), f.ruleFor(true, src))
	if up == nil {
		return
	}
	st := f.stats[up]
	st.flows.Add(1)

	// The netstack hands us one conn per (app src, dst) pair, and we give it
	// one connected socket on one uplink, so the flow stays pinned to that
	// uplink until it goes idle.
	// A connected UDP socket sends nothing yet, so success proves nothing
	// about the line; only errors (e.g. no route) are reported.
	out, err := up.DialUDP4(context.Background(), dst.String())
	if err != nil {
		f.bal.NoteDial(up, dst, err)
		return
	}
	defer out.Close()
	st.udp.Add(1)
	defer st.udp.Add(-1)

	var last atomic.Int64 // unix nanos of last packet in either direction
	last.Store(time.Now().UnixNano())
	pump := func(dst, src net.Conn, n *atomic.Int64) {
		buf := make([]byte, 65535)
		for {
			src.SetReadDeadline(time.Now().Add(f.udpIdle))
			k, err := src.Read(buf)
			if err != nil {
				// Read deadlines fire per direction. Only end the flow if the
				// *whole* flow has been idle, not just this direction.
				if errors.Is(err, os.ErrDeadlineExceeded) &&
					time.Since(time.Unix(0, last.Load())) < f.udpIdle {
					continue
				}
				return
			}
			last.Store(time.Now().UnixNano())
			if _, err := dst.Write(buf[:k]); err != nil {
				return
			}
			n.Add(int64(k))
		}
	}
	done := make(chan struct{})
	go func() { pump(out, c, &st.tx); close(done) }()
	pump(c, out, &st.rx)
	// Whichever side ends first, unblock the other.
	c.SetReadDeadline(time.Now())
	out.SetReadDeadline(time.Now())
	<-done
}

type countWriter struct {
	w io.Writer
	n *atomic.Int64
}

func (cw countWriter) Write(p []byte) (int, error) {
	n, err := cw.w.Write(p)
	cw.n.Add(int64(n))
	return n, err
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	} else {
		c.Close()
	}
}

// PrintStats prints per-uplink throughput every interval until ctx ends.
// Columns: health, weight, throughput over the last interval, active TCP/UDP
// flows, flows assigned since start, and destinations stuck to this line.
func (f *Forwarder) PrintStats(ctx context.Context, ups []*netif.Adapter, weights map[*netif.Adapter]int, bal *balance.Balancer, every time.Duration) {
	prev := map[*netif.Adapter][2]int64{}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		var totRx, totTx float64
		sticky := bal.StickyCount()
		for i, a := range ups {
			st := f.stats[a]
			rx, tx := st.rx.Load(), st.tx.Load()
			p := prev[a]
			prev[a] = [2]int64{rx, tx}
			down, up := mbit(rx-p[0], every), mbit(tx-p[1], every)
			totRx += down
			totTx += up
			state := "UP"
			switch {
			case weights[a] == 0:
				state = "off"
			case !bal.Healthy(a):
				state = "DOWN"
			}
			when := "        "
			if i == 0 {
				when = ts()
			}
			fmt.Printf("%s  %-12s %-4s w%-3d down %5.1f  up %5.1f Mbit/s  tcp %4d  udp %3d  flows %6d  sticky dsts %4d\n",
				when, a.Name, state, weights[a], down, up, st.tcp.Load(), st.udp.Load(), st.flows.Load(), sticky[a])
		}
		fmt.Printf("          %-12s %-4s %-4s down %5.1f  up %5.1f Mbit/s\n", "total", "", "", totRx, totTx)
		if s := f.ruleSummary(); s != "" {
			fmt.Println("         ", s)
		}
		fmt.Println()
	}
}

func mbit(bytes int64, d time.Duration) float64 {
	return float64(bytes) * 8 / d.Seconds() / 1e6
}

// Loops returns how many flows were dropped as routing loops. The startup
// self-check compares it before and after dialing through every uplink.
func (f *Forwarder) Loops() int64 { return f.loops.Load() }

func ts() string { return time.Now().Format("15:04:05") }
