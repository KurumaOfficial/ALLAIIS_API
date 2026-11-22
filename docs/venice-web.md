# Venice web transport (Tier C)

Status: **working live** against `venice.ai`, on a guest session and on a Pro
session. Verified 2026-11-22: streaming and non-streaming Chat Completions,
attestation minting, catalog discovery, quota probe, a `read_file` tool call,
Claude Opus 5.5, an `e2ee-*` model, image input, and a text-file attachment. Guest requests need no login; Pro models
need a captured signed-in session.

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
remaining chat/image/scrape counters, the reset time, and whether the session is
signed in or a guest. `allaiis up` prints every alias it serves.

### Per-OS notes

- **Windows.** `web chrome` finds Chrome under `Program Files`. Chrome 136 and
  later only open a DevTools port for a non-default profile, which is exactly why
  the capture profile exists.
- **Linux.** Install Chrome or Chromium; `web chrome` also probes `google-chrome`
  on `PATH`. Run headless hosts behind Xvfb, or log in on a desktop and copy the
  data directory as a whole (not its files individually).
- **macOS.** `web chrome` uses
  `/Applications/Google Chrome.app/Contents/MacOS/Google Chrome`. Chrome may ask
  for Keychain access the first time; allow it so cookies stay decryptable.

## Operating notes

- **Keep the profile directory.** Chrome derives its cookie encryption key from
  the `--user-data-dir` path. Copying the profile elsewhere makes every cookie
  undecryptable, and the session looks signed out. `web chrome` always reuses
  `<data dir>/chrome-profile`.
- **Clearance expires.** `cf_clearance` lives for hours. On `403` the route
  returns `cloudflare_challenge`; run `web login` again.
- **Guest quota is tiny** (about 10 chats/day, one quota window per day). A spent
  plan answers `402 insufficientBalance`; `allaiis web quota` and the panel's
  Quota button show the reset time without spending a message.
  Attestation tokens are per-request, so the client mints in batches of five.
- **Usage is estimated.** The web stream carries no token counters, so the reply
  marks `"estimated": true` and sets `X-ALLAIIS-Emulated-Features: usage_estimate`.
- **Reasoning** is forwarded as `reasoning_content` when the model is a
  reasoning model or the request sets `reasoning_effort`/`reasoning.effort`.
- **Debugging.** Set `ALLAIIS_WEB_DEBUG=1` to log each upstream event and to
  drop the last multipart body and SSE frames into the OS temp directory.
- **Transient failures retry once.** A transport error, a `5xx`, a spent
  attestation, or a stream that dies before its first event triggers one retry
  with a fresh attestation. A stream that already delivered text is not retried.
- **Empty replies are retried.** A 200 with no content (or only the `processing`
  status) gets one more attempt, and an empty first event no longer swallows the
  text after it.
- **Tool calls** are emulated: the gateway injects a call envelope, hides the
  call block from the client, validates JSON, retries once on a malformed block,
  and returns native `tool_calls` with `finish_reason: tool_calls`. Two call
  dialects are accepted — the sentinel JSON block the envelope asks for, and the
  Claude-style `<invoke name="…"><parameter name="…">…</parameter></invoke>`
  form that Claude models often prefer. Both are normalized, and a model that
  emits the same call twice in both forms yields one call. Declared via
  `X-ALLAIIS-Emulated-Features: tools`.
- **Cooldowns are per credential.** A `429` on one captured session cools only
  that label, so a limited guest does not block a Pro alias of the same provider.
  An unpinned alias sits in a shared pool and cools the whole provider. The panel
  can lift a cooldown with **Resume** once the plan resets.
- **Pin aliases to a label.** A pool with several captured sessions picks one by
  request hash. Pass `--label` to `models add` so a route always uses the
  intended account.
