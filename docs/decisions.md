# Design decisions

**Per-flow balancing, not bonding.** Bonding one connection across lines
needs a server on the far end to reassemble it. Per-flow balancing needs
nothing outside the PC and is enough for apps that open several connections
(Steam, browsers). Cost: one connection never exceeds one line.

**Wintun TUN + userspace netstack, not a SOCKS/HTTP proxy.** A proxy only
helps apps configured to use it. A TUN captures all IPv4 traffic system-wide
without per-app setup. Wintun is the maintained, signed TUN driver used by
WireGuard.

**tun2socks core as a library with our own dialer.** It already wires
gVisor's TCP/UDP forwarders to a TUN device correctly (promiscuous mode,
spoofing, handshake handling). We use only `core` and `core/device/tun`, with
a custom `TransportHandler` that dials directly, so there is no SOCKS hop.

**Two half-default routes (0.0.0.0/1 + 128.0.0.0/1), not replacing the
default route.** Longest-prefix match makes them win over any 0.0.0.0/0
without touching the real default routes, which bound sockets still need.
Anything more specific (router LANs, multicast, broadcast) is unaffected.

**Non-persistent everything (`store=active`).** Routes and the TUN address
live only in memory, and the routes vanish with the adapter. A reboot
always fixes things, whatever state duolink crashed in.

**netsh instead of iphlpapi route calls.** The commands are printed as duolink
runs them, so users can see, repeat and undo every change by hand. Interface
arguments use the index, not the name, to avoid quoting and localized names.

**`bind()` + `IP_UNICAST_IF` together.** `bind()` alone fixes the source IP
but not the egress interface, so traffic would follow the TUN routes and loop.
`IP_UNICAST_IF` restricts the route lookup to the line's interface.
The local-address check after dialing and the startup loop test catch it if
this ever stops working.

**TUN address in 198.18.0.0/15.** Reserved for benchmarking (RFC 2544), so it
won't collide with home or office LANs. Windows needs some address on the
adapter to pick a source IP for traffic routed into it.

**DAD off on the TUN, wait for "preferred".** A tentative address can't be
used as a source, which made route lookups through the TUN fail right after
setup. A TUN has no neighbours, so duplicate detection is pointless.

**Smooth weighted round-robin.** Deterministic, interleaves lines evenly for
any integer weights, and needs no randomness or per-flow measurements.

**Health = TCP connect to two operators' anycast IPs.** Cheap (no data
sent), tests the real bound path, and one provider's outage doesn't mark a
healthy line down. All-down falls back to all lines: more likely the probe
targets are blocked than every line is dead.

**A regular probe needs two misses in a row.** During a full-speed download
the router's buffers fill and latency can spike past the probe timeout, so a
single missed probe marked a working line DOWN (seen in the milestone 9
test) and halved the throughput for no reason. Two misses, the second only
`down_interval_seconds` later, keep detection fast but filter those spikes.
Stronger evidence (a disconnected adapter, or failed real connections
confirmed by a probe) still acts on the first sign. Targets are probed in
parallel so the one-timeout worst case stays short.

**Adapter events mark DOWN directly; dial failures only trigger a probe.**
A disconnected adapter can't carry anything, so waiting would only lose
flows. Failed dials are weaker evidence: the destinations may be down, not
the line. So two failures to different IPs only ask for a probe, and the
probe to known-good targets decides. A refused or reset connection is a reply
from the destination, so it counts as proof the line works.

**Only a successful probe brings a line back UP.** An adapter that reconnects
may still be waiting for DHCP or for its DSL line to sync, so "connected"
alone isn't enough.

**On adapter events, re-read the whole adapter list.** The notification rows
carry partial data and come in bursts. Coalescing the burst and comparing
each line with its startup state (`netif.Issue`) is simpler and can't miss
a change.

**A changed address or interface index is reported, not followed.** Sockets
are bound to the address seen at startup. Following changes live is part of
the hot-plugging idea; until then the line stays DOWN and the log says to
restart.

**Existing flows never move.** A TCP connection's source IP is fixed;
moving it to another line would break it. Only new flows react to health
and weights.

**Stickiness per destination IP, sliding timer, on by default.** Keeps sites
and game servers seeing one public IP, which avoids session and anti-cheat
trouble. Big downloads still spread because CDNs use several IPs (verified
with Steam). A destination moved by failover stays on its new line, since
the point is IP consistency. Under review: with a sliding timer this keeps a
long download on one line after the failed line recovers (see
docs/action_items.md).

