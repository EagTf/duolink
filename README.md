# duolink

[![CI](https://github.com/EagTf/duolink/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/EagTf/duolink/actions/workflows/ci.yml)

Use several internet connections on one Windows PC at the same time.

duolink spreads your PC's connections across all your network adapters, for
example Ethernet to one router and Wi-Fi to another. With two 25 Mbit/s
lines, a Steam download that opens several connections can reach about
50 Mbit/s.

## What it does, and what it doesn't

- **Per-flow load balancing, not bonding.** Each new TCP connection or UDP
  flow goes out through one line: the least busy one, counting the
  connections each line has open. Each line's share follows its measured
  speed, so a faster line gets more (or set fixed weights). A flow stays on
  its line until it ends. If a line gives no answer when a connection
  starts, duolink tries the other line, so the app doesn't see an error.
- **One connection = one line.** A single download over one connection never
  gets faster than the line it's on. You gain when an app uses many
  connections in parallel: Steam, browsers, download managers, torrent clients.
- **No server needed.** True bonding (splitting one connection across lines)
  needs a server on the other end to reassemble it, as Speedify and similar
  services do. duolink needs nothing outside your PC.
- **Each line keeps its own public IP.** Websites may see your connections
  coming from two IPs (see [Known limits](#known-limits)).

Similar projects: net-merge and
[dispatch-proxy](https://github.com/alexkirsz/dispatch-proxy) (a SOCKS proxy;
only apps that you point at it are balanced). duolink works system-wide without
any per-app setup.

## Requirements

- Windows 10 or 11 (64-bit)
- Administrator rights (duolink changes the routing table)
- Two or more adapters that each have their own internet connection and
  default gateway, ideally on different subnets (e.g. `192.168.1.0/24` and
  `192.168.2.0/24`)
- [Go](https://go.dev/dl/) to build it
- `wintun.dll` from [wintun.net](https://www.wintun.net/) (not included in
  this repository)

## Build

```
git clone https://github.com/eagtf/duolink.git
cd duolink
go build
```

Then download the Wintun zip from <https://www.wintun.net/>, and copy
`wintun\bin\amd64\wintun.dll` next to `duolink.exe`. duolink looks for it only
there, not in the current directory or on PATH, and refuses to start without it.

## What duolink changes on your system, and how to undo it

While duolink is running (`duolink run`, or the tray app not paused):

1. A virtual network adapter named `duolink` (Wintun) with the address
   `198.18.0.1/30`, a range reserved for benchmarking that never appears on
   real networks.
2. Two routes, `0.0.0.0/1` and `128.0.0.0/1`, that send internet traffic into
   that adapter. Your existing default routes are not touched.

Both are **temporary**: the routes are added with `store=active`, so they're
never written to disk, and they belong to the adapter, which disappears when
duolink exits. Every command duolink runs is printed as it runs.

To undo:

- Normally: press **Ctrl+C**, close the window, or use `--for` (see below);
  in the tray, **Pause** or **Quit**. duolink removes the routes first, then
  the adapter.
- After a crash: run `duolink cleanup` as administrator.
- Always works: **reboot**. Nothing duolink does to routing survives a restart.

Two things are only there if you ask for them, and stay until you remove them:

- `duolinkw.exe` and `duolink.log` next to `duolink.exe`, when you use the
  tray (delete them any time; `duolink tray` recreates the exe).
- A Task Scheduler task named `duolink`, when you turn on "Start with
  Windows" or run `duolink autostart on`. Remove it with
  `duolink autostart off` (or the menu item, or Task Scheduler).

Your routers' own networks (e.g. `192.168.1.0/24`) stay directly reachable
while duolink runs, so router admin pages keep working.

## Usage

`run`, `tray`, `cleanup` and `autostart on/off` need an administrator terminal.

```
duolink list                 show adapters and which ones duolink will use, and why others are skipped
duolink list --ip            also show each line's public IP (asks api.ipify.org through each adapter)
duolink tray                 run in the system tray, without a console window
duolink autostart on         start the tray at every logon (elevated, no UAC prompt); off / status
duolink run                  start balancing in this terminal; Ctrl+C stops and cleans up
duolink run --for 2m         stop and clean up automatically after 2 minutes
duolink run --weights eth=2,wifi=1
duolink run --sticky-minutes 0
duolink config               print the effective config
duolink config --init        write a default duolink.json to edit
duolink cleanup              remove routes left behind by a crash
duolink debug route 1.1.1.1  show which adapter Windows would use (read-only)
```

`--for` is a safety net while trying duolink out: if anything goes wrong with
your connection, it reverts on its own.

`duolink run` first checks that the routes work, that your routers stay
reachable and that there is no routing loop. Then it prints live stats every
2 seconds:

```
19:20:01  Ethernet     UP   w1   down  21.3  up   0.8 Mbit/s  tcp   12  udp   2  flows    140  sticky dsts   31
          Wi-Fi        UP   w1   down  20.9  up   0.7 Mbit/s  tcp   11  udp   2  flows    139  sticky dsts   29
          total                  down  42.2  up   1.5 Mbit/s
```

Adapter names in `--weights` match by unique prefix, ignoring case and
punctuation: `eth` = Ethernet, `wifi` = Wi-Fi. Run `duolink list` to see the names.

### Tray app

`duolink tray` puts duolink in the notification area and returns your
terminal at once. The icon is green while running, amber when a line is
down, and grey when paused. Hover for the total speed. **Left-click** the
icon (or choose "Open duolink" in the menu) for the status window: a table
of each line's state, weight (`Auto` when measured), speed, connections and
sticky destinations, and the adapter's whole download as Windows counts it
(`PC down`, all apps), the per-app
rule counts, and the recent events (lines going down and up, pauses). Closing
the window with X just hides it; duolink keeps running in the tray.
**Right-click** for the menu with each line's state and speed, and:

- **Pause / Start**: Pause restores normal routing; Start brings duolink
  back and re-reads `duolink.json`, so you can change settings without
  quitting. If starting fails because something isn't ready yet (say Wi-Fi
  isn't connected), it keeps retrying every 10 seconds. Other failures stop
  and show why; click Start to try again.
- **Start with Windows**: same as `duolink autostart on`.
- **Open log**: `duolink.log`, which has everything `duolink run` would print.
- **Quit**: cleans up, then exits.

How it avoids a console window: Windows decides from a flag in the exe
whether a program gets a console. `duolink tray` keeps a copy of itself,
`duolinkw.exe`, with that flag set to "windowed", and runs the tray from
it. Double-clicking `duolinkw.exe` also starts the tray (Windows asks for
admin rights). After rebuilding duolink, run `duolink tray` once to refresh the copy.

Only one duolink can run at a time: `duolink run` refuses while the tray is
active, and the other way round. `duolink cleanup` also refuses while duolink runs:
it's only for leftovers after a crash.

## Configuration: duolink.json

The easiest way to change settings is the tray app: **Settings** in the
status window (or "Settings…" in the tray menu) shows every option with an
explanation, checks your changes, saves them to `duolink.json` and restarts
duolink with them.

You can also edit the file by hand. Put `duolink.json` next to `duolink.exe`
(`duolink config --init` writes one with all defaults). It only needs the
settings you want to change. Command-line flags override it. Unknown or
invalid settings are reported as errors.

```json
{
  "auto_weights": true,
  "weights": {},
  "sticky": { "enabled": true, "minutes": 10 },
  "health": {
    "interval_seconds": 15,
    "down_interval_seconds": 4,
    "timeout_seconds": 5,
    "targets": ["1.1.1.1:443", "8.8.8.8:443"],
    "failures_before_down": 2
  },
  "processes": {
    "sticky_exempt": ["steam.exe"],
    "pin": { "game.exe": "eth" },
    "exclude": ["backup.exe"]
  },
  "udp_idle_seconds": 60
}
```

| Setting | Meaning |
|---|---|
| `auto_weights` | With no `weights` set: each line's share of new flows follows its measured speed. duolink tracks the best speed each line has shown (holding it while the line is idle) and gives the fastest line 100 and the others their fraction (at least 5). All lines are equal until each has carried some traffic. On by default. |
| `weights` | Fixed share of new flows per line, e.g. `{"eth": 2, "wifi": 1}`; setting any turns `auto_weights` off. Lines not listed get 0 (unused). Empty with `auto_weights` off = 1 for every line. |
| `sticky` | A destination IP that got a flow in the last `minutes` keeps using the same line, so sites and game servers see one public IP. |
| `health` | Every `interval_seconds`, duolink TCP-connects to all `targets` at once through each line (no data is sent); one answer within `timeout_seconds` is enough. A line goes DOWN after `failures_before_down` failed checks in a row (a busy line can miss one), or at once when its adapter disconnects. A DOWN line gets no new flows and is re-checked every `down_interval_seconds`. |
| `processes` | Per-app rules, by exe file name (case doesn't matter). See below. |
| `udp_idle_seconds` | A UDP flow ends after this long with no packets. |

### Per-app rules

duolink finds which program opened each connection and applies its rule:

- **`sticky_exempt`**: always balanced per connection, ignoring stickiness.
  For apps that download over many connections to the same few servers.
  Default: `["steam.exe"]`.
- **`pin`**: always use one line, e.g. `{"game.exe": "eth"}` for a game that
  should stay on the steadier line. If that line is down, its connections are
  balanced over the others until it's back.
- **`exclude`**: don't balance this app. It always uses the line Windows
  itself would pick (the one with the lowest interface metric, shown by
  `duolink list`), or another line while that one is down. Its traffic still
  passes through duolink; it's just never spread.

An app can be in only one list. Programs not listed are balanced normally
with stickiness. The live stats show how many connections each rule applied
to. Setting a list to `[]` removes the default (e.g. to pin Steam, set
`"sticky_exempt": []`).

In the tray app, **Settings → Per-app rules** shows the rules as a list:
pick a program with **Browse…** (or type its name), choose "Balance every
connection", "Never balance" or "Pin to" one of your lines, and click
**Add / update**. Select a rule to change or remove it.

### Adapters coming and going

duolink follows your adapters while it runs. Plug in a new one (say a new
Wi-Fi stick or a tethered phone) and it becomes a new line, with its weight
from the settings (1 if you haven't set any). An adapter that comes back
with a new address is used at the new address, and an unplugged one is
dropped. Each new or changed line is checked first and only gets traffic
once it works. After a line comes back from an outage, the sites that had
moved to the other line are spread over both lines again. Downloads from
"Balance every connection" apps like Steam are reconnected too, so they use
both lines again without pausing them (Settings, or
`"reconnect_after_outage": false` under `processes`, turns that off).

## Known limits

- **Ping doesn't work for internet addresses** while duolink runs. duolink
  re-creates TCP and UDP connections in user space and can't forward ICMP.
  Pinging your routers still works. Use a TCP-based test or a browser instead.
- **Two public IPs.** Different connections can come from different IPs.
  Stickiness keeps one destination on one line, but a site that spreads over
  several IPs may still see both of yours. Some banking or login pages may
  not like that.
- **IPv4 only.** IPv6 traffic bypasses duolink. `duolink run` warns if any adapter
  has a public IPv6 address and shows how to turn IPv6 off on it.
- **DNS** goes straight to whatever DNS servers Windows uses. If those are
  your routers, DNS doesn't go through duolink at all, which is fine.
- **Existing flows don't move.** If a line fails, new flows avoid it, but
  connections already on it can't be moved to another line; they time out
  and the app reconnects.

## Privacy

duolink sends nothing anywhere except the health probes (a TCP connect to the
`health.targets`, `1.1.1.1:443` and `8.8.8.8:443` by default) and, only when
you ask with `duolink list --ip`, one request to api.ipify.org per line. There
is no telemetry.

## License

MIT, see [LICENSE](LICENSE).
