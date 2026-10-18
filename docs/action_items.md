# Action items

## Before the first public commit

- [x] Fill in the copyright holder in `LICENSE`.

## Open tests

- [ ] Autostart at logon: tick "Start with Windows", sign out and in; the
  tray should appear by itself, elevated, no UAC prompt, no console window
  (docs/testing.md, Tray app step 10).

- [ ] Crash test: `duolink run`, then `taskkill /f /im duolink.exe`, then
  `duolink cleanup`. Confirm whether the Wintun adapter and its routes vanish
  with the process (expected) or `cleanup` reports a leftover adapter.
- [ ] Single-line runs: `--weights eth=1,wifi=0` and `--weights eth=0,wifi=1`.

## Known issues

- **Health probes could mark a busy line DOWN** (seen in the milestone 9
  test during a Steam download). Fixed: two missed probes in a row are needed,
  probes run in parallel, default timeout 5 s. Seen working once: a single
  missed probe was tolerated, and the line went DOWN only on the second
  miss, when the Wi-Fi adapter really dropped. Still to test: a long
  saturated download with a healthy Wi-Fi adapter shows no false DOWN.
- **The test PC's USB Wi-Fi adapter fails under sustained load** (hardware,
  not duolink): probes time out, then Windows reports the adapter
  disconnected and it sees no networks until it's re-plugged. Being
  replaced. duolink handled each drop correctly (DOWN at once, back UP when
  it returns).

- After rebuilding, the logon task keeps running the old `duolinkw.exe` until
  `duolink tray` (or `duolink autostart on`) is run once to refresh it.
  The tray could notice a newer `duolink.exe` next to it and say so.
- `duolinkw.exe` is an unsigned exe written at runtime; some antivirus
  products may look at it more closely than at `duolink.exe`. Not seen so far.


- Failover in milestone 4 was observed within about 2 s, faster than the
  15 s probe interval should allow. Nothing in milestone 4's code marks a line
  DOWN between probes, so it was most likely a probe that happened to fall in
  that window. With milestone 7 an unplug should log `adapter disconnected`
  within about 1 s every time; close this item once that's confirmed.
- The failed-dial trigger reacts only as fast as dials fail: a silently
  dropped connect takes up to the 10 s dial timeout, so "internet dead behind
  a working LAN" is detected in about 10–20 s, not instantly.
- Per-app rules see only the exe name. Two programs with the same file name
  in different folders share a rule. Rules can't target services inside
  `svchost.exe` individually.
- `exclude` can't make a program bypass duolink, only keep it on one line
  (routes are per destination). Documented in README and decisions.md.
- The owner lookup reads the whole TCP or UDP table on a miss. Cheap for
  normal socket counts; worth measuring if a machine has tens of thousands
  of sockets (stats line `owner unknown` would also show misses).
- `go test -race` needs cgo (a C compiler), which isn't set up; the
  concurrency in the balancer is covered by review and unit tests only.
- If every line fails its health check, flows are spread over all lines
  instead of refused. Deliberate (probe targets may be blocked), but worth
  revisiting with instant failover.
- TCP half-close has no timeout: if one side sends FIN and the other never
  does, the flow lingers until TCP keepalive kills it.
- ICMP (ping, traceroute) to internet addresses doesn't work while running.
- Binary is about 14 MB, mostly gVisor.
- Stats print to the console every 2 s, which floods the scrollback on long
  runs. The tray app (milestone 9) replaces this.

## Ideas


- Status window: remember its position; per-monitor DPI (it uses the
  system DPI, so on a second monitor with a different scale Windows
  stretches it); a speed graph.

- Per-process rules (milestone 8) could also cover game traffic: pin game
  executables to the lower-latency line.
- UDP heuristic for games: UDP not to port 443 (QUIC) → pin to the game line.
- ICMP echo forwarding via `IcmpSendEcho2Ex` bound to a line's source address.
- Automatic weights from passive peak throughput per line.
