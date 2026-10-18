// Package balance decides which uplink each new flow uses: weighted
// round-robin, per-destination stickiness, per-app rules, health checks and
// failover.
package balance

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/windows"

	"github.com/eagtf/duolink/internal/config"
	"github.com/eagtf/duolink/internal/netif"
)

// dialFailuresToProbe is how many consecutive failed dials, to different
// destinations, make us probe a line at once instead of waiting for the
// next interval.
const dialFailuresToProbe = 2

type uplink struct {
	a       *netif.Adapter
	weight  int
	healthy atomic.Bool
	current int // smooth WRR state, guarded by balancer.mu

	// kick asks this line's health loop to probe now. It carries what
	// triggered the probe, for the log. Buffered 1: one pending request is enough.
	kick chan string

	// probeFails counts regular probes that failed in a row while UP. Only
	// the line's health loop (check) touches it.
	probeFails int

	// cancel stops this line's health loop when the line is replaced (its
	// adapter came back at a new address). Set by startLoop.
	cancel context.CancelFunc

	mu       sync.Mutex       // guards the fields below
	failDsts []netip.AddrPort // consecutive failed dials, one per destination IP
	issue    string           // adapter problem seen by the watcher; "" = none
}

type stickyEntry struct {
	a    *netif.Adapter
	last time.Time // last new flow to this destination
	// duringOutage: the line was picked while another line was DOWN. Such
	// entries are forgotten when a line comes back, so the destination is
	// balanced over all lines again instead of staying on the survivor.
	duringOutage bool
}

// Balancer assigns each new flow to an uplink. Once assigned, a flow stays on
// its uplink for its whole life; only new flows see weight or health changes.
type Balancer struct {
	health     config.HealthConfig
	weightSpec map[string]int // configured weights, for lines that appear later

	mu        sync.Mutex
	links     []*uplink // the current lines; changes when adapters come, go or move
	runCtx    context.Context
	stickyTTL time.Duration // 0 = stickiness off
	sticky    map[netip.Addr]*stickyEntry
	allDown   bool // for printing the fallback warning once per outage

	onLineUp func(*netif.Adapter) // set by OnLineUp

	// Replaceable in tests.
	now   func() time.Time
	probe func(ctx context.Context, a *netif.Adapter) error
	logf  func(format string, args ...any)
}

func New(ups []*netif.Adapter, weights map[*netif.Adapter]int, cfg *config.Config) *Balancer {
	b := &Balancer{
		health:     cfg.Health,
		weightSpec: cfg.Weights,
		stickyTTL:  cfg.StickyTTL(),
		sticky:     map[netip.Addr]*stickyEntry{},
		now:        time.Now,
		logf: func(format string, args ...any) {
			fmt.Printf(ts()+" "+format+"\n", args...)
		},
	}
	b.probe = func(ctx context.Context, a *netif.Adapter) error { return Probe(ctx, a, b.health) }
	for _, a := range ups {
		l := &uplink{a: a, weight: weights[a], kick: make(chan string, 1)}
		l.healthy.Store(true)
		b.links = append(b.links, l)
	}
	return b
}

// Pick chooses the uplink for a new flow to dst that no process rule covers.
func (b *Balancer) Pick(dst netip.Addr) *netif.Adapter { return b.PickFor(dst, Rule{}) }

// PickFor chooses the uplink for a new flow to dst under rule r.
//
//   - pin / exclude: the rule's line, as long as it's a candidate (weighted and
//     UP). Otherwise the flow is balanced like a sticky-exempt one.
//   - sticky-exempt: weighted round-robin; the sticky table is neither read
//     nor written, so one app's many connections to the same few servers
//     spread over all lines.
//   - no rule: with stickiness on, a destination that got a flow in the last
//     stickyTTL goes to the same uplink again (if that uplink is still
//     usable), so the remote side sees one public IP. Everything else is
//     balanced.
func (b *Balancer) PickFor(dst netip.Addr, r Rule) *netif.Adapter {
	a, _ := b.PickFlow(dst, r)
	return a
}

