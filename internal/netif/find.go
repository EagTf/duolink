package netif

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
)

// Find matches a user-given key against uplink names. Case, spaces and
// punctuation are ignored, and a unique prefix is enough: "eth" → "Ethernet",
// "wifi" → "Wi-Fi". An exact match wins over prefixes ("ethernet" vs "Ethernet 2").
func Find(ups []*Adapter, key string) (*Adapter, error) {
	k := norm(key)
	var hits []*Adapter
	for _, a := range ups {
		if norm(a.Name) == k {
			return a, nil
		}
		if strings.HasPrefix(norm(a.Name), k) {
			hits = append(hits, a)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return nil, fmt.Errorf("no usable uplink matches %q (see `duolink list`)", key)
	}
	var names []string
	for _, a := range hits {
		names = append(names, a.Name)
	}
	return nil, fmt.Errorf("%q is ambiguous: matches %s", key, strings.Join(names, ", "))
}

func norm(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// OverlapWarnings flags uplinks that are probably on the same LAN or router.
// Either case breaks the "one line per adapter" assumption: the same gateway
// usually means the same internet line, and overlapping subnets make the
// on-link routes ambiguous, so Windows can ARP for the wrong router.
func OverlapWarnings(up []*Adapter) []string {
	var out []string
	for i, a := range up {
		for _, b := range up[i+1:] {
			if a.Gateway == b.Gateway {
				out = append(out, fmt.Sprintf("%s and %s use the same gateway %s: probably the same router and line, so merging gains nothing",
					a.Name, b.Name, a.Gateway))
			} else if a.IPv4.Masked().Overlaps(b.IPv4.Masked()) {
				out = append(out, fmt.Sprintf("%s (%s) and %s (%s) have overlapping subnets: give one router a different LAN range (e.g. 192.168.2.0/24)",
					a.Name, a.IPv4.Masked(), b.Name, b.IPv4.Masked()))
			}
		}
	}
	return out
}

// WithGlobalIPv6 lists adapters that have a public (2000::/3) IPv6 address.
// duolink routes only IPv4 into the TUN, so these adapters' IPv6 traffic goes
// straight out and is never balanced.
func WithGlobalIPv6(all []*Adapter) []*Adapter {
	var out []*Adapter
	for _, a := range all {
		if a.Type != windows.IF_TYPE_SOFTWARE_LOOPBACK && len(a.GlobalV6) > 0 {
			out = append(out, a)
		}
	}
	return out
}
