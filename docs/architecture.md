# Architecture

## Data flow

```
 app (Steam, browser, ...)
   │  connect(203.0.113.5:443)            unbound socket, Windows picks the route
   ▼
 Windows routing table
   │  0.0.0.0/1, 128.0.0.0/1  →  "duolink" adapter   (more specific than the real 0.0.0.0/0)
   ▼
 Wintun adapter "duolink" (198.18.0.1/30)
   │  raw IPv4 packets, read in user space via wintun.dll
   ▼
 gVisor netstack (tun2socks core, promiscuous + spoofing)
   │  completes the TCP handshake with the app / accepts the UDP flow,
   │  hands us one net.Conn per flow: (app src, real dst)
   ▼
 forwarder (internal/forward)
   │  filters loops and non-unicast, asks the balancer for a line
   ▼
 balancer (internal/balance)
   │  sticky destination?  → same line as before
   │  otherwise            → smooth weighted round-robin over healthy lines
   ▼
 bound dialer (dialer.go)
   │  socket with bind(line's IPv4) + IP_UNICAST_IF(line's ifIndex)
   │  → ignores the TUN's /1 routes, uses that line's own default route
   ▼
 router A or router B → internet
```

The forwarder then copies bytes both ways. TCP passes half-closes on with
`CloseWrite`. UDP ends after `udp_idle_seconds` without packets in either
direction.

