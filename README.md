# Spotvee

Spotvee syncs the colors of your smart lights to the album art of whatever
you are currently playing on Spotify, in real time. It watches Spotify
playback, extracts a vivid dominant color (plus a palette) from the artwork,
and pushes that color to the selected Govee lights. A React Native (Expo) app
controls the whole thing over HTTP + WebSocket.

Built on top of the [Govee](https://github.com/Sh4wnK/Govee) Go wrapper, which
provides the device API (`Devices`, `SetPower`, `SetBrightness`,
`SetColorRGB`) that Spotvee's engine consumes.

```
Spotify Web API ──► playback watcher ──► album-art fetch + palette extract
                                            │
                                            ▼
                                    dominant color
                                            │
              React Native app ◄── HTTP/WS ─┤
                                            ▼
                                  sync engine (Govee devices)
```

## Features

- **Spotify OAuth2 Authorization Code + PKCE** (`user-read-currently-playing`,
  `user-read-playback-state`) with encrypted-at-rest token persistence and
  automatic refresh on 401/expiry.
- **Adaptive playback polling**: the watcher polls at a base interval,
  accelerates near a track's end (so the next track's color lands within
  ~0.5 s), backs off on 429 with `Retry-After` honored, and treats 204 as
  "nothing playing".
- **Pure-Go color extraction**: artwork is downloaded (bounded LRU byte
  cache), downscaled, quantized with median cut, and a vivid dominant color
  is picked by saturation/luminance scoring. Optional palette cycling.
- **Resilient sync engine**: concurrent fan-out to selected devices, dedupe
  of identical colors, per-device circuit breakers with backoff so one dead
  light can never crash or stall the loop, and end-to-end latency percentile
  instrumentation (p50/p95/p99).
- **HTTP + WebSocket API** for the mobile app: auth status / login URL,
  device listing and selection, sync enable/disable, live state pushes
  (track, extracted color, palette), health endpoint, latency metrics
  (JSON and Prometheus text). Optional bearer-token auth
  (`SPOTVEE_APP_TOKEN`).
- **Built for continuous runtime**: panic recovery in goroutines, graceful
  shutdown, bounded caches, structured logging via `log/slog`, and a
  reproducible stress harness (`cmd/soak`) that runs the full engine against
  fake Spotify + fake Govee servers.

## Quickstart

### 1. Prerequisites

- Go 1.23+ (tested with 1.23.3)
- A Spotify app: https://developer.spotify.com/dashboard
  - Redirect URI registered there must match `SPOTIFY_REDIRECT_URI`.
  - PKCE only needs the client ID — no client secret required.
- A Govee developer API key: https://developer.govee.com
- (Optional) Node 20+ to run the mobile app.

### 2. Run the server

```bash
cp .env.example .env            # fill in SPOTIFY_CLIENT_ID, GOVEE_API_KEY, SPOTVEE_APP_TOKEN
go run ./cmd/spotvee
```

On first run, open the login URL the app provides (or
`GET http://localhost:8080/api/auth/login-url`) and authorize Spotify. The
token is encrypted with AES-256-GCM using a key stored in
`.spotvee-token-key` (0600) and persisted to `spotvee-state.json` (0600).

### 3. Launch the mobile app

```bash
cd mobile
npm install
npx expo start
```

On the Connection screen enter your server URL and `SPOTVEE_APP_TOKEN`,
connect Spotify, pick your Govee devices, and enable color sync.

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `SPOTIFY_CLIENT_ID` | — | Spotify app client ID (required) |
| `SPOTIFY_CLIENT_SECRET` | — | Optional with PKCE; used if set |
| `SPOTIFY_REDIRECT_URI` | `http://localhost:8080/api/auth/callback` | Must match the Spotify dashboard |
| `GOVEE_API_KEY` | — | Govee developer API key (required) |
| `SPOTVEE_APP_TOKEN` | — | Bearer token for the API (empty = unauthenticated) |
| `SPOTVEE_LISTEN_ADDR` | `:8080` | HTTP listen address |
| `SPOTVEE_TOKEN_FILE` | `spotvee-state.json` | Encrypted token location |
| `SPOTVEE_TOKEN_KEY_FILE` | `.spotvee-token-key` | Token encryption key (created once, 0600) |
| `SPOTVEE_POLL_INTERVAL` | `3s` | Base playback poll interval |
| `SPOTVEE_MIN_POLL` | `500ms` | Fast-poll interval near track end |
| `SPOTVEE_MAX_POLL` | `5s` | Poll interval ceiling |
| `SPOTVEE_FAST_POLL_LEAD` | `10s` | Remaining-track-time window for fast polling |
| `SPOTVEE_PALETTE_SIZE` | `4` | Palette entries (median cut) |
| `SPOTVEE_SAT_FLOOR` | `0.18` | Min saturation for vivid candidates |
| `SPOTVEE_LUM_HIGH`/`LUM_LOW` | `0.86`/`0.12` | Luminance window for vivid candidates |
| `SPOTVEE_VIVID_WEIGHT` | `0.7` | Blend between presence and luminance anchor |
| `SPOTVEE_PALETTE_CYCLE` | `false` | Cycle lights through the palette |
| `SPOTVEE_IMAGE_CACHE_ENTRIES` | `256` | Artwork cache entry cap |
| `SPOTVEE_IMAGE_CACHE_BYTES` | `64 MiB` | Artwork cache byte cap |
| `SPOTVEE_MAX_ART_BYTES` | `8 MiB` | Per-image download cap |
| `SPOTVEE_METRICS` | `true` | Serve `/metrics` (Prometheus text) |
| `SPOTVEE_LOG_LEVEL` | `info` | `debug` \| `info` \| `warn` \| `error` |

