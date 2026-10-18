// Package route gives the TUN adapter its address and points the internet at
// it with two non-persistent half-default routes, and checks the result.
package route

import (
	"errors"
	"fmt"
	"net/netip"

	"strings"
	"time"

	"github.com/eagtf/duolink/internal/netif"
	"golang.org/x/sys/windows"

	"github.com/eagtf/duolink/internal/winexec"
)

// The TUN adapter gets a tiny /30 from 198.18.0.0/15, the range RFC 2544
// reserves for benchmarking: it never appears on real LANs or the internet.
// Windows needs some address on the adapter so apps can pick a source IP when
// their traffic is routed into it. The netstack terminates every flow anyway,
// so this address never leaves the PC.
const (
	TunAddr = "198.18.0.1"
	tunMask = "255.255.255.252"
)

// TunMTU is the TUN's MTU, on the Windows side (ConfigureTUN) and in the
// netstack. The TUN's packets never leave the PC (every flow is re-dialed
// with a normal socket on a real line, which uses the line's own MTU), so the
// usual 1500 doesn't apply. Bigger packets mean far fewer trips through the
// netstack per megabyte, which saves CPU on fast downloads. Not 65535: the
// netstack reads every packet into a buffer of this size, even a tiny ACK.
const TunMTU = 9000

// TunPrefix is the TUN's /30; nothing inside it is a real destination.
var TunPrefix = netip.MustParsePrefix(TunAddr + "/30")

// The two half-default routes. Together they cover all of IPv4, and because a
// /1 is more specific than the real 0.0.0.0/0 default routes, they win without
// us touching (or even knowing about) those. Anything more specific than /1
// still wins over them: the routers' 192.168.x.0/24 on-link routes, loopback,
// multicast (224.0.0.0/4) and broadcast (/32) routes are unaffected.
var halfDefaults = []string{"0.0.0.0/1", "128.0.0.0/1"}

