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
 forwarder (forward.go)
   │  filters loops and non-unicast, asks the balancer for a line
   ▼
 balancer (balance.go)
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

Startup self-check (`selfCheck` in run.go) verifies with `GetBestInterfaceEx`
that 1.1.1.1 resolves to the TUN and each gateway to its own adapter, then
dials out through each line and checks no flow from our own uplink IPs
appeared in the netstack (routing loop).

Setup order: create adapter → start netstack → `dadtransmits=0` + set address →
wait until up and address "preferred" → add routes → self-check → run.
Teardown order (deferred): remove routes → close netstack → close adapter.

## Health checks

One goroutine per weighted line (`healthLoop`). Each probe TCP-connects to the
`health.targets` in turn through the bound dialer; one success means up.
Interval `interval_seconds` while up, `down_interval_seconds` while down.
State changes are logged. A down line gets no new flows; its existing flows
are left alone. If every line is down, all weighted lines are used anyway.

The startup loop test runs one probe per line in parallel and marks lines that
fail it as down from the start.

### Instant failover

Besides the interval, two things can act on a line between probes:

```
 Windows adapter events ──► watchAdapters (netwatch.go)
   NotifyIpInterfaceChange        coalesce burst (300 ms)
   NotifyUnicastIpAddressChange   └► onAdapterChange: GetAdaptersAddresses,
                                     adapterIssue(startup state, now) per line
                                       issue appears → setDown(reason)  (no probe)
                                       issue clears  → kick probe "adapter reconnected"

 real-traffic dials ──► forwarder ──► noteDial(line, dst, err)
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

## Stickiness

`balancer.sticky` maps destination IP → (line, last new flow). On a new flow,
an entry younger than `sticky.minutes` whose line is still a candidate wins
and its timer restarts. Otherwise WRR picks a line and the entry is
(re)written. A sweeper drops expired entries every minute. Stickiness is per
destination IP, not per port or app.

## Per-app rules

```
 new flow (app src 198.18.0.1:port, dst) ──► forwarder.ruleFor
   procTable.lookup(tcp|udp, src)
     owner snapshot (GetExtendedTcpTable / GetExtendedUdpTable): addr:port → PID
       miss and snapshot older than this lookup → re-read once
       UDP also tries 0.0.0.0:port (unconnected sockets)
     PID → exe (OpenProcess + QueryFullProcessImageName), cached 10 s
   procRules.forExe(exe) → flowRule {none | sticky-exempt | pin line | exclude → primary}
 ──► balancer.PickFor(dst, rule)
       pin / exclude: rule.line if it's a candidate, else WRR
       sticky-exempt: WRR, sticky table untouched
       none:          sticky table, else WRR (as before)
```

The app's TCP handshake with the netstack is complete before the forwarder
gets the flow, so the app's socket is always in the owner table. Snapshots
are ordered with a counter, not timestamps (Windows' clock ticks too coarsely
to order events microseconds apart): a lookup re-reads only if no read
started after it began waiting, so a burst of new flows shares one read.
`procs` is nil when no rules are configured, so then no lookups happen.

`resolveRules` runs at startup: pin targets are matched like weights and must
have a non-zero weight; the primary line for `exclude` is the weighted line
with the lowest interface metric.

## Config

`config.go`: `defaultConfig()` → JSON from `duolink.json` decoded on top (unknown
fields rejected) → flags that were actually given (`flag.Visit`) → `validate()`.
Weights stay as name → number until `run` knows the adapters, then
`resolveWeights` matches names to adapters by unique normalized prefix.

## File map

| File | Contents |
|---|---|
| `main.go` | CLI dispatch, `list`, `debug route`, overlap warnings, `--ip` check |
| `adapters.go` | `GetAdaptersAddresses` enumeration, skip reasons |
| `dialer.go` | adapter-bound TCP/UDP dialing (`IP_UNICAST_IF`), ipify lookup |
| `winnet.go` | admin and wintun.dll checks, netsh address/routes, `GetBestInterfaceEx`, TUN readiness, routing dump, IPv6 warning |
| `run.go` | `run` and `cleanup`, setup/teardown order, self-check, adapter name matching |
| `forward.go` | netstack TransportHandler: flow filtering, loop guard, TCP/UDP copy, stats |
| `balance.go` | WRR, stickiness, health checks, failed-dial tracking, line state transitions, weight parsing |
| `netwatch.go` | adapter change subscriptions, `adapterIssue` |
| `proc.go` | flow → process lookup (`GetExtendedTcpTable` / `GetExtendedUdpTable`, `QueryFullProcessImageName`) |
| `rules.go` | per-app rules: resolution against the uplinks, `flowRule` |
| `config.go` | `duolink.json`, flag overrides, `duolink config` |
| `*_test.go` | unit tests (no admin or network needed) |

## Dependencies

- `golang.org/x/sys/windows`: Win32 calls.
- `golang.zx2c4.com/wireguard/tun` (+ `wintun`): creates the Wintun adapter.
- `github.com/xjasonlyu/tun2socks/v2/core`: gVisor stack setup and
  TCP/UDP forwarders. Only the core packages are used, not its proxy layer.
- `gvisor.dev/gvisor`: the netstack itself (most of the binary size).
