# Live acceptance record

What has actually been exercised against Venice, on this machine, with the
stored Pro session. Every row is a request that left the gateway, not a fixture.
The offline suites are listed in the [status matrix](../README.md#status); this
page is the other half.

Recorded 2026-11-23 and 2026-11-24. The label was the Pro session unless a row
says otherwise; the guest session was out of its chat window.

| Check | How | Result |
| --- | --- | --- |
| Chat Completions, JSON and SSE | `venice-deepseek`, plain and streaming | 200, deltas and `[DONE]` |
| Anthropic Messages, JSON and SSE | `cc-free-venice-web` and `cc-venice-web` | 200, `message_start` … `message_stop` |
| OpenAI Responses, JSON and SSE | `gpt-free-venice-web`, `gpt-venice-web` | 200, `response.created` … `response.completed` |
| Tool call, three shapes | `read_file` / `get_weather` through Chat, Messages, Responses | a `tool_calls` / `tool_use` / `function_call` each |
| Tool result round trip | second turn carrying the result | the answer used it on every shape |
| Streaming tool call | a tool offered with `stream: true` on each shape | Chat deltas + `tool_calls`, Messages `input_json_delta` + `tool_use`, Responses done events + `completed.output` |
| Vision | a red PNG through Chat and Responses | described red on both |
| Structured output | `response_format` / `text.format` with a schema | a JSON object matching it |
| Stop sequences | a stop word mid-reply, JSON and stream | cut before the word |
| Forced tool choice | `required`, `any`, a named tool, `none` | a call, a call, a call, prose |
| `count_tokens` | a Messages body | `{"input_tokens": …}`, no upstream call |
| Model list shapes | `x-api-key` + `Anthropic-Version`, and Bearer | Anthropic and OpenAI shapes |
| E2EE sealed reply | `e2ee-gpt-oss-120b-p` canary | `decrypted: true` |
| E2EE tool loop | call and result inside the sealed turns | the final answer used the result |
| Quota and plan | `web quota`, doctor | `pro: … chat, PRO, 0/100 credits`, reset dates |
| Daily window reset | after the window rolled over, `guest-venice-web` | 200 again; `pro` read 9988 chats and doctor's plan line went from warn to ok |
| Long agent task | eight-file `stats` package and a smaller task | 14 steps/27 tools and 11/13, zero tool errors |
| Service restart | kill the gateway under the supervisor | one restart in thirty seconds |
| Panel | all seven views, Quota button | no console error, plan and credits rendered |
| Upgrade | `update --force` from the release | checksum verified, binary swapped, service up |

Two facts worth keeping in view: the Pro **credit** bucket is empty (paid models
answer `402`), and the **chat** allowance is the counter free models draw on.
`allaiis web quota` prints both.
