# gps-service

A small, dependency-free Go service that reads **NMEA-0183** sentences from a USB
GPS receiver, decodes them into a live navigation fix, and prints the current UTC
time and coordinates to the terminal.

It is built to **run unattended**: it auto-detects the receiver, survives
unplug/replug, and keeps reconnecting on its own — no port configuration and no
external libraries required (only the Go standard library).

```
connected: /dev/ttyACM0 — waiting for fix (needs clear sky view)...
LOCK  UTC 2026-06-30 18:22:41  lat 41.311081  lon 69.240562  sats 9  (GPS)
```

---

## Table of contents

- [What it does](#what-it-does)
- [Where and how you can use it](#where-and-how-you-can-use-it)
- [Hardware & OS support](#hardware--os-support)
- [Quick start](#quick-start)
- [Command-line flags](#command-line-flags)
- [How it works (architecture)](#how-it-works-architecture)
- [Building for multiple platforms](#building-for-multiple-platforms)
- [Testing](#testing)
- [Troubleshooting](#troubleshooting)
- [Limitations](#limitations)

---

## What it does

1. **Finds the GPS automatically.** USB GPS units (e.g. u-blox CDC-ACM) show up as
   `/dev/ttyACM*` or `/dev/ttyUSB*`, but the exact node is not stable across reboots
   or replugs. The service probes each candidate for live NMEA data and picks the one
   that is actually a GPS.
2. **Decodes the position.** It parses the three most useful NMEA sentence types —
   `RMC` (time, date, validity, position), `GGA` (fix quality + satellites used) and
   `GSV` (satellites in view) — into a single rolling `Fix`.
3. **Reports status continuously.** While searching for a fix it shows how many
   satellites are visible; once it has a valid lock it prints UTC time and decimal
   coordinates.
4. **Self-heals.** If you unplug the receiver, the service reports the disconnect and
   keeps scanning with capped exponential backoff until a device reappears — even on a
   different device node.

## Where and how you can use it

| Use case | How it helps |
|----------|--------------|
| **Headless / IoT devices** (Raspberry Pi, industrial gateways) | Plug in a USB GPS and get position + accurate UTC time with zero configuration. |
| **Time synchronization reference** | GPS `RMC` gives UTC traceable to satellite clocks — useful as a coarse time source where NTP is unavailable. |
| **Fleet / vehicle telemetry** | Run it as a systemd service that streams the latest fix into your own pipeline via the `Handler` hooks. |
| **Field data collection / surveying** | The `-once` flag grabs a single valid fix and exits — handy in scripts. |
| **Embedding in a larger Go program** | The `internal/nmea` parser is pure (no I/O) and the `service` package exposes an event `Handler`, so you can reuse the logic and feed fixes into a database, MQTT, an HTTP endpoint, etc. |

> Because everything is the standard library and statically linkable
> (`CGO_ENABLED=0`), the resulting binary drops onto any Linux box — including
> minimal containers and embedded images — with no runtime dependencies.

## Hardware & OS support

- **Receivers:** any USB GPS that presents a serial CDC-ACM / USB-serial interface and
  emits standard NMEA-0183 at the default rate. Tested against u-blox-class modules.
- **OS:** auto-detection works on **Linux** (scans `/dev/ttyACM*`, `/dev/ttyUSB*`) and
  **macOS** (scans `/dev/cu.usbmodem*`, `/dev/cu.usbserial*`). On Windows, pin the COM
  port with `-port`. The decoder and service logic are fully cross-platform.

## Quick start

```bash
# Build
make build          # produces ./gps for your current machine

# Run with auto-detection (you may need permission to read the serial device)
sudo ./gps
# …or add yourself to the 'dialout' group once, then run without sudo:
#   sudo usermod -aG dialout "$USER"   # then log out / back in

# Pin a specific device
./gps -port /dev/ttyACM0

# Grab a single fix and exit (great for scripts / cron)
./gps -once
```

## Command-line flags

| Flag | Default | Description |
|------|---------|-------------|
| `-port` | _(auto-detect)_ | Pin a specific serial device and skip discovery probing. |
| `-once` | `false` | Exit after the first complete, valid fix. |
| `-probe-timeout` | `3s` | How long each candidate device is sniffed for NMEA data during discovery. |
| `-serve` | _(off)_ | Run as a TCP server, streaming live coordinates to clients on this address (e.g. `:9000`). |
| `-rate` | `100ms` | In `-serve` mode, how often each connected client is sent the newest fix (`100ms` = 10 Hz). |
| `-version` | `false` | Print the version and exit. |

## How it works (architecture)

The code is split into small, single-responsibility packages plus a thin
`main`. Dependencies point **inward** — `main` knows about `service`, `service` knows
about `serial` and `nmea`, and `nmea` knows about nothing (pure logic). Socket mode
adds three more: `internal/live` (background tracker), `internal/server` (TCP
stream), and the public `gpsproto` (wire format) and `client` (consumer) packages.

```
 main.go
   │  builds Config + Handler, wires signals
   ▼
 internal/service   ── supervises lifecycle: discover → stream → reconnect (backoff)
   │            │
   │            └──► internal/serial   ── finds & opens the device node, probes for NMEA
   ▼
 internal/nmea      ── stateful, I/O-free decoder: sentence → Fix
```

### `internal/nmea` — the decoder (pure, testable)

- A `Parser` keeps a rolling `Fix` and a `Feed(line)` method. Each raw line is
  trimmed, checked for a `$` prefix, and **validated with the NMEA XOR checksum**
  before anything is parsed — corrupt or partial lines are silently ignored.
- It recognizes `RMC`, `GGA` and `GSV` by suffix, so it works regardless of the
  talker prefix (`GP`, `GN`, `GL`, …).
- Coordinates are converted from NMEA `ddmm.mmmm` + hemisphere into **signed decimal
  degrees**; time is built from the `hhmmss` + `ddmmyy` fields into a UTC `time.Time`.
- `Fix.HasLock()` reports a usable solution only when validity **and** time **and**
  coordinates are all present.
- Because it does no I/O, it is fully unit-tested (`nmea_test.go`).

### `internal/serial` — device discovery & I/O

- `Candidates()` globs `/dev/ttyACM*` then `/dev/ttyUSB*` in priority order.
- `Discover()` opens each candidate and **probes** it: it reads with a timeout and
  accepts the first port that produces a checksum-valid NMEA sentence. This is what
  makes "just plug it in" work — the GPS is identified by its *data*, not its name.
- A `Port` simply embeds `*os.File`, so it satisfies `io.Reader` for the scanner.

### `internal/service` — the supervisor

- `Run(ctx)` is the heart: a loop that **acquires** a port (open-pinned or discover),
  **streams** sentences through the parser, and on any disconnect **reconnects** with
  capped exponential backoff (`500ms → 10s`). A healthy connection resets the backoff.
- Streaming uses a `bufio.Scanner`; a watcher goroutine closes the port on
  `ctx.Done()` to unblock a stuck `Read`, giving clean, immediate shutdown on
  `SIGINT`/`SIGTERM`.
- Callers don't poll — they react through a `Handler` of optional callbacks
  (`OnConnect`, `OnDisconnect`, `OnWaiting`, `OnSentence`). This **dependency
  inversion** is what lets you reuse the service to push fixes anywhere without
  touching its internals.

### `internal/live`, `internal/server`, `gpsproto`, `client` — socket mode

- `internal/live.Tracker` runs the supervisor in a background goroutine via the
  same `Handler` hook and stores the newest fix in an `atomic.Pointer`, so
  `Latest()` is a lock-free read that never touches the serial port.
- `internal/server.Server` accepts TCP clients and pushes the tracker's latest
  sample to each one on a ticker (`-rate`), skipping until the first real fix.
- `gpsproto` is the dependency-free wire contract (`Sample` + JSON-Lines
  encode/decode); `client.Client` dials the server, keeps `Latest()` current,
  and reconnects automatically. Both are public so other programs can import them.

### `main.go` — presentation only

`main` builds the config from flags, installs a signal-cancelled context, and supplies
a console `Handler` that renders a single rewriting status line (`\r`) for live
search/lock updates while sending lifecycle messages to the logger.

### Reusing it in your own program

```go
svc := service.New(service.DefaultConfig(), service.Handler{
    OnSentence: func(fix nmea.Fix, _ nmea.SentenceKind) {
        if fix.HasLock() {
            publish(fix.Lat, fix.Lon, fix.Time) // your sink: MQTT, HTTP, DB…
        }
    },
})
_ = svc.Run(ctx)
```

## Streaming coordinates to a media-portal PC (socket mode)

The GPS receiver lives on one machine (the **GPS box**); the **media-portal PC**
that actually needs the position sits elsewhere on the network. Instead of
sharing a serial cable, the GPS box runs as a small TCP server and the media
portal connects to it and receives a fast, steady feed of coordinates.

```
 ┌─────────────── GPS box ───────────────┐          ┌──── media-portal PC ────┐
 │  USB GPS → gps-service -serve :9000    │          │  your app + gps-service │
 │                                        │   TCP    │      /client package    │
 │  goroutine 1: read receiver (serial)   │  JSON    │                         │
 │  goroutine 2: TCP server ──────────────┼─────────►│  client.Stream(...)     │
 │  latest fix kept in a lock-free slot   │  Lines   │  → c.Latest() anytime   │
 └────────────────────────────────────────┘          └─────────────────────────┘
```

**Why it's fast.** The receiver is read in a background goroutine that publishes
each new fix into a single lock-free slot (`atomic.Pointer`). Serving a client is
just an atomic load — it never waits on the serial port — so every connected
client gets the freshest position at up to the `-rate` you choose (default 10 Hz).

### 1. Run the server on the GPS box

```bash
# auto-detect the receiver and stream on port 9000 at 10 Hz
gps-service -serve :9000

# pin a device and stream at 20 Hz instead
gps-service -serve :9000 -rate 50ms -port /dev/ttyACM0
```

### 2. The wire format

Newline-delimited JSON (**JSON Lines**): one object per line, one line per
update. Any language can consume it — just read a line and JSON-decode it. Each
line is a `gpsproto.Sample`:

```json
{"time":"2026-07-01T06:55:00Z","lat":37.951234,"lon":58.389012,"alt_m":42,
 "hdop":0.9,"sats":11,"in_view":18,"quality":1,"lock":true,
 "avg_lat":37.951230,"avg_lon":58.389015,"spread_m":1.4,"samples":128}
```

`lat`/`lon` are the instantaneous fix; `avg_lat`/`avg_lon` are the noise-averaged
best estimate the server accumulates for a stationary receiver, with `spread_m`
the 1σ scatter and `samples` the number of fixes folded in.

Test it from anywhere without writing code:

```bash
# raw stream in the terminal
nc <gps-box-ip> 9000

# pretty-printed, one fix per line
nc <gps-box-ip> 9000 | jq .
```

### 3. Consume it on the media portal (Go)

If the media portal is a Go program, import the ready-made client and run it in a
goroutine. It keeps the newest sample available for instant, non-blocking reads
and reconnects on its own if the link drops or the server restarts.

```go
import "gps-service/client"

c := client.New()
go c.Stream(ctx, "192.168.1.50:9000") // GPS box address

// ...anywhere you need the position, this never blocks:
if s, ok := c.Latest(); ok {
    render(s.Lat, s.Lon)          // or s.AvgLat, s.AvgLon for the tight estimate
}

// Prefer a push instead of polling? Set a callback before Stream:
c.OnSample = func(s gpsproto.Sample) { render(s.Lat, s.Lon) }
```

Not a Go program? Open a TCP socket to `<gps-box-ip>:9000`, read it line by line,
and JSON-decode each line — the format above is all you need.

## Building for multiple platforms

A `Makefile` cross-compiles static binaries (no CGO) into `dist/`.

```bash
make build          # current machine → ./gps
make ubuntu         # Ubuntu / Linux amd64
make linux/arm64    # e.g. Raspberry Pi 64-bit, ARM servers
make all            # every target in dist/ (linux amd64/arm64, darwin amd64/arm64, windows amd64)
make clean          # remove dist/ and ./gps
```

Output is named `dist/gps-<os>-<arch>` (Windows gets a `.exe`). The version string is
injected from `git describe` via `-ldflags`, so `./gps -version` reports the build.

Add or remove targets by editing the `PLATFORMS` list in the `Makefile`.

## Testing

```bash
make test     # go test ./...
make vet      # go vet ./...
```

The `nmea` package ships with a test suite covering checksum validation, coordinate
conversion across all four hemispheres, RMC/GGA decoding, and lock detection.

## Getting more satellites (multi-GNSS)

The number of satellites you see depends on the **antenna's view of the sky, warm-up
time, and which constellations the receiver is configured to track** — *not* on the
computer's CPU. A faster PC does not find more satellites.

The biggest single improvement is enabling **multi-GNSS**. Many u-blox units ship in
**GPS-only** mode. Turning on GLONASS (and Galileo/BeiDou where supported) can roughly
double the satellites in view — especially valuable in regions with strong GLONASS
coverage such as Central Asia.

To check what your receiver currently emits, look at the talker prefixes in the raw
stream:

```bash
stty -F /dev/ttyACM0 9600 raw -echo      # Linux  (macOS: stty -f /dev/cu.usbmodem*)
cat /dev/ttyACM0 | grep -oE '^\$G[A-Z]{4}' | sort | uniq -c
```

- Only `$GP…` lines → **GPS-only**.
- `$GL…` (GLONASS) / `$GA…` (Galileo) / `$GB…` (BeiDou) present → multi-GNSS already on.

To enable additional constellations, configure the chip with the vendor tool
(u-blox **u-center**: *View → Configuration → GNSS*, tick GLONASS, send, then *CFG →
Save* to persist). Once enabled, this service automatically **sums** the per-constellation
counts, so the `sats in view` figure reflects every system at once.

A good fix typically needs **4+ satellites used** and a low **HDOP** (≤ ~2 is good). The
`LOCK` line reports both: `sats <used>/<in view>` and `HDOP`.

## Troubleshooting

| Symptom | Likely cause / fix |
|---------|--------------------|
| `permission denied` opening `/dev/ttyACM0` | Run with `sudo`, or add your user to the `dialout` group: `sudo usermod -aG dialout "$USER"` (re-login). |
| `no GPS device detected — will keep scanning` | Receiver not plugged in, on a non-standard node (use `-port`), or claimed by another program (e.g. `gpsd`). Stop competing services. |
| Connects but never locks | The antenna needs a **clear view of the sky**; a cold start can take 30–60 s+. The `SEARCH` line shows satellites in view. |
| Wrong/blank coordinates indoors | Normal — no fix without sky view. |

## Limitations

- **Auto-discovery covers Linux and macOS** (device-node globs). On Windows use `-port`.
- The serial port is opened with defaults; for the rare receiver that needs a specific
  baud or raw termios mode, pin it with `-port` and pre-configure with `stty`.
- The decoder targets `RMC`/`GGA`/`GSV`. Other sentence types are validated but ignored.
- Two-digit NMEA years are interpreted as `2000+yy` (correct for modern receivers).
