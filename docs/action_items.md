# Action items

## Before the first public commit

- [x] Fill in the copyright holder in `LICENSE`.

## Open tests

- [ ] Crash test: `duolink run`, then `taskkill /f /im duolink.exe`, then
  `duolink cleanup`. Confirm whether the Wintun adapter and its routes vanish
  with the process (expected) or `cleanup` reports a leftover adapter.
- [ ] Single-line runs: `--weights eth=1,wifi=0` and `--weights eth=0,wifi=1`.

## Known issues

- Failover in milestone 4 was observed within about 2 s, faster than the
  15 s probe interval should allow. Not explained yet; milestone 7 makes
  failover deterministic anyway.
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
