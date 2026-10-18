// Package engine runs duolink: it creates the TUN adapter, starts the
// netstack, adds the routes, checks them, and balances flows until stopped.
// Stop undoes everything, routes first. An engine can be started and stopped
// any number of times (the tray's Pause/Start); only one runs per PC.
package engine

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/eagtf/duolink/internal/balance"
	"github.com/eagtf/duolink/internal/config"
	"github.com/eagtf/duolink/internal/forward"
	"github.com/eagtf/duolink/internal/netif"
	"github.com/eagtf/duolink/internal/route"
	"github.com/xjasonlyu/tun2socks/v2/core"
	t2stun "github.com/xjasonlyu/tun2socks/v2/core/device/tun"
	"golang.org/x/sys/windows"
	wgtun "golang.zx2c4.com/wireguard/tun"
)

// Options configure one run of the engine.
type Options struct {
	Config     config.Config
	KeepOnFail bool              // dump the routing table if the self-check fails
	OnStats    func(Stats)       // called every StatsEvery while running
	OnEvent    func(line string) // health and failover events (DOWN, back UP, ...)
	StatsEvery time.Duration     // default 2 s
}

// Engine is one running instance. Everything it prints goes to stdout,
// which the tray redirects to its log file.
type Engine struct {
	opts    Options
	ups     []*netif.Adapter
	weights map[*netif.Adapter]int
	bal     *balance.Balancer
	fwd     *forward.Forwarder

	cancel context.CancelFunc
	undo   []func() // run in reverse by Stop
	once   sync.Once
}

// Start brings duolink up and returns once it's running. On any error it
// undoes what it already did before returning, so a failed Start leaves the
// system as it was.
func Start(o Options) (_ *Engine, err error) {
	if o.StatsEvery == 0 {
		o.StatsEvery = 2 * time.Second
	}
	e := &Engine{opts: o}
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	// touched turns true just before the first change to the system (the
	// TUN adapter). A failure before that changed nothing, so it's safe to
	// simply try again later (e.g. Wi-Fi not connected yet at logon).
	touched := false
	// On failure, undo whatever was already set up. This uses the local e:
	// `return nil, err` has already cleared the result by the time it runs.
	defer func() {
		if err != nil {
			e.Stop()
			if !touched {
				err = retryable{err}
			}
		}
	}()

	// Admin first: without it, even creating the global mutex fails, with a
	// less helpful "access denied".
	if !IsAdmin() {
		return nil, errors.New("duolink must run as administrator (it changes routes)")
	}
	if err := e.lock(); err != nil {
		return nil, err
	}
	if err := CheckWintunDLL(); err != nil {
		return nil, err
	}
	all, err := netif.List()
	if err != nil {
		return nil, err
	}
	e.ups = netif.Uplinks(all)
	if len(e.ups) < 2 {
		return nil, fmt.Errorf("need at least 2 usable uplinks, found %d (run `duolink list` to see why)", len(e.ups))
	}
	for _, w := range netif.OverlapWarnings(e.ups) {
		fmt.Println("warning:", w)
	}
	PrintIPv6Warning(all)

	cfg := &e.opts.Config
	if e.weights, err = balance.ResolveWeights(cfg.Weights, e.ups); err != nil {
		return nil, err
	}
	fmt.Println("uplinks:")
	active := 0
	for _, a := range e.ups {
		note := ""
		if e.weights[a] == 0 {
			note = "  (weight 0: not used)"
		} else {
			active++
		}
		fmt.Printf("  %-12s %-15s gateway %-15s weight %d%s\n", a.Name, a.IPv4.Addr(), a.Gateway, e.weights[a], note)
	}
	if active < 2 {
		fmt.Println("note: only one uplink has a non-zero weight, so nothing is being combined")
	}
	e.bal = balance.New(e.ups, e.weights, cfg)
	if o.OnEvent != nil {
		e.bal.OnLog(o.OnEvent)
	}
	if ttl := cfg.StickyTTL(); ttl > 0 {
		fmt.Printf("stickiness: on, a destination stays on its line for %v after its last new flow\n", ttl)
	} else {
		fmt.Println("stickiness: off, every flow is balanced on its own")
	}
	rules, err := balance.ResolveRules(cfg.Processes, e.ups, e.weights)
	if err != nil {
		return nil, err
	}
	fmt.Println("process rules:", rules.Describe())

	// A previous run that crashed may have left routes behind.
	RemoveLeftovers(all)

	// Create the Wintun adapter. wireguard-go's tun package calls
	// WintunCreateAdapter in wintun.dll; tun2socks wraps the result as a gVisor
	// link endpoint. The adapter exists as long as this process holds it.
	wgtun.WintunTunnelType = "duolink" // shown in Device Manager instead of "WireGuard"
	touched = true
	dev, err := t2stun.Open(netif.TunName, 0)
	if err != nil {
		return nil, fmt.Errorf("create TUN adapter: %w", err)
	}
	e.undo = append(e.undo, dev.Close)
	tun, err := waitForAdapter(dev.Name())
	if err != nil {
		return nil, err
	}
	fmt.Printf("created adapter %q (index %d)\n", tun.Name, tun.Index)

	// The netstack: every TCP/UDP flow Windows routes into the TUN is
	// terminated here and handed to the forwarder.
	e.fwd = forward.New(e.ups, e.bal, rules, cfg.UDPIdle())
	st, err := core.CreateStack(&core.Config{LinkEndpoint: dev, TransportHandler: e.fwd})
	if err != nil {
		return nil, fmt.Errorf("start netstack: %w", err)
	}
	e.undo = append(e.undo, st.Close)

	if err := route.ConfigureTUN(tun.Index); err != nil {
		return nil, err
	}
	// Don't route anything into the adapter until it's up and its address is
	// usable as a source; otherwise lookups via it fail as "unreachable".
	if tun, err = route.WaitTUNReady(tun.Name, 10*time.Second); err != nil {
		return nil, err
	}
	fmt.Printf("adapter is up, %s is ready\n", tun.IPv4)
	// Registered before adding, so a half-finished AddRoutes is undone too.
	// Last in the undo list = first to run: routes go before the netstack
	// and adapter.
	tunIndex := tun.Index
	e.undo = append(e.undo, func() { restoreRoutes(tunIndex) })
	if err := route.AddRoutes(tun.Index); err != nil {
		return nil, err
	}
	if err := e.selfCheck(ctx, tun); err != nil {
		if o.KeepOnFail {
			fmt.Fprintln(os.Stderr, "self-check failed:", err)
			route.Dump(netip.MustParseAddr("1.1.1.1"), tun.Index)
		}
		return nil, err
	}

	e.bal.Run(ctx)
	// Instant failover on adapter changes. If the subscription fails, the
	// health probes still catch a dead line, just later, so carry on.
	if err := netif.Watch(ctx, e.bal.OnAdapterChange); err != nil {
		fmt.Println("warning: can't watch adapter changes, failover relies on health probes only:", err)
	}
	if o.OnStats != nil {
		go e.sampleStats(ctx)
	}
	return e, nil
}

