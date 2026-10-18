# Manual test plan

Unit tests (`go test ./...`) cover the pure logic: WRR order, health
fallback, stickiness and expiry, weight parsing, config loading and flag
overrides, overlap warnings. Everything that touches Windows networking is
tested by hand on a PC with two working uplinks, in an **administrator**
terminal, with `wintun.dll` next to `duolink.exe`.

Use `--for 2m` (or longer) on every `run` while testing: it reverts on its own
if something breaks your connection.

## Adapter list (`duolink list`)

1. `duolink list`: both uplinks show `yes`, with different gateways. Wi-Fi Direct
   and hotspot virtual adapters, loopback and disconnected adapters show
   `skipped:` with a reason.
2. Connect two adapters to the same router: expect the "same gateway" warning.

## Bound dialer (`duolink list --ip`)

1. `duolink list --ip`: each line shows a different public IP.
2. If two lines show the same IP, they share a line or binding failed; a
   warning says so.

## Run, routes and cleanup

1. `duolink run --for 2m`. Expect: `adapter is up, 198.18.0.1/30 is ready`, then
   self-check lines all `ok`:
   - internet (1.1.1.1) is routed into the TUN
   - each router's /24 stays direct on its adapter
   - each line: bound socket reached the internet directly (no loop)
2. While running: browse; open both router admin pages (e.g.
   http://192.168.1.1 and http://192.168.2.1).
3. While running, in a second terminal: `duolink list --ip` returns the same
   per-line public IPs as without duolink (leak test).
4. While running: `duolink debug route 1.1.1.1` shows the `duolink` adapter.
5. Let `--for` expire: the delete commands are printed, then "internet traffic
   now goes via interface N again". `route print -4` shows no `0.0.0.0/1` or
   `128.0.0.0/1` routes.
6. Ctrl+C during a run: same cleanup as above.
7. Crash: `duolink run`, then `taskkill /f /im duolink.exe` from another terminal.
   Then `duolink cleanup`, check browsing and `route print -4`. Note whether
   cleanup reported a leftover adapter.
8. Self-check failure diagnostics: `--keep-on-fail` prints `route print -4`,
   `Find-NetRoute` and the TUN's address/interface state before cleanup.
9. Refusals: non-admin terminal → error; rename wintun.dll → clear error
   with the download link; disconnect one line → "need at least 2 usable uplinks".

## Balancing and health

1. `duolink run --for 5m` with a multi-connection download (Steam): both lines
   carry traffic; total ≈ sum of the lines (about 45 of 50 Mbit/s observed).
2. `--weights eth=1,wifi=0`: only Ethernet carries traffic (Wi-Fi shows `off`).
   Same with `eth=0,wifi=1`.
3. `--weights eth=2,wifi=1`: the `flows` column grows about 2:1.
4. Failover: unplug Ethernet (or disable the adapter) during a download.
   Expect `Ethernet is DOWN`, new flows only on Wi-Fi, the download continues.
   Replug: `Ethernet is back UP` within about 4 s.

## Instant failover

Run `duolink run --for 10m` and keep a download going (Steam, or several
browser downloads) so both lines carry traffic. Watch the log lines between
the stats.

1. **Unplug Ethernet.** Within about 1 s: `Ethernet is DOWN (adapter
   disconnected)`. New flows go to Wi-Fi only (the `flows` column stops
   growing on Ethernet); the download continues.
2. **Plug it back in.** Expect `Ethernet: adapter is connected again;
   probing`, then `Ethernet is back UP` once DHCP is done (a few seconds).
   Until the probe succeeds the line stays DOWN.
3. **Disable the Wi-Fi adapter** (Settings → Network, or
   `Disable-NetAdapter -Name 'Wi-Fi'` in another admin window), then
   re-enable it. Same as 1–2 for Wi-Fi; disabling may log `adapter removed`
   instead of `disconnected`.
4. **Disconnect Wi-Fi from the network** (pick "Disconnect" in the Wi-Fi
   menu) and reconnect. Same pattern.
5. **Internet dead behind a working LAN** (the case probes and dials catch,
   not the watcher): unplug the *router's* WAN/DSL cable, keep the PC's
   cable in. Expect within about 10–20 s either `... 2 failed dials in a
   row (...), then probe failed ...` or a regular `probe failed`. Plug the
   DSL back in: `back UP` within a few seconds after the line syncs.
6. **Dead websites must not take a line down:** while running, connect to
   four unreachable documentation addresses (nothing answers there). New
   destinations alternate between the lines, so four give each line two
   failures. In another PowerShell window:
   ```
   foreach ($ip in '203.0.113.1','203.0.113.2','198.51.100.1','198.51.100.2') {
     Start-Job { Test-NetConnection $using:ip -Port 81 } | Out-Null }
   ```
   About 10 s later (the dial timeout) expect, for each line,
   `... 2 failed dials in a row (...), but the probe succeeded: stays UP`,
   and no DOWN. (Test-NetConnection itself may report success: the
   netstack accepts the connection locally before dialing out. Ignore it;
   the duolink log is what counts.)
7. **Cleanup unchanged:** let `--for` expire: routes removed as before.

## Per-app rules

Get each line's public IP first (`duolink list --ip`, without duolink
running). During the tests, open https://api.ipify.org in the browser to see
which line the browser's connections use (reload with Ctrl+F5). Replace
`chrome.exe` with your browser's exe (`msedge.exe`, `firefox.exe`).