- **Attachments work.** An `image_url` part (a `data:` URL or an http(s) image
  the gateway downloads, up to 8 MiB), or a `file` part
  with `file_data`, is re-encoded as a `multipart/form-data` turn to
  `/api/inference/chat/agents/venice`: the JSON lives in the `request` part and
  each attachment in a `media.<id>.<n>` part, so a message carrying attachments
  needs an `id` equal to the media prefix. Non-image MIME types become
  `input_file` with their filename; text-only turns stay on the cheaper JSON
  endpoint.
- **Agent endpoint differences.** Image turns run on the agent endpoint, which
  answers with SSE (`response.output_text.delta`) instead of NDJSON, so both
  decoders coexist.
- **Privacy**: routes default to the operator's classification; use
  `--privacy zdr` for Venice Private models. `e2ee-*` ids work when signed in and
  are served over the same plain transport; per-field client-side encryption is
  not implemented.

## Inbound protocols

A web route can be registered for Chat Completions or Anthropic Messages:

```sh
./allaiis models add --provider venice-web --upstream-model claude-opus-5-5 \
  --name cc --label pro --protocol messages
```

Messages routes translate `system`, `content[]` blocks (text, image, tool_use,
tool_result) into web turns and answer with real Anthropic events
(`message_start`, `content_block_delta`, `message_delta`, `message_stop`) or a
`message` object. Tool calls come back as `tool_use` blocks with
`stop_reason: tool_use`.

`/v1/responses` is wired too: `instructions`, typed `input` items
(`message`, `function_call`, `function_call_output`) and flat function tools are
translated, and the reply uses `response.created`, `response.output_text.delta`,
`response.function_call_arguments.delta` and `response.completed`, or a
`response` object.

The transport is stateless, so the gateway stores turn lists and replays one
when a request sends only `previous_response_id`. Stored turns sit in the vault
SQLite file, AES-GCM sealed like credentials, expire after an hour and are capped
at 128 entries, so a follow-up survives a gateway restart without exposing
prompts on disk.

## Client setup (Claude Code)

```sh
export ANTHROPIC_BASE_URL=http://127.0.0.1:8420
export ANTHROPIC_API_KEY="$(./allaiis key)"
# register a messages alias first, then pick it in the client
```

Claude Code sends `x-api-key`, an `anthropic-version` header and Messages bodies;
all three are accepted. Tool calls come back as `tool_use`, so the agent loop
works without any client-side shim.

## Client setup (OpenCode)

```sh
./allaiis export opencode > opencode.json
export ALLAIIS_API_KEY="$(./allaiis key)"
# pick an alias from `allaiis up` output, e.g. allaiis/opus-venice-web
```

OpenCode then talks to the loopback base URL with the local key, and the alias
decides whether the request goes to a key API or to the web transport. Messages
aliases are exported under a separate `allaiis-anthropic` provider. Web
aliases also carry tool calls, so an agent loop works; usage stays estimated.

### Failure codes

| Code | Meaning |
| --- | --- |
| `cloudflare_challenge` | stored clearance rejected; recapture |
| `web_session_expired` | Clerk session dead and the guest fallback failed |
| `sign_in_required` | the model needs a signed-in session (guest cookie) |
| `context_length_exceeded` | upstream `userPromptTooLong` |
| `rate_limit_exceeded` | the pool hit a `429`; that credential cools until the reset header says |
| `insufficient_balance` | the plan allowance is spent (`402 insufficientBalance`); no cooldown is set, check `web quota` for the reset |

## Implementation

- `core/internal/webprov/venice.go` — transport, attestation pool, Clerk mint, NDJSON decoding, error typing
- `core/internal/webprov/cdp.go` — one-time cookie/User-Agent capture over DevTools
- `core/internal/webprov/catalog.go` — text-model discovery
- `core/internal/gateway/web.go` — OpenAI translation, streaming guard, estimated usage
- `core/internal/gateway/tools.go` — tool envelope, sentinel guard buffer, repair message
- Offline coverage: `core/internal/webprov/venice_test.go`, `core/internal/gateway/web_test.go`, `core/internal/gateway/web_tools_test.go`, `core/internal/gateway/tools_test.go`
