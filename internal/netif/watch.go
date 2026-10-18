package netif

import (
	"context"
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Watch calls onChange whenever Windows reports that an IPv4 interface
// or IPv4 address changed, until ctx ends. Events come in bursts (unplugging
// a cable changes the link, the address and the routes within milliseconds),
// so they're coalesced: onChange runs once, shortly after a burst settles.
//
// Two iphlpapi subscriptions feed it:
//   - NotifyIpInterfaceChange(AF_INET): an interface was added, removed or
//     changed state, e.g. media connected/disconnected.
//   - NotifyUnicastIpAddressChange(AF_INET): an IPv4 address was added,
//     removed or changed, e.g. DHCP lease lost or renewed with a new address.
//
// Windows calls the callback on one of its own thread-pool threads. It only
// pokes a channel; the real work happens in our goroutine. The rows passed to
// the callback are deliberately ignored: onChange re-reads the full adapter
// list, which is simpler and can't miss a change.
func Watch(ctx context.Context, onChange func()) error {
	poke := make(chan struct{}, 1)
	cb := windows.NewCallback(func(callerContext, row, notificationType uintptr) uintptr {
		select {
		case poke <- struct{}{}:
		default: // a poke is already pending; it covers this event too
		}
		return 0
	})

	var hIf, hAddr windows.Handle
	if err := windows.NotifyIpInterfaceChange(windows.AF_INET, cb, unsafe.Pointer(nil), false, &hIf); err != nil {
		return fmt.Errorf("NotifyIpInterfaceChange: %w", err)
	}
	if err := windows.NotifyUnicastIpAddressChange(windows.AF_INET, cb, unsafe.Pointer(nil), false, &hAddr); err != nil {
		windows.CancelMibChangeNotify2(hIf)
		return fmt.Errorf("NotifyUnicastIpAddressChange: %w", err)
	}

	go func() {
		// CancelMibChangeNotify2 unsubscribes; it waits for any callback that
		// is running right now to return, so none fires after this.
		defer windows.CancelMibChangeNotify2(hAddr)
		defer windows.CancelMibChangeNotify2(hIf)
		for {
			select {
			case <-ctx.Done():
				return
			case <-poke:
			}
			// Let the burst settle, then drop the pokes it produced meanwhile.
			select {
			case <-ctx.Done():
				return
			case <-time.After(300 * time.Millisecond):
			}
			select {
			case <-poke:
			default:
			}
			onChange()
		}
	}()
	return nil
}

// Issue compares an uplink as it was at startup with its current state
// and returns why it can't carry traffic now, or "" if it still can. cur is
// nil if the adapter no longer exists.
//
// A changed address or interface index is reported, not followed: sockets are
// bound to the startup address, so using the new one needs a restart.
// (Picking up such changes live is the "hot-plugging" item in the plan.)
func Issue(orig, cur *Adapter) string {
	switch {
	case cur == nil:
		return "adapter removed"
	case !cur.Up:
		return "adapter disconnected"
	case !cur.IPv4.IsValid() || cur.IPv4.Addr().IsLinkLocalUnicast():
		return "adapter lost its IPv4 address"
	case cur.IPv4.Addr() != orig.IPv4.Addr():
		return fmt.Sprintf("address changed from %s to %s; restart duolink to use it", orig.IPv4.Addr(), cur.IPv4.Addr())
	case cur.Index != orig.Index:
		return "interface index changed; restart duolink to use it"
	case !cur.Gateway.IsValid():
		return "adapter lost its default gateway"
	}
	return ""
}

// FindByLUID returns the adapter with the given LUID, or nil. The LUID stays
// the same while an adapter exists, even across disconnects.
func FindByLUID(all []*Adapter, luid uint64) *Adapter {
	for _, a := range all {
		if a.LUID == luid {
			return a
		}
	}
	return nil
}
