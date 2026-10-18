package main

import (
	"errors"
	"fmt"
	"net/netip"
	"unsafe"

	"golang.org/x/sys/windows"
)

// tunName is the name of the Wintun adapter duolink creates (milestone 3).
// It is never a candidate uplink: dialing through it would loop forever.
const tunName = "duolink"

// Adapter is one network interface as seen by Windows, reduced to what duolink needs.
type Adapter struct {
	Name    string // friendly name, e.g. "Ethernet", "Wi-Fi"
	Desc    string // driver description, e.g. "Intel(R) Wi-Fi 6E AX211"
	Index   uint32 // IPv4 interface index; what IP_UNICAST_IF and route commands take
	LUID    uint64 // locally unique ID; stable for the adapter's lifetime
	Type    uint32 // IF_TYPE_* (6 = Ethernet, 71 = Wi-Fi, ...)
	IPv4    netip.Prefix
	Gateway netip.Addr
	Metric  uint32 // interface metric Windows uses to pick between default routes
	SpeedRx uint64 // link speed in bit/s as reported by the driver

	GlobalV6 []netip.Addr // public IPv6 addresses; v1 doesn't balance these

	Up      bool  // OperStatus == IfOperStatusUp
	IPv4DAD int32 // duplicate-address-detection state of IPv4 (IpDadState*)

	Skip string // non-empty if the adapter can't be used, with the reason
}

func (a *Adapter) Usable() bool { return a.Skip == "" }

func (a *Adapter) TypeName() string {
	switch a.Type {
	case windows.IF_TYPE_ETHERNET_CSMACD:
		return "ethernet"
	case windows.IF_TYPE_IEEE80211:
		return "wifi"
	case windows.IF_TYPE_SOFTWARE_LOOPBACK:
		return "loopback"
	case windows.IF_TYPE_TUNNEL:
		return "tunnel"
	case 53: // IF_TYPE_PROP_VIRTUAL: Wintun, Hyper-V, many VPNs
		return "virtual"
	case 243, 244: // IF_TYPE_WWANPP / WWANPP2: built-in mobile broadband
		return "mobile"
	}
	return fmt.Sprintf("type %d", a.Type)
}

// listAdapters enumerates every adapter Windows knows about and marks the ones
// duolink can't use as an uplink.
//
// GetAdaptersAddresses (iphlpapi.dll) is the modern replacement for GetAdaptersInfo.
// Family AF_UNSPEC returns IPv4 and IPv6 addresses. We balance only IPv4 but
// need to see IPv6 to warn about it. The flags:
//   - GAA_FLAG_INCLUDE_GATEWAYS: also fill FirstGatewayAddress. Windows takes this
//     from the adapter's default route (0.0.0.0/0), so a gateway here means
//     "this adapter can reach the internet on its own".
//   - GAA_FLAG_SKIP_ANYCAST/MULTICAST/DNS_SERVER: leave out lists we don't need.
//
// The function writes a linked list of IP_ADAPTER_ADDRESSES into a buffer we
// provide. If the buffer is too small it returns ERROR_BUFFER_OVERFLOW and puts
// the required size in size, so we grow and retry (adapters can appear between calls).
func listAdapters() ([]*Adapter, error) {
	const flags = windows.GAA_FLAG_INCLUDE_GATEWAYS |
		windows.GAA_FLAG_SKIP_ANYCAST |
		windows.GAA_FLAG_SKIP_MULTICAST |
		windows.GAA_FLAG_SKIP_DNS_SERVER

	size := uint32(16 * 1024)
	var buf []byte
	for {
		buf = make([]byte, size)
		first := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, flags, 0, first, &size)
		if err == nil {
			break
		}
		if !errors.Is(err, windows.ERROR_BUFFER_OVERFLOW) {
			return nil, fmt.Errorf("GetAdaptersAddresses: %w", err)
		}
	}

	var out []*Adapter
	for aa := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])); aa != nil; aa = aa.Next {
		a := &Adapter{
			Name:    windows.UTF16PtrToString(aa.FriendlyName),
			Desc:    windows.UTF16PtrToString(aa.Description),
			Index:   aa.IfIndex,
			LUID:    aa.Luid,
			Type:    aa.IfType,
			Metric:  aa.Ipv4Metric,
			SpeedRx: aa.ReceiveLinkSpeed,
			Up:      aa.OperStatus == windows.IfOperStatusUp,
		}
		// First usable unicast IPv4 address. An adapter can have several; we
		// bind outbound sockets to one, so the first non-link-local one is enough.
		for u := aa.FirstUnicastAddress; u != nil; u = u.Next {
			ip, ok := sockaddrToAddr(&u.Address)
			if !ok {
				continue
			}
			if ip.Is6() {
				// 2000::/3 global unicast; excludes link-local fe80:: and ULA fd00::.
				if ip.IsGlobalUnicast() && !ip.IsPrivate() {
					a.GlobalV6 = append(a.GlobalV6, ip)
				}
				continue
			}
			p := netip.PrefixFrom(ip, int(u.OnLinkPrefixLength))
			if !a.IPv4.IsValid() || a.IPv4.Addr().IsLinkLocalUnicast() {
				a.IPv4 = p
				a.IPv4DAD = u.DadState
			}
		}
		for g := aa.FirstGatewayAddress; g != nil; g = g.Next {
			if ip, ok := sockaddrToAddr(&g.Address); ok && ip.Is4() && !ip.IsUnspecified() {
				a.Gateway = ip
				break
			}
		}
		a.Skip = skipReason(a, aa.OperStatus)
		out = append(out, a)
	}
	return out, nil
}

// skipReason decides whether an adapter can carry traffic. Order matters: the
// first reason that applies is the most useful one to show.
func skipReason(a *Adapter, oper uint32) string {
	switch {
	case a.Type == windows.IF_TYPE_SOFTWARE_LOOPBACK:
		return "loopback"
	case isTunName(a.Name):
		return "duolink's own virtual adapter"
	case oper != windows.IfOperStatusUp:
		return "not connected"
	case !a.IPv4.IsValid():
		return "no IPv4 address"
	case a.IPv4.Addr().IsLinkLocalUnicast():
		return "only a 169.254.x.x address (DHCP failed?)"
	case !a.Gateway.IsValid():
		return "no default gateway (not an internet uplink)"
	}
	return ""
}

// sockaddrToAddr converts the SOCKET_ADDRESS Windows hands back into a netip.Addr.
func sockaddrToAddr(sa *windows.SocketAddress) (netip.Addr, bool) {
	if sa.Sockaddr == nil {
		return netip.Addr{}, false
	}
	ip, ok := netip.AddrFromSlice(sa.IP())
	return ip.Unmap(), ok
}

// usableAdapters returns only adapters that can be an uplink.
func usableAdapters() ([]*Adapter, error) {
	all, err := listAdapters()
	if err != nil {
		return nil, err
	}
	var out []*Adapter
	for _, a := range all {
		if a.Usable() {
			out = append(out, a)
		}
	}
	return out, nil
}
