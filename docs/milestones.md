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

### 9. System tray app
Status: done (logon start of autostart not yet tested).
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
Tested (steps in docs/testing.md, "Tray app"):
- Start from an admin terminal: prompt returns at once, no console window,
  icon grey then green, tooltip with both lines UP.
- Menu: status, per-line rows and total with live speeds during a Steam
  download; Open log shows the full startup, self-check all ok; no console
  windows flashed (netsh runs without a window).
- Pause / Start: routes removed on Pause (`route print` shows only the two
  normal defaults), restored on Start. Editing duolink.json while paused
  (weights 2:1) took effect on Start.
- Line down: unplugging Ethernet gave an amber icon and a DOWN row; the log
  shows the failed-dial trigger and the adapter watcher both firing within
  the same second, then `back UP` after replugging. Disconnecting and
  removing the USB Wi-Fi adapter was handled the same way, including
  "adapter removed" and its return.
- Single instance: a second `duolink tray` shows the dialog; `duolink run`
  and `duolink cleanup` refuse while the tray runs.
- Quit: routes removed first, icon gone, normal routing.
- Retry: started with Wi-Fi disconnected: grey "(retrying)", the error
  logged once; came up by itself after Wi-Fi reconnected.
- Not elevated: refused from a normal terminal; `duolinkw.exe tray` showed
  UAC: No gave the dialog, Yes started the tray.
- Start with Windows: the menu tick created the task (`autostart status`:
  on) and unticking deleted it. Not yet tested: the actual start at logon.
- Rebuild while running: the running copy was moved to `.old`, the new copy
  written, and the new build ran after Quit and start.
- Found during the test (fixed): double-clicking duolinkw.exe did nothing
  (now starts the tray); the log didn't say why a second tray exited or
  when a quiet retry finally succeeded (both logged now).
- Seen during the test (open): Wi-Fi marked DOWN twice under a saturated
  download (probe timeouts); see docs/action_items.md.
- No retry loop after a late failure: covered by unit tests (can't be
  triggered safely by hand).

### 10. Status window
Status: done.
Delivered:
- `internal/window`: a small native Win32 window (no GUI framework, no cgo)
  on its own message-loop thread: status line, a table per line (state,
  weight, down/up, TCP/UDP, flows, sticky destinations), totals, per-app
  rule counts, recent events (newest first), and Pause/Start, Open log and
  Quit buttons. Current Windows button style via an activation context;
  DPI-aware text; white text area.
- Opens with a left click on the tray icon or "Open duolink" in the menu.
  X hides it to the tray; only Quit exits. A second `duolink tray` shows the
  running tray's window instead of the "already running" dialog.
- Health and failover events reach the window through an engine `OnEvent`
  hook (`Balancer.OnLog`), alongside the tray's start/pause/failure events.
- Tests: text formatting, the real window created hidden and found by class
  name, the event hook. Layout checked by rendering the window off-screen.
Tested (steps in docs/testing.md, "Status window"), all passed:
- Left click opens the window in front with the icon in its title bar;
  status, per-line table, totals and recent events shown.
- During a download the table updated every 2 s on both lines; rules line
  shown; layout clean, nothing cut off.
- X hid the window while balancing continued (routes still in place); a
  left click brought it back.
- Pause / Start / Open log from the window, with the tray menu in sync.
- Unplugging and replugging Ethernet showed DOWN / back UP in the window
  within about a second, with the amber / green icon.
- A second `duolink tray` brought the running window to the front, no
  dialog (log: "showed its window").
- Quit from the window removed the routes and exited (log: stopping, both
  routes deleted, quit).
- Scaled display: sharp text, nothing cut off.
- Requested during the test: edit the settings from the window, so it's
  the full app. Planned as milestone 11.

### 11. Settings in the window
Status: done.
Delivered:
- A Settings window (status window button, or "Settings…" in the tray
  menu) with every option in duolink.json and a short explanation per
  section: a weight per connected line, stickiness, health checks
  (intervals, timeout, misses before DOWN, targets), UDP idle timeout, and
  the per-app rules as three lists (balance every connection, never
  balance, pin `exe = line`).
