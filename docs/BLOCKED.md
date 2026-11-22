# Integration status

## B1. Open items

Venice web works live as a guest session. Remaining gaps are scoped to the Pro
path and to feature parity with the official API.

- Pro session captured and replayed on Windows, Linux, and macOS.
- Two Pro accounts in one pool with session isolation.
- Vision and file upload for web routes.
- E2EE (`e2ee-*`) models that encrypt in the browser.
- A second web provider on the same capture flow.

## B2. Handoff

1. **Pro session.** A live run requires the operator to log in through
   `allaiis web chrome` and then run `allaiis web login`. Acceptance evidence is
   an `X-ALLAIIS-Upstream-Model` header and non-empty text for a Pro-only model.
   Guest fallback is implemented and live-tested.
2. **Pool isolation.** A live run requires two captured sessions with different
   labels. Acceptance evidence covers label selection via `--label` and
   independent 429 cooldowns.

| Item | Status | Prerequisite |
| --- | --- | --- |
| Guest capture and replay | done | — |
| Pro capture and replay | implemented, needs a live check | operator login |
| Tool emulation on web routes | done | live check passed |
| Vision and uploads | open | upstream multipart contract |
| E2EE models | open | page-driven transport |
| Second web provider | open | per-provider protocol capture |

## B3. Live acceptance checklist

- [x] Capture a guest session and replay a Chat Completions request.
- [x] Verify the streaming path emits deltas and a terminal event.
- [x] Verify a `403` maps to `cloudflare_challenge` and a `429` cools the pool.
- [x] Verify catalog discovery and the quota probe.
- [x] Verify a tool call round trip (`read_file`).
- [ ] Capture a Pro session and replay a Pro-only model.
- [ ] Run two sessions side by side and confirm isolation.
- [ ] Record the test name, provider, model, and result in the README status matrix.
