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
- Loopback control panel (`allaiis gui`) for keys, aliases, discovery, probes,
  metrics, logs and settings, with atomic hot reload so edits apply without a
  restart.
- **Venice web transport (Tier C)**: page-free replay of the web app's own
  inference API with a browser TLS fingerprint, single-use attestation pool,
  Clerk session tokens, NDJSON streaming, quota probe, model discovery, image
  and file input through a multipart agent turn, emulated tool calls with a sentinel guard buffer and one repair retry, and
  Anthropic Messages and OpenAI Responses on both JSON and SSE. See
  [docs/venice-web.md](docs/venice-web.md).
- Upstream model discovery with per-provider results, public catalogs, and
  authenticated catalogs for supported APIs. Exact upstream IDs stay attached to
  their provider.
- OpenCode configuration export and integration tests for authorization,
  routing, streaming, cancellation, and credential boundaries.
- Offline compatibility fixtures for SSE parsing and tool-call formats under
  `core/internal/gateway`.

## Next

- Document the login walkthrough on each OS.
- Add tier labels to the catalog page and a `--transport` column so web and
  official routes for one model are visibly distinct.
- Larger attachment handling: chunked bodies and multiple files per turn.
- Panel: per-alias quick probe results history and a usage chart over time.
- Add richer catalog metadata for modalities, context windows, and endpoint
  compatibility where providers publish those fields.
- Add cross-platform CI and reproducible Windows, Linux, and macOS releases,
  plus opt-in smoke tests for official provider APIs.

## Later

- Gemini's native generation API and expanded conformance tests for
  provider-specific streaming and tool events.
- A second web provider (DeepSeek or Qwen) reusing the same capture and error
  taxonomy.
- E2EE models on Venice: the payload is encrypted in the browser, so those need
  a page-driven transport instead of HTTP replay.
- Add a versioned out-of-process adapter protocol and documented extension
  points for authorized integrations.
- Add separate local client keys, usage reporting, and operator-managed
  access policies.

Provider coverage depends on the catalogs that each API exposes to the
credential used. Model discovery is read-only; route registration stays
explicit so operators can choose where each client's prompts go.