// PickFlow is PickFor that also reports whether a weighted line was DOWN
// when the flow was placed: such a flow is on the surviving line only because
// of the outage (see OnLineUp).
func (b *Balancer) PickFlow(dst netip.Addr, r Rule) (_ *netif.Adapter, duringOutage bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	cands, outage := b.candidates()
	if len(cands) == 0 {
		return nil, outage
	}
	switch r.Kind {
	case RulePin, RuleExclude:
		// Resolved now, not at startup, so a pin follows a line whose
		// adapter was replaced or came back at a new address.
		target := b.primary()
		if r.Kind == RulePin {
			target = b.byName(r.Pin)
		}
		if target != nil && contains(cands, target) {
			return target, outage
		}
		return wrr(cands), outage
	case RuleStickyExempt:
		return wrr(cands), outage
	}
	now := b.now()
	if b.stickyTTL > 0 {
		if e := b.sticky[dst]; e != nil && now.Sub(e.last) < b.stickyTTL && contains(cands, e.a) {
			e.last = now
			return e.a, outage
		}
	}
	a := wrr(cands)
	if b.stickyTTL > 0 {
		b.sticky[dst] = &stickyEntry{a: a, last: now, duringOutage: outage}
	}
	return a, outage
}

// candidates returns the uplinks that may get new flows: weighted and
// healthy. outage reports that some weighted line is missing from them
// (DOWN). If every uplink looks down, all weighted ones are used anyway. More
// likely the probe targets are blocked than that every line is dead, and
// refusing all flows would be worse. Caller holds b.mu.
func (b *Balancer) candidates() (cands []*uplink, outage bool) {
	var weighted []*uplink
	for _, l := range b.links {
		if l.weight > 0 {
			weighted = append(weighted, l)
			if l.healthy.Load() {
				cands = append(cands, l)
			}
		}
	}
	if len(cands) > 0 {
		b.allDown = false
		return cands, len(cands) < len(weighted)
	}
	if !b.allDown && len(weighted) > 0 {
		b.allDown = true
		b.logf("every uplink is DOWN; spreading flows over all of them anyway")
	}
	return weighted, true
}

// byName finds the weighted line a pin names ("eth" → Ethernet), matched the
// same way as in the config. Caller holds b.mu.
func (b *Balancer) byName(key string) *netif.Adapter {
	var ads []*netif.Adapter
	for _, l := range b.links {
		if l.weight > 0 {
			ads = append(ads, l.a)
		}
	}
	a, err := netif.Find(ads, key)
	if err != nil {
		return nil
	}
	return a
}

// primary is the weighted line with the lowest interface metric: the one
// Windows itself would use. "exclude" rules stay on it. Caller holds b.mu.
func (b *Balancer) primary() *netif.Adapter {
	var p *netif.Adapter
	for _, l := range b.links {
		if l.weight > 0 && (p == nil || l.a.Metric < p.Metric) {
			p = l.a
		}
	}
	return p
}

// forgetOutageEntries drops sticky entries made while a line was DOWN, so
// their destinations are balanced over all lines again. Called when a line
// comes back. Entries from before the outage keep their line.
func (b *Balancer) forgetOutageEntries() {
	b.mu.Lock()
	n := 0
	for ip, e := range b.sticky {
		if e.duringOutage {
			delete(b.sticky, ip)
			n++
		}
	}
	b.mu.Unlock()
	if n > 0 {
		b.logf("rebalancing %d destinations that moved during the outage", n)
	}
}

// wrr is smooth weighted round-robin (as in nginx): every candidate's current
// score grows by its weight, the highest score wins and pays back the total.
// With weights 2:1 the sequence is A B A, A B A, ...: interleaved, never long
// runs on one link.
func wrr(cands []*uplink) *netif.Adapter {
	total := 0
	var best *uplink
	for _, l := range cands {
		l.current += l.weight
		total += l.weight
		if best == nil || l.current > best.current {
			best = l
		}
	}
	best.current -= total
	return best.a
}