// netsh runs one netsh command and echoes it, so every change duolink makes to
// the system is visible and can be repeated or undone by hand.
func netsh(args ...string) error {
	fmt.Println("  + netsh", strings.Join(args, " "))
	out, err := winexec.Command("netsh", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("netsh %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ConfigureTUN gives the TUN adapter its address.
//
// `netsh interface ipv4 set address ... store=active` writes to the "active"
// store only: the setting lives in memory and is gone after a reboot. (The
// adapter itself is gone even sooner; see RemoveRoutes.) A freshly created
// Wintun adapter can take a moment to show up in the IP stack, hence the retries.
//
// Before that, `netsh interface ipv4 set interface ... dadtransmits=0` turns
// off duplicate address detection on the TUN. With DAD on, a new address stays
// "tentative" for a while and can't be used as a source address. Route lookups
// that land on the TUN then fail with ERROR_NETWORK_UNREACHABLE. A TUN has no
// neighbours that could hold a duplicate, so DAD is pointless here. WireGuard
// for Windows does the same on its Wintun adapters.
//
// Last, `netsh interface ipv4 set subinterface ... mtu=TunMTU store=active`
// sets the TUN's IPv4 MTU (in memory only), so Windows sends packets as big
// as the netstack accepts and fragments bigger UDP datagrams to fit. If that
// fails, duolink still works: TCP segment sizes follow the MSS the netstack
// announces, so it's only a warning.
func ConfigureTUN(ifIndex uint32) error {
	var err error
	for i := 0; i < 10; i++ {
		err = netsh("interface", "ipv4", "set", "interface", fmt.Sprintf("interface=%d", ifIndex),
			"dadtransmits=0", "store=active")
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		return err
	}
	for i := 0; i < 10; i++ {
		err = netsh("interface", "ipv4", "set", "address", fmt.Sprintf("name=%d", ifIndex),
			"source=static", "address="+TunAddr, "mask="+tunMask, "store=active")
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		return err
	}
	if err := netsh("interface", "ipv4", "set", "subinterface", fmt.Sprint(ifIndex),
		fmt.Sprintf("mtu=%d", TunMTU), "store=active"); err != nil {
		fmt.Println("warning: could not set the duolink adapter's MTU, using Windows' default:", err)
	}
	return nil
}

// AddRoutes points 0.0.0.0/1 and 128.0.0.0/1 at the TUN adapter.
//
// `netsh interface ipv4 add route prefix=P interface=IDX nexthop=0.0.0.0
// metric=1 store=active`:
//   - nexthop=0.0.0.0 makes the route "on-link": packets are handed straight to
//     the adapter, with no gateway. That's right for a TUN, which has no
//     neighbours to ARP for.
//   - metric=1 is the lowest route metric. It only matters against another
//     /1 route on some other interface (e.g. a VPN that uses the same trick).
//   - store=active again means non-persistent: a reboot always fixes things.
func AddRoutes(ifIndex uint32) error {
	for _, p := range halfDefaults {
		err := netsh("interface", "ipv4", "add", "route", "prefix="+p,
			fmt.Sprintf("interface=%d", ifIndex), "nexthop=0.0.0.0", "metric=1", "store=active")
		if err != nil {
			return err
		}
	}
	return nil
}

// RemoveRoutes deletes the half-default routes from one interface. Errors are
// collected but don't stop the loop: we want both deletes attempted.
//
// Routes belong to their interface, so when the Wintun adapter disappears its
// routes go with it. Wintun creates the adapter as a software device tied to
// our process handle, so even a crash or `taskkill /f` should remove it. This
// explicit delete restores connectivity *first*, before the netstack and
// adapter are torn down, and covers anything the automatic path misses.
// Only routes on duolink's own adapter are touched, so a VPN that also uses
// 0.0.0.0/1 (OpenVPN's def1 does) is left alone.
func RemoveRoutes(ifIndex uint32) error {
	var errs []error
	for _, p := range halfDefaults {
		err := netsh("interface", "ipv4", "delete", "route", "prefix="+p,
			fmt.Sprintf("interface=%d", ifIndex), "nexthop=0.0.0.0", "store=active")
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// BestInterface asks Windows which interface it would send a packet to ip out
// of, using the normal routing table (no socket options involved).
//
// GetBestInterfaceEx (iphlpapi.dll) runs the same longest-prefix-match + metric
// lookup the stack uses for an unbound socket. We use it to check that the
// routes behave as intended: internet → TUN, routers' LANs → the real adapters.
func BestInterface(ip netip.Addr) (uint32, error) {
	var idx uint32
	sa := &windows.SockaddrInet4{Addr: ip.As4()}
	err := windows.GetBestInterfaceEx(sa, &idx)
	return idx, err
}

// BestInterfaceRetry retries BestInterface for a few seconds until it returns
// want. Right after the routes go in, the stack may briefly report the TUN as
// unreachable.
func BestInterfaceRetry(ip netip.Addr, want uint32, timeout time.Duration) (uint32, error) {
	deadline := time.Now().Add(timeout)
	for {
		idx, err := BestInterface(ip)
		if (err == nil && idx == want) || time.Now().After(deadline) {
			return idx, err
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// WaitTUNReady polls until the TUN is operationally up and its address has
// left the tentative DAD state ("preferred" = usable as a source address).
func WaitTUNReady(name string, timeout time.Duration) (*netif.Adapter, error) {
	deadline := time.Now().Add(timeout)
	var last *netif.Adapter
	for {
		all, err := netif.List()
		if err != nil {
			return nil, err
		}
		for _, a := range all {
			if a.Name == name {
				last = a
			}
		}
		if last != nil && last.Up && last.IPv4.Addr().String() == TunAddr &&
			last.IPv4DAD == windows.IpDadStatePreferred {
			return last, nil
		}
		if time.Now().After(deadline) {
			if last == nil {
				return nil, fmt.Errorf("adapter %q disappeared", name)
			}
			addr := "none"
			if last.IPv4.IsValid() {
				addr = last.IPv4.String()
			}
			return last, fmt.Errorf("adapter %q not ready after %v: up=%v address=%s DAD state=%s",
				name, timeout, last.Up, addr, dadName(last.IPv4DAD))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func dadName(s int32) string {
	return [...]string{"invalid", "tentative", "duplicate", "deprecated", "preferred"}[min(max(s, 0), 4)]
}

// Dump prints the real routing state for a failed self-check:
//   - `route print -4`: the whole IPv4 routing table, as Windows sees it;
//   - `Find-NetRoute -RemoteIPAddress <ip>`: PowerShell's route lookup, which
//     also shows the source address Windows would pick (or why it can't);
//   - `Get-NetIPAddress -InterfaceIndex <tun>`: the TUN's address and DAD state.
func Dump(ip netip.Addr, tunIndex uint32) {
	run := func(name string, args ...string) {
		fmt.Printf("\n===== %s %s =====\n", name, strings.Join(args, " "))
		out, err := winexec.Command(name, args...).CombinedOutput()
		fmt.Print(string(out))
		if err != nil {
			fmt.Println("(exit:", err, ")")
		}
	}
	run("route", "print", "-4")
	run("powershell", "-NoProfile", "-Command",
		fmt.Sprintf("Find-NetRoute -RemoteIPAddress %s | Format-List *", ip))
	run("powershell", "-NoProfile", "-Command",
		fmt.Sprintf("Get-NetIPAddress -InterfaceIndex %d -AddressFamily IPv4 | Format-List IPAddress,PrefixLength,AddressState,SkipAsSource; "+
			"Get-NetIPInterface -InterfaceIndex %d -AddressFamily IPv4 | Format-List ConnectionState,DadTransmits,NlMtu,InterfaceMetric", tunIndex, tunIndex))
	fmt.Println("\n===== end of routing dump =====")
}
