# Changelog

Not released yet. Entries are by milestone; see
[docs/milestones.md](docs/milestones.md) for test results.

## Milestone 6: config file and stickiness

- `duolink.json` next to `duolink.exe`: weights, stickiness, health-check
  settings, UDP idle timeout. Defaults < file < flags. Unknown fields and
  invalid values are errors.
- `duolink config` prints the effective config; `duolink config --init` writes the
  defaults (never overwrites).
- Per-destination stickiness, on by default (10 minutes): new flows to a
  destination IP seen recently use the same line. The timer restarts on every
  new flow; if the line goes down, the destination moves to another line.
- New flags: `--config`, `--sticky-minutes` (0 = off).
- Live stats show sticky destinations per line.

## Milestones 4 and 5: balancing, health checks, live stats

- Smooth weighted round-robin per new flow; flows stay on their line.
- `--weights eth=1,wifi=1` (unique-prefix name matching; unnamed lines get 0).
  Replaces milestone 3's `--via`.
- Health checks: TCP connect to 1.1.1.1:443 / 8.8.8.8:443 through each line,
  every 15 s (4 s while down). Down lines get no new flows; existing flows
  stay. If every line looks down, flows are spread over all of them.
- The startup loop test doubles as the first health check.
- Live stats every 2 s: state, weight, throughput, active TCP/UDP flows,
  total flows per line.
- Milestone 5 was a test only: parallel downloads reached the sum of both lines.

## Milestone 3: TUN, routes and forwarding through one line

- `duolink run`: Wintun adapter `duolink` (198.18.0.1/30), half-default routes
  0.0.0.0/1 and 128.0.0.0/1 with `store=active`, gVisor netstack (tun2socks
  core) forwarding all TCP/UDP through one uplink.
- Startup self-check: internet routed into the TUN, routers' LANs stay
  direct, bound sockets bypass the TUN (routing-loop test).
- Runtime loop guard: flows from our own uplink IPs are dropped and reported.
- Cleanup on Ctrl+C, console close and `--for <duration>`; `duolink cleanup` for
  leftovers.
- Refuses to start without admin rights, wintun.dll, or 2 usable uplinks.
- IPv6 warning when an adapter has a public IPv6 address.
- Fix: disable DAD on the TUN and wait for it to be up with a usable address
  before adding routes (the first run failed the self-check with "network
  location cannot be reached").
- `--keep-on-fail` dumps `route print`, `Find-NetRoute` and the TUN's state
  when the self-check fails. `duolink debug route <ip>` shows the routing
  decision without changing anything.

## Milestone 2: adapter-bound dialer

- Sockets pinned to an adapter with `bind()` to its IPv4 address plus
  `IP_UNICAST_IF`.
- `duolink list --ip` shows each line's public IP via api.ipify.org.
- `duolink list` warns when two uplinks share a gateway or have overlapping subnets.

## Milestone 1: adapter list

- `duolink list`: adapters, IPv4, gateway, metric, link speed, and why any are
  skipped (GetAdaptersAddresses).