- Save and apply: the form is checked first (numbers, the same validation
  as loading the file, and against the real lines, e.g. a pin to a line
  that doesn't exist); errors show in red in the window's own words and
  nothing is written. Then duolink.json is written atomically
  (`config.Save`: temp file + rename) and duolink restarts with it if it
  was running (if paused, it applies on Start).
- Weights all 1 are saved as no weights (`{}`), so the file keeps working
  when a line is renamed or replaced (e.g. a new Wi-Fi adapter).
- Reset to defaults fills the form without saving; Cancel or X discards.
- Tests: form <-> config conversion (prefix-matched weights, rule lists,
  pins, errors in the window's words), `config.Save` (round trip, invalid
  config leaves the file untouched). Layout checked by rendering off-screen.
Tested (steps in docs/testing.md, "Settings window"), all passed:
- Opens from the status window and the tray menu with the current
  settings; readable, nothing cut off, Tab order fine.
- Invalid input (DOWN after 0, steam.exe in two lists, a pin to "phone")
  showed red errors in the window's words; duolink.json untouched and no
  restart.
- Save and apply (stickiness 15 min): saved, restarted in about a second,
  the log showed the new value.
- Weights 2:1 saved per line; back to 1:1 saved as `{}`.
- Saved while paused: applied on Start, no restart before.
- Reset to defaults filled the form without saving; Cancel and X discard
  edits; reopening shows the saved settings.
- Broken duolink.json: tray showed the config error and retried; Settings
  showed the defaults with a red note; Save replaced the file and duolink
  started by itself.
- Suggested during the test: friendlier per-app rule editing (file picker,
  line dropdown for pins). Noted in docs/action_items.md.

## Awaiting test

### 12. Recovery after failover, hot-plug, friendlier per-app rules
Status: done.
Delivered:
- After an outage: sticky entries created while a line was DOWN are
  forgotten when a line comes back UP, so those destinations are balanced
  over all lines again (log: `rebalancing N destinations that moved during
  the outage`). Entries from before the outage keep their line.
- Hot-plug, adapters tracked by LUID: a line whose adapter returns at a new
  address or interface index becomes a new line at the new address (same
  weight; log: `now at ... (was ...); probing it`); a removed adapter is
  dropped (`adapter removed; no longer a line`); a new usable adapter is
  added with its configured weight (`new line (weight N); probing it`). New
  and replaced lines start DOWN until a probe succeeds. Health loops, the
  loop guard, stats, the tray menu and the window follow the current lines.
- Pins and "exclude" are resolved by line name at each pick, so they follow
  a replaced adapter.
- Found in the test: Steam kept downloading on Ethernet after Wi-Fi came
  back, because it reuses connections opened during the outage (it's
  sticky-exempt, so the sticky rebalance doesn't touch it). Added: when a
  line comes back UP, connections that "balance every connection" apps
  opened during the outage are closed so they reconnect over all lines
  (`processes.reconnect_after_outage`, default on; checkbox in Settings).
- Settings window: per-app rules as a list (one rule per program), with a
  program field, Browse… (standard Open dialog for an .exe), a rule
  dropdown (balance every connection, never balance, pin to each connected
  line) and Add / update / Remove.
- Tests: rebalancing after recovery; moved, removed and new adapters;
  weights for new lines; pins following a replaced line; health loops
  starting for new lines; rule list conversion, options, upsert, exe names,
  the Open-dialog struct size. Layout checked by rendering off-screen.
- Review fix: unplugging a line's adapter counts as an outage until a new
  line takes its place, so download apps are also rebalanced after the
  adapter is plugged back in; a probe that finishes after its line was
  replaced is ignored.
Tested: all steps pass. Steam spreads over both lines again after a Wi-Fi
outage (outage connections reconnected), and after unplugging and
replugging the USB Wi-Fi adapter; a new adapter becomes a line, a new
address is followed, pins stay on their line, no routing loop
(`duolink list --ip`), and the Settings rules list adds, edits and removes
rules.

### 13. Smarter balancing
Status: done (retry on the other line not tested yet).
Delivered:
- Least-busy picking: each line counts its open TCP flows, and a new flow
  goes to the lowest open / weight; ties go to smooth weighted
  round-robin, so with nothing open the order is as before. A flow stops
  counting at its first half-close. Stickiness and per-app rules are
  unchanged; least-busy replaces the round-robin step only.
- Retry on another line: a TCP or UDP dial that gets no answer
  (unreachable, timeout) is repeated through the next line; refusals
  aren't. Count shown as "Connections saved by trying the other line".
- Broken-line cooldown: network down / unreachable / address gone through
  a line keeps new flows off it for 15 s and probes it at once (log: `dial
  to ... failed (...); no new flows on it for 15s, probing it`). A good
  probe ends it. Not treated as an outage.
- MTU 9000 on the TUN: netstack and Windows' side (`set subinterface ...
  mtu=9000 store=active`, a warning if it fails).
- Pooled 128 KB copy buffers; a half-closed TCP flow ends after 2 minutes
  without data. TCP keepalive on the netstack side was already on
  (tun2socks sets it).
- Tests: least-busy order and weights, Done counting once, skipping tried
  lines (sticky entry moves, pin falls back), cooldown (one log line, probe
  kicked, all-cooling fallback, ended by time or probe), dial error classes,
  the half-close idle timeout.
Tested: a Steam download reached about 50 Mbit/s over both lines (a new
best), with visibly less CPU for duolinkw.exe than before. The log shows
the TUN's MTU set to 9000 with no warning. Wi-Fi disconnected and
reconnected during the download: DOWN, back UP after 11 s, 2 destinations
rebalanced and 29 download connections reconnected onto both lines. Not
tested yet: retry on the other line (needs a line that stays connected but
loses internet; an adapter disconnect marks the line DOWN before any dial
fails) and 2:1 weights with least-busy picking.

### 14. Automatic weights
Status: done.
Delivered:
- `auto_weights` (default on): with no weights set, each line's weight
  follows its measured speed (peak of what it carried through duolink,
  per second; fastest line 100, floor 5; equal until every line carried
  50 KB/s). Busy-but-slower lines sink 3%/s, idle lines hold and drift
  back up to half the best. Logged when first measured and on changes of
  15+, at most once a minute.
- Fixed weights (file, `--weights`, Settings) turn it off.
- New and replaced lines start at 100 and are measured afresh.
- Settings: "Automatic" checkbox; the weight fields are greyed out while
  it's ticked.
- Status window: `Auto` weight column, and `PC down`, each adapter's whole
  download from Windows' counters (GetIfEntry2Ex), all apps. Window
  widened to fit. `duolink run` prints `pc down` too.
- Tests: weights follow speed, idle hold and cap, busy decay, floor and
  recovery, logging limits, new lines, fixed weights win, the form's
  checkbox round trip, the window's new columns.
- Planned as `"weights": "auto"`; built as a separate `auto_weights` switch
  so existing files stay valid.
Tested: all steps pass. During a Steam download the weights were measured
as Ethernet 100, Wi-Fi 90, and the TCP column followed them. PC down
matched duolink's own numbers during the download and was higher with
LAN traffic. Settings: unticking Automatic and setting 2 / 1 showed
`Weight` 2 and 1 with TCP about 2:1 (also covers milestone 13's 2:1
test), and ticking it again went back to `Auto`; duolink.json followed.
The wider status window fits the table. After a Wi-Fi outage the line got
traffic again with its weight back near its old value.

## Planned

| # | Milestone | Status |
|---|---|---|
| 7 | Instant failover: mark a line DOWN as soon as its adapter disconnects, or probe at once after 2 failed dials to different destinations | done (partially tested) |
| 8 | Per-process rules via `GetExtendedTcpTable` / `GetExtendedUdpTable` (flow's local port → PID → exe): `sticky_exempt` (default `["steam.exe"]`), `pin` (process → line, with fallback when that line is down), `exclude` (not balanced). Configured in duolink.json | done (partially tested) |
| 9 | System tray app: `duolink tray`, no console, Start/Pause, speeds, Quit with cleanup, single instance, autostart via Task Scheduler with highest privileges | done (logon start pending) |
| 10 | Small window opened from the tray; X hides it to the tray, only Quit exits | done |
| 11 | Settings in the window, so it's the full app: edit everything in duolink.json (weights, stickiness, health checks, per-app rules) with explanations, validation before saving, and Apply (restarts duolink with the new settings) | done |
| 12 | Recovery after failover (rebalance destinations that moved during an outage), hot-plug (lines follow adapters that come, go or change address), friendlier per-app rules (list, file picker, line dropdown) | done |
| 13 | Smarter balancing: retry a failed dial on another line, least-busy picking, MTU 9000 on the TUN | done (retry untested) |
| 14 | Automatic weights from measured speed, per-adapter total traffic | done |
| 15 | Auto-pause with fewer than two working lines; control the running tray from the CLI | planned |
| 16 | IPv6 | planned |
| 17 | SOCKS5 proxy mode, no admin needed | planned |
| 18 | Releases and installer | planned |
| 19 | Cross-platform groundwork (split Windows code behind interfaces) | planned |
| 20 | Linux, then macOS | planned (needs a test machine) |
| later | Metered/phone lines with a data cap; ICMP (ping) forwarding; speed graph in the window | ideas |

## Plan for milestones 13–20

Milestones 13–20 add faster balancing, automatic weights, IPv6, more ways
to run and install duolink, and other systems. What duolink already does
well stays as it is, and every new feature has to work with it:

- per-destination stickiness, so sites and games see one public IP;
- per-app rules (balance every connection, pin to a line, never balance);
- recovery after an outage, including reconnecting download apps;
- health checks that need two misses before DOWN, plus instant failover on
  adapter events;
- hot-plug by LUID through adapter change events;
- the settings and status windows, the event history, and the self-check
  before routes go live; routes with `store=active` only.

### 13. Smarter balancing
- **Retry on another line.** When a TCP dial fails with a line-level error
  (network down or unreachable, no address, timeout), dial the same
  destination through the next line at once instead of resetting the app's
  connection. The failed line is skipped for new flows for a short cooldown
  and gets a probe (the probe still decides DOWN). Refused and reset
  connections are answers from the destination: no retry. Same for UDP
  socket setup.
- **Least-busy picking.** Replace smooth round-robin with weighted
  least-active-flows: a new flow goes to the line with the lowest
  (open flows + 1) / weight, ties broken from a random start. A flow stops
  counting as open at its first half-close. Stickiness and per-app rules
  are applied first, as now; least-busy only replaces the round-robin step.
- **MTU 9000 on the TUN** (Windows side and netstack). TUN traffic never
  leaves the PC, and bigger packets mean far fewer trips through the
  netstack. Measure CPU per Mbit/s before and after.
- Pooled copy buffers, and an idle timeout for half-closed flows (covers the
  half-close item in action_items). TCP keepalive on the netstack side
  turned out to be on already.

### 14. Automatic weights
- Each line's weight follows its measured speed: the peak throughput it
  carried recently, relative to the best line (minimum 0.05, so a slow line
  still gets the odd flow and is re-measured). Weights stay equal until
  every line has been measured. The peak only drops while a line is busy
  but slower; an idle line's estimate drifts back up (capped at half the
  best line's), so one bad moment can't starve it.
- Configured weights stay and win: `"weights": "auto"` (the new default) or
  fixed numbers as now; weight 0 still means unused. Settings window gets
  the choice.
- Each line's total traffic from Windows' own counters (all apps, not only
  duolink's flows) in the window, next to duolink's own numbers.

### 15. Auto-pause and CLI control
- With fewer than two working lines there's nothing to balance: pause by
  itself, resume a few seconds after a second line works. Pausing waits for
  quiet (about 10 s, longer while a download runs), and automatic switches
  are at least 30 s apart so a flapping line can't toggle it. A Pause or
  Start by the user always wins. On by default, a checkbox in Settings and
  the tray menu.
- `duolink on`, `duolink off`, `duolink quit`, `duolink status` control
  the running tray from a terminal, over a local named pipe that only the
  same user can open.
- README troubleshooting: Windows' "minimize the number of simultaneous
  connections" policy, phone tethering as a second line.

### 16. IPv6
- Dual-stack TUN: a private (ULA) IPv6 address on it, so Windows still
  prefers IPv4 for dual-stack sites and they spread over every line, and
  `::/1` + `8000::/1` routes (`store=active`).
- IPv6 dialer (`IPV6_UNICAST_IF`, host byte order) and IPv6 probe targets;
  health per line and per address family.
- Mode auto / on / off. In auto, IPv6 is balanced only when every line has
  working IPv6; otherwise new IPv6 connections are refused before the
  handshake completes (TCP reset, UDP port unreachable), so apps fall back
  to IPv4 at once.
- Stickiness, rules, the loop guard, cleanup and `duolink list` cover IPv6.

### 17. SOCKS5 proxy mode
- `duolink socks`: a SOCKS5 proxy on 127.0.0.1 that balances only the apps
  pointed at it. No admin, no TUN, no routes. Same balancer, health checks
  and retry; per-app rules don't apply (no owner lookup).

### 18. Releases and installer
- A GitHub Actions release workflow on `vX.Y.Z` tags; CHANGELOG section as
  release notes.
- A script that downloads `wintun.dll` and checks its SHA-256 and
  signature, used by the release build and documented for source builds.
- Release assets: portable zip (duolink.exe + wintun.dll), an installer
  (Start menu entry, optional autostart, clean uninstall), and a one-line
  PowerShell install command in the README.
- `duolink version`.

### 19. Cross-platform groundwork
- Put the Windows-only parts behind small interfaces with
  `_windows.go` files: adapter list and change events (`netif`), routes and
  TUN setup (`route`), socket owner lookup (`procs`), the dialer's interface
  binding, autostart, single instance. The windows (Win32) stay
  Windows-only; other systems get the tray menu only.
- No behaviour change on Windows; CI also builds for Linux and macOS.

### 20. Linux, then macOS
- **Linux:** kernel TUN, routes with `ip route`, `SO_BINDTODEVICE`, owners
  from `/proc/net/tcp` + `/proc/<pid>/fd`, systemd user unit or desktop
  autostart, capabilities instead of running as root.
- **macOS:** utun, `IP_BOUND_IF` plus a scoped default route per line,
  owners from libproc, launchd for autostart.
- Each needs a test machine; built and tested one system at a time.
