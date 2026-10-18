package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/netip"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/eagtf/duolink/internal/balance"
	"github.com/eagtf/duolink/internal/config"
	"github.com/eagtf/duolink/internal/forward"
	"github.com/eagtf/duolink/internal/netif"
	"github.com/eagtf/duolink/internal/route"
	"github.com/xjasonlyu/tun2socks/v2/core"
	t2stun "github.com/xjasonlyu/tun2socks/v2/core/device/tun"
	wgtun "golang.zx2c4.com/wireguard/tun"
)

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cf := addConfigFlags(fs)
	dur := fs.Duration("for", 0, "stop and clean up automatically after this long, e.g. 2m")
	keepOnFail := fs.Bool("keep-on-fail", false, "if the self-check fails, dump the routing table before cleaning up")
	fs.Parse(args)
	cfg, found, err := cf.load()
	if err != nil {
		return err
	}
	if found {
		fmt.Println("config:", *cf.path)
	} else {
		fmt.Println("config: defaults (no", *cf.path+")")
	}

	if !isAdmin() {
		return errors.New("duolink run must be started as administrator (it changes routes)")
	}
	if err := checkWintunDLL(); err != nil {
		return err
	}
	all, err := netif.List()
	if err != nil {
		return err
	}
	ups := netif.Uplinks(all)
	if len(ups) < 2 {
		return fmt.Errorf("need at least 2 usable uplinks, found %d (run `duolink list` to see why)", len(ups))
	}
	for _, w := range netif.OverlapWarnings(ups) {
		fmt.Println("warning:", w)
	}
	printIPv6Warning(all)

	weights, err := balance.ResolveWeights(cfg.Weights, ups)
	if err != nil {
		return err
	}
	fmt.Println("uplinks:")
	active := 0
	for _, a := range ups {
		note := ""
		if weights[a] == 0 {
			note = "  (weight 0: not used)"
		} else {
			active++
		}
		fmt.Printf("  %-12s %-15s gateway %-15s weight %d%s\n", a.Name, a.IPv4.Addr(), a.Gateway, weights[a], note)
	}
	if active < 2 {
		fmt.Println("note: only one uplink has a non-zero weight, so nothing is being combined")
	}
	bal := balance.New(ups, weights, &cfg)
	if ttl := cfg.StickyTTL(); ttl > 0 {
		fmt.Printf("stickiness: on, a destination stays on its line for %v after its last new flow\n", ttl)
	} else {
		fmt.Println("stickiness: off, every flow is balanced on its own")
	}
	rules, err := balance.ResolveRules(cfg.Processes, ups, weights)
	if err != nil {
		return err
	}
	fmt.Println("process rules:", rules.Describe())

	// A previous run that crashed may have left routes behind.
	removeLeftovers(all)

	// Ctrl+C, closing the console window (Go maps CTRL_CLOSE_EVENT to SIGTERM),
	// or --for expiring all end up cancelling ctx, which runs the cleanup below.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *dur > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *dur)
		defer cancel()
		fmt.Printf("will stop and clean up automatically after %v\n", *dur)
	}

	// Create the Wintun adapter. wireguard-go's tun package calls
	// WintunCreateAdapter in wintun.dll; tun2socks wraps the result as a gVisor
	// link endpoint. The adapter exists as long as this process holds it.
	wgtun.WintunTunnelType = "duolink" // shown in Device Manager instead of "WireGuard"
	dev, err := t2stun.Open(netif.TunName, 0)
	if err != nil {
		return fmt.Errorf("create TUN adapter: %w", err)
	}
	defer dev.Close()
	tun, err := waitForAdapter(dev.Name())
	if err != nil {
		return err
	}
	fmt.Printf("created adapter %q (index %d)\n", tun.Name, tun.Index)

	// The netstack: every TCP/UDP flow Windows routes into the TUN is
	// terminated here and handed to the forwarder.
	fwd := forward.New(ups, bal, rules, cfg.UDPIdle())
	st, err := core.CreateStack(&core.Config{LinkEndpoint: dev, TransportHandler: fwd})
	if err != nil {
		return fmt.Errorf("start netstack: %w", err)
	}
	defer st.Close()

	if err := route.ConfigureTUN(tun.Index); err != nil {
		return err
	}
	// Don't route anything into the adapter until it's up and its address is
	// usable as a source; otherwise lookups via it fail as "unreachable".
	if tun, err = route.WaitTUNReady(tun.Name, 10*time.Second); err != nil {
		return err
	}
	fmt.Printf("adapter is up, %s is ready\n", tun.IPv4)
	// Registered before adding, so a half-finished AddRoutes is undone too.
	// Deferred last = runs first: routes go before the netstack and adapter.
	defer restoreRoutes(tun.Index)
	if err := route.AddRoutes(tun.Index); err != nil {
		return err
	}
	if err := selfCheck(ctx, tun, ups, fwd, bal, cfg.Health); err != nil {
		if *keepOnFail {
			fmt.Fprintln(os.Stderr, "self-check failed:", err)
			route.Dump(netip.MustParseAddr("1.1.1.1"), tun.Index)
		}
		return err
	}
	if ctx.Err() != nil {
		return nil
	}

	bal.Run(ctx)
	// Instant failover on adapter changes. If the subscription fails, the
	// health probes still catch a dead line, just later, so carry on.
	if err := netif.Watch(ctx, bal.OnAdapterChange); err != nil {
		fmt.Println("warning: can't watch adapter changes, failover relies on health probes only:", err)
	}
	fmt.Println("\nrunning. Press Ctrl+C to stop.")
	fwd.PrintStats(ctx, ups, weights, bal, 2*time.Second)
	fmt.Println("\nstopping...")
	return nil
}

