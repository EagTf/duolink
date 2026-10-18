package engine

import (
	"context"
	"time"
)

// LineStats is one uplink's state and throughput over the last interval.
type LineStats struct {
	Name     string
	State    string // "UP", "DOWN" or "off" (weight 0)
	Weight   int
	DownMbps float64
	UpMbps   float64
	TCP, UDP int64 // active flows
	Flows    int64 // flows assigned since start
	Sticky   int   // destinations currently stuck to this line
}

// Stats is one sample of the whole engine.
type Stats struct {
	Time     time.Time
	Lines    []LineStats
	DownMbps float64
	UpMbps   float64
	Rules    string // per-app rule counts, "" if none
}

// AnyDown reports whether a line that should carry traffic is DOWN.
func (s Stats) AnyDown() bool {
	for _, l := range s.Lines {
		if l.State == "DOWN" {
			return true
		}
	}
	return false
}

// sampleStats computes throughput from the byte counters every StatsEvery
// and hands the sample to OnStats, until ctx ends.
func (e *Engine) sampleStats(ctx context.Context) {
	prev := map[string][2]int64{}
	every := e.opts.StatsEvery
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s := Stats{Time: time.Now(), Rules: e.fwd.RuleSummary()}
		sticky := e.bal.StickyCount()
		for _, a := range e.ups {
			c := e.fwd.Counters(a)
			p := prev[a.Name]
			prev[a.Name] = [2]int64{c.Rx, c.Tx}
			l := LineStats{
				Name:     a.Name,
				State:    "UP",
				Weight:   e.weights[a],
				DownMbps: mbit(c.Rx-p[0], every),
				UpMbps:   mbit(c.Tx-p[1], every),
				TCP:      c.TCP,
				UDP:      c.UDP,
				Flows:    c.Flows,
				Sticky:   sticky[a],
			}
			switch {
			case l.Weight == 0:
				l.State = "off"
			case !e.bal.Healthy(a):
				l.State = "DOWN"
			}
			s.DownMbps += l.DownMbps
			s.UpMbps += l.UpMbps
			s.Lines = append(s.Lines, l)
		}
		e.opts.OnStats(s)
	}
}

func mbit(bytes int64, d time.Duration) float64 {
	return float64(bytes) * 8 / d.Seconds() / 1e6
}