1. **Default (Steam exempt).** No `processes` in duolink.json.
   `duolink run --for 10m`: startup prints `process rules: steam.exe
   sticky-exempt`. Start a Steam download: the stats show
   `rules (flows): steam.exe sticky-exempt N` growing, both lines carry
   traffic, total about 45 Mbit/s. `owner unknown` stays at 0 or small.
2. **Pin.** Add `"processes": {"pin": {"chrome.exe": "wifi"}}`. Run, reload
   api.ipify.org several times: always the Wi-Fi public IP. The stats show
   `chrome.exe pinned N`. (Browsers also use UDP (QUIC/HTTP3), so this covers
   the UDP lookup too.)
3. **Pin fallback.** While running with the pin, disconnect Wi-Fi: reloads
   now show the Ethernet IP. Reconnect; after `Wi-Fi is back UP`, new
   connections show the Wi-Fi IP again. Close and reopen the tab if the
   browser keeps the old connection alive.
4. **Exclude.** Replace the pin with `"exclude": ["chrome.exe"]`. The startup
   line shows `chrome.exe excluded → Ethernet` (the lowest-metric line from
   `duolink list`). api.ipify.org always shows the Ethernet IP. Unplug
   Ethernet: it switches to Wi-Fi.
5. **Config errors.** `duolink config` with `"pin": {"steam.exe": "eth"}`
   (and no `sticky_exempt`): error explaining steam.exe is in sticky_exempt
   by default. With `"pin": {"chrome.exe": "phone"}`: `duolink run` fails
   with "no usable uplink matches".
6. **No rules.** `"processes": {"sticky_exempt": []}`: startup prints
   `process rules: none`, no rules line in the stats, everything balanced as
   in milestone 6.

## Tray app

Build with `go build`, then in an **admin** terminal:

1. **Start.** `duolink tray`: prints `updated duolinkw.exe ...` (first time)
   and `duolink is starting in the tray`, and the prompt comes back at once.
   No console window appears. Within a few seconds the icon turns green;
   hover shows `duolink ↓ … ↑ … Mbit/s` and both lines UP.
2. **Menu.** Right-click: status `Running`, one row per line with state and
   speeds (updating every 2 s during a download), a Total row, Pause, Start
   with Windows, Open log, Quit.
3. **Log.** Open log: notepad shows the startup (uplinks, netsh commands,
   self-check `ok` lines). No console windows flashed during startup.
4. **Pause / Start.** Pause: icon grey, status `Paused`; `route print -4`
   shows no `/1` routes and browsing works normally. Start: green again. Edit
   duolink.json (e.g. a pin) while paused; after Start, the log shows the new
   rules.
5. **Line down.** Unplug Ethernet: icon amber, status `Running: a line is
   DOWN`, Ethernet row DOWN. Replug: green again.
6. **Single instance.** Run `duolink tray` again: a dialog says it's already
   running. `duolink run` in the terminal: refused with "already running (in
   the tray ...)".
7. **Quit.** Quit: icon disappears; log ends with route removal and `quit`;
   `route print -4` clean.
8. **Not elevated.** From a normal (non-admin) terminal, `duolink tray`
   refuses with a message. Double-clicking `duolinkw.exe` in Explorer shows
   the UAC prompt; after Yes the tray starts; after No a dialog explains.
9. **Retry.** Disconnect Wi-Fi, start the tray: status `Not running: need at
   least 2 usable uplinks ... (retrying)`, grey icon. Reconnect Wi-Fi: within
   about 10 s it starts by itself. The log shows the error once, not every
   10 s.
10. **Autostart.** Tray menu → Start with Windows (gets a tick), or
    `duolink autostart on`. `duolink autostart status` says on; Task
    Scheduler shows task `duolink` (Run with highest privileges; Conditions:
    no AC-power restriction; Settings: no "stop if runs longer than").
    Sign out and in (or reboot): the tray starts by itself, no UAC prompt,
    no console window. `duolink autostart off` removes the task.
11. **Rebuild while running.** With the tray running, `go build` and then
    `duolink tray`: prints `updated duolinkw.exe` (the running copy is moved
    to `duolinkw.exe.old`) and says it's already running. After Quit and
    `duolink tray`, the new build runs.

## Stickiness

1. Default run: `sticky dsts` grows on both lines while browsing.
2. Steam download with stickiness on still spreads over both lines (several
   CDN IPs). Compare with `--sticky-minutes 0`.

## Config

1. `duolink config` with no duolink.json: prints defaults, says "no duolink.json".
2. `duolink config --init` writes duolink.json; running it again refuses to overwrite.
3. Edit a value (e.g. `"minutes": 20`): `duolink config` shows it.
4. Typo a key (`"stickey"`): every command that loads config fails with
   `unknown field "stickey"`.
5. Flags override the file: `duolink config --sticky-minutes 0` shows
   `"enabled": false`; `--weights wifi=1` replaces the file's weights.
