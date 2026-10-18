# Changelog

Not released yet. Entries are by milestone; see
[docs/milestones.md](docs/milestones.md) for test results.

## Milestone 14: automatic weights

- Each line's share of new connections now follows its measured speed by
  default (`auto_weights`), so a faster line gets more. Weights set by hand
  still work and turn this off. Settings has an "Automatic" checkbox.
- The status window shows the automatic weights (`Auto`) and each
  adapter's whole download as Windows counts it (`PC down`, all apps).

## Milestone 13: smarter balancing

- New connections go to the least busy line (fewest open connections for
  its weight) instead of taking turns, so a line whose downloads finished
  gets the next ones at once.
- If a line gives no answer when a connection starts, the same connection
  is tried through the other line, so the app doesn't see an error. A line
  whose network is down or unreachable gets no new connections for 15 s
  and is checked at once.
- The duolink adapter runs at MTU 9000: fewer, bigger packets inside the
  PC, less CPU on fast downloads.
- A connection that one side has finished and the other side leaves idle
  for 2 minutes is closed.

## Milestone 12: recovery, hot-plug, friendlier per-app rules

- When a line comes back after an outage, the destinations that moved to
  the other line during it are balanced over both lines again.
- Lines follow the adapters while duolink runs: a new adapter (e.g. a new
  Wi-Fi stick) is picked up with its configured weight, an adapter that
  returns with a new address or interface index is used at the new address,
  and a removed adapter is dropped. New and changed lines are probed before
  they get traffic. No more "restart duolink to use it".
- When a line comes back, connections that "balance every connection" apps
  (Steam) opened during the outage are closed so they reconnect over all
  lines; their downloads would otherwise stay on the surviving line, since
  they keep reusing those connections. Setting
  `processes.reconnect_after_outage` (on by default), also in Settings.
- Pins and "never balance" follow a line whose adapter was replaced.
- Unplugging a line's adapter counts as an outage until a new line takes its
  place, so download apps are rebalanced when the adapter is plugged back in
  too. A probe that finishes after its line was replaced is ignored.
- Settings: per-app rules as a list with Browse… (pick the program's .exe),
  a rule dropdown (pin to a connected line by name) and Add / update /
  Remove.

## Milestone 11: settings in the window

- Settings window (button in the status window, "Settings…" in the tray
  menu): every option in duolink.json with explanations, including the
  per-app rules as simple lists.
- Save and apply checks everything first (also against the lines that are
  connected) and shows problems in the window's own words; nothing is
  written until it's valid. duolink.json is written atomically and duolink
  restarts with the new settings if it was running.
- Reset to defaults fills in the defaults without saving.

## Health checks: no false DOWN on a busy line

- A regular health probe must fail `health.failures_before_down` times in a
  row (default 2) before a line goes DOWN; after a miss it re-checks after
  `down_interval_seconds`, and logs whether the line recovered. Disconnected
  adapters and failed real connections still act at once.
- The probe targets are tried in parallel; the first answer counts.
- Default `health.timeout_seconds` raised from 3 to 5.

## Milestone 10: status window

- A small window with the live state: a table per line (state, weight,
  down/up speed, TCP/UDP flows, flows assigned, sticky destinations),
  totals, per-app rule counts and recent events (DOWN, back UP, pause,
  failures), plus Pause/Start, Open log and Quit buttons.
- Open it with a left click on the tray icon or "Open duolink" in the menu.
  X hides it to the tray; only Quit exits.
- Starting `duolink tray` again shows the running tray's window instead of
  the "already running" dialog.
- Native Win32 window, no new dependencies; current Windows look and sharp
  text on scaled displays.

## Repository: CI and protected main

- GitHub Actions CI on Windows for every PR and push to `main`: gofmt,
  `go vet`, `go test`, `go build` (job `test`), plus `govulncheck`. Actions
  are pinned to commit SHAs; the workflow token is read-only.
- `main` only changes through pull requests that pass `test` and are up to
  date with `main`; no force-pushes or deletion; applies to everyone.
- Dependabot: vulnerability alerts, security-fix PRs, and weekly update PRs
  for Go modules and the pinned actions.

## Milestone 9: system tray app

- `duolink tray`: runs duolink in the system tray with no console window.
  From a terminal it refreshes `duolinkw.exe` (a copy of `duolink.exe` with
  Windows' "windowed" flag) next to the exe, starts it, and returns.
- Tray icon colour shows the state (green running, amber a line is DOWN,
  grey paused); tooltip and menu show per-line state and speeds every 2 s.
- Menu: Pause/Start (Pause removes the routes), Start with Windows, Open
  log, Quit (cleans up first). Start re-reads `duolink.json`. If starting
  fails before changing anything (e.g. Wi-Fi not up yet at logon), it retries
  every 10 s; other failures wait for Start, so routes are never added and
  removed in a loop.
- Single instance; started without admin rights it asks for elevation (UAC).
  Double-clicking `duolinkw.exe` starts the tray.
- `duolink autostart on|off|status`: a logon task in Task Scheduler with
  highest privileges, no battery limits and no time limit.
- Tray output goes to `duolink.log` next to the exe (restarts beyond 1 MiB).
- `duolink run` and the tray now share one engine (`internal/engine`); a
  PC-wide mutex stops two engines (tray + run) from running at once.
  `duolink cleanup` refuses while one runs (it would remove live routes).
- Console tools (netsh, schtasks, route) start without a window, so the
  tray never flashes consoles.
- `.gitattributes`: files are checked out with LF on every machine (gofmt
  needs it; `core.autocrlf` used to turn them into CRLF).

## Project layout

- Split the single package into packages by job under
  `internal/{config,netif,route,procs,balance,forward}`. The CLI stays at the
  root (`main.go`, `cmd_*.go`). Tests moved with their code. No behaviour
  changes.
- Build command is now just `go build` (it names the binary duolink.exe).

## Milestone 8: per-app rules

- Each new flow is mapped to the program that opened it: the app's socket
  address (198.18.0.1:port) is looked up with `GetExtendedTcpTable` /
  `GetExtendedUdpTable`, and the PID's exe name read with
  `QueryFullProcessImageName`.
- New `processes` section in duolink.json:
  - `sticky_exempt` (default `["steam.exe"]`): balanced per flow, ignoring stickiness.
  - `pin`: exe → line; balanced over the other lines while that line is DOWN.
  - `exclude`: never balanced; always the line with the lowest interface
    metric (what Windows would pick), or another one while it's DOWN.
  - Each exe may appear in only one list; names must be exe file names.
- `duolink run` prints the active rules; live stats show flows per rule and
  how many flows had no identifiable owner.
- No lookups at all when no rules are configured.

## Milestone 7: instant failover

- Adapter watcher (`NotifyIpInterfaceChange` + `NotifyUnicastIpAddressChange`):
  when an uplink's adapter disconnects, disappears, or loses its IPv4 address
  or gateway, the line goes DOWN at once with the reason in the log. When the
  adapter is back, a probe runs immediately; only a successful probe brings
  the line back UP.
- An address or interface index that changes while running is reported
  ("restart duolink to use it") and the line stays DOWN.
- Failed dials on real traffic: 2 consecutive failures to different
  destination IPs trigger an immediate health probe on that line. The line
  goes DOWN only if the probe fails, so dead websites can't take down a
  healthy line. A refused or reset connection counts as proof the line works.
- Log lines say why a line went DOWN, and note when failed dials were
  followed by a good probe ("stays UP").
- Unchanged: routes, cleanup, existing flows on a DOWN line, sticky behaviour.

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