func contains(ls []*uplink, a *netif.Adapter) bool {
	for _, l := range ls {
		if l.a == a {
			return true
		}
	}
	return false
}

// StickyCount returns how many destinations are currently pinned to each uplink.
func (b *Balancer) StickyCount() map[*netif.Adapter]int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := map[*netif.Adapter]int{}
	for _, e := range b.sticky {
		n[e.a]++
	}
	return n
}

// expireSticky drops destinations that haven't had a new flow within the TTL,
// so the table doesn't grow forever.
func (b *Balancer) expireSticky() {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	for ip, e := range b.sticky {
		if now.Sub(e.last) >= b.stickyTTL {
			delete(b.sticky, ip)
		}
	}
}

// OnLineUp calls f whenever a line comes (back) UP: after an outage, or a new
// or moved line's first good probe. It runs on that line's health loop. Call
// it before Run.
func (b *Balancer) OnLineUp(f func(a *netif.Adapter)) { b.onLineUp = f }

// OnLog also hands every health and failover log line (DOWN, back UP, ...)
// to f, e.g. for a window's event list. Call it before Run.
func (b *Balancer) OnLog(f func(line string)) {
	b.logf = func(format string, args ...any) {
		line := ts() + " " + fmt.Sprintf(format, args...)
		fmt.Println(line)
		f(line)
	}
}

func (b *Balancer) link(a *netif.Adapter) *uplink {
	for _, l := range b.links {
		if l.a == a {
			return l
		}
	}
	return nil
}

// Healthy reports the last health-check result for a (false for a line that
// is gone).
func (b *Balancer) Healthy(a *netif.Adapter) bool {
	b.mu.Lock()
	l := b.link(a)
	b.mu.Unlock()
	return l != nil && l.healthy.Load()
}

func (b *Balancer) SetHealthy(a *netif.Adapter, ok bool) {
	b.mu.Lock()
	l := b.link(a)
	b.mu.Unlock()
	if l != nil {
		l.healthy.Store(ok)
	}
}

// Line is one current line, for display.
type Line struct {
	Adapter *netif.Adapter
	Weight  int
	Healthy bool
}

// Lines returns the current lines, in the order they were added.
func (b *Balancer) Lines() []Line {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Line, 0, len(b.links))
	for _, l := range b.links {
		out = append(out, Line{Adapter: l.a, Weight: l.weight, Healthy: l.healthy.Load()})
	}
	return out
}

// IsUplinkIP reports whether ip is one of the lines' own addresses. A flow
// from such an address that arrives in the TUN is a routing loop.
func (b *Balancer) IsUplinkIP(ip netip.Addr) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, l := range b.links {
		if l.a.IPv4.Addr() == ip {
			return true
		}
	}
	return false
}

// Run starts the health checks and the sticky-table sweeper; both stop with ctx.
// Flows already on a link that goes down are left alone: they either recover
// or time out by themselves, and moving a TCP connection to another line
// isn't possible.
func (b *Balancer) Run(ctx context.Context) {
	b.mu.Lock()
	b.runCtx = ctx
	for _, l := range b.links {
		b.startLoop(l)
	}
	b.mu.Unlock()
	if b.stickyTTL > 0 {
		go func() {
			t := time.NewTicker(time.Minute)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					b.expireSticky()
				}
			}
		}()
	}
}

// startLoop starts l's health loop, if l is weighted and Run has started.
// The loop gets its own context, so a replaced line's loop can be stopped
// alone. Caller holds b.mu.
func (b *Balancer) startLoop(l *uplink) {
	if b.runCtx == nil || l.weight == 0 {
		return
	}
	ctx, cancel := context.WithCancel(b.runCtx)
	l.cancel = cancel
	go b.healthLoop(ctx, l)
}

