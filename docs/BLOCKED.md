# Integration status

## B1. Open items

- Browser session cookie import (`__client`).
- JWT issuance through Clerk.
- JWT refresh with clock skew and single-flight coordination.
- Private web-client attestation.
- Curl-compatible transport for the protected web endpoint.
- Session cookie rotation from `Set-Cookie`.
- A standalone server for the web integration.
- Web session import on a machine without a browser.

## B2. Handoff

1. **A1, real host.** Use the existing gateway against a provider-documented Chat Completions endpoint with an authorized API credential. Record whether the official endpoint returns HTTP `200` and `Content-Type: text/event-stream`. The protected web transport is blocked and uncovered. The gateway path has offline coverage in `TestQuickStartOffline`.
2. **A9, two credentials.** Use two provider-issued API keys with separate labels on an authorized test account or workspace. Check that each pinned alias uses its own key and that the gateway waits for `Retry-After` when the provider throttles the pool. Private web-session switching after a rate limit is blocked and uncovered. `TestProviderAliasesAndPinnedCredentials` and `TestRoutingAndProviderCooldown` cover the official API behavior offline.

| Item | Status | Supported handoff |
| --- | --- | --- |
| Browser session cookie import | blocked | Use a provider-issued API key with the existing key import command. |
| JWT issuance through Clerk | blocked | A sanctioned integration needs a provider-documented authentication contract. |
| JWT refresh | blocked | Refresh handling depends on a sanctioned token contract. |
| Private web-client attestation | blocked | A sanctioned integration needs a documented authorization mechanism. |
| Curl-compatible private transport | blocked | The gateway already streams from documented endpoints through `core/internal/gateway/gateway.go`. |
| Session cookie rotation | blocked | Credential replacement for official APIs is available through `allaiis account add`. |
| Standalone web integration server | blocked | A local opt-in worker example is documented in [Browser bridge](browser-bridge.md). |
| Browserless web session import | blocked | For a headless host, `allaiis account add --key-env` stores an official API key in the local vault. |

The requested private authentication and inference commands are blocked. An authorized integration can start with the documented request format and test key issued for that endpoint. [Venice API reference](https://docs.venice.ai/api-reference/endpoint/chat/completions)

## B3. Live acceptance checklist

- [ ] A1 authorized transport check. Receive HTTP `200` with `Content-Type: text/event-stream` through the gateway from a documented provider endpoint.
- [ ] A9 two-credential check. Verify both named API routes and provider `Retry-After` handling within the permitted test scope.
- [ ] Confirm the stream contains a content event and a terminal event.
- [ ] Disconnect the client mid-stream and confirm upstream cancellation.
- [ ] Record the test name, provider, model, and result in the README status matrix.
