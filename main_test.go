package main

import (
	"net/netip"
	"strings"
	"testing"
)

func TestOverlapWarnings(t *testing.T) {
	ad := func(name, ip, gw string) *Adapter {
		return &Adapter{Name: name, IPv4: netip.MustParsePrefix(ip), Gateway: netip.MustParseAddr(gw)}
	}
	cases := []struct {
		a, b *Adapter
		want string // substring of the warning, "" for none
	}{
		{ad("eth", "192.168.2.10/24", "192.168.2.1"), ad("wifi", "192.168.1.20/24", "192.168.1.1"), ""},
		{ad("eth", "192.168.1.10/24", "192.168.1.1"), ad("wifi", "192.168.1.20/24", "192.168.1.1"), "same gateway"},
		{ad("eth", "192.168.1.10/24", "192.168.1.1"), ad("wifi", "192.168.1.20/24", "192.168.1.254"), "overlapping subnets"},
		{ad("eth", "10.0.0.5/8", "10.0.0.1"), ad("wifi", "10.1.2.3/24", "10.1.2.1"), "overlapping subnets"},
	}
	for _, c := range cases {
		got := strings.Join(overlapWarnings([]*Adapter{c.a, c.b}), "\n")
		if c.want == "" && got != "" || !strings.Contains(got, c.want) {
			t.Errorf("%s / %s: got %q, want %q", c.a.IPv4, c.b.IPv4, got, c.want)
		}
	}
}
