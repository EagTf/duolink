// duolink spreads a PC's connections across several internet uplinks
// (per-flow load balancing). See README for the design.
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
)

const usage = `usage:
  duolink list [--ip]                     show adapters and which ones duolink will use
                                          (--ip: fetch each uplink's public IP from api.ipify.org)
  duolink run [--weights eth=1,wifi=1]    start balancing flows across uplinks (admin)
              [--sticky-minutes N]        --weights: flow share per uplink (default 1 each;
              [--for 2m] [--keep-on-fail]   unnamed uplinks get 0, so eth=1,wifi=0 = Ethernet only)
              [--config file]             --sticky-minutes: keep a destination on one line this
                                            long after its last new flow; 0 = off
                                          --for: stop and clean up automatically after this long
                                          --keep-on-fail: on a failed self-check, dump the
                                            routing table before cleaning up
  duolink config [--init] [run flags]     print the effective config (defaults < duolink.json < flags)
                                          --init: write the defaults to duolink.json
  duolink cleanup                         remove routes left behind by a crash (admin)
  duolink debug route <ip>...             show which interface Windows routes <ip> to
                                          (read-only: changes nothing)
`

// cmdDebug runs GetBestInterfaceEx for each IP against the current routing
// table, with no changes, to check the lookup itself.
func cmdDebug(args []string) error {
	if len(args) < 2 || args[0] != "route" {
		return errors.New("usage: duolink debug route <ip>...")
	}
	all, err := listAdapters()
	if err != nil {
		return err
	}
	for _, s := range args[1:] {
		ip, err := netip.ParseAddr(s)
		if err != nil || !ip.Is4() {
			return fmt.Errorf("%q is not an IPv4 address", s)
		}
		idx, err := bestInterface(ip)
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

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "list":
		err = cmdList(os.Args[2:])
	case "run":
		err = cmdRun(os.Args[2:])
	case "cleanup":
		err = cmdCleanup()
	case "config":
		err = cmdConfig(os.Args[2:])
	case "debug":
		err = cmdDebug(os.Args[2:])
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func cmdList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	checkIP := fs.Bool("ip", false, "fetch each uplink's public IP via api.ipify.org")
	fs.Parse(args)

	all, err := listAdapters()
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "USE\tNAME\tIDX\tTYPE\tIPv4\tGATEWAY\tMETRIC\tLINK\tNOTE")
	usable := 0
	for _, a := range all {
		use, note := "yes", a.Desc
		if !a.Usable() {
			use, note = "-", "skipped: "+a.Skip
		} else {
			usable++
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\t%d\t%s\t%s\n",
			use, a.Name, a.Index, a.TypeName(), orDash(a.IPv4), orDash(a.Gateway),
			a.Metric, speed(a.SpeedRx), note)
	}
	tw.Flush()
	fmt.Printf("\n%d usable uplink(s).", usable)
	if usable < 2 {
		fmt.Print(" duolink needs at least 2 to do anything useful.")
	}
	fmt.Println()

	var up []*Adapter
	for _, a := range all {
		if a.Usable() {
			up = append(up, a)
		}
	}
	for _, w := range overlapWarnings(up) {
		fmt.Println("warning:", w)
	}
	if len(globalIPv6(all)) > 0 {
		printIPv6Warning(all)
	}
	if *checkIP {
		fmt.Println()
		checkPublicIPs(up)
	}
	return nil
}

// overlapWarnings flags uplinks that are probably on the same LAN or router.
// Either case breaks the "one line per adapter" assumption: the same gateway
// usually means the same internet line, and overlapping subnets make the
// on-link routes ambiguous, so Windows can ARP for the wrong router.
func overlapWarnings(up []*Adapter) []string {
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

// checkPublicIPs fetches each uplink's public IP in parallel. Different IPs
// prove that the bound dialer really leaves through different lines.
func checkPublicIPs(up []*Adapter) {
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
			ip, err := a.publicIP(ctx)
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