// healthLoop probes l on its interval, or at once when something kicks it
// (failed dials, adapter reconnected). It is the only caller of check for l,
// so probes on one line never overlap.
func (b *Balancer) healthLoop(ctx context.Context, l *uplink) {
	for {
		wait := time.Duration(b.health.IntervalSeconds) * time.Second
		// While DOWN, or after a missed probe, check again sooner.
		if !l.healthy.Load() || l.probeFails > 0 {
			wait = time.Duration(b.health.DownIntervalSeconds) * time.Second
		}
		why := ""
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		case why = <-l.kick:
		}
		b.check(ctx, l, why)
	}
}

// check probes l and applies the result. why is what triggered the probe ("" =
// the regular interval). Only a successful probe ever brings a line back UP.
//
// A regular probe needs FailuresBeforeDown misses in a row to take a line
// DOWN: on a saturated line one probe can time out although the line works.
// A kicked probe (after failed dials) already has other evidence, so one
// failure is enough there; a disconnected adapter never waits for a probe.
func (b *Balancer) check(ctx context.Context, l *uplink, why string) {
	err := b.probe(ctx, l.a)
	if ctx.Err() != nil {
		return
	}
	if err != nil && why == "" && l.healthy.Load() {
		l.probeFails++
		if l.probeFails < b.health.FailuresBeforeDown {
			b.logf("%s: probe failed (%v), %d of %d before DOWN; checking again in %ds",
				l.a.Name, err, l.probeFails, b.health.FailuresBeforeDown, b.health.DownIntervalSeconds)
			return
		}
		err = fmt.Errorf("%w (%d in a row)", err, l.probeFails)
	}
	missed := l.probeFails
	l.probeFails = 0
	if err != nil {
		reason := "probe failed: " + err.Error()
		if why != "" {
			reason = why + ", then " + reason
		}
		b.setDown(l, reason)
		return
	}
	switch {
	case !l.healthy.Swap(true):
		b.logf("%s is back UP", l.a.Name)
		b.forgetOutageEntries()
		if f := b.onLineUp; f != nil {
			f(l.a)
		}
	case why != "":
		b.logf("%s: %s, but the probe succeeded: stays UP", l.a.Name, why)
	case missed > 0:
		b.logf("%s: probe succeeded again: stays UP", l.a.Name)
	}
}

// setDown marks l DOWN and logs why, if it was UP.
func (b *Balancer) setDown(l *uplink, reason string) {
	if l.healthy.Swap(false) {
		b.logf("%s is DOWN (%s): no new flows until it recovers", l.a.Name, reason)
	}
}

// kickProbe asks l's health loop to probe now. If a request is already
// pending, that one covers this too.
func (b *Balancer) kickProbe(l *uplink, why string) {
	select {
	case l.kick <- why:
	default:
	}
}

// Dial outcomes, as evidence about the line (not the destination).
const (
	dialLineOK     = iota // connected, or the remote side answered with a refusal
	dialLineFailed        // no answer at all: timeout, unreachable, no route, ...
	dialIgnored           // says nothing about the line (we cancelled it)
)

// dialOutcome classifies a dial error. A refused or reset connection means a
// packet came back from the destination, so the line works even though the
// site doesn't. Treating those as line failures would let a dead website take
// down a healthy line.
func dialOutcome(err error) int {
	switch {
	case err == nil:
		return dialLineOK
	case errors.Is(err, context.Canceled):
		return dialIgnored
	case errors.Is(err, windows.WSAECONNREFUSED), errors.Is(err, windows.WSAECONNRESET):
		return dialLineOK
	}
	return dialLineFailed
}

