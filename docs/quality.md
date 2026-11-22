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
| Cooldowns | one credential label or an unpinned provider | the upstream reset header, capped at ten minutes | **Resume**, key replacement or removal, expiry |
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
| Prompt over the model context | rejected before sending when the alias carries `max_context` |
| Too many attachments | rejected before upload when the alias carries `max_images` |
| Silent stream | aborted after the alias inactivity window (default 2 min), answered `upstream_timeout` |
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
gauge and `allaiis_up`. The tool parser adds `allaiis_tool_unwrap_total`,
`allaiis_tool_name_fold_total`, `allaiis_tool_name_suffix_total` and
`allaiis_tool_unknown_total`, so reshaping is visible without the log; a rising
unknown count means the model keeps naming tools outside the catalog.
The four tool counters continue across a restart: they and their name set live
in `tool-names.json` in the data directory, written at most every three seconds
through a temporary file and read at startup, so a doctor warning is not wiped by
a restart. The panel's **Reset** button on the Tool reshaping card (or
`POST /api/tools/reset`, or `allaiis tools reset`) zeroes both and deletes the
file; `allaiis tools reset --expired`, or the same endpoint with `{"expired": true}`,
drops only the names past retention, which keeps the fresh evidence, which is how a fixed tool list stops carrying old blame. `allaiis tools
show` prints the record with the time each name was last seen and marks a name
past retention as expired, so a stopped gateway cannot make the view lie; a running
gateway notices an outside edit or deletion within a couple of seconds. A name also expires after
`tool_name_ttl_days` (default 30, `0` keeps names forever): it stops being listed
and stop counting as evidence, and the drop button or a restart removes it, so an
alias fixed months ago does not keep the warning alive. Saving does not prune, so
the drop has something to act on; the totals are cumulative and stay.
Cardinality follows the configured aliases. Scrape it from a loopback collector.

## Graceful shutdown

`SIGINT`/`SIGTERM` stop accepting connections and give in-flight requests ten
seconds before the listener closes.