## HTTP API

All `/api/*` routes except the OAuth callback require
`Authorization: Bearer <SPOTVEE_APP_TOKEN>` when that token is set.

| Route | Method | Purpose |
| --- | --- | --- |
| `/healthz` | GET | Liveness |
| `/api/auth/status` | GET | `{"authenticated": bool}` |
| `/api/auth/login-url` | GET | Spotify authorization URL for the current device |
| `/api/auth/callback` | GET | OAuth callback (browser hits this) |
| `/api/devices` | GET | Discovered Govee devices + current selection |
| `/api/select` | POST | `{"ids": [...]}` set selected devices |
| `/api/sync` | POST | `{"enabled": bool, "cycle": bool}` toggle sync |
| `/api/state` | GET | Current track, extracted color + palette |
| `/api/ws` | WS | Live state pushes on track change / sync toggle |
| `/api/metrics` | GET | Latency p50/p95/p99 (ms) |
| `/metrics` | GET | Same percentiles in Prometheus text format |

## Latency

End-to-end apply latency is measured from track-change detection to all
selected device commands being acknowledged, and is exposed at
`/api/metrics`. The `cmd/soak` harness reports these numbers reproducibly:

```
$ go run ./cmd/soak -duration=2m -devices=4 -track-every=2s -cmd-latency=50ms
soak complete
  runtime:        2m0.002s
  panics:         0
  goroutines:     7
  heap:           1.59 MiB in use (22.23 MiB total allocated)
  track changes:  60
  device cmds:    240 (2.0/s)
  latency ms:     p50 52.0 p95 52.0 p99 53.0 max 53.0 (n=60)
  breakers open:  0/4
  injected failures: 2 (all isolated)
```

Measured 2026-10-26 on an Apple M3 (macOS) with an in-process fake Spotify
and fake Govee devices, each command simulating 50 ms latency. The soak runs
the real watcher, coordinator and engine against accelerated track changes,
injects periodic device failures to exercise the circuit breakers, and
reports panic count, goroutine growth and heap so long-run stability is
verifiable (`-duration` accepts any length; 2 minutes was used above, not a
24 h run).

## Project layout

```
cmd/spotvee/        Server entrypoint (wiring + graceful shutdown)
cmd/soak/           Stress harness (fake Spotify + fake Govee servers)
internal/
  api/              HTTP + WebSocket handlers, bearer auth
  art/              Bounded artwork fetcher + LRU cache
  auth/             Spotify OAuth2 PKCE flow + token persistence
  config/           Environment configuration
  devices/          Controller interface, circuit breaker, device source
  engine/           Color fan-out engine: dedupe, isolation, latency hist
  govee/            Adapter from the shared Govee wrapper to devices.Controller
  metrics/          Percentile histogram + Prometheus text
  palette/          Median-cut quantization + vivid dominant picker
  spotify/          Spotify Web API client (PKCE, refresh, 204/429 handling)
  store/            Encrypted token store + LRU cache
  sync/             Coordinator: watcher -> art -> palette -> engine -> WS
  watcher/          Adaptive playback poller
mobile/             React Native (Expo, TypeScript) app
```

## Testing

```bash
go vet ./...
go build ./...
go test -race ./...

cd mobile && npx tsc --noEmit
```

Tests cover: palette extraction on generated solid/two-tone images,
watcher behavior against a fake Spotify (track change, 204, 429, transient
error, stop), engine behavior with a fake light controller (dedupe, error
isolation, breaker tripping, latency recording, empty selection), the full
HTTP API + WebSocket + auth, the PKCE exchange + persistence, and the token
store round-trip with 0600 permissions.

## License

MIT — see [LICENSE](LICENSE). Copyright (c) 2026 Gurshawn Kaloty.