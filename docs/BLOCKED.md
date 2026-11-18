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

- [ ] Receive HTTP `200` with `Content-Type: text/event-stream` from an authorized inference endpoint.
- [ ] Confirm the stream contains a content event and a terminal event.
- [ ] Disconnect the client mid-stream and confirm upstream cancellation.
- [ ] Record the test name, provider, model, and result in the README status matrix.
