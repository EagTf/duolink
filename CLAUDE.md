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
go build                   # makes duolink.exe in the root; wintun.dll sits next to it
go vet ./...
go test ./...
gofmt -l .                 # must print nothing
```

Safe for Claude to run (read-only): `duolink list`, `duolink config`,
`duolink debug route <ip>`, and the unprivileged error paths of the other
commands. `duolink list --ip` makes outbound requests; only run it when asked.

## Layout

One binary. The `main` package (the CLI, and later the tray app and window)
is at the root; everything else is in `internal/` (one package per job; see
the package table in docs/architecture.md). Where new code goes:

- A new command or flag: a `cmd_<name>.go` file at the root. Commands stay
  thin: they parse flags, call internal packages and print.
- UI code (tray, window): an `internal/` package, started by a command at
  the root (e.g. `duolink tray`). It is part of the same build.
- A new setting: `internal/config` (type, default, validation), then the
  package that uses it.
- Windows API calls belong to the package that owns that job (adapters in
  `netif`, routes in `route`, sockets → processes in `procs`).
- Tests go next to the code in the same package (`foo_test.go` beside
  `foo.go`), never in a separate tests folder: most tests need unexported
  internals.
- Dependencies only point down (`main` → `forward` → `balance` → `netif`,
  `config`). No import cycles; `config` and `netif` import nothing internal.

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
- Keep it small and readable, with minimal dependencies. Explain every Windows
  networking call in a comment where it's used.
- CLAUDE.md and docs/ are the source of truth. Read CLAUDE.md,
  docs/architecture.md, docs/milestones.md and docs/decisions.md before
  starting a milestone.

## End-of-milestone routine

Follow this at the end of every milestone:

1. `gofmt -l .` prints nothing; `go vet`, `go test` and the build pass.
2. Update CHANGELOG.md, docs/milestones.md (status → "awaiting test"),
   docs/testing.md (steps for this feature), and docs/architecture.md /
   docs/decisions.md if anything changed. Add new to-dos to
   docs/action_items.md.
3. Run the privacy check again: no real IPs or personal info in any tracked file.
4. Give the user a short summary, the exact test steps, and a suggested commit
   message. Don't commit; the user commits after their test passes.
5. Stop and wait for the test results. When they come in, record them in
   docs/milestones.md (status → "done") before starting the next milestone.

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
