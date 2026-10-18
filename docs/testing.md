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
