package main

import (
	"context"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type uplink struct {
	a       *Adapter
	weight  int
	healthy atomic.Bool
	current int // smooth WRR state, guarded by balancer.mu
}

type stickyEntry struct {
	a    *Adapter
	last time.Time // last new flow to this destination
}

// balancer assigns each new flow to an uplink. Once assigned, a flow stays on
// its uplink for its whole life; only new flows see weight or health changes.
type balancer struct {
	health HealthConfig

	mu        sync.Mutex
	links     []*uplink
	stickyTTL time.Duration // 0 = stickiness off
	sticky    map[netip.Addr]*stickyEntry
	allDown   bool // for printing the fallback warning once per outage

	now func() time.Time // replaceable in tests
}

func newBalancer(ups []*Adapter, weights map[*Adapter]int, cfg *Config) *balancer {
	b := &balancer{
		health:    cfg.Health,
		stickyTTL: cfg.stickyTTL(),
		sticky:    map[netip.Addr]*stickyEntry{},
		now:       time.Now,
	}
	for _, a := range ups {
		l := &uplink{a: a, weight: weights[a]}
		l.healthy.Store(true)
		b.links = append(b.links, l)
	}
	return b
}

// Pick chooses the uplink for a new flow to dst.
//
// With stickiness on, a destination that got a flow in the last stickyTTL
// goes to the same uplink again (if that uplink is still usable), so the
// remote side sees one public IP. Everything else is balanced.
func (b *balancer) Pick(dst netip.Addr) *Adapter {
	b.mu.Lock()
	defer b.mu.Unlock()
	cands := b.candidates()
	if len(cands) == 0 {
		return nil
	}
	now := b.now()
	if b.stickyTTL > 0 {
		if e := b.sticky[dst]; e != nil && now.Sub(e.last) < b.stickyTTL && contains(cands, e.a) {
			e.last = now
			return e.a
		}
	}
	a := wrr(cands)
	if b.stickyTTL > 0 {
		b.sticky[dst] = &stickyEntry{a: a, last: now}
	}
	return a
}

// candidates returns the uplinks that may get new flows: weighted and healthy.
// If every uplink looks down, all weighted ones are used anyway. More likely
// the probe targets are blocked than that every line is dead, and refusing all
// flows would be worse. Caller holds b.mu.
func (b *balancer) candidates() []*uplink {
	var cands, weighted []*uplink
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
		return cands
	}
	if !b.allDown && len(weighted) > 0 {
		b.allDown = true
		fmt.Println(ts(), "every uplink failed its health check; spreading flows over all of them anyway")
	}
	return weighted
}

// wrr is smooth weighted round-robin (as in nginx): every candidate's current
// score grows by its weight, the highest score wins and pays back the total.
// With weights 2:1 the sequence is A B A, A B A, ...: interleaved, never long
// runs on one link.
func wrr(cands []*uplink) *Adapter {
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

func contains(ls []*uplink, a *Adapter) bool {
	for _, l := range ls {
		if l.a == a {
			return true
		}
	}
	return false
}

// stickyCount returns how many destinations are currently pinned to each uplink.
func (b *balancer) stickyCount() map[*Adapter]int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := map[*Adapter]int{}
	for _, e := range b.sticky {
		n[e.a]++
	}
	return n
}

// expireSticky drops destinations that haven't had a new flow within the TTL,
// so the table doesn't grow forever.
func (b *balancer) expireSticky() {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	for ip, e := range b.sticky {
		if now.Sub(e.last) >= b.stickyTTL {
			delete(b.sticky, ip)
		}
	}
}

func (b *balancer) link(a *Adapter) *uplink {
	for _, l := range b.links {
		if l.a == a {
			return l
		}
	}
	return nil
}

// healthy reports the last health-check result for a.
func (b *balancer) healthy(a *Adapter) bool { return b.link(a).healthy.Load() }

func (b *balancer) setHealthy(a *Adapter, ok bool) { b.link(a).healthy.Store(ok) }

// run starts the health checks and the sticky-table sweeper; both stop with ctx.
// Flows already on a link that goes down are left alone: they either recover
// or time out by themselves, and moving a TCP connection to another line
// isn't possible.
func (b *balancer) run(ctx context.Context) {
	for _, l := range b.links {
		if l.weight > 0 {
			go b.healthLoop(ctx, l)
		}
	}
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

func (b *balancer) healthLoop(ctx context.Context, l *uplink) {
	for {
		wait := time.Duration(b.health.IntervalSeconds) * time.Second
		if !l.healthy.Load() {
			wait = time.Duration(b.health.DownIntervalSeconds) * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		err := probe(ctx, l.a, b.health)
		if ctx.Err() != nil {
			return
		}
		was := l.healthy.Swap(err == nil)
		switch {
		case was && err != nil:
			fmt.Printf("%s %s is DOWN (%v): no new flows until it recovers\n", ts(), l.a.Name, err)
		case !was && err == nil:
			fmt.Printf("%s %s is back UP\n", ts(), l.a.Name)
		}
	}
}

// probe TCP-connects to each target through a until one answers (no data is
// sent). The dial uses the same adapter binding as real traffic, so it tests
// the actual path. Several targets, ideally different operators, so one
// outage doesn't mark a healthy line down.
func probe(ctx context.Context, a *Adapter, h HealthConfig) error {
	var errs []string
	for _, t := range h.Targets {
		pctx, cancel := context.WithTimeout(ctx, time.Duration(h.TimeoutSeconds)*time.Second)
		c, err := a.DialTCP4(pctx, t)
		cancel()
		if err == nil {
			c.Close()
			return nil
		}
		errs = append(errs, fmt.Sprintf("%s: %v", t, shortErr(err)))
	}
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

// parseWeightSpec turns the --weights text "eth=2,wifi=1" into a name → weight
// map, the same shape as "weights" in duolink.json.
func parseWeightSpec(spec string) (map[string]int, error) {
	w := map[string]int{}
	for _, part := range strings.Split(spec, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("bad weight %q: want name=number, e.g. eth=1", part)
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("bad weight %q: %q is not a number", part, v)
		}
		w[k] = n
	}
	return w, nil
}

// resolveWeights matches weight names to uplinks with findAdapter ("eth" →
// Ethernet). With no weights at all every uplink gets 1. Otherwise uplinks
// that aren't named get 0, i.e. they're not used.
func resolveWeights(spec map[string]int, ups []*Adapter) (map[*Adapter]int, error) {
	w := map[*Adapter]int{}
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
		a, err := findAdapter(ups, k)
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
