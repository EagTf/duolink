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

## Planned

| # | Milestone | Status |
|---|---|---|
| 7 | Instant failover: mark a line DOWN as soon as its adapter disconnects (`NotifyIpInterfaceChange`) or after 2 consecutive failed dials, not only on the next probe | next |
| 8 | Per-process rules via `GetExtendedTcpTable` / `GetExtendedUdpTable` (flow's local port → PID → exe): `sticky_exempt` (default `["steam.exe"]`), `pin` (process → line, with fallback when that line is down), `exclude` (not balanced). Configured in duolink.json | planned |
| 9 | System tray app: `duolink tray`, no console, Start/Pause, speeds, Quit with cleanup, single instance, autostart via Task Scheduler with highest privileges | planned |
| 10 | Small window opened from the tray; X hides it to the tray, only Quit exits | planned |
| later | Metered/phone lines with a data cap; hot-plugging uplinks; automatic weights from passive throughput measurement; IPv6 | ideas |
