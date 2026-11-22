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

## Model capabilities

The web catalog publishes per-model limits and traits. Discovery keeps them, and
an alias created from the panel stores them:

| Field | Effect |
|---|---|
| `max_context` | prompt estimate is checked before a request; oversized prompts get `context_length_exceeded` without spending quota |
| `max_response` | `max_tokens` / `max_output_tokens` is clamped down to it |
| `max_images` | attachment count is checked before upload (`too_many_attachments`) |
| `reasoning` | forces thinking on or off instead of guessing from the model name |
| `temperature`, `top_p` | used only when the client did not send its own |
| `max_tools` | tool count is checked before the envelope is built (`too_many_tools`) |
| `web_search` | sets the provider's web tool for every turn |
| `large_context` | asks for the extended-context mode when the model is eligible (`enableLargeContextChat`) |
| `strict_order` | merges consecutive same-role turns for models that require strict order |
| `inactivity_ms` | aborts a stream that stays silent that long; clamped to 1 s – 10 min, default 2 min |
| `no_sampling` | models that ignore sampling get neither `temperature` nor `top_p` |
| `frequency_penalty`, `presence_penalty` | forwarded only when the model supports them |
| `replacement` | an upstream ID returned with `model_not_found` as a hint |
| `max_videos` | video attachment cap; videos also count separately from images |
| `min_p` | `min_p` is forwarded only to models that support it |
| `anon_user_id` | the anonymous user id is sent by default and suppressed only when the model says so; a signed-in id is always sent |
| `apiRateLimitSize` | shown as a badge, informational only |
| `reasoning_round_trip` | keeps the thinking of the last reply (2 h, 512 entries, in memory) and replays it on the assistant turn of a follow-up, for models that require it |

The CLI sets the same fields with `--reasoning true|false`, `--max-context`,
`--max-response`, `--max-images`, `--max-videos`, `--max-tools`, `--web-search`, `--large-context`,
`--strict-order` and `--inactivity-ms`; the rest are set from discovery. The catalog also tags `pro_only` and
`uses_credits`, which the panel shows so a paid model is not picked by accident.

### Thinking depth

`reasoning_effort` (Chat) and `thinking.budget_tokens` (Messages) are passed to
the web payload as `reasoningEffort`; a budget under 2048 becomes `low`, under
8192 `medium`, under 32000 `high`, and larger `max`. The provider advertises the
options it accepts.

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
`stop_reason: tool_use`. Thinking is surfaced as a `thinking` block, as
`reasoning_content` on Chat, and as a `reasoning` output item on Responses.

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

`allaiis export opencode` prints a ready provider block. Merge its `allaiis`
entry into `providers`, then point `settings.baseURL` at the loopback listener
and supply the key through the environment:

```jsonc
{
  "providers": {
    "allaiis": {
      "name": "ALLAIIS Venice",
      "env": ["ALLAIIS_API_KEY"],
      "package": "@opencode/ai/providers/openai-compatible",
      "settings": {
        "baseURL": "http://127.0.0.1:8420/v1",
        "apiKey": "{env:ALLAIIS_API_KEY}"
      },
      "models": {
        "venice-deepseek": {
          "name": "DeepSeek V4.1 Flash Uncensored (Venice web)",
          "limit": { "context": 524288, "input": 480000, "output": 131072 },
          "capabilities": {
            "tools": true,
            "input": ["text", "image"],
            "output": ["text"],
            "temperature": true
          },
          "compatibility": {
            "reasoningField": "reasoning_content",
            "maxTokensField": "max_tokens"
          }
        }
      }
    }
  }
}
```

```sh
export ALLAIIS_API_KEY="$(./allaiis key)"
opencode run --standalone --model allaiis/venice-deepseek "…"
```

The alias decides whether the request goes to a key API or to the web
transport. Keep the real model limits and capabilities in the OpenCode entry:
without them OpenCode assumes a 200k window and may truncate a long turn.

What the gateway guarantees to an OpenAI-compatible client:

- a `tool_calls` delta carries a **global** `index`, so calls that arrive in
  separate SSE batches are not merged by the client;
- `stream_options.include_usage` produces one final chunk with estimated
  `usage`, and non-stream replies always carry usage;
- the native DeepSeek DSML syntax, the sentinel envelope and Claude-style
  `<invoke>` XML all become normal `tool_calls`;
- a tool block cut off at the end of a stream is repaired instead of being
  silently dropped.

Measured on a free model, 2026-11-22: five of five streams returned 200 and
`[DONE]`, three of them in parallel; time to first content ran 2.5–7.4 s
sequentially and 2.5–14.4 s under three-way load; content then arrived at
2.0–2.4 k characters per second. The lead-in dominates perceived speed.

An omitted `max_tokens` becomes 8192 (clamped by the model's `max_response`):
Venice's own default cuts a long answer mid-word. When the estimate reaches the
cap the reply is reported as `finish_reason: length` (`stop_reason: max_tokens`
on Messages, `status: incomplete` on Responses) so a client can continue.

A call that repeats one of the last eight calls with the same arguments is
dropped, so a model echoing its own block does not run a side effect twice.

Usage is estimated from characters, not tokenized, and flagged
`"estimated": true`; `X-ALLAIIS-Emulated-Features: tools,usage_estimate`
declares both on every tool request. Messages aliases are exported under a
separate `allaiis-anthropic` provider.

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
- `core/internal/gateway/tools.go` — tool envelope, sentinel/XML/DSML guard buffer, repair message, truncated-block repair
- `core/internal/gateway/shape_chat.go` — global tool indexes and the optional final usage chunk
- Offline coverage: `core/internal/webprov/venice_test.go`, `core/internal/gateway/web_test.go`, `core/internal/gateway/web_tools_test.go`, `core/internal/gateway/tools_test.go`
