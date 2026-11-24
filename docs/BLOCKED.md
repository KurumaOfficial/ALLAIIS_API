# Integration status

## B1. Open items

Venice web works live as a guest and on a Pro session, over Chat Completions,
Anthropic Messages and OpenAI Responses. The control panel manages keys, aliases,
probes and logs against a live gateway with hot reload. Inline attachments are
bounded by the configurable `max_request_mb`.

- **E2EE models: verified.** Attestation, sealing and opening are implemented and
  covered offline; live, a sealed turn on `e2ee-gpt-oss-120b-p` came back
  decrypted, a tool call inside the sealed content parsed, and a two-turn tool
  loop answered from its result, with the TEE headers on the wire. The credit-priced `e2ee-*` models still wait on credits;
  the free ones need a Pro session only.
- A second web provider on the same capture flow.

## B1b. Current environment notes

Checked 2026-11-24 with the stored Pro session's own claims:

- The account is **PRO** and the daily chat allowance refilled overnight
  (`remaining: 9988`, resets 07:00); the guest session got its eight chats back
  at 07:19 and answered again after yesterday's `429`, without a recapture.
- The **credit** bucket paid models draw on is empty
  (`availableCredits: 0`, `usedThisCycle: 100` of `100`, refill on
  2026-12-16). Venice answers `{"error":"insufficientBalance"}` for a credit
  model, which is the credit bucket, not the plan; free models keep answering on
  the same session.
- The quota probe used to report the anonymous bucket because it asked without
  the session token, so a healthy session looked spent. It now asks as the
  account, and `allaiis doctor` prints a `web plan` line with the chat allowance,
  the plan and the credits.
- An unattested programmatic call to the agent endpoint answers `403` with a
  Terms-of-Service warning; attested calls are answered normally. The warning is
  worth knowing about for a long-running automation on a personal account.
- Free E2EE route: `e2ee-gpt-oss-120b-p-venice` on `venice-web` with `e2ee-gpt-oss-120b-p`, sealed
  and opened live, tools included.
- Working free route for the model in the browser: `deepseek-v4-1-flash-uncensored-venice` on
  `venice-web` with `deepseek-v4-1-flash-uncensored` (vision, tools, 524K
  context), pinned to the `pro` label so the pool does not land on the spent
  guest session.

## B2. Handoff

1. **Pro session (done 2026-11-22).** `allaiis web login` after a login in the
   capture Chrome captured a Pro session; `claude-opus-5-5` and
   `openai-gpt-62-astra-pro` answered with streaming and with a tool call.
2. **Pool isolation (done 2026-11-22).** `guest-venice-web` (guest) and
   `claude-opus-5-5-venice` (Pro) answered side by side from one local key; each alias is
   pinned with `--label`. Independent 429 cooldowns follow from per-key
   selection.

| Item | Status | Prerequisite |
| --- | --- | --- |
| Guest capture and replay | done | — |
| Pro capture and replay | done | — |
| Tool emulation on web routes | done | live check passed |
| Attachments (images, files) | done | live colour and text-file answers |
| E2EE models | implemented | a live check with Pro allowance |
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
- [x] Verify an `e2ee-*` model live: attestation passes, a sealed prompt and answer round-trip.
- [x] Run two sessions side by side and confirm isolation.
- [x] Record the test name, provider, model, and result in the README status matrix.
- [x] Add and delete an alias through the panel and confirm the live gateway picks
  it up without a restart.
