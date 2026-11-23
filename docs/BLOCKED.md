# Integration status

## B1. Open items

Venice web works live as a guest and on a Pro session, over Chat Completions,
Anthropic Messages and OpenAI Responses. The control panel manages keys, aliases,
probes and logs against a live gateway with hot reload. Inline attachments are
bounded by the configurable `max_request_mb`.

- **E2EE models.** An `e2ee-*` alias is sent to the plain endpoint, so the reply
  is not decrypted in the page; the key exchange and the client-side crypto are
  not implemented. Doctor warns while an alias points at one, and smoke reports
  the model like any other, so the gap is visible rather than silent.
- A second web provider on the same capture flow.

## B1b. Current environment notes

The guest session is out of its daily allowance until 2026-11-23 13:33 and the
Pro session reports a zero chat counter. Premium models answer
`402 insufficient_balance` and are parked per model; the free uncensored,
role-play and DeepSeek uncensored models still answer. `web quota` and the
panel's Quota button show the remaining count and reset without spending a
message.

## B2. Handoff

1. **Pro session (done 2026-11-22).** `allaiis web login` after a login in the
   capture Chrome captured a Pro session; `claude-opus-5-5` and
   `openai-gpt-62-astra-pro` answered with streaming and with a tool call.
2. **Pool isolation (done 2026-11-22).** `guest-venice-web` (guest) and
   `opus-venice-web` (Pro) answered side by side from one local key; each alias is
   pinned with `--label`. Independent 429 cooldowns follow from per-key
   selection.

| Item | Status | Prerequisite |
| --- | --- | --- |
| Guest capture and replay | done | — |
| Pro capture and replay | done | — |
| Tool emulation on web routes | done | live check passed |
| Attachments (images, files) | done | live colour and text-file answers |
| E2EE models | open | page key exchange and client-side field encryption |
| Control panel and hot reload | done | — |
| Gateway key rotation | done | — |
| Second web provider | open | per-provider protocol capture |
| Second live web session | done | — |

## B3. Live acceptance checklist

- [x] Capture a guest session and replay a Chat Completions request.
- [x] Verify the streaming path emits deltas and a terminal event.
- [x] Verify a `403` maps to `cloudflare_challenge` and a `429` cools the pool.
- [x] Verify catalog discovery and the quota probe.
- [x] Verify a tool call round trip (`read_file`).
- [x] Capture a Pro session and replay a Pro-only model.
- [x] Verify a tool call on a Pro model.
- [ ] Verify an `e2ee-*` model with credits and a page-side decrypt.
- [x] Run two sessions side by side and confirm isolation.
- [x] Record the test name, provider, model, and result in the README status matrix.
- [x] Add and delete an alias through the panel and confirm the live gateway picks
  it up without a restart.
