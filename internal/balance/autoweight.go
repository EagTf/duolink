package balance

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/eagtf/duolink/internal/netif"
)

// Automatic weights: each line's share of new flows follows how fast it has
// proven to be. Every second the balancer reads how many bytes each line
// carried (through duolink) and keeps a peak per line: the line's capacity
// as far as we know. Weights are the peaks relative to the fastest line.
const (
	autoFull    = 100    // weight of the fastest line; the others scale to it
	autoMin     = 5      // a slow line still gets the odd flow, so it is re-measured
	minMeasured = 50_000 // bytes/s (about 0.4 Mbit/s) before a line counts as measured
	busyShare   = 0.05   // below this share of its peak, a line counts as idle
	peakDecay   = 0.97   // per second, while busy but slower than its peak
	peakDrift   = 1.03   // per second, while idle, up to half the fastest peak
	autoEvery   = time.Second
	autoLogGap  = time.Minute // log weight changes at most this often
)

// meter is one line's speed measurement. Only the auto-weight loop uses it.
type meter struct {
	last   int64 // byte count at the previous tick
	primed bool
	peak   float64 // bytes/s
}

// SetMeter gives the balancer each line's byte count so far (sent and
// received through duolink), for automatic weights. Call it before Run.
func (b *Balancer) SetMeter(f func(a *netif.Adapter) int64) { b.meterFn = f }

// autoLoop updates the weights every autoEvery until ctx ends.
func (b *Balancer) autoLoop(ctx context.Context) {
	t := time.NewTicker(autoEvery)
	defer t.Stop()
	last := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			b.autoWeights(now.Sub(last))
			last = now
		}
	}
}

// autoWeights measures every weighted line over the last dt and sets the
// weights from the peaks.
//
//   - A busy line's peak follows its speed: up at once, down slowly (3% a
//     second) while it carries traffic but less than its peak.
//   - An idle line (under 5% of its peak) keeps its peak: an idle line isn't
//     slow, just unused. A low peak drifts back up to at most half the
//     fastest line's, so one bad moment can't starve a line for good.
//   - Until every line has carried at least minMeasured, all weights stay
//     equal, so every line gets traffic and can be measured.
func (b *Balancer) autoWeights(dt time.Duration) {
	b.mu.Lock()
	var ls []*uplink
	for _, l := range b.links {
		if l.weight > 0 {
			ls = append(ls, l)
		}
	}
	b.mu.Unlock()
	if len(ls) == 0 || dt <= 0 {
		return
	}

	maxPeak := 0.0
	var idle []*uplink
	for _, l := range ls {
		n := b.meterFn(l.a)
		m := &l.meter
		rate := 0.0
		if m.primed {
			rate = math.Max(0, float64(n-m.last)/dt.Seconds())
		}
		m.last, m.primed = n, true
		if rate > 0 && rate >= busyShare*m.peak {
			m.peak = math.Max(rate, m.peak*peakDecay)
		} else {
			idle = append(idle, l)
		}
		maxPeak = math.Max(maxPeak, m.peak)
	}
	for _, l := range idle {
		if m := &l.meter; m.peak > 0 {
			m.peak = math.Min(m.peak*peakDrift, math.Max(m.peak, maxPeak/2))
		}
	}

	measured := true
	for _, l := range ls {
		measured = measured && l.meter.peak >= minMeasured
	}
	weights := make([]int, len(ls))
	for i, l := range ls {
		weights[i] = autoFull
		if measured {
			weights[i] = max(autoMin, int(math.Round(autoFull*l.meter.peak/maxPeak)))
		}
	}
	b.mu.Lock()
	for i, l := range ls {
		l.weight = weights[i]
	}
	b.mu.Unlock()
	if measured {
		b.logAutoWeights(ls, weights)
	}
}

// logAutoWeights logs the weights when they first come from measurements and
// when one changed by 15 or more since the last log, at most once a minute.
func (b *Balancer) logAutoWeights(ls []*uplink, weights []int) {
	now := b.now()
	changed := b.autoLogged == nil
	for i, l := range ls {
		if w, ok := b.autoLogged[l]; !ok || abs(w-weights[i]) >= 15 {
			changed = true
		}
	}
	if !changed || (b.autoLogged != nil && now.Sub(b.autoLogAt) < autoLogGap) {
		return
	}
	b.autoLogged = map[*uplink]int{}
	var parts []string
	for i, l := range ls {
		b.autoLogged[l] = weights[i]
		parts = append(parts, fmt.Sprintf("%s %d (%.1f Mbit/s)", l.a.Name, weights[i], l.meter.peak*8/1e6))
	}
	b.autoLogAt = now
	b.logf("automatic weights from measured speed: %s", strings.Join(parts, ", "))
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
