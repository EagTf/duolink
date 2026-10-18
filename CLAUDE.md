# duolink

`duolink` is a Windows CLI (Go) that uses several internet uplinks at once by
balancing flows across network adapters: per-flow load balancing, not bonding.
A Wintun TUN adapter captures IPv4 traffic via two half-default routes, a gVisor
netstack (tun2socks core, used as a library) terminates each TCP/UDP flow, and
a balancer re-dials it through one uplink with a socket bound to that adapter.

Design: [docs/architecture.md](docs/architecture.md). Status and plan:
[docs/milestones.md](docs/milestones.md). Why things are the way they are:
[docs/decisions.md](docs/decisions.md).

## Commands

```
go build -o duolink.exe .     # binary must be named duolink.exe; wintun.dll sits next to it
go vet ./...
go test ./...
gofmt -l .                  # must print nothing
```

Safe for Claude to run (read-only): `duolink list`, `duolink config`,
`duolink debug route <ip>`, and the unprivileged error paths of the other
commands. `duolink list --ip` makes outbound requests; only run it when asked.

## Rules

- **Never run `duolink run`, `duolink cleanup`, `netsh`, `route add/delete`, or
  anything else that changes routing or adapters.** The user tests on their own
  PC. Build, vet and test, then hand over a test plan.
- **Work in milestones and stop after each one** so the user can test. Don't
  start the next milestone until told to.
- **Routes are always added with `store=active`** (non-persistent), so a reboot
  always restores the system. Never add persistent routes or change real
  default routes.
- **Keep cleanup working.** On every exit path (Ctrl+C, console close, `--for`,
  failed self-check) routes are removed first, then the netstack and adapter.
  `duolink cleanup` must remove leftovers after a crash, and only ever touches
  routes on duolink's own adapter.
- **No telemetry or outbound calls** except the health probes and `duolink list --ip`.
- **Never put real IPs or personal info in code, tests or docs.** Public IPs:
  use 203.0.113.0/24, 198.51.100.0/24 (RFC 5737). LAN examples:
  192.168.1.x / 192.168.2.x. No usernames, absolute paths, MACs, hostnames.
- **Update the docs at the end of every milestone:** CHANGELOG.md,
  docs/milestones.md (delivered + test results), docs/action_items.md, and
  docs/architecture.md, docs/testing.md, docs/decisions.md where affected.
- Keep it small and readable, with minimal dependencies. Explain every Windows
  networking call in a comment where it's used.

## Windows gotchas (already handled; don't regress)

- **`IP_UNICAST_IF` takes the interface index in network byte order
  (big-endian) for IPv4.** `IPV6_UNICAST_IF` takes host order. Set it in
  `Dialer.Control` (after `socket()`, before `bind`/`connect`). Binding the
  local IP alone does not pick the egress interface: without
  `IP_UNICAST_IF` outbound sockets follow the TUN's /1 routes and loop.
- **Disable DAD on the TUN** (`netsh interface ipv4 set interface ...
  dadtransmits=0`) and wait until the TUN is up and its address is
  "preferred" before adding routes. Otherwise lookups through the TUN fail
  with ERROR_NETWORK_UNREACHABLE while the address is still tentative.
- gVisor calls the UDP forwarder handler inline on the packet path, and
  tun2socks frees the TCP handshake slot only when `HandleTCP` returns, so
  both handlers must return immediately (each flow runs in a goroutine).
- A panic in a goroutine kills the process without running deferred cleanup.
  Flow goroutines recover.
- netsh's interface arguments use the IPv4 interface index, not the name, to
  avoid quoting and localization issues.
