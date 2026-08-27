# pixoo

A small daemon that drives a Divoom Pixoo 64 over the LAN without the Divoom app.
It renders 64x64 frames itself (clock, beacon chain, text, images), pushes them
through one carefully paced HTTP connection, and serves a web UI to control it.

## Why one connection

The panel runs a stock ESP-IDF HTTP server: one request at a time and a hard
cap of seven open sockets. Clients that open a connection per request leave
idle keep-alive sockets behind, and once seven pile up the panel stops answering
until it is power-cycled. This daemon keeps a single keep-alive connection,
serialises every call, rate-limits frames, skips unchanged frames, and drops the
connection on any transport error so the panel gets its socket back.

The firmware also leaks heap on every pushed frame and goes deaf (power cycle
only) when it runs out — about 780 pushes on a 2025 firmware. Every push is
budgeted: scenes re-render only when their content changes, and the daemon
reboots the panel (`Device/SysReboot`, ~30 s of boot logo) every
`reboot_after_pushes` frames before the heap runs dry.

## Run

```
cp config.example.yaml config.yaml
go run ./cmd/pixoo --config config.yaml
```

Open http://localhost:6464.

## API

| Method | Path | Body |
|---|---|---|
| GET | `/api/status` | |
| GET | `/api/scenes` | |
| GET | `/api/preview.png?scale=6` | |
| POST | `/api/show` | `{"scene":"clock","seconds":300}` (0 = until resume) |
| POST | `/api/resume` | |
| POST | `/api/brightness` | `{"value":50}` |
| POST | `/api/screen` | `{"on":false}` |
| POST | `/api/text` | `{"text":"hi","color":"#fff","font":"small\|tiny\|big","scroll":false,"seconds":60}` |
| POST | `/api/image` | multipart `file` (PNG/JPEG/GIF, animated GIFs loop on the panel) + `seconds` |
| POST | `/api/command` | `{"command":"Channel/GetIndex","args":{}}` raw passthrough |

## Docker

```
docker run -p 6464:6464 -v $PWD/config.yaml:/config.yaml:ro ghcr.io/samcm/pixoo:latest
```
