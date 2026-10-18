# Milestones

Each milestone is built, then tested by the user on a real two-line setup
(Windows 11, Ethernet to router A, Wi-Fi to router B, two 25 Mbit/s VDSL
lines) before the next one starts. IPs below are placeholders.

## Done

### 1. `duolink list`
Delivered: adapter enumeration via `GetAdaptersAddresses`, with gateway,
metric, link speed and skip reasons.
Tested: both uplinks listed correctly (Ethernet via 192.168.2.1, Wi-Fi via
192.168.1.1, separate subnets).

### 2. Adapter-bound dialer
Delivered: `bind()` + `IP_UNICAST_IF` dialer; `duolink list --ip`; warnings for
shared gateways and overlapping subnets.
Tested: two different public IPs, one per line (203.0.113.10 via Ethernet,
203.0.113.20 via Wi-Fi). Wi-Fi has the higher metric, so its result proves
the binding overrides the routing table.

### 3. Wintun + routes + netstack through one line
Delivered: `duolink run` with TUN, half-default routes, netstack forwarding via
one line, startup self-check, loop guard, cleanup on every exit, `--for`,
`duolink cleanup`, IPv6 warning.
Tested:
- First run failed the self-check: `GetBestInterfaceEx(1.1.1.1)` returned
  "network location cannot be reached". Cleanup still worked. Cause: the
  TUN's address was still tentative (DAD). Fixed by `dadtransmits=0` and
  waiting for the adapter to be ready; added `--keep-on-fail` and
  `duolink debug route`.
- After the fix: self-check all ok; Steam downloaded at about 18–20 Mbit/s
  through the TUN on Ethernet; `--for` auto-stop cleaned up correctly.
- Not yet tested: crash test (`taskkill /f` then `duolink cleanup`) and the
  Wi-Fi-only path (now `--weights eth=0,wifi=1`).

### 4. Weighted per-flow balancing, health checks, live stats
Delivered: smooth WRR, `--weights`, health probes, live stats table.
Tested: failover works: unplugging Ethernet marked it DOWN within about 2 s
and the download continued on Wi-Fi; it came back UP when replugged.
Single-line weight tests (`eth=1,wifi=0` and `eth=0,wifi=1`) still to run.

### 5. Throughput test
Tested: a Steam download reached about 45 Mbit/s total across both lines
(sum of lines: 50).

### 6. Config file and stickiness
Delivered: `duolink.json`, `duolink config [--init]`, per-destination stickiness
(default on, 10 minutes), `--sticky-minutes`, sticky column in stats.
Tested: Steam still reached about 45 Mbit/s with stickiness on (it downloads
from several CDN IPs, so they still spread). `sticky dsts` rose on both lines.
`config --init` and editing duolink.json work (a changed `minutes` shows in
`duolink config`).

### 7. Instant failover
Status: done (partially tested).
Delivered:
- Adapter watcher (`NotifyIpInterfaceChange` + `NotifyUnicastIpAddressChange`,
  coalesced, then re-read with `GetAdaptersAddresses`). Disconnected, removed,
  no IPv4 address or no gateway → line DOWN immediately, reason logged.
  Adapter back → immediate probe; a good probe brings it UP.
- Changed address or interface index while running → reported, line stays
  DOWN until restart.
- Real-traffic dial failures: 2 in a row to different destination IPs →
  immediate probe; DOWN only if the probe fails. Refused/reset = line works.
- Unit tests for every transition, including "dials fail but the probe
  succeeds → stays UP", and a smoke test of the real subscription calls.
Tested:
- Disconnected and reconnected an uplink during a Steam download (about
  45 Mbit/s before). Found: after the reconnect the download didn't spread
  back over both lines, because stickiness keeps the CDN IPs on the line they
  moved to (see docs/action_items.md). Deferred.
- Not yet run: the remaining steps in docs/testing.md "Instant failover"
  (exact log lines on unplug/replug, Wi-Fi disconnect, dead DSL behind a
  working LAN, dead destinations → "stays UP").

### 8. Per-app rules
Status: done (partially tested).
Delivered:
- Flow → process lookup: the app socket's address (198.18.0.1:port) in
  `GetExtendedTcpTable` / `GetExtendedUdpTable` (UDP also matches 0.0.0.0:port),
  PID → exe via `QueryFullProcessImageName`. Table snapshots are shared by
  concurrent lookups and re-read on a miss; PID names cached 10 s.
- `processes` in duolink.json: `sticky_exempt` (default `["steam.exe"]`),
  `pin` (exe → line, balanced while that line is DOWN), `exclude` (always the
  lowest-metric line, or another while it's DOWN). One list per exe.
- Startup prints the rules; stats show flows per rule and unknown owners.
- Unit tests: rule resolution, pick behaviour per rule (incl. fallback),
  config validation, and the real socket-table lookups on loopback sockets.
Tested:
- Pin (`chrome.exe` → Wi-Fi, with `steam.exe` sticky-exempt in the config):
  works.
- Performance, measured on the test PC (about 140 TCP / 70 UDP sockets):
  reading the TCP owner table takes about 0.12 ms, the UDP table about
  0.06 ms, once per new connection; no effect on throughput.
- Not reported yet: Steam `sticky-exempt` count during a download, pin
  fallback, exclude, config error messages, no-rules run.

## Awaiting test

### 9. System tray app
Status: awaiting test.
Delivered:
- `internal/engine`: start/stop-able engine shared by `duolink run` and the
  tray; PC-wide mutex (one engine at a time); undo in reverse on Stop or a
  failed Start. Failures before any system change are marked retryable;
  `duolink cleanup` refuses while an engine runs.
- `duolink tray`: windowed copy `duolinkw.exe` (PE Subsystem = GUI) made
  and started from the console exe; no console window, logs to duolink.log.
- Tray: state icons (green / amber / grey), per-line speeds in menu and
  tooltip, Pause/Start (with 10 s retry while starting fails), Start with
  Windows, Open log, Quit with cleanup; single instance; UAC relaunch when
  not elevated.
- `duolink autostart on|off|status`: Task Scheduler logon task, highest
  privileges, no battery or time limits.
- Console tools started with `CREATE_NO_WINDOW` (no flashing windows).
- Unit tests: PE patch (incl. replacing a running copy), task XML, icon
  (Windows loads it), engine lifecycle (failed Start, undo order).
Tested: (pending)

## Planned

| # | Milestone | Status |
|---|---|---|
| 7 | Instant failover: mark a line DOWN as soon as its adapter disconnects, or probe at once after 2 failed dials to different destinations | done (partially tested) |
| 8 | Per-process rules via `GetExtendedTcpTable` / `GetExtendedUdpTable` (flow's local port → PID → exe): `sticky_exempt` (default `["steam.exe"]`), `pin` (process → line, with fallback when that line is down), `exclude` (not balanced). Configured in duolink.json | done (partially tested) |
| 9 | System tray app: `duolink tray`, no console, Start/Pause, speeds, Quit with cleanup, single instance, autostart via Task Scheduler with highest privileges | awaiting test |
| 10 | Small window opened from the tray; X hides it to the tray, only Quit exits | next |
| later | Metered/phone lines with a data cap; hot-plugging uplinks; automatic weights from passive throughput measurement; IPv6 | ideas |
