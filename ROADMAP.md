# ALLAIIS API roadmap

ALLAIIS brings multiple official AI APIs to one self-hosted, OpenAI-compatible
endpoint. A provider-specific alias identifies the backend, and a single local
key gives clients access to the routes the operator has configured.

## Available now

- Local Go gateway with Chat Completions, OpenAI Responses creation, and
  Anthropic Messages creation. All three support native SSE and share
  OpenAI-compatible model discovery for configured aliases.
- Encrypted key storage, named credentials, session affinity, and provider
  cooldowns that respect `Retry-After`.
- Upstream model discovery with per-provider results, public catalogs, and
  authenticated catalogs for supported APIs. Exact upstream IDs stay attached to
  their provider.
- OpenCode configuration export and integration tests for authorization,
  routing, streaming, cancellation, and credential boundaries.
- A [loopback browser bridge example](docs/browser-bridge.md) for an app
  that explicitly participates in the worker protocol.

## Next

- Add richer catalog metadata for modalities, context windows, and endpoint
  compatibility where providers publish those fields.
- Add a local catalog view that shows discovered models, configured aliases,
  and the credential scope used to look them up.
- Add cross-platform CI and reproducible Windows, Linux, and macOS releases,
  plus opt-in smoke tests for official provider APIs.

## Later

- Add Gemini's native generation API and expand conformance tests for
  provider-specific streaming and tool events.
- Add a versioned out-of-process adapter protocol and documented extension
  points for authorized integrations.
- Add separate local client keys, usage reporting, and operator-managed
  access policies.

Provider coverage depends on the catalogs that each API exposes to the
credential used. Model discovery is read-only; route registration stays
explicit so operators can choose where each client's prompts go.