func restoreRoutes(tunIndex uint32) {
	fmt.Println("removing routes:")
	if err := route.RemoveRoutes(tunIndex); err != nil {
		fmt.Fprintln(os.Stderr, "warning:", err, "\n  run `duolink cleanup`, or reboot (routes are not persistent)")
	}
	if idx, err := route.BestInterface(netip.MustParseAddr("1.1.1.1")); err == nil {
		fmt.Printf("internet traffic now goes via interface %d again\n", idx)
	}
}

// selfCheck verifies the routing does what we intend before we call it running:
//  1. internet destinations are routed into the TUN (the /1 routes took effect);
//  2. each router's own LAN is still reached directly, because its /24 on-link
//     route is more specific than our /1s, so admin pages keep working;
//  3. sockets bound to an uplink skip the TUN: we dial out through each uplink
//     and check that nothing from our own uplink addresses showed up in the
//     netstack (that would be a routing loop).
//
// Step 3 doubles as the first health check: an uplink that fails it starts out
// marked down, so it gets no flows and is re-probed every few seconds.
func selfCheck(ctx context.Context, tun *netif.Adapter, ups []*netif.Adapter, fwd *forward.Forwarder, bal *balance.Balancer, health config.HealthConfig) error {
	fmt.Println("\nself-check:")
	target := netip.MustParseAddr("1.1.1.1")
	idx, err := route.BestInterfaceRetry(target, tun.Index, 5*time.Second)
	if err != nil {
		return fmt.Errorf("GetBestInterfaceEx(%s) still failing after 5s: %w", target, err)
	}
	if idx != tun.Index {
		return fmt.Errorf("routes not effective: %s still goes via interface %d, not the TUN (%d)", target, idx, tun.Index)
	}
	fmt.Printf("  ok   internet (%s) is routed into the TUN\n", target)

	for _, a := range ups {
		idx, err := route.BestInterface(a.Gateway)
		if err == nil && idx == a.Index {
			fmt.Printf("  ok   %s (router %s) stays direct on %s\n", a.IPv4.Masked(), a.Gateway, a.Name)
		} else {
			fmt.Printf("  WARN %s (router %s) is routed via interface %d, not %s: router admin page may be unreachable\n",
				a.IPv4.Masked(), a.Gateway, idx, a.Name)
		}
	}

	before := fwd.Loops()
	dialErrs := make([]error, len(ups))
	var wg sync.WaitGroup
	for i, a := range ups {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dialErrs[i] = balance.Probe(ctx, a, health)
		}()
	}
	wg.Wait()
	// A looped connection is accepted by the netstack first and only then
	// reaches the forwarder, so give it a moment to be counted.
	time.Sleep(300 * time.Millisecond)
	if fwd.Loops() != before {
		return errors.New("ROUTING LOOP: sockets bound to an uplink went into the TUN. IP_UNICAST_IF binding is not working; aborting")
	}
	for i, a := range ups {
		if err := dialErrs[i]; err != nil {
			bal.SetHealthy(a, false)
			fmt.Printf("  WARN %s: health probe failed (%v): starting it as DOWN\n", a.Name, err)
		} else {
			fmt.Printf("  ok   %s: bound socket reached the internet directly, not through the TUN (no loop)\n", a.Name)
		}
	}
	return nil
}

// waitForAdapter finds the freshly created TUN in the adapter list. Windows
// can take a moment to bind IPv4 to a new adapter (its IPv4 IfIndex is 0 until then).
func waitForAdapter(name string) (*netif.Adapter, error) {
	for i := 0; i < 20; i++ {
		all, err := netif.List()
		if err != nil {
			return nil, err
		}
		for _, a := range all {
			if a.Name == name && a.Index != 0 {
				return a, nil
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return nil, fmt.Errorf("adapter %q did not appear with IPv4 enabled", name)
}

// removeLeftovers deletes duolink's routes from any duolink adapter that is still
// around. Returns how many such adapters it found.
func removeLeftovers(all []*netif.Adapter) int {
	n := 0
	for _, a := range all {
		if !netif.IsTunName(a.Name) || a.Index == 0 {
			continue
		}
		n++
		fmt.Printf("found leftover adapter %q (index %d), removing its routes:\n", a.Name, a.Index)
		if err := route.RemoveRoutes(a.Index); err != nil {
			fmt.Println("  (not all routes were present, which is fine)")
		}
	}
	return n
}

func cmdCleanup() error {
	if !isAdmin() {
		return errors.New("duolink cleanup must be run as administrator")
	}
	all, err := netif.List()
	if err != nil {
		return err
	}
	if removeLeftovers(all) == 0 {
		fmt.Println("no duolink adapter found. Its routes only exist while the adapter does, so there is nothing to remove.")
	}
	if idx, err := route.BestInterface(netip.MustParseAddr("1.1.1.1")); err == nil {
		for _, a := range all {
			if a.Index == idx {
				fmt.Printf("internet traffic goes via %s (interface %d)\n", a.Name, idx)
			}
		}
	}
	return nil
}
