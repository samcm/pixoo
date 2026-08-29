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
| GET | `/api/stream` | buffered stream status |
| POST | `/api/stream/frame` | multipart still-image `file`, stable `source`, and optional `seconds` |
| POST | `/api/stream/flush` | `{"source":"renderer","seconds":0}` closes the partial clip now |
| DELETE | `/api/stream?source=renderer` | discard the stream, release its lease, and resume rotation |
| POST | `/api/command` | `{"command":"Channel/GetIndex","args":{}}` raw passthrough |

## Buffered streams

Programs which render individual frames should use `/api/stream/frame` instead
of repeatedly replacing `/api/image`. The daemon samples submissions at the
configured frame delay, assembles a bounded animation and gives every completed
clip one picture ID. The panel loops the current clip locally while the next is
built. There is no unbounded queue: if the producer outruns the panel, the newest
complete clip replaces the older pending clip.

`source` is a producer-chosen stable name. It leases the stream so frames from
two applications cannot be interleaved. It may also be supplied in the
`X-Pixoo-Source` header. Turning the screen off clears the buffered and active
stream state so partial clips never carry across a blackout.

```sh
curl -F source=my-renderer -F file=@frame.png http://localhost:6464/api/stream/frame
curl -H 'content-type: application/json' \
  -d '{"source":"my-renderer","seconds":0}' \
  http://localhost:6464/api/stream/flush
```

## Docker

```
docker run -p 6464:6464 -v $PWD/config.yaml:/config.yaml:ro ghcr.io/samcm/pixoo:latest
```
