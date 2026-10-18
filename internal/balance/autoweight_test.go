package balance

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/eagtf/duolink/internal/config"
	"github.com/eagtf/duolink/internal/netif"
)

// autoRig is a balancer with automatic weights and a fake byte meter.
type autoRig struct {
	b         *Balancer
	eth, wifi *netif.Adapter
	bytes     map[*netif.Adapter]int64
	log       []string
	now       time.Time
}

func newAutoRig(t *testing.T) *autoRig {
	t.Helper()
	eth, wifi, ups := testUplinks()
	cfg := config.Default() // automatic weights, no weights set
	w, err := ResolveWeights(cfg.Weights, ups)
	if err != nil {
		t.Fatal(err)
	}
	r := &autoRig{b: New(ups, w, &cfg), eth: eth, wifi: wifi, bytes: map[*netif.Adapter]int64{}, now: time.Unix(1000, 0)}
	r.b.SetMeter(func(a *netif.Adapter) int64 { return r.bytes[a] })
	r.b.logf = func(format string, args ...any) { r.log = append(r.log, fmt.Sprintf(format, args...)) }
	r.b.now = func() time.Time { return r.now }
	return r
}

// second lets each line carry the given Mbit/s for one second.
func (r *autoRig) second(ethMbps, wifiMbps float64) {
	r.bytes[r.eth] += int64(ethMbps * 1e6 / 8)
	r.bytes[r.wifi] += int64(wifiMbps * 1e6 / 8)
	r.now = r.now.Add(time.Second)
	r.b.autoWeights(time.Second)
}

func (r *autoRig) weights() string {
	return fmt.Sprintf("%d/%d", r.b.link(r.eth).weight, r.b.link(r.wifi).weight)
}

func TestAutoWeightsFollowSpeed(t *testing.T) {
	r := newAutoRig(t)
	if !r.b.AutoWeights() || r.weights() != "100/100" {
		t.Fatalf("start: auto %v, weights %s; want equal", r.b.AutoWeights(), r.weights())
	}
	r.second(0, 0) // primes the counters
	r.second(20, 0)
	if r.weights() != "100/100" {
		t.Errorf("Wi-Fi not measured yet: weights %s, want equal", r.weights())
	}
	r.second(20, 10)
	if r.weights() != "100/50" {
		t.Errorf("20 and 10 Mbit/s: weights %s, want 100/50", r.weights())
	}
	if len(r.log) != 1 || !strings.Contains(r.log[0], "Ethernet 100 (20.0 Mbit/s), Wi-Fi 50 (10.0 Mbit/s)") {
		t.Errorf("log: %q", r.log)
	}

	// Wi-Fi idle for a while: its estimate holds (it isn't slow, only
	// unused) and doesn't climb past half of Ethernet's.
	for i := 0; i < 30; i++ {
		r.second(20, 0)
	}
	if w := r.b.link(r.wifi).weight; w != 50 {
		t.Errorf("idle Wi-Fi: weight %d, want 50 (held at half the best)", w)
	}

	// Ethernet busy but slower (its line got worse): its peak sinks a few %
	// a second, so the weights follow.
	for i := 0; i < 30; i++ {
		r.second(8, 10)
	}
	if e, w := r.b.link(r.eth).weight, r.b.link(r.wifi).weight; w != 100 || e > 90 {
		t.Errorf("Ethernet down to 8 Mbit/s: weights %d/%d, want Wi-Fi 100 and Ethernet lower", e, w)
	}
	// The change was logged once more, not every second.
	if len(r.log) != 2 {
		t.Errorf("want 2 log lines (first measurement, one change), got %d: %q", len(r.log), r.log)
	}
}

// A slow line keeps at least autoMin, so it still gets the odd flow and its
// speed is noticed when it recovers.
func TestAutoWeightsFloor(t *testing.T) {
	r := newAutoRig(t)
	r.second(0, 0)
	r.second(100, 1)
	if w := r.b.link(r.wifi).weight; w != autoMin {
		t.Errorf("1 vs 100 Mbit/s: Wi-Fi weight %d, want the floor %d", w, autoMin)
	}
	r.second(100, 60)
	if w := r.b.link(r.wifi).weight; w != 60 {
		t.Errorf("Wi-Fi recovered to 60: weight %d, want 60", w)
	}
}

// A new line starts equal and makes all weights equal until it's measured.
func TestAutoWeightsNewLine(t *testing.T) {
	r := newAutoRig(t)
	r.second(0, 0)
	r.second(20, 10)
	phone := &netif.Adapter{Name: "Phone", LUID: 9, Index: 40, IPv4: r.eth.IPv4, Up: true}
	phone.Gateway = phone.IPv4.Addr()
	// applyAdapters matches by LUID; give the rig's lines theirs.
	r.eth.LUID, r.wifi.LUID = 1, 2
	r.eth.Up, r.wifi.Up = true, true
	r.eth.Gateway, r.wifi.Gateway = r.eth.IPv4.Addr(), r.wifi.IPv4.Addr()
	r.b.applyAdapters([]*netif.Adapter{r.eth, r.wifi, phone})
	if l := r.b.link(phone); l == nil || l.weight != autoFull {
		t.Fatalf("new line: %+v, want weight %d", l, autoFull)
	}
	r.second(20, 10)
	if r.weights() != "100/100" {
		t.Errorf("phone not measured yet: weights %s, want equal", r.weights())
	}
}

// Weights set by hand turn automatic weights off.
func TestAutoWeightsOffWithFixedWeights(t *testing.T) {
	eth, wifi, ups := testUplinks()
	cfg := config.Default()
	cfg.Weights = map[string]int{"eth": 2, "wifi": 1}
	b := New(ups, map[*netif.Adapter]int{eth: 2, wifi: 1}, &cfg)
	if b.AutoWeights() || b.link(eth).weight != 2 || b.link(wifi).weight != 1 {
		t.Errorf("fixed weights: auto %v, weights %d/%d", b.AutoWeights(), b.link(eth).weight, b.link(wifi).weight)
	}
}