// NoteDial records the result of a real-traffic dial through a. After
// dialFailuresToProbe consecutive failures to different destination IPs it
// kicks an immediate probe. The probe, not the dials, decides whether the
// line goes DOWN: dials can fail because the destinations are down.
// Several failures to the same IP count once, so retries to one dead server
// never trigger a probe.
func (b *Balancer) NoteDial(a *netif.Adapter, dst netip.AddrPort, err error) {
	b.mu.Lock()
	l := b.link(a)
	b.mu.Unlock()
	if l == nil {
		return
	}
	outcome := dialOutcome(err)
	if outcome == dialIgnored {
		return
	}
	l.mu.Lock()
	if outcome == dialLineOK {
		l.failDsts = nil
		l.mu.Unlock()
		return
	}
	if !slices.ContainsFunc(l.failDsts, func(d netip.AddrPort) bool { return d.Addr() == dst.Addr() }) {
		l.failDsts = append(l.failDsts, dst)
	}
	why := ""
	if len(l.failDsts) >= dialFailuresToProbe {
		var s []string
		for _, d := range l.failDsts {
			s = append(s, d.String())
		}
		why = fmt.Sprintf("%d failed dials in a row (%s)", len(l.failDsts), strings.Join(s, ", "))
		l.failDsts = nil
	}
	l.mu.Unlock()
	if why != "" {
		b.kickProbe(l, why)
	}
}

// setAdapterIssue applies what the adapter watcher sees for l's adapter.
// A new problem marks the line DOWN at once, with no probe: a disconnected
// adapter can't carry anything. When the problem clears, a probe is kicked,
// and that probe decides whether the line comes back UP.
func (b *Balancer) setAdapterIssue(l *uplink, issue string) {
	l.mu.Lock()
	prev := l.issue
	l.issue = issue
	l.mu.Unlock()
	switch {
	case issue == prev:
	case issue != "":
		if l.healthy.Load() {
			b.setDown(l, issue)
		} else {
			b.logf("%s: %s", l.a.Name, issue)
		}
	default:
		b.logf("%s: adapter is connected again; probing", l.a.Name)
		b.kickProbe(l, "adapter reconnected")
	}
}

// OnAdapterChange re-reads the adapters and updates the lines. Called by
// netif.Watch after Windows reports a change.
func (b *Balancer) OnAdapterChange() {
	all, err := netif.List()
	if err != nil {
		b.logf("could not re-read adapters after a change: %v", err)
		return
	}
	b.applyAdapters(all)
}

// applyAdapters brings the lines in line with the adapters Windows has now
// (hot-plugging). Adapters are matched by LUID, which stays the same while
// an adapter exists:
//   - same adapter at a new address or interface index (new DHCP lease, USB
//     adapter plugged back in): replaced by a line at the new address, which
//     keeps the weight and starts DOWN until a probe succeeds;
//   - adapter removed: dropped; its flows are gone anyway, and if it comes
//     back it's picked up as a new line;
//   - disconnected, no address or no gateway: DOWN, as before;
//   - a usable adapter that isn't a line yet (a new Wi-Fi adapter): added with
//     its weight from the config, DOWN until a probe succeeds.
//
// Flows already on a replaced or dropped line are left alone; their sockets
// were bound to an address that is gone, so they end by themselves.
func (b *Balancer) applyAdapters(all []*netif.Adapter) {
	ups := netif.Uplinks(all)
	b.mu.Lock()
	defer b.mu.Unlock()
	known := map[uint64]bool{}
	var lines []*uplink
	for _, l := range b.links {
		cur := netif.FindByLUID(all, l.a.LUID)
		known[l.a.LUID] = true
		switch {
		case cur == nil:
			b.setDown(l, "adapter removed")
			b.logf("%s: adapter removed; no longer a line", l.a.Name)
			l.stop()
			continue
		case netif.Moved(l.a, cur):
			nl := newUplink(cur, l.weight)
			b.logf("%s: now at %s, index %d (was %s, index %d); probing it",
				cur.Name, cur.IPv4.Addr(), cur.Index, l.a.IPv4.Addr(), l.a.Index)
			l.stop()
			lines = append(lines, nl)
			b.startLoop(nl)
			b.kickProbe(nl, "new address")
			continue
		}
		lines = append(lines, l)
		if l.weight > 0 {
			b.setAdapterIssue(l, netif.Issue(l.a, cur))
		}
	}
	for _, u := range ups {
		if known[u.LUID] {
			continue
		}
		w := weightFor(b.weightSpec, u, ups)
		nl := newUplink(u, w)
		lines = append(lines, nl)
		if w == 0 {
			b.logf("%s: new line, but its weight in the settings is 0, so it isn't used", u.Name)
			continue
		}
		b.logf("%s: new line (weight %d); probing it", u.Name, w)
		b.startLoop(nl)
		b.kickProbe(nl, "new line")
	}
	b.links = lines
}

