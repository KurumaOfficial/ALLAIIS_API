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
remaining chat/image/scrape counters, the reset time, whether the session is
signed in or a guest, and, for a signed-in session, the plan and the credit
balance the paid models draw on. The counters are asked for with the session
token: without it the endpoint answers the anonymous bucket, which made a
signed-in label look empty. `allaiis up` prints every alias it serves.

### Capacity planning

The web quota counts **completions**, not tokens. A 100-token answer and a
128K-token context each cost one chat. A tool round costs another chat because
the client sends another assistant turn. The reset is a fixed daily timestamp,
not a rolling window.

At 10,000 chats/day and the 128K context of the built-in alias, the absolute
ceiling is about 1.28B input tokens per day, or **38B tokens a month**. Actual
traffic is lower because most requests are smaller. One trillion tokens a month
would require 3.3M tokens per request (far above the context window) or roughly
260,000 requests a day (26 Pro labels). The panel's Limits tab shows the
remaining count, the reset countdown and a safe pace for the hours left.

The cheapest token strategy is therefore one large, well-supplied request
instead of many small ones. Do not truncate code or history to save quota:
truncation does not reduce the request count and costs answer quality. Split a
task only when the context would exceed the model window. Independent tool
calls are requested in one batch, which keeps e.g. two file reads to one chat
instead of two. Every response with tool history carries `X-ALLAIIS-Tool-Calls`;
a history of 40 calls adds `X-ALLAIIS-Tool-Warning` and a WARN log line, but the
request still runs.

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
- **Terms of service.** Venice answers an unattested programmatic call with
  `403` and a warning that continued use can suspend the account. This transport
  mints the app's own client attestation, so a request looks like the web app's,
  which is what that warning is aimed at. Running it on a real account is the
  operator's risk: prefer free models and a session you can afford to lose.
- **Guest quota is tiny** (about 10 chats/day, one quota window per day).
  `402 insufficientBalance` means the credit bucket for paid models is at zero;
  it is not the same counter as the chat allowance, and a plan can be active
  with thousands of chats left and no credits for a paid model. `allaiis web
  quota` and the panel's Quota button show both counters and the reset without
  spending a message; `allaiis doctor` prints the plan line too. The model is
  parked on that label for 30 minutes, while a free model on the same session
  keeps answering from the chat allowance.
  Attestation tokens are per-request, so the client mints in batches of five.
- **E2EE models.** An id starting `e2ee-` is sealed to the attested enclave when
  the route sets `e2ee`; without it the prompt goes out in plaintext, and doctor
  warns while an alias points at one.
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
  An unpinned alias sits in a shared pool and cools the whole provider. A published
  reset longer than ten minutes is capped at ten, because a burst window clears
  long before the quota reset Venice reports. The panel can lift a cooldown with
  **Resume** once the plan resets.
- **Pin aliases to a label.** A pool with several captured sessions picks one by
  request hash. Pass `--label` to `models add` so a route always uses the
  intended account.
- **Attachments work.** An `image_url` part (a `data:` URL or an http(s) image
  the gateway downloads, up to 8 MiB), an Anthropic base64 `image` block, or a
  `file` part with `file_data`, is re-encoded as a `multipart/form-data` turn to
  `/api/inference/chat/agents/venice`: the JSON lives in the `request` part and
  each attachment in a `media.<id>.<n>` part, so a message carrying attachments
  needs an `id` equal to the media prefix. Non-image MIME types become
  `input_file` with their filename; text-only turns stay on the cheaper JSON
  endpoint. An attachment that produces no bytes — an unreachable URL, bad
  base64, a `file_id`, an Anthropic `document` block — is refused with
  `invalid_request` naming it, rather than dropped while the model answers as if
  it had been read. An image in a system or developer turn is refused for the
  same reason: the web endpoint pairs images with user content only.
- **Agent endpoint differences.** Image turns run on the agent endpoint, which
  answers with SSE (`response.output_text.delta`) instead of NDJSON, so both
  decoders coexist.
