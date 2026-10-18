package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

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
	all, err := listAdapters()
	if err != nil {
		return err
	}
	var ups []*Adapter
	for _, a := range all {
		if a.Usable() {
			ups = append(ups, a)
		}
	}
	if len(ups) < 2 {
		return fmt.Errorf("need at least 2 usable uplinks, found %d (run `duolink list` to see why)", len(ups))
	}
	for _, w := range overlapWarnings(ups) {
		fmt.Println("warning:", w)
	}
	printIPv6Warning(all)

	weights, err := resolveWeights(cfg.Weights, ups)
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
	bal := newBalancer(ups, weights, &cfg)
	if ttl := cfg.stickyTTL(); ttl > 0 {
		fmt.Printf("stickiness: on, a destination stays on its line for %v after its last new flow\n", ttl)
	} else {
		fmt.Println("stickiness: off, every flow is balanced on its own")
	}
	rules, err := resolveRules(cfg.Processes, ups, weights)
	if err != nil {
		return err
	}
	fmt.Println("process rules:", rules.describe())

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
	dev, err := t2stun.Open(tunName, 0)
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
	fwd := newForwarder(ups, bal, rules, time.Duration(cfg.UDPIdleSeconds)*time.Second)
	st, err := core.CreateStack(&core.Config{LinkEndpoint: dev, TransportHandler: fwd})
	if err != nil {
		return fmt.Errorf("start netstack: %w", err)
	}
	defer st.Close()

	if err := configureTUN(tun.Index); err != nil {
		return err
	}
	// Don't route anything into the adapter until it's up and its address is
	// usable as a source; otherwise lookups via it fail as "unreachable".
	if tun, err = waitTUNReady(tun.Name, 10*time.Second); err != nil {
		return err
	}
	fmt.Printf("adapter is up, %s is ready\n", tun.IPv4)
	// Registered before adding, so a half-finished addRoutes is undone too.
	// Deferred last = runs first: routes go before the netstack and adapter.
	defer restoreRoutes(tun.Index)
	if err := addRoutes(tun.Index); err != nil {
		return err
	}
	if err := selfCheck(ctx, tun, ups, fwd, bal); err != nil {
		if *keepOnFail {
			fmt.Fprintln(os.Stderr, "self-check failed:", err)
			dumpRouting(netip.MustParseAddr("1.1.1.1"), tun.Index)
		}
		return err
	}
	if ctx.Err() != nil {
		return nil
	}

	bal.run(ctx)
	// Instant failover on adapter changes. If the subscription fails, the
	// health probes still catch a dead line, just later, so carry on.
	if err := watchAdapters(ctx, bal.onAdapterChange); err != nil {
		fmt.Println("warning: can't watch adapter changes, failover relies on health probes only:", err)
	}
	fmt.Println("\nrunning. Press Ctrl+C to stop.")
	fwd.printStats(ctx, ups, weights, bal, 2*time.Second)
	fmt.Println("\nstopping...")
	return nil
}

func restoreRoutes(tunIndex uint32) {
	fmt.Println("removing routes:")
	if err := removeRoutes(tunIndex); err != nil {
		fmt.Fprintln(os.Stderr, "warning:", err, "\n  run `duolink cleanup`, or reboot (routes are not persistent)")
	}
	if idx, err := bestInterface(netip.MustParseAddr("1.1.1.1")); err == nil {
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
func selfCheck(ctx context.Context, tun *Adapter, ups []*Adapter, fwd *forwarder, bal *balancer) error {
	fmt.Println("\nself-check:")
	target := netip.MustParseAddr("1.1.1.1")
	idx, err := bestInterfaceRetry(target, tun.Index, 5*time.Second)
	if err != nil {
		return fmt.Errorf("GetBestInterfaceEx(%s) still failing after 5s: %w", target, err)
	}
	if idx != tun.Index {
		return fmt.Errorf("routes not effective: %s still goes via interface %d, not the TUN (%d)", target, idx, tun.Index)
	}
	fmt.Printf("  ok   internet (%s) is routed into the TUN\n", target)

	for _, a := range ups {
		idx, err := bestInterface(a.Gateway)
		if err == nil && idx == a.Index {
			fmt.Printf("  ok   %s (router %s) stays direct on %s\n", a.IPv4.Masked(), a.Gateway, a.Name)
		} else {
			fmt.Printf("  WARN %s (router %s) is routed via interface %d, not %s: router admin page may be unreachable\n",
				a.IPv4.Masked(), a.Gateway, idx, a.Name)
		}
	}

	before := fwd.loops.Load()
	dialErrs := make([]error, len(ups))
	var wg sync.WaitGroup
	for i, a := range ups {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dialErrs[i] = probe(ctx, a, bal.health)
		}()
	}
	wg.Wait()
	// A looped connection is accepted by the netstack first and only then
	// reaches the forwarder, so give it a moment to be counted.
	time.Sleep(300 * time.Millisecond)
	if fwd.loops.Load() != before {
		return errors.New("ROUTING LOOP: sockets bound to an uplink went into the TUN. IP_UNICAST_IF binding is not working; aborting")
	}
	for i, a := range ups {
		if err := dialErrs[i]; err != nil {
			bal.setHealthy(a, false)
			fmt.Printf("  WARN %s: health probe failed (%v): starting it as DOWN\n", a.Name, err)
		} else {
			fmt.Printf("  ok   %s: bound socket reached the internet directly, not through the TUN (no loop)\n", a.Name)
		}
	}
	return nil
}

// waitForAdapter finds the freshly created TUN in the adapter list. Windows
// can take a moment to bind IPv4 to a new adapter (its IPv4 IfIndex is 0 until then).
func waitForAdapter(name string) (*Adapter, error) {
	for i := 0; i < 20; i++ {
		all, err := listAdapters()
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

// isTunName reports whether name is our TUN adapter, including the " 2"-style
// suffix Windows adds when a name is taken.
func isTunName(name string) bool {
	return name == tunName || strings.HasPrefix(name, tunName+" ")
}

// removeLeftovers deletes duolink's routes from any duolink adapter that is still
// around. Returns how many such adapters it found.
func removeLeftovers(all []*Adapter) int {
	n := 0
	for _, a := range all {
		if !isTunName(a.Name) || a.Index == 0 {
			continue
		}
		n++
		fmt.Printf("found leftover adapter %q (index %d), removing its routes:\n", a.Name, a.Index)
		if err := removeRoutes(a.Index); err != nil {
			fmt.Println("  (not all routes were present, which is fine)")
		}
	}
	return n
}

func cmdCleanup() error {
	if !isAdmin() {
		return errors.New("duolink cleanup must be run as administrator")
	}
	all, err := listAdapters()
	if err != nil {
		return err
	}
	if removeLeftovers(all) == 0 {
		fmt.Println("no duolink adapter found. Its routes only exist while the adapter does, so there is nothing to remove.")
	}
	if idx, err := bestInterface(netip.MustParseAddr("1.1.1.1")); err == nil {
		for _, a := range all {
			if a.Index == idx {
				fmt.Printf("internet traffic goes via %s (interface %d)\n", a.Name, idx)
			}
		}
	}
	return nil
}

// findAdapter matches a user-given key against uplink names. Case, spaces and
// punctuation are ignored, and a unique prefix is enough: "eth" → "Ethernet",
// "wifi" → "Wi-Fi". An exact match wins over prefixes ("ethernet" vs "Ethernet 2").
func findAdapter(ups []*Adapter, key string) (*Adapter, error) {
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
