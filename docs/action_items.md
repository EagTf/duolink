# Action items

## Before the first public commit

- [x] Fill in the copyright holder in `LICENSE`.

## Open tests

- [ ] Crash test: `duolink run`, then `taskkill /f /im duolink.exe`, then
  `duolink cleanup`. Confirm whether the Wintun adapter and its routes vanish
  with the process (expected) or `cleanup` reports a leftover adapter.
- [ ] Single-line runs: `--weights eth=1,wifi=0` and `--weights eth=0,wifi=1`.

## Known issues

- **Stickiness blocks rebalancing after a failover.** While a line is DOWN,
  its destinations move to the other line and stick there. The sticky timer
  restarts on every new flow, so a long download (Steam reconnecting to the
  same CDN IPs) stays on one line even after the failed line is back UP.
  Seen in the milestone 7 test: after unplugging and replugging Ethernet,
  the download didn't spread back. Workaround: restart duolink, or pause
  for longer than `sticky.minutes`. Planned fix: when a line comes back UP,
  forget sticky entries created or moved while it was DOWN, so their next
  new flow is balanced again; entries from before the outage keep their line.

- Failover in milestone 4 was observed within about 2 s, faster than the
  15 s probe interval should allow. Nothing in milestone 4's code marks a line
  DOWN between probes, so it was most likely a probe that happened to fall in
  that window. With milestone 7 an unplug should log `adapter disconnected`
  within about 1 s every time; close this item once that's confirmed.
- If an uplink's address or interface index changes while running (new DHCP
  lease, USB adapter re-plugged), the line stays DOWN until duolink is
  restarted. Fix with the hot-plugging item.
- The failed-dial trigger reacts only as fast as dials fail: a silently
  dropped connect takes up to the 10 s dial timeout, so "internet dead behind
  a working LAN" is detected in about 10–20 s, not instantly.
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

- Per-process rules (milestone 8) could also cover game traffic: pin game
  executables to the lower-latency line.
- UDP heuristic for games: UDP not to port 443 (QUIC) → pin to the game line.
- ICMP echo forwarding via `IcmpSendEcho2Ex` bound to a line's source address.
- Automatic weights from passive peak throughput per line.
- Detect adapters appearing/disappearing at runtime (hot-plug, phone tethering).