- **Privacy**: routes default to the operator's classification; use
  `--privacy zdr` for Venice Private models. An `e2ee-*` id works when signed in;
  it is sealed to the enclave once the route carries `e2ee`, and goes out in
  plaintext without it.
- **Proxies**: a pool saved in Controls is used for provider requests and
  catalog discovery, web chats included. HTTP, HTTPS and SOCKS5 proxies are tunnelled before the
  Chrome TLS fingerprint is applied and rotated per connection; the capture
  browser keeps its own network.

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
| `inactivity_ms` | aborts a stream that stays silent that long; clamped to 1 s – 10 min, default 2 min (5 min on a reasoning route) |
| `no_sampling` | models that ignore sampling get neither `temperature` nor `top_p` |
| `frequency_penalty`, `presence_penalty` | forwarded only when the model supports them |
| `replacement` | an upstream ID returned with `model_not_found` as a hint |
| `max_videos` | video attachment cap; videos also count separately from images |
| `min_p` | `min_p` is forwarded only to models that support it |
| `anon_user_id` | the anonymous user id is sent by default and suppressed only when the model says so; a signed-in id is always sent |
| `apiRateLimitSize` | shown as a badge, informational only |
| `reasoning_round_trip` | keeps the thinking of the last reply (2 h, 512 entries, in memory) and replays it on the assistant turn of a follow-up, for models that require it; the key ignores surrounding whitespace, keeps the tool arguments compact and matches both the call block and the bare prose, so a client may rebuild a turn either way |

The CLI sets the same fields with `--reasoning true|false`, `--max-context`,
`--max-response`, `--max-images`, `--max-videos`, `--max-tools`, `--web-search`, `--large-context`,
`--strict-order`, `--inactivity-ms` and `--e2ee`; the rest are set from discovery. The catalog also tags `pro_only` and
`uses_credits`, which the panel shows so a paid model is not picked by accident.

The request body itself is capped globally by `max_request_mb` (default 8, up to
128); inline data URLs count at their encoded size, so an alias with `max_images`
above one usually wants 16 or more. Doctor warns while the cap is that low.

### Thinking depth

`reasoning_effort` (Chat) and `thinking.budget_tokens` (Messages) are passed to
the web payload as `reasoningEffort`; a budget under 2048 becomes `low`, under
8192 `medium`, under 32000 `high`, and larger `max`. The provider advertises the
options it accepts.

### End-to-end encryption

An `e2ee-*` model takes client-encrypted messages and answers with sealed
chunks. Set `"e2ee": true` on the route (or pass `--e2ee` to `models add`; an
`e2ee-` upstream id turns it on by itself). Per turn the transport:

1. generates a secp256k1 key pair and a 32-byte nonce;
2. asks `/tee/attestation`, and rejects the enclave unless `verified` is true,
   the nonce comes back unchanged, debug mode is off, a signing key is present,
   and the server's own TDX verdict (valid, signature, certificate chain) passes
   when it is sent;
3. derives an AES-256-GCM key with ECDH and HKDF-SHA256 (`ecdsa_encryption`);
4. seals every message into `ephemeral key ‖ nonce ‖ ciphertext` hex and sends
   the `X-Venice-TEE-Client-Pub-Key`, `X-Venice-TEE-Model-Pub-Key` and
   `X-Venice-TEE-Signing-Algo: ecdsa` headers;
5. opens every sealed chunk of the reply with the session key.

Sessions are cached per model for five minutes and dropped when a chunk fails to
open; the turn is then retried once with a fresh attestation while the client has
seen nothing. A failure is answered `e2ee_failed`.