**Per-app rules by exe file name, looked up per flow.** Mapping the app's
socket (198.18.0.1:port) through the owner tables is exact and needs no
driver or hooks. Exe names (not paths) keep the config portable and short.
One list per exe avoids having to define precedence between rules.

**`exclude` means "the line Windows would use", not "bypass duolink".**
Routes are per destination, not per process, so a process can't skip the
TUN. The nearest useful meaning is "never spread": always the lowest-metric
weighted line, with failover.

**`steam.exe` is sticky-exempt by default.** Steam downloads over many
connections to a few CDN IPs; stickiness would put them all on one line if
Steam ever used a single server. Being explicit costs nothing for other apps.

**Pinned or excluded flows fall back to WRR, not to the sticky table, when
their line is DOWN.** They have no sticky history to honour, and writing one
would keep them away from their line after it recovers.

**Config: defaults < duolink.json < flags, unknown fields rejected.** A file
only needs what differs, typos fail loudly instead of being ignored, and
flags allow one-off overrides without editing the file.

**The tray runs from a windowed copy of the exe (duolinkw.exe).** Windows
fixes per exe whether it gets a console. The CLI needs one; a tray must not
have one, or a terminal window appears at every logon. Rather than two build
commands, `duolink tray` writes a copy with the PE Subsystem field changed
from console (3) to GUI (2), the same idea as python.exe / pythonw.exe. One
`go build` still makes everything. A running copy can't be overwritten but
can be renamed, so updates move it to `.old` first.

**One engine for CLI and tray, guarded by a PC-wide mutex.** Both create the
`duolink` adapter and the same routes; two at once would conflict. A
`Global\` named mutex is released by Windows even if the process crashes, so
it can never get stuck.

**Autostart is a Task Scheduler logon task defined in XML.** Only a task can
start an admin tool at logon without a UAC prompt. `schtasks` flags alone
leave the defaults "don't start on battery", "stop on battery" and "kill
after 72 hours", which would silently end the tray; the XML turns them off
and sets normal priority.

**Tray Start means "keep trying", but only when that is harmless.** At logon
Wi-Fi often connects after the tray starts, so a start that failed *before
changing anything* (fewer than 2 lines, another duolink running, a config
error) retries every 10 s, logging each distinct error once. A start that
failed *after* the adapter and routes were set up (a failed self-check, say)
is not retried automatically: that would add and remove routes every 10 s and
make the connection blip each time. The engine marks the first kind
(`engine.Retryable`); the tray shows the second as "Start tries again".

**Console tools always start with CREATE_NO_WINDOW.** From the windowed
process every netsh / schtasks call would otherwise flash a console. Their
output still comes back through pipes, and the CLI looks the same.

**`main` is protected; CI runs on Windows.** Only pull requests that pass
the `test` job (gofmt, vet, test, build) and are up to date with `main` can
change it, and the rule has no bypass, not even for the owner: a solo
project gets the most from "nothing untested reaches main". No required
approvals, since GitHub doesn't let you approve your own PR. CI runs on
Windows because the code calls Windows APIs; the runner is elevated, so the
one test that would change routing (`engine.Start`) skips itself there.
Actions are pinned to commit SHAs so a moved tag can't change what runs.

**`main` at the root + `internal/` packages, tests beside the code.** One flat
package was fine up to milestone 8, but the tray app (milestone 9) adds a
second user interface over the same engine. duolink is one binary (the UI is
part of the same build), so `main` stays at the root and `go build` alone
makes duolink.exe; a `cmd/` folder only pays off for several binaries. Packages by job (`netif`, `route`, `procs`,
`balance`, `forward`, `config`) keep each part small and testable, and
`internal/` keeps them private to this module. Tests stay in the package they
test, as Go expects: most of them check unexported internals (WRR order,
failover transitions), which a separate tests folder couldn't reach.

**IPv4 only for now.** Both target lines have no IPv6. Adding IPv6 routes
means handling two address families everywhere; until then duolink warns when
IPv6 would bypass it.

**Explicit refusals.** No admin, no wintun.dll, or fewer than 2 usable lines:
fail at once with a clear message, before touching anything.
