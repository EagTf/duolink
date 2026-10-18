package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/netip"
	"os"
	"os/signal"
	"syscall"

	"github.com/eagtf/duolink/internal/engine"
	"github.com/eagtf/duolink/internal/netif"
	"github.com/eagtf/duolink/internal/route"
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

	// Ctrl+C, closing the console window (Go maps CTRL_CLOSE_EVENT to SIGTERM),
	// or --for expiring all end up cancelling ctx, which stops the engine.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *dur > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *dur)
		defer cancel()
		fmt.Printf("will stop and clean up automatically after %v\n", *dur)
	}

	eng, err := engine.Start(engine.Options{Config: cfg, KeepOnFail: *keepOnFail, OnStats: printStats})
	if err != nil {
		return err
	}
	fmt.Println("\nrunning. Press Ctrl+C to stop.")
	<-ctx.Done()
	fmt.Println("\nstopping...")
	eng.Stop()
	return nil
}

// printStats prints one stats sample. Columns: health, weight, throughput
// over the last interval, active TCP/UDP flows, flows assigned since start,
// and destinations stuck to this line.
func printStats(s engine.Stats) {
	for i, l := range s.Lines {
		when := "        "
		if i == 0 {
			when = s.Time.Format("15:04:05")
		}
		fmt.Printf("%s  %-12s %-4s w%-3d down %5.1f  up %5.1f Mbit/s  tcp %4d  udp %3d  flows %6d  sticky dsts %4d\n",
			when, l.Name, l.State, l.Weight, l.DownMbps, l.UpMbps, l.TCP, l.UDP, l.Flows, l.Sticky)
	}
	fmt.Printf("          %-12s %-4s %-4s down %5.1f  up %5.1f Mbit/s\n", "total", "", "", s.DownMbps, s.UpMbps)
	if s.Rules != "" {
		fmt.Println("         ", s.Rules)
	}
	if s.Retried > 0 {
		fmt.Println("          connections saved by trying the other line:", s.Retried)
	}
	fmt.Println()
}

func cmdCleanup() error {
	if !engine.IsAdmin() {
		return errors.New("duolink cleanup must be run as administrator")
	}
	// The adapter of a running duolink isn't a leftover: removing its routes
	// would silently stop the balancing while the tray still says Running.
	if engine.Running() {
		return errors.New("duolink is running (in the tray or a `duolink run`): use Pause/Quit or Ctrl+C there instead; cleanup is for leftovers after a crash")
	}
	all, err := netif.List()
	if err != nil {
		return err
	}
	if engine.RemoveLeftovers(all) == 0 {
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