A sealed reply carries `X-ALLAIIS-E2EE: envelope`, so a log shows which calls
were encrypted, and `/v1/models` marks an e2ee alias with `allaiis_e2ee: true`. `allaiis doctor` attests every e2ee alias (no quota spent) and warns when the
pinned label is a guest session, which the enclave takes but the call refuses, and
`allaiis web e2ee --model <e2ee model>` (add `--label` for one session) runs the
checks by hand:
it prints the enclave provider, hardware, signing key and whether the TDX verdict
was checked, then sends one sealed canary and reports the reply text and any
error, which is the shortest way to tell an accepted request from a decrypted
answer once a label has balance. Tools and attachments work through the same path: the call block travels inside
the encrypted content, and the multipart agent path seals the text the same way.

Attestation lives at `{base}/v1/tee/attestation` and answers without a key. Live,
a sealed turn on `e2ee-gpt-oss-120b-p` (a free, Pro-only E2EE model) came back
decrypted, and a tool call inside the sealed content parsed: the end-to-end path
is verified on 2026-11-23. The credit-priced `e2ee-*` models still need credits;
the free ones need a Pro session only. Keep `e2ee` off on a plain model; a failure
is loud, not silent.

## Inbound protocols

A web route can be registered for Chat Completions or Anthropic Messages:

```sh
./allaiis models add --provider venice-web --upstream-model claude-opus-5-5 \
  --name cc --label pro --protocol messages
```

Both shapes build the same turns from a history, a replayed tool result
included, so an agent loop behaves the same whichever one it speaks. Register a
free model for both if you want the fallback:

```sh
./allaiis models add --provider venice-web --upstream-model venice-uncensored-role-play \
  --name cc-free --label pro --protocol messages
./allaiis models add --provider venice-web --upstream-model venice-uncensored-role-play \
  --name gpt-free --label pro --protocol responses
```

`GET /v1/models` and `GET /v1/models/{id}` accept either header shape, so a
Claude client that sends `x-api-key` can read the catalogue and validate one
alias before a long run; with `Anthropic-Version` the list comes back in the
Anthropic shape (`type`, `display_name`, `has_more`), otherwise in the OpenAI
one. Either list carries only the routes that shape can call: Messages aliases
for the Anthropic one, Chat and Responses aliases for the OpenAI one.

`/v1/messages/count_tokens` answers with the same character-based estimate the
context guard uses and spends no message, so an Anthropic client can budget a
turn before sending it.

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
`response` object. The completed event carries the finished items — the message
text and every function call — so a client that reads only the last event still
has the answer.

