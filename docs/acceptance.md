# Live acceptance record

What has actually been exercised against Venice, on this machine, with the
stored Pro session. Every row is a request that left the gateway, not a fixture.
The offline suites are listed in the [status matrix](../README.md#status); this
page is the other half.

Recorded 2026-11-23 and 2026-11-24. The label was the Pro session unless a row
says otherwise; the guest session was out of its chat window. On 2026-11-24 the
aliases were renamed to the upstream model ID with `-venice` at the end; rows
recorded before that keep the names that were in use then.

| Check | How | Result |
| --- | --- | --- |
| Chat Completions, JSON and SSE | `venice-deepseek`, plain and streaming | 200, first chunk carries the assistant role, deltas and `[DONE]` |
| Anthropic Messages, JSON and SSE | `cc-free-venice-web` and `cc-venice-web` | 200, `message_start` … `message_stop` |
| OpenAI Responses, JSON and SSE | `gpt-free-venice-web`, `gpt-venice-web` | 200, `response.created` … `response.completed` |
| Tool call, three shapes | `read_file` / `get_weather` through Chat, Messages, Responses | a `tool_calls` / `tool_use` / `function_call` each |
| Misspelled tool marker | a Messages turn where the model writes `<<<ALLTOOL_CALL>>>` or `<<<ALL_AIIS_TOOL_CALL>>>` | a `tool_use` block; no marker text reaches the client |
| Tool result round trip | second turn carrying the result | the answer used it on every shape |
| Parallel tool results | two calls in one reply, both results, then a question | the answer used both, no repeat call; Chat merges the results and the question into one labelled turn |
| Streaming tool call | a tool offered with `stream: true` on each shape | Chat deltas + `tool_calls`, Messages `input_json_delta` + `tool_use`, Responses done events + `completed.output` |
| Stop sequence | `stop_sequences: ["6"]` on a count to ten, plain and streamed | content cut before 6, `stop_reason: stop_sequence`, `stop_sequence: "6"` in both |
| Emulation header | a request with `stop` and a forced `tool_choice` | `X-ALLAIIS-Emulated-Features` adds `stop` and `tool_choice` |
| Anthropic vision | a base64 `image` block in Messages | the image arrives in the agent multipart turn |
| Bad attachment | an unreachable image URL, a document block | 400 `invalid_request`, upstream not called |
| Out-of-range sampling | `temperature: 99` and `top_p: 5` on Chat | 400 `invalid_request`, upstream not called; `temperature: 1.5` answers |
| Empty conversation | `messages: []` on Chat and Messages, no `input` on Responses | 400 `invalid_request`, upstream not called |
| Unsupported tool | `web_search_preview` on Chat and Responses | 400 "only function tools", upstream not called |
| Wrong field type | `temperature: "hot"` on Chat | 400 naming `temperature` |
| CORS surface | preflight and a parked 402 | `X-ALLAIIS-Probe` allowed, `Retry-After` exposed and present (1800) |
| Bad format or tool choice | `response_format: {"type":"xml"}`, a forced choice naming an unoffered tool | 400 `invalid_request`, upstream not called |
| Vision | a red PNG through Chat and Responses | described red on both |
| Structured output | `response_format` / `text.format` with a schema | a JSON object matching it |
| Stop sequences | a stop word mid-reply, JSON and stream | cut before the word |
| Forced tool choice | `required`, `any`, a named tool, `none` | a call, a call, a call, prose |
| `count_tokens` | a Messages body | `{"input_tokens": …}`, no upstream call |
| Model list shapes | `x-api-key` + `Anthropic-Version`, and Bearer | Anthropic and OpenAI shapes, each carrying only the routes it can call (2 Messages, 10 OpenAI) |
| E2EE sealed reply | `e2ee-gpt-oss-120b-p` canary | `decrypted: true` |
| E2EE tool loop | call and result inside the sealed turns | the final answer used the result |
| Quota and plan | `web quota`, doctor | `pro: … chat, PRO, 0/100 credits`, reset dates |
| Daily window reset | after the window rolled over, `guest-venice-web` | 200 again; `pro` read 9988 chats and doctor's plan line went from warn to ok |
| Long agent task | eight-file `stats` package and a smaller task | 14 steps/27 tools and 11/13, zero tool errors |
| Service restart | kill the gateway under the supervisor | one restart in thirty seconds |
| Panel | all seven views, Quota button | no console error, plan and credits rendered |
| Upgrade | `update --force` from the release | checksum verified, binary swapped, service up |
| Continuation | a stored response id, and one created with `store: false` | the stored turns replay; the unstored id answers `404 previous_response_not_found` |
| Stream terminal | a streaming reply, tail inspected | ends with `data: [DONE]`, no `: ping` after it |
| First run | the release binary with no arguments in an empty directory | config, vault and passphrase created; panel opened; health 200 |
| Console shell | seven tabs, settings sidebar, no console errors | all views shown; logo in header and favicon; Limits rows; proxy list saved and cleared from Controls |
| Limits countdown | open Limits with a stored Pro session | resets in, safe pace, amber at 1,000 and red at 500 |
| Deep tool history | a Chat body with 40 past tool calls | 200, `X-ALLAIIS-Tool-Calls: 40`, warning header and a WARN log line |
| Stray DSML prose | a tool turn whose model writes `｜DSML｜ calls>` before the block | marker removed; prose and the parsed call stay intact |
| Parallel tool batch | two independent `read_file` calls | one assistant turn with both calls, one chat |
| Proxy tunnel | a local CONNECT proxy in Controls, one Venice chat | clerk and outerface tunnels opened; 200 "Ready." |
| Proxy auth | an authenticated CONNECT proxy, one Venice chat | tunnels opened; wrong credentials answered 502 |
| Panel proxy paths | a local CONNECT proxy in Controls, then Discovery, Limits and the E2EE probe | clerk and outerface tunnels opened for each path |
| Native catalog proxy | a fake public provider behind a local HTTP proxy, panel and CLI Discovery | `GET /v1/models` seen at the proxy; both return the fake model |
| SOCKS5 proxy | a SOCKS5 server with user/pass, one Venice chat | outerface tunnel opened; 200 "Ready." |
| E2EE canary proxy | `allaiis web e2ee` with a local CONNECT proxy | attestation and `decrypted: true` through clerk/outerface tunnels |
| Proxy after restart | save a local CONNECT proxy, restart the gateway, one chat | clerk and outerface tunnels opened after the restart |
| Proxy at rest | save a proxy with a unique host and password, search the vault file | no plaintext of either in `vault.sqlite` |
| Limits tab | open Limits | guest and PRO rows with chat left, credits and reset dates |
| Config export | `/api/export`, served from a fresh dir on a spare port | `providers: {}`, health 200, 11 models |
| Health probe | `GET /healthz` | `{"status":"ok","version":"0.1.57"}` |

Two facts worth keeping in view: the Pro **credit** bucket is empty (paid models
answer `402`), and the **chat** allowance is the counter free models draw on.
`allaiis web quota` prints both.
