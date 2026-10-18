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

**Existing flows never move.** A TCP connection's source IP is fixed;
moving it to another line would break it. Only new flows react to health
and weights.

**Stickiness per destination IP, sliding timer, on by default.** Keeps sites
and game servers seeing one public IP, which avoids session and anti-cheat
trouble. Big downloads still spread because CDNs use several IPs (verified
with Steam). A destination moved by failover stays on its new line, since
the point is IP consistency.

**Config: defaults < duolink.json < flags, unknown fields rejected.** A file
only needs what differs, typos fail loudly instead of being ignored, and
flags allow one-off overrides without editing the file.

**IPv4 only for now.** Both target lines have no IPv6. Adding IPv6 routes
means handling two address families everywhere; until then duolink warns when
IPv6 would bypass it.

**Explicit refusals.** No admin, no wintun.dll, or fewer than 2 usable lines:
fail at once with a clear message, before touching anything.
