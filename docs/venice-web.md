# Venice web transport (Tier C)

Status: **working live** against `venice.ai` as a guest session. Verified with the
`uncensored-venice-web` alias: streaming and non-streaming Chat Completions,
attestation minting, catalog discovery, and quota probe all ran against the live
host on 2026-11-22.

Tier: `tier_c3` (`X-ALLAIIS-Transport-Tier: tier_c3`). The gateway talks to the
web app's own inference API over HTTP with a Chrome TLS fingerprint; no browser
process is needed after the one-time capture.

## Why this transport exists

The official Venice API needs an inference key. A paid subscription on the web
account is a second, independent quota. This transport exposes the web quota
behind the same local key and OpenAI contract as every other route.

## Protocol facts (captured 2026-11-22)

| Item | Value |
| --- | --- |
| Inference | `POST https://outerface.venice.ai/api/inference/chat` |
| Attestation | `POST /api/app/user/client_attestation/mint`, `count` max 5, **tokens are single-use** |
| Catalog | `GET /api/app/models?matureFilter=true&onlySafeVenice=true` |
| Auth | web session (`__client` on `clerk.venice.ai`) → Clerk session JWT → `Authorization: Bearer` |
| Required headers | `User-Agent` (must match the captured browser), `x-venice-version`, `x-venice-middleface-version`, `x-venice-request-timestamp-ms`, `x-venice-client-attestation` |
| Response | newline-delimited JSON, `Content-Type: text/html`, frames `{"kind":"content","content":…}` and `{"kind":"reasoning","reasoning_content":…}` |
| Cloudflare | `cf_clearance` is bound to IP + User-Agent + TLS fingerprint; the gateway reproduces the fingerprint with `utls` (`HelloChrome`) and HTTP/2 |

Attestation `sub` is `anonymous` for guest cookies. A stored `__client` without
an active Clerk session degrades to the guest quota instead of failing.

## Setup

```sh
./allaiis init
./allaiis web chrome                     # dedicated profile + DevTools port
# log in at the opened venice.ai tab (optional, Pro quota), then:
./allaiis web login --label personal
./allaiis models discover --provider venice-web --match 'uncensored'
./allaiis models add --provider venice-web --upstream-model venice-uncensored-1-2 \
  --name uncensored --privacy zdr
./allaiis up
```

`web login` without `--cookie` captures from the Chrome started by `web chrome`.
To capture from another browser, paste a cookie header instead:

```sh
./allaiis web login --label personal --cookie "cf_clearance=…; __client=…"
```

The status of the pool is visible with `./allaiis web quota`; it reports the
remaining chat/image/scrape counters and the reset time.

## Operating notes

- **Keep the profile directory.** Chrome derives its cookie encryption key from
  the `--user-data-dir` path. Copying the profile elsewhere makes every cookie
  undecryptable, and the session looks signed out. `web chrome` always reuses
  `<data dir>/chrome-profile`.
- **Clearance expires.** `cf_clearance` lives for hours. On `403` the route
  returns `cloudflare_challenge`; run `web login` again.
- **Guest quota is tiny** (about 10 chats/day, one quota window per day).
  Attestation tokens are per-request, so the client mints in batches of five.
- **Usage is estimated.** The web stream carries no token counters, so the reply
  marks `"estimated": true` and sets `X-ALLAIIS-Emulated-Features: usage_estimate`.
- **Reasoning** is forwarded as `reasoning_content` when the model is a
  reasoning model or the request sets `reasoning_effort`/`reasoning.effort`.
- **No tools** yet: the web endpoint ignores an OpenAI `tools` array. Aliases
  that need tool calls should point at the official Venice API or another tier.
- **Privacy**: routes default to the operator's classification; use
  `--privacy zdr` for Venice Private models. E2EE (`e2ee-*`) models negotiate
  client-side encryption in the browser and are not covered yet.

## Failure codes

| Code | Meaning |
| --- | --- |
| `cloudflare_challenge` | stored clearance rejected; recapture |
| `web_session_expired` | Clerk session dead and the guest fallback failed |
| `context_length_exceeded` | upstream `userPromptTooLong` |
| `rate_limit_exceeded` | guest or plan quota spent; pool cools down for 10 minutes |

## Implementation

- `core/internal/webprov/venice.go` — transport, attestation pool, Clerk mint, NDJSON decoding, error typing
- `core/internal/webprov/cdp.go` — one-time cookie/User-Agent capture over DevTools
- `core/internal/webprov/catalog.go` — text-model discovery
- `core/internal/gateway/web.go` — OpenAI translation, streaming guard, estimated usage
- Offline coverage: `core/internal/webprov/venice_test.go`, `core/internal/gateway/web_test.go`
