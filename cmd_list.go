package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/netip"
	"os"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/eagtf/duolink/internal/netif"
	"github.com/eagtf/duolink/internal/route"
)

func cmdList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	checkIP := fs.Bool("ip", false, "fetch each uplink's public IP via api.ipify.org")
	fs.Parse(args)

	all, err := netif.List()
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "USE\tNAME\tIDX\tTYPE\tIPv4\tGATEWAY\tMETRIC\tLINK\tNOTE")
	for _, a := range all {
		use, note := "yes", a.Desc
		if !a.Usable() {
			use, note = "-", "skipped: "+a.Skip
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\t%d\t%s\t%s\n",
			use, a.Name, a.Index, a.TypeName(), orDash(a.IPv4), orDash(a.Gateway),
			a.Metric, speed(a.SpeedRx), note)
	}
	tw.Flush()
	up := netif.Uplinks(all)
	fmt.Printf("\n%d usable uplink(s).", len(up))
	if len(up) < 2 {
		fmt.Print(" duolink needs at least 2 to do anything useful.")
	}
	fmt.Println()

	for _, w := range netif.OverlapWarnings(up) {
		fmt.Println("warning:", w)
	}
	if len(netif.WithGlobalIPv6(all)) > 0 {
		printIPv6Warning(all)
	}
	if *checkIP {
		fmt.Println()
		checkPublicIPs(up)
	}
	return nil
}

// checkPublicIPs fetches each uplink's public IP in parallel. Different IPs
// prove that the bound dialer really leaves through different lines.
func checkPublicIPs(up []*netif.Adapter) {
	type result struct {
		ip  string
		err error
		dur time.Duration
	}
	res := make([]result, len(up))
	var wg sync.WaitGroup
	for i, a := range up {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			t := time.Now()
			ip, err := a.PublicIP(ctx)
			res[i] = result{ip, err, time.Since(t).Round(time.Millisecond)}
		}()
	}
	wg.Wait()

	seen := map[string]string{}
	for i, a := range up {
		r := res[i]
		if r.err != nil {
			fmt.Printf("%-12s via %-15s  FAILED: %v\n", a.Name, a.IPv4.Addr(), r.err)
			continue
		}
		fmt.Printf("%-12s via %-15s  public IP %-15s  (%v)\n", a.Name, a.IPv4.Addr(), r.ip, r.dur)
		if other, dup := seen[r.ip]; dup {
			fmt.Printf("warning: %s and %s exit with the same public IP: they share a line, or binding failed\n", other, a.Name)
		}
		seen[r.ip] = a.Name
	}
}

// cmdDebug runs GetBestInterfaceEx for each IP against the current routing
// table, with no changes, to check the lookup itself.
func cmdDebug(args []string) error {
	if len(args) < 2 || args[0] != "route" {
		return errors.New("usage: duolink debug route <ip>...")
	}
	all, err := netif.List()
	if err != nil {
		return err
	}
	for _, s := range args[1:] {
		ip, err := netip.ParseAddr(s)
		if err != nil || !ip.Is4() {
			return fmt.Errorf("%q is not an IPv4 address", s)
		}
		idx, err := route.BestInterface(ip)
		if err != nil {
			fmt.Printf("%-15s  GetBestInterfaceEx failed: %v\n", ip, err)
			continue
		}
		name := "?"
		for _, a := range all {
			if a.Index == idx {
				name = a.Name
			}
		}
		fmt.Printf("%-15s  -> interface %d (%s)\n", ip, idx, name)
	}
	return nil
}

func orDash(v interface {
	IsValid() bool
	String() string
}) string {
	if !v.IsValid() {
		return "-"
	}
	return v.String()
}

func speed(bps uint64) string {
	switch {
	case bps == 0 || bps == ^uint64(0): // unknown
		return "-"
	case bps >= 1e9:
		return fmt.Sprintf("%.1fG", float64(bps)/1e9)
	default:
		return fmt.Sprintf("%dM", bps/1e6)
	}
}
