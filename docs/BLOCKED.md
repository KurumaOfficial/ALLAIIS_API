# Integration status

## B1. Open items

Venice web works live as a guest and on a Pro session, over Chat Completions
Anthropic Messages and OpenAI Responses. Remaining gaps are larger attachments and a second provider.

- A second web provider on the same capture flow.

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
| E2EE models | done (server-side path) | client-side field encryption is open |
| Second web provider | open | per-provider protocol capture |
| Second live web session | done | — |

## B3. Live acceptance checklist

- [x] Capture a guest session and replay a Chat Completions request.
- [x] Verify the streaming path emits deltas and a terminal event.
- [x] Verify a `403` maps to `cloudflare_challenge` and a `429` cools the pool.
- [x] Verify catalog discovery and the quota probe.
- [x] Verify a tool call round trip (`read_file`).
- [x] Capture a Pro session and replay a Pro-only model.
- [x] Verify a tool call on a Pro model and an `e2ee-*` model.
- [x] Run two sessions side by side and confirm isolation.
- [x] Record the test name, provider, model, and result in the README status matrix.
