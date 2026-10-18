// Command duolink spreads a PC's connections across several internet uplinks
// (per-flow load balancing). See README.md for usage and docs/ for the design.
package main

import (
	"fmt"
	"os"

	"github.com/eagtf/duolink/internal/guiexe"
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
  duolink tray [--config file]            run in the system tray, no console (admin);
                                          returns at once, logs to duolink.log
  duolink autostart on|off|status         start the tray at logon, elevated (admin for on/off)
  duolink cleanup                         remove routes left behind by a crash (admin)
  duolink debug route <ip>...             show which interface Windows routes <ip> to
                                          (read-only: changes nothing)
`

func main() {
	if len(os.Args) < 2 {
		// The windowed copy (duolinkw.exe) only exists for the tray, and has
		// nowhere to print usage; double-clicking it starts the tray.
		if exe, err := os.Executable(); err == nil {
			if gui, _ := guiexe.IsGUI(exe); gui {
				os.Args = append(os.Args, "tray")
			}
		}
	}
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
	case "tray":
		err = cmdTray(os.Args[2:])
	case "autostart":
		err = cmdAutostart(os.Args[2:])
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
