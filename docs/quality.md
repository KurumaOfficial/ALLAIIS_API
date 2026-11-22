# Reliability and cache behaviour

The gateway is designed so that stale state never outlives the credential or the
configuration it belongs to.

## Request IDs

Every response carries `X-Request-Id`. A client-supplied value is kept when it is
at most 64 characters of `A-Z a-z 0-9 . _ -`; otherwise a fresh `req_…` is
generated. The ID is returned in every error payload as `request_id`, written to
the structured log next to provider failures, and exposed through CORS so browser
clients can read it. Quote it when reporting a problem.

## What is cached, and for how long

| Layer | Scope | Lifetime | Invalidated by |
|---|---|---|---|
| Clerk session JWT | one credential hash | token expiry (about a minute) | expiry, credential replacement, `Reload` sweep |
| Attestation token pool | one subject | token expiry (about four minutes, single use) | use, `403` retry, credential replacement, sweep |
| Catalog results (panel) | provider + label + filter | 10 minutes | **bypass cache**, key add/remove/recapture, restart |
| Cooldowns | one credential label or an unpinned provider | the upstream reset header | **Resume**, key replacement or removal, expiry |
| HTTP/2 connection | client | process | process restart |

The in-process sweeper runs every few seconds while `up` or `gui` is serving and
drops expired tokens, expired attestations and empty pools.

Replacing a key for an existing label (`account add`, the panel's **Save
credential**, a recapture) clears that label's cooldown, drops its cached
sessions and drops the provider's catalog cache. Removing a key does the same.
Hot reload also prunes expired state.

The CLI `models discover` is always uncached; only the panel reuses catalog
results, and its checkbox forces a refetch.

## Retries and timeouts

| Situation | Behaviour |
|---|---|
| Transport error before any byte | one retry |
| `5xx` before streaming starts | one retry |
| Stream dies before the first event | one retry |
| `403` attestation | one retry with a fresh token, then `cloudflare_challenge` |
| Malformed tool block | one repair turn, only when nothing was streamed |
| Empty reply (200, or only `processing`) | one retry, only when nothing was streamed |
| `429` | no retry; the label cools for the stated reset |
| `402 insufficientBalance` | no retry, no cooldown; surface `insufficient_balance` |
| Request body | 8 MiB cap, 90 s response-header timeout, 10 min whole call |

A retry happens only while the client has seen nothing, so streaming clients
never get duplicated text.

## Errors

Errors share one shape per protocol: `{"error":{"message","type","code","request_id"}}`
for OpenAI endpoints, `{"type":"error","error":{…}}` for Anthropic. Codes are
stable and documented in [venice-web.md](venice-web.md#failure-codes): callers can
branch on `code` instead of matching prose. `Retry-After` accompanies `429`.

## CORS

`OPTIONS` preflights answer `204` without a key, and responses allow
`Authorization`, `X-Api-Key` and the protocol headers. Credentials are not used,
so `Access-Control-Allow-Origin: *` is safe. A browser page can therefore call the
loopback gateway directly.

## Metrics

`GET /metrics` serves Prometheus text without a key: `allaiis_requests_total`
per alias, protocol and status, duration and character sums, the served-model
gauge and `allaiis_up`. Cardinality follows the configured aliases. Scrape it
from a loopback collector.

## Graceful shutdown

`SIGINT`/`SIGTERM` stop accepting connections and give in-flight requests ten
seconds before the listener closes.