Replies from the internet come back to the bound socket (its source IP is the
line's address), go through the forwarder into the netstack, and the netstack
writes packets with the original destination as source back into the TUN, so
the app sees a normal connection.

## Routes

| Route | Where | Purpose |
|---|---|---|
| `0.0.0.0/0` via each router | existing, untouched | used by bound sockets (`IP_UNICAST_IF` restricts lookup to the line's interface) |
| `0.0.0.0/1`, `128.0.0.0/1` on-link on `duolink`, metric 1 | added by `duolink run`, `store=active` | capture all IPv4 internet traffic |
| `192.168.x.0/24` on-link per adapter | existing | more specific than /1, so routers and the LANs stay direct |
| `224.0.0.0/4`, `255.255.255.255/32`, loopback | existing | more specific than /1, unaffected |

Startup self-check (`selfCheck` in cmd_run.go) verifies with `GetBestInterfaceEx`
that 1.1.1.1 resolves to the TUN and each gateway to its own adapter, then
dials out through each line and checks no flow from our own uplink IPs
appeared in the netstack (routing loop).

Setup order: create adapter → start netstack → `dadtransmits=0` + set address →
wait until up and address "preferred" → add routes → self-check → run.
Teardown order (deferred): remove routes → close netstack → close adapter.

## Health checks

One goroutine per weighted line (`healthLoop` in internal/balance). Each probe TCP-connects to all
`health.targets` in parallel through the bound dialer; the first success means
up, and a dead line fails after one `timeout_seconds`. Interval
`interval_seconds` while up, `down_interval_seconds` while down or after a miss.
A regular probe needs `failures_before_down` misses in a row (default 2) to
mark a line DOWN; probes kicked by failed dials and adapter events act at
once (see below).
State changes are logged. A down line gets no new flows; its existing flows
are left alone. If every line is down, all weighted lines are used anyway.

The startup loop test runs one probe per line in parallel and marks lines that
fail it as down from the start.

### Instant failover

Besides the interval, two things can act on a line between probes:

```
 Windows adapter events ──► netif.Watch
   NotifyIpInterfaceChange        coalesce burst (300 ms)
   NotifyUnicastIpAddressChange   └► Balancer.OnAdapterChange: netif.List,
                                     netif.Issue(startup state, now) per line
                                       issue appears → setDown(reason)  (no probe)
                                       issue clears  → kick probe "adapter reconnected"

 real-traffic dials ──► forward ──► Balancer.NoteDial(line, dst, err)
                                       ok / refused / reset → reset count
                                       cancelled            → ignored
                                       other failure        → add dst IP (distinct)
                                       2 distinct IPs       → kick probe "2 failed dials (...)"

 kick ──► healthLoop ──► check(why) ──► probe ok:   UP (logs "stays UP" if kicked)
                                        probe fail: DOWN ("why, then probe failed: ...")
```

Each line has a 1-slot `kick` channel, so its health loop is the only
place that probes it and probes never overlap. Only a successful probe sets a
line UP; adapter events and dials can only make it DOWN or ask for a probe.
UDP dials only report errors, because a connected UDP socket sends nothing
and its success proves nothing.

## Hot-plug and recovery after an outage

`Balancer.applyAdapters` runs after every adapter event (`OnAdapterChange`),
matching lines to adapters by LUID:

```
 same LUID, usable, new address or index → new line at the new address,
                                            same weight, DOWN, probe kicked
 LUID gone                                → line dropped (logged DOWN first)
 disconnected / no address / no gateway   → line DOWN (Issue), as before
 usable adapter with an unknown LUID      → new line, weight from the config
                                            (weightFor), DOWN, probe kicked
```

The set of lines lives under the balancer's mutex; health loops are started
per line (`startLoop`, own context, stopped when the line is replaced or
dropped). The forwarder's loop guard asks `Balancer.IsUplinkIP`, its
counters are created per line on first use, and the engine's stats come from
`Balancer.Lines()`, so the window and tray menu show the current lines. Pins
and "exclude" are resolved by name at each pick (`byName`, `primary`).

A sticky entry created while a weighted line is DOWN is flagged
`duringOutage`; when any line comes back UP (`check`), flagged entries are
forgotten (`forgetOutageEntries`) and those destinations are balanced again.

## Stickiness

`balancer.sticky` maps destination IP → (line, last new flow). On a new flow,
an entry younger than `sticky.minutes` whose line is still a candidate wins
and its timer restarts. Otherwise WRR picks a line and the entry is
(re)written. A sweeper drops expired entries every minute. Stickiness is per
destination IP, not per port or app.

## Per-app rules

```
 new flow (app src 198.18.0.1:port, dst) ──► Forwarder.ruleFor
   procs.Table.Lookup(tcp|udp, src)
     owner snapshot (GetExtendedTcpTable / GetExtendedUdpTable): addr:port → PID
       miss and snapshot older than this lookup → re-read once
       UDP also tries 0.0.0.0:port (unconnected sockets)
     PID → exe (OpenProcess + QueryFullProcessImageName), cached 10 s
   Rules.ForExe(exe) → balance.Rule {none | sticky-exempt | pin line | exclude → primary}
 ──► Balancer.PickFor(dst, rule)
       pin / exclude: rule.line if it's a candidate, else WRR
       sticky-exempt: WRR, sticky table untouched
       none:          sticky table, else WRR (as before)
```

The app's TCP handshake with the netstack is complete before the forwarder
gets the flow, so the app's socket is always in the owner table. Snapshots
are ordered with a counter, not timestamps (Windows' clock ticks too coarsely
to order events microseconds apart): a lookup re-reads only if no read
started after it began waiting, so a burst of new flows shares one read.
The forwarder has no process table when no rules are configured, so then no lookups happen.

`balance.ResolveRules` runs at startup: pin targets are matched like weights and must
have a non-zero weight; the primary line for `exclude` is the weighted line
with the lowest interface metric.

## Tray app

```
 terminal (admin):  duolink.exe tray
   └► guiexe.Update: duolinkw.exe = copy with PE Subsystem = GUI (only if changed)
   └► start duolinkw.exe tray, return at once

 logon task (highest privileges) ──► duolinkw.exe tray

 duolinkw.exe tray (no console):
   stdout/stderr/log → duolink.log
   not elevated → relaunch via ShellExecute "runas" (UAC)
   Local\duolink-tray mutex taken → show the running tray's window, exit
                                     (dialog only if that window isn't found)
   tray.Run: controller loop
     want && not running → config.Load + engine.Start
       failed before any change (engine.Retryable) → retry every 10 s, log once per error
       failed after the adapter/routes were set up → stop retrying; Start tries again
     Pause → engine.Stop (routes first) ; Start → want = true
     Quit → wait for a start in progress, engine.Stop, exit
   engine OnStats every 2 s → menu rows, tooltip, icon colour, window
   engine OnEvent (DOWN, back UP, ...) + tray start/pause/failure → window events
   left click / "Open duolink" → window.Show ; X hides it, only Quit exits
   Settings (window button / menu) → form from config.Load + connected lines
     Save and apply → form checks → config.Validate → ResolveWeights/ResolveRules
       against the real lines → config.Save (temp file + rename) → if running:
       stop + start (start re-reads the file); if paused: applies on Start
```

The engine takes the `Global\duolink-engine` mutex for as long as it runs,
so `duolink run` and the tray (or two runs) can never both own the routes.
Every start re-reads `duolink.json`, so Pause → edit → Start applies changes.
Console tools run from the windowed process would each open a console
window; `winexec.Command` starts them with `CREATE_NO_WINDOW`.

## Config

`internal/config`: `Default()` → JSON from `duolink.json` decoded on top (unknown
fields rejected) → flags that were actually given (`flag.Visit`, in
`cmd_config.go`) → `Validate()`.
Weights stay as name → number until `run` knows the adapters, then
`balance.ResolveWeights` matches names to adapters (`netif.Find`) by unique normalized prefix.

## File map

| Package | Contents |
|---|---|
| root (`package main`) | the CLI: command dispatch (`main.go`), `list` and `debug route` (`cmd_list.go`), `run` (prints engine stats) and `cleanup` (`cmd_run.go`), config flags and `duolink config` (`cmd_config.go`), `tray` and `autostart` (`cmd_tray.go`) |
| `internal/engine` | one run of duolink: preconditions (admin, wintun.dll, 2 uplinks), TUN + netstack + routes, self-check, the "one engine per PC" mutex, Stop (undo in reverse), stats sampling |
| `internal/config` | `duolink.json`: types, defaults, loading, validation, `--weights` syntax, atomic `Save` |
| `internal/netif` | adapters (`GetAdaptersAddresses`, skip reasons), the bound dialer (`IP_UNICAST_IF`, ipify lookup), the adapter watcher and `Issue`, name matching (`Find`), overlap and IPv6 checks |
| `internal/route` | netsh: TUN address, half-default routes; `GetBestInterfaceEx` lookups, TUN readiness, routing dump |
| `internal/procs` | socket → process lookup (`GetExtendedTcpTable` / `GetExtendedUdpTable`, `QueryFullProcessImageName`) |
| `internal/balance` | WRR, stickiness, health checks, failed-dial tracking, line state transitions, per-app rules, weight resolution |
| `internal/forward` | the netstack `TransportHandler`: flow filtering, loop guard, TCP/UDP copy, per-uplink counters |
| `internal/tray` | the tray app (fyne.io/systray): menu, tooltip, state icons, Pause/Start controller with retry, single-instance mutex |
| `internal/window` | the status and settings windows: plain Win32 (user32/gdi32), one message-loop thread, visual styles via an activation context, DPI awareness; `Format` (status text) and `form` (settings fields <-> config) are the testable parts |
| `internal/guiexe` | the windowed copy: duolinkw.exe = duolink.exe with the PE Subsystem field set to GUI |
| `internal/autostart` | the logon Task Scheduler task (XML, `schtasks`) |
| `internal/winexec` | runs console tools (netsh, schtasks, route) with `CREATE_NO_WINDOW` |

Dependencies only point down: `main` → `tray` → `window` → `engine` → `forward` → `balance` → `netif`, `config`; `engine` also uses `route`, `forward` uses `procs` and `route`, `route` and `autostart` use `winexec`. Tests sit next to the code they test (`*_test.go` in the same package) and need neither admin rights nor network changes.

## Dependencies

- `golang.org/x/sys/windows`: Win32 calls.
- `golang.zx2c4.com/wireguard/tun` (+ `wintun`): creates the Wintun adapter.
- `github.com/xjasonlyu/tun2socks/v2/core`: gVisor stack setup and
  TCP/UDP forwarders. Only the core packages are used, not its proxy layer.
- `gvisor.dev/gvisor`: the netstack itself (most of the binary size).
- `fyne.io/systray`: the tray icon and menu (pure Go on Windows, no cgo).
