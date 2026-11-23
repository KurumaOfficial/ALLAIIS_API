# Changelog

Releases are tagged from `main`; the binaries are on the release page with
`SHA256SUMS`.

## Unreleased

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