// Stop undoes Start: background work first, then routes (so normal internet
// is back at once), then the netstack and the adapter. Safe to call twice.
func (e *Engine) Stop() {
	e.once.Do(func() {
		e.cancel()
		for i := len(e.undo) - 1; i >= 0; i-- {
			e.undo[i]()
		}
	})
}

// Uplinks returns the lines this run balances over.
func (e *Engine) Uplinks() []*netif.Adapter { return e.ups }

// retryable marks a Start error that happened before anything on the system
// changed, so retrying it costs nothing and can't disturb the network.
type retryable struct{ error }

func (r retryable) Unwrap() error { return r.error }

// Retryable reports whether a failed Start changed nothing on the system, so
// it can safely be retried automatically. Errors after that point (a failed
// self-check, say) had already added and removed routes; retrying those in a
// loop would make the connection blip every time.
func Retryable(err error) bool {
	var r retryable
	return errors.As(err, &r)
}

const mutexName = `Global\duolink-engine`

// Running reports whether an engine (the tray or `duolink run`) is running
// on this PC. OpenMutex only opens an existing mutex, never creates one; it
// fails with ERROR_FILE_NOT_FOUND when nothing holds it. Any other failure
// (e.g. access denied) means the mutex exists, so an engine is running.
func Running() bool {
	name, _ := windows.UTF16PtrFromString(mutexName)
	h, err := windows.OpenMutex(windows.SYNCHRONIZE, false, name)
	if err != nil {
		return !errors.Is(err, windows.ERROR_FILE_NOT_FOUND)
	}
	windows.CloseHandle(h)
	return true
}

