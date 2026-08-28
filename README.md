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

The firmware's ESP-IDF HTTP server times out an idle session after five seconds,
allows only seven sockets, and has LRU eviction disabled. Testing this panel
also found that a small command heartbeat does not keep the large GIF-upload
path healthy: 12-second frame updates wedged it while one-second frame updates
survived 689 consecutive pushes. The beacon scene therefore renders at 1 Hz by
default. `reboot_after_pushes` remains available as a disabled legacy safety
valve.

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