The transport is stateless, so the gateway stores turn lists and replays one
when a request sends only `previous_response_id`. Stored turns sit in the vault
SQLite file, AES-GCM sealed like credentials, expire after an hour and are capped
at 128 entries, so a follow-up survives a gateway restart without exposing
prompts on disk. `store: false` keeps a reply out of that file: a later request
naming its id is refused with `previous_response_not_found` (404), since a fresh
conversation that looked continuous would be worse than a clear error. The same
code answers an expired or mistyped id.

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
        "deepseek-v4-1-flash-uncensored-venice": {
          "name": "DeepSeek V4.1 Flash Uncensored (Venice)",
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
opencode run --standalone --model allaiis/deepseek-v4-1-flash-uncensored-venice "…"
```

OpenCode runs models in **Code Mode** by default: its prompt says the tools can
only be reached from a JavaScript `execute` sandbox while it still declares them.
A text-tool transport does not need that layer, and the sandbox costs prompt
space and turns. Denying `execute` removes Code Mode and lists the tools directly:

```jsonc
{ "permissions": [ { "action": "execute", "resource": "*", "effect": "deny" } ] }
```

Measured on the same task: 11 steps with the sandbox versus 11 without, 3 sandbox
calls against 0, and 25 s against 15 s.

Long work also needs an agent with a step budget, and the prompt has to say that
a finished step is not a reason to yield:

```md
---
description: DeepSeek on the Venice web transport for long tasks
mode: primary
model: allaiis/deepseek-v4-1-flash-uncensored-venice
steps: 200
---
```

```sh
opencode run --auto --agent venice "..." < TASK.md
```

An eight-function task then ran 44 steps, 73 tool calls, zero errors, zero leaked
markers, 91 passing subtests, and finished with a summary.

A twelve-function task of the same shape ran 85 steps and 125 subtests, all
twelve delivered. Its four tool errors were client-side anchor mistakes (a read
offset past the end, an `oldString` that matched twice) and every one recovered
on the next step: a longer task costs turns, not correctness.

The alias decides whether the request goes to a key API or to the web
transport. Keep the real model limits and capabilities in the OpenCode entry:
without them OpenCode assumes a 200k window and may truncate a long turn.

What the gateway guarantees to an OpenAI-compatible client:

- a `tool_calls` delta carries a **global** `index`, so calls that arrive in
  separate SSE batches are not merged by the client;
- `stream_options.include_usage` produces one final chunk with estimated
  `usage`, and non-stream replies always carry usage;
- the native DeepSeek DSML syntax, the sentinel envelope and Claude-style
  `<invoke>` XML all become normal `tool_calls`, including a DSML envelope whose
  inner tags lost the prefix, and a lone `arguments` object is unwrapped;
- a tool block cut off at the end of a stream is repaired instead of being
  silently dropped, including a DSML block whose closing tags never arrived;
- the tool names are repeated at the top of the system prompt together with the
  required argument shape of each one, so a model does not spend turns searching
  for them; when the harness routes tools through a sandbox, the header says a
  direct call works anyway and names the sandbox as the alternative;
- a lone `arguments` wrapper is unwrapped whether its value is an object or a
  stringified object;
- a closed block that names an unknown tool is delivered as a call instead of
  stalling the stream, so the client can report the name to the model;
- a call written as a bare object without the `calls` envelope is accepted, and
  missing arguments default to `{}` instead of failing the turn;
- a name that differs from exactly one tool only by case or separators is folded
  onto it (`Read-File` becomes `read_file`); an ambiguous fold is left alone;
- a single wrapper member named `arguments`, `input`, `params`, `parameters` or
  `args` is unwrapped, including stacked wrappers, a stringified object and a run
  of `name=value` pairs; prose stays a string;
- a trailing word after the tool name is dropped (`shell command` becomes
  `shell`) only when the leading word is an exact tool name.

The name counters behind those repairs are exported on `/metrics`, and
`allaiis doctor` (and the panel's Doctor action) warns as `tool names` when the
unknown count outruns the repaired one: that means the model keeps inventing
tools the catalog does not declare, so the client's tool list is the thing to
fix. The warning names the three most frequent unknown tools, because `bash`
next to `shell` reads as a client mistake while `grep` reads as a missing tool.
The set behind it is capped at 16, so a model that sprays random names cannot
grow it without bound. Both counts and names are saved to `tool-names.json` in
the data directory, so the warning survives a restart instead of blaming the
model only until the next one.

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

The guard scans only the newly arrived tail instead of the whole accumulated
buffer, so a long streaming block stays linear. A sentinel that lost one or two
brackets is still read as a call, since models have written it that way. An unclosed block is abandoned
with a repair request after 512 KiB instead of buffering forever. A literal
`</parameter>` inside an argument value is kept, and a close tag the value never
opened is escaped before XML parsing.

Usage is estimated as characters/4, thinking included, not tokenized, and flagged
`"estimated": true`; `X-ALLAIIS-Emulated-Features: tools,usage_estimate`
declares both on every tool request. Messages aliases are exported under a
separate `allaiis-anthropic` provider.

Structured output is emulated the same way: `response_format` on Chat and
`text.format` on Responses (`json_object`, or `json_schema` with its schema)
append the schema to the system turn and add `json_mode` to
`X-ALLAIIS-Emulated-Features`. The web endpoint has no switch behind it, so a
reply can still drift — validate it if a wrong shape matters. A format type
other than `text`, `json_object` or `json_schema` is a 400, since ignoring it
would return prose to a client waiting for a document.

Fields a web turn cannot fill are refused rather than faked: `n` above one and
`logprobs` answer `invalid_request`. `max_completion_tokens` is read like
`max_tokens`, since the newer clients send the cap under that name. Sampling
values are checked against the protocol's own range before the call —
`temperature` 0–2 (0–1 on Messages), `top_p` and `min_p` 0–1, penalties −2–2, a
non-negative cap — because Venice's 400 for a bad value used to surface as a 502
blaming the provider.

A forced `tool_choice` is spelled out the same way: Chat `required` and Messages
`any` become "call one of the tools now", a named function becomes a sentence
naming it, and `parallel_tool_calls: false` (or Messages
`disable_parallel_tool_use`) asks for a single call. `"none"` drops the tools
from the request altogether. None of it is enforced by the endpoint, and the
header adds `tool_choice` whenever a stand-in was written. A forced choice
naming a tool that is not in the offered list is a 400: the instruction would
otherwise ask the model for a call it cannot make.

Stop sequences are cut by the gateway, because the endpoint ignores them: Chat
`stop` (one string or an array), Messages `stop_sequences` and a `stop` on
Responses. A sequence split across two chunks is still found, and the cut runs on
the stream too, with `stop` added to `X-ALLAIIS-Emulated-Features`. Anthropic
Messages names the caller's own string: `stop_reason: stop_sequence` and that
sequence in the reply and in `message_delta`. A route with tools leaves them off
— a word inside a call block would otherwise cut the block in half — so an agent
loop should end on its own.

A failure after the stream has started is reported in the protocol's own shape
instead of ending as a silent EOF: a Chat `data` line with an error and `[DONE]`,
an Anthropic `error` event, and a typed `error` event (`code:
upstream_error`) on Responses, which a Responses client can act on.

The reply names the credential label in `X-ALLAIIS-Credential` and the panel
shows it next to the request, so a pool can be followed in a log.

A client may send `X-ALLAIIS-Probe: <anything>` to mark a synthetic call: its
unknown tool names are still delivered but kept out of the unknown counters and
the name record, so probes and synthetic checks do not read as model mistakes.

### Failure codes

| Code | Meaning |
| --- | --- |
| `cloudflare_challenge` | stored clearance rejected; recapture |
| `web_session_expired` | Clerk session dead and the guest fallback failed |
| `sign_in_required` | the model needs a signed-in session (guest cookie) |
| `context_length_exceeded` | upstream `userPromptTooLong` |
| `rate_limit_exceeded` | the pool hit a `429`; that credential cools until the reset header says |
| `ambiguous_credentials` | the request carried both `x-api-key` and `Authorization`; send one, since two keys in one request is a client bug rather than a wrong key |
| `insufficient_balance` | the credit bucket for paid models is empty (`402 insufficientBalance`), not the chat allowance; that model is parked on the label for 30 minutes with a `Retry-After`, free models are unaffected, `web quota` shows both counters |
| `e2ee_failed` | the enclave attestation or a sealed chunk failed; retry, or clear `e2ee` to send plaintext |

## Implementation

- `core/internal/webprov/venice.go` — transport, attestation pool, Clerk mint, NDJSON decoding, error typing
- `core/internal/webprov/e2ee.go` — enclave attestation, ECDH/HKDF/AES-GCM seal and open, session cache
- `core/internal/webprov/cdp.go` — one-time cookie/User-Agent capture over DevTools
- `core/internal/webprov/catalog.go` — text-model discovery
- `core/internal/gateway/web.go` — OpenAI translation, streaming guard, estimated usage
- `core/internal/gateway/tools.go` — tool envelope, sentinel/XML/DSML guard buffer, repair message, truncated-block repair
- `core/internal/gateway/shape_chat.go` — global tool indexes and the optional final usage chunk
- Offline coverage: `core/internal/webprov/venice_test.go`, `core/internal/gateway/web_test.go`, `core/internal/gateway/web_tools_test.go`, `core/internal/gateway/tools_test.go`
