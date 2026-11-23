# Changelog

Releases are tagged from `main`; the binaries are on the release page with
`SHA256SUMS`.

## v0.1.5

A fuzz target found a marker leak in the tool guard.


- **Stray close marker.** A `<<<END_TOOL_CALL>>>` that arrived without its block
  was passed to the client as prose, and a split marker could leak its prefix.
  The guard now holds a close tag like an open one and drops it. A new fuzz
  target found it; the event decoder has one too.

## v0.1.4

Small fixes that keep the checks honest.


- **Quota button** shows the plan, the credits and the refill date beside the chat
  counter, so an empty credit bucket is visible without a CLI.
- **Status matrix** carries the new work, and the web dossier states the
  Terms-of-Service position plainly: Venice objects to programmatic use of its
  web app, so a web route runs at the operator's risk.
- **Smoke tool probe** asks the route in its own shape. A Messages alias was sent
  through Chat Completions, answered `model_not_found`, and the probe said
  nothing about that route's tools.

## v0.1.3

The quota a client sees is the quota the account really has.


- **Quota tells the truth.** The probe asked the rate-limits endpoint without
  the session token, so a signed-in label looked empty while the account had
  thousands of chats left. It now asks as the account and reports the plan and
  the credit balance beside the counters. The `402` wording says the credit
  bucket is empty, which is what Venice means, instead of calling the plan spent.
- **Doctor plan line.** `web plan` prints each session's chat allowance, plan and
  credits, so a `402` can be read without guessing.

## v0.1.2

The protocol shapes now match each other, and each one says what it can and
cannot honour.

- **Honest refusals.** `n` above one and `logprobs` answer `invalid_request`
  instead of returning one plain answer, and Chat reads `max_completion_tokens`
  as the cap when `max_tokens` is absent.

- **`store: false`.** A Responses reply sent with it stays out of the vault, so a
  follow-up by its id carries only what the client sends.

- **Forced tool choice.** `required` and Messages `any`, a named function and
  the no-parallel switch become an instruction, and `"none"` drops the tools
  from the request; the endpoint has no switch to enforce either.
- **Stop sequences.** Chat `stop`, Messages `stop_sequences` and a `stop` on
  Responses are cut by the gateway, since the endpoint ignores them; the cut
  works on a stream and finds a sequence split across chunks. A route with tools
  leaves them off so a call block is never cut in half.
- **Structured output.** `response_format` on Chat and `text.format` on
  Responses now append the schema to the system turn and declare `json_mode`;
  the web endpoint has no switch behind it, so this is spelled out rather than
  enforced, and a reply can still drift.
- **Responses roles.** A `developer` item arrived as a user message, so its
  rules read like something the user had just said; it joins the system prompt
  now, as the Chat shape already did.
- **Responses images.** `input_image` carries `image_url` as a bare string, but
  the part decoder only knew the `{"url": ...}` object Chat uses. The decode
  failed, so the whole part list came through as JSON text and the model saw no
  picture. Both shapes decode now, and a test sends a real upload through
  `/v1/responses`.
- **Responses history.** A tool result replayed through `/v1/responses` reached
  the model without the blank line the Messages shape puts in front of it, so
  the model tended to call the same tool again. Both shapes now produce the same
  prompt, and a test compares them end to end.

## v0.1.1

Recovery before the client sees a failure, and the last transport gap.

- **Tools.** A call short of a required argument is held to the end of the stream
  and replaced by a repair turn that names the field, also after prose has
  streamed; the prose is not repeated. Label switches, empty replies and repairs
  each have their own single retry budget.
- **Pools.** A failing label (429, 402, expired or challenged session) is parked
  and the request retried on another label while nothing was streamed. The
  response names the serving label.
- **Balance.** A `402` parks one model on its label, not the label: free models on
  the same session keep answering. Later calls answer `402` with `Retry-After`.
- **End-to-end encryption.** `e2ee` routes seal every message to the attested
  enclave (secp256k1, HKDF-SHA256, AES-256-GCM) after checking the nonce, the
  verified flag, debug mode and the TDX verdict; replies are opened per chunk.
  Doctor, the panel and `allaiis web e2ee` attest without quota. Tools and
  attachments travel the same path. A live sealed reply is still unverified.
- **Observation.** `: ping` keep-alives, failover/repair/enclave counters,
  doctor cooldown and guest-enclave warnings, smoke codes with `--allow`.
- **Ops.** `--offline` for doctor, Go 1.26.11, no known vulnerabilities,
  `staticcheck` and `govulncheck` in CI.

## v0.1.0

First public release: the Venice web transport, all three inbound protocol
shapes, tool emulation with repairs, attachments, quotas and cooldowns, the
control panel, service autostart, backup/restore and self-update.
