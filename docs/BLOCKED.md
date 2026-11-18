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

1. **A1, real web host.** A live run requires a transport approved by the provider for that host. Acceptance evidence is HTTP `200` with `Content-Type: text/event-stream`. Current state is blocked. Implementation file and test are absent.
2. **A9, two web-account sessions.** A live run requires two provider-approved test sessions on the web integration. Acceptance evidence covers session isolation and compliance with the provider's rate limits. Current state is blocked. Implementation file and test are absent.

| Item | Status | Prerequisite |
| --- | --- | --- |
| Browser session cookie import | blocked | Provider-approved session handoff |
| JWT issuance through Clerk | blocked | Provider-documented authentication contract |
| JWT refresh | blocked | Provider-documented token lifetime and refresh contract |
| Private web-client attestation | blocked | Provider-approved authorization mechanism |
| Curl-compatible private transport | blocked | Provider-approved web transport |
| Session cookie rotation | blocked | Provider-documented session rotation contract |
| Standalone web integration server | blocked | Implemented web integration |
| Browserless web session import | blocked | Provider-approved headless authentication |

## B3. Live acceptance checklist

- [ ] Run A1 against the authorized real web host and record HTTP `200` with `Content-Type: text/event-stream`.
- [ ] Run A9 with two approved web-account test sessions and verify session isolation and rate-limit handling.
- [ ] Confirm the stream contains a content event and a terminal event.
- [ ] Disconnect the client mid-stream and confirm upstream cancellation.
- [ ] Record the test name, provider, model, and result in the README status matrix.