// lock takes the PC-wide "an engine is running" mutex. A second
// `duolink run`, or `run` while the tray is active, would create a second
// adapter and duplicate routes, so it's refused instead.
//
// CreateMutex with a "Global\" name is visible across sessions; if it already
// exists, Windows still returns a handle but sets ERROR_ALREADY_EXISTS.
// Windows releases the mutex when the process exits, even after a crash.
func (e *Engine) lock() error {
	name, _ := windows.UTF16PtrFromString(mutexName)
	h, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		windows.CloseHandle(h)
		return errors.New("duolink is already running (in the tray or another `duolink run`)")
	}
	if err != nil {
		return fmt.Errorf("CreateMutex: %w", err)
	}
	e.undo = append(e.undo, func() { windows.CloseHandle(h) })
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
func (e *Engine) selfCheck(ctx context.Context, tun *netif.Adapter) error {
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

	for _, a := range e.ups {
		idx, err := route.BestInterface(a.Gateway)
		if err == nil && idx == a.Index {
			fmt.Printf("  ok   %s (router %s) stays direct on %s\n", a.IPv4.Masked(), a.Gateway, a.Name)
		} else {
			fmt.Printf("  WARN %s (router %s) is routed via interface %d, not %s: router admin page may be unreachable\n",
				a.IPv4.Masked(), a.Gateway, idx, a.Name)
		}
	}

	before := e.fwd.Loops()
	dialErrs := make([]error, len(e.ups))
	var wg sync.WaitGroup
	for i, a := range e.ups {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dialErrs[i] = balance.Probe(ctx, a, e.opts.Config.Health)
		}()
	}
	wg.Wait()
	// A looped connection is accepted by the netstack first and only then
	// reaches the forwarder, so give it a moment to be counted.
	time.Sleep(300 * time.Millisecond)
	if e.fwd.Loops() != before {
		return errors.New("ROUTING LOOP: sockets bound to an uplink went into the TUN. IP_UNICAST_IF binding is not working; aborting")
	}
	for i, a := range e.ups {
		if err := dialErrs[i]; err != nil {
			e.bal.SetHealthy(a, false)
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

// RemoveLeftovers deletes duolink's routes from any duolink adapter that is
// still around. Returns how many such adapters it found.
func RemoveLeftovers(all []*netif.Adapter) int {
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

// IsAdmin reports whether the process runs elevated. The process token's
// TokenElevation flag is true only under "Run as administrator" (UAC), which
// is what changing routes needs.
func IsAdmin() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// CheckWintunDLL makes sure wintun.dll sits next to the running exe. The
// wintun package loads it with LoadLibraryEx(LOAD_LIBRARY_SEARCH_APPLICATION_DIR|
// LOAD_LIBRARY_SEARCH_SYSTEM32), so a DLL dropped in the current directory or
// on PATH is never picked up. That also means a missing DLL fails late and
// cryptically, so we check up front.
func CheckWintunDLL() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	p := filepath.Join(filepath.Dir(exe), "wintun.dll")
	if _, err := os.Stat(p); err != nil {
		return fmt.Errorf("wintun.dll not found at %s\n"+
			"  download the official zip from https://www.wintun.net/ and copy\n"+
			"  wintun\\bin\\amd64\\wintun.dll next to duolink.exe", p)
	}
	return nil
}

// PrintIPv6Warning says whether IPv6 bypasses duolink on this PC: duolink
// routes only IPv4 into the TUN.
func PrintIPv6Warning(all []*netif.Adapter) {
	v6 := netif.WithGlobalIPv6(all)
	if len(v6) == 0 {
		fmt.Println("IPv6: no adapter has a global IPv6 address. All traffic goes through duolink.")
		return
	}
	fmt.Println("WARNING: IPv6 is active and bypasses duolink (it balances IPv4 only):")
	for _, a := range v6 {
		fmt.Printf("  %-12s %s\n", a.Name, a.GlobalV6[0])
	}
	fmt.Println("  Apps that prefer IPv6 (Steam's CDN may) will use only that line.")
	fmt.Println("  To turn IPv6 off on an adapter (reversible with Enable-NetAdapterBinding), in admin PowerShell:")
	for _, a := range v6 {
		fmt.Printf("    Disable-NetAdapterBinding -Name '%s' -ComponentID ms_tcpip6\n", a.Name)
	}
}