// newUplink is a line for a, DOWN until its first probe succeeds.
func newUplink(a *netif.Adapter, weight int) *uplink {
	return &uplink{a: a, weight: weight, kick: make(chan string, 1)}
}

// stop ends the line's health loop, if it has one.
func (l *uplink) stop() {
	if l.cancel != nil {
		l.cancel()
	}
}

// weightFor is the configured weight of a line that appears while running:
// 1 with no weights configured, otherwise the weight whose name matches it
// (matched among all current lines, like at startup), or 0 if none does.
func weightFor(spec map[string]int, a *netif.Adapter, ups []*netif.Adapter) int {
	if len(spec) == 0 {
		return 1
	}
	for key, n := range spec {
		if m, err := netif.Find(ups, key); err == nil && m == a {
			return n
		}
	}
	return 0
}

// Probe TCP-connects to all targets at once through a; the line is fine if
// any of them answers within the timeout (no data is sent). The dials use the
// same adapter binding as real traffic, so they test the actual path. Several
// targets, ideally different operators, so one outage doesn't mark a healthy
// line down.
func Probe(ctx context.Context, a *netif.Adapter, h config.HealthConfig) error {
	// All targets at once: the first to answer settles it, and a dead line
	// fails after one timeout instead of one per target.
	pctx, cancel := context.WithTimeout(ctx, time.Duration(h.TimeoutSeconds)*time.Second)
	defer cancel() // also stops the dials still running after a success
	type result struct {
		target string
		err    error
	}
	results := make(chan result, len(h.Targets))
	for _, t := range h.Targets {
		go func() {
			c, err := a.DialTCP4(pctx, t)
			if err == nil {
				c.Close()
			}
			results <- result{t, err}
		}()
	}
	var errs []string
	for range h.Targets {
		r := <-results
		if r.err == nil {
			return nil
		}
		errs = append(errs, fmt.Sprintf("%s: %v", r.target, shortErr(r.err)))
	}
	sort.Strings(errs) // stable log lines
	return fmt.Errorf("%s", strings.Join(errs, "; "))
}

func shortErr(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 {
		s = s[i+2:]
	}
	return s
}

func ts() string { return time.Now().Format("15:04:05") }

// ResolveWeights matches weight names to uplinks with findAdapter ("eth" →
// Ethernet). With no weights at all every uplink gets 1. Otherwise uplinks
// that aren't named get 0, i.e. they're not used.
func ResolveWeights(spec map[string]int, ups []*netif.Adapter) (map[*netif.Adapter]int, error) {
	w := map[*netif.Adapter]int{}
	if len(spec) == 0 {
		for _, a := range ups {
			w[a] = 1
		}
		return w, nil
	}
	total := 0
	for k, n := range spec {
		if n < 0 || n > 100 {
			return nil, fmt.Errorf("weight for %q must be 0..100, got %d", k, n)
		}
		a, err := netif.Find(ups, k)
		if err != nil {
			return nil, err
		}
		if _, dup := w[a]; dup {
			return nil, fmt.Errorf("%s is given a weight twice", a.Name)
		}
		w[a] = n
		total += n
	}
	if total == 0 {
		return nil, fmt.Errorf("all weights are 0: nothing to send traffic through")
	}
	return w, nil
}
