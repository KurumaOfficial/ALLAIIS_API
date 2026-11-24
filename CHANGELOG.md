# Changelog

Releases are tagged from `main`; the binaries are on the release page with
`SHA256SUMS`.

## Unreleased

- **A Limits tab shows every web session's remaining chats, plan, credits and
  reset dates.** Refresh probes the counters without spending a message.
## v0.1.40

Web chats share the proxy pool, and the tab says ALLAIIS Console.

- **The browser tab says ALLAIIS Console.**

## v0.1.39

The control panel is an ALLAIIS console, and a proxy pool can be pasted in.

- **The panel is an ALLAIIS console in the OpenCode cabinet's language.** A top
  bar, horizontal tabs, cards, tables and a settings sidebar replace the old
  rail; Models/Providers mirror Aliases/Keys, and the tool controls moved to
  Controls.
- **A proxy pool can be pasted into the panel and is used for provider
  requests, including web chats.** Each line is `host:port`,
  `host:port:user:pass` or a URL; the list is validated, sealed in the vault and
  rotated per outbound connection. HTTP, HTTPS and SOCKS5 proxies are tunnelled
  before the Chrome TLS fingerprint is applied.
- **A downloaded binary starts itself.** With no command it creates the
  per-user data directory, config and vault (generating a passphrase file when
  none is configured), starts the gateway and opens the panel. The First steps
  card leads to keys, OpenCode import and Venice capture.
- **OpenCode keys import from the panel.** The import reads every provider in
  the auth file, not only providers that already have a model.
- **Web-chat aliases end in `WebAPI`.** A web transport other than Venice uses
  that suffix when an alias is generated; Venice keeps `-venice`.
- **The capture browser accepts Edge and Brave.** Chrome is no longer the only
  option on a fresh Windows PC.

## v0.1.38

New aliases carry the real model name, with Venice at the end.

- **New aliases are named after the real model, with the provider at the end.**
  The panel used to put the provider first and the CLI appended `-venice-web`;
  both now use the upstream model ID first and end with the provider (`-venice`
  for `venice-web`), so a client sees the real model name in the API.

## v0.1.37

An exported config round-trips, and the diagnostics say what they mean.

- **An exported config keeps `providers` an object.** A config using only the
  built-in providers answered `"providers": null`; it is `{}` now, so a reader
  always sees the same shape.
- **The unknown-tool metric says what it counts.** It includes a name the
  repair replaced, so "Calls delivered with a name no tool declares" was
  wrong; the HELP names the attempt before repair.

## v0.1.36

The keep-alive stops before the terminal frame, and a parked model is not
probed again.

- **A keep-alive ping never follows `[DONE]`.** The ticker stopped only when
  the handler returned, so a tick could land after the terminal frame; pings are
  stopped before any terminal write now. A slow-writer test reproduces the old
  ordering.
- **The panel's smoke skips a parked model.** It used to spend a request that
  could only answer `402`; the model-scoped balance mark is honoured now, and a
  test pins that the upstream is not called.

## v0.1.35

A tool result is labelled, merged with the question that follows it, and read
as an answer rather than a new instruction.

- **Tool results are labelled and merged.** Consecutive results were bare user
  turns, so a model could read them as new instructions and call the same tools
  again; they are one turn beginning `Tool result from <name>:`, a question
  asked right after a result joins that turn, and the tool prompt explains the
  format instead of telling the model to call a tool on every turn. An
  Anthropic text block after a `tool_result` no longer runs into the result, and
  Responses keeps the same boundary.

## v0.1.34

Bad input is named, and a garbled call still works.

- **Browser clients can read `Retry-After` and send `X-ALLAIIS-Probe`.** Both
  were missing from the CORS lists, so a page could not see when to retry or
  mark a synthetic call.
- **`smoke --allow` takes an HTTP status or an error code.** `--allow 402` was
  silently ignored, because only the error code was matched; a person reading
  the failure sees the status.
- **A wrong field type names the field.** A string where a number belongs used
  to answer "the request body must be a JSON object", which sent the caller
  looking in the wrong place.
- **A tool the gateway cannot offer is a 400.** A built-in type such as
  `web_search_preview` was offered with an empty name on Chat and skipped
  silently on Responses; only function tools with a name are accepted now.
- **A closing sentinel that lost a bracket still closes the call.** The open
  side already tolerated the typo; the close side dropped the call instead of
  parsing it.
- **An empty conversation is a 400 on every web shape.** An empty Chat or
  Messages array and a Responses request with no input used to become an empty
  prompt and a generic answer instead of naming the missing required field.
- **A tool sentinel with a dropped bracket is still a call.** A model wrote
  `<ALLAIIS_TOOL_CALL>>>` with one `<`, and the guard took it for prose, so the
  client received the sentinel and half an argument; one to three brackets are
  recognised now, including a marker split across chunks.

## v0.1.33

A model that miswrites a call no longer poisons its own repair.

- **A repair after a malformed tool block sends no empty assistant turn.**
  Venice rejects an empty assistant message, so a model that called a tool
  before writing any prose turned its own repair into a 502. The repair carries
  what the model actually wrote, or a statement of the malformed call.
- **A tool block cut by the token cap is dropped, not copied into the answer.**
  Its text is the gateway's sentinel and half-written arguments, and streaming
  it put raw emulation syntax into the client's transcript.

## v0.1.32

Bad input is refused locally, and every shape says what happened.

- **A UTF-8 BOM before the JSON is accepted.** A Windows tool that writes one
  used to get a syntax error for a document that was otherwise valid.
- **`top_logprobs: 0` is not a logprobs request.** It asks for no token data,
  and it used to be refused like a positive value.
- **A tool choice naming an unoffered tool is a 400.** The stand-in instruction
  used to name it anyway, so the model was asked for a call it could not make.
- **An unknown structured-output type is a 400.** It used to be ignored, and a
  client waiting for a document got prose.
- **A sampling value out of range is a local 400.** Venice rejects it with a
  400, which the gateway turned into a 502 blaming the provider; temperature,
  `top_p`, `min_p`, penalties and a negative cap are checked against the
  protocol's own range first, and nothing is sent.
- **The runbook states what protects the launcher on Windows.** The `0600` bits
  Go writes do not restrict other Windows accounts; the per-user Startup folder
  does, and the page says so in both languages now.

## v0.1.31

Attachments are read or refused, and every stream says what happened.

- **An unknown `previous_response_id` is a 404.** A follow-up on an id that was
  never stored (`store: false`, expired, a typo) answered with no history, so a
  fresh conversation looked continuous. It is `previous_response_not_found` now
  and upstream is not called.
- **A Chat stream opens with the assistant role.** The first chunk carried only
  content; a client that reads `delta.role` from it found nothing.
- **Anthropic image blocks reach the model.** A Messages `image` part with a
  base64 `source` used to fall through the parser, so vision answered as if no
  image had been sent.
- **An image in a system turn is refused.** The web endpoint pairs images with
  user content, so a system or developer image used to vanish without a word.
- **An unreadable attachment is refused.** An unreachable image URL, bad base64,
  a `file_id` or a document block was silently dropped; it is now a 400 that
  names the attachment, and the request never reaches upstream.
- **A stream failure is a protocol event.** After some text had gone out, a dead
  web stream left a Responses client with a bare `data` line and no terminal
  event; it now receives a typed `error` event with `code: upstream_error`.
- **The emulation header names stop cutting and forced choice.** `stop` and
  `tool_choice` join `usage_estimate`, `tools` and `json_mode` whenever the
  gateway stood in for them.
- **A broken body says what is wrong with it.** Every decode failure answered
  "expected a JSON object", so a truncated document and a JSON array read the
  same; a syntax error now quotes the decoder, and a non-object says so.
- **A matched stop sequence is named.** Anthropic Messages answered
  `stop_reason: end_turn` with a null `stop_sequence` even when the caller's own
  sequence ended the reply; it now reports `stop_sequence` and the string.
- **A NUL byte is refused locally.** Venice answers 400 for a prompt carrying
  one, which the gateway turned into a 502 and blamed on upstream; an agent can
  produce it by pasting a binary file. It is a 400 naming the byte now, and the
  request never leaves.
- **Invalid UTF-8 is refused.** Go's decoder replaces bad bytes with U+FFFD, so
  the model answered text the caller never sent; the body must be valid UTF-8.
- **An unreadable tool-name record says so.** It was ignored silently, so a
  counter reset looked unexplained; boot now logs the path and the reason.

## v0.1.30

The health probe says which build answered.


- **`/healthz` names the build.** A monitor or a bug report could see that the
  process was healthy but not which binary answered; the probe carries the
  version now, still without a key.

## v0.1.29

Two keys in one request is a client bug, and it says so.


- **A request with two credential headers says so.** Sending both `x-api-key`
  and `Authorization` used to answer a bare `401`, which reads as a wrong key;
  it is a `400 ambiguous_credentials` now.

## v0.1.28

A streamed call closes the way a client expects.


- **A streamed function call closes its item.** It went from the argument delta
  straight to `response.completed`; a client that finalises on done events now
  gets `response.function_call_arguments.done` and `response.output_item.done`.

## v0.1.27

The last Responses event holds the answer.


- **`response.completed` carries the finished items.** Its `output` array was
  empty, so a streaming client that reads the last event instead of rebuilding
  the deltas lost the text and the calls.

## v0.1.26

Each model list offers only what that client can call.


- **The model list is filtered by protocol.** A Claude client was offered Chat
  aliases that answer only on the OpenAI shape, and an OpenAI client the reverse;
  each list now carries what its shape can actually call.

## v0.1.25

The tool-name warning says what it counts.


- **The tool-name warning counts names, not calls.** It said "only 0 were
  repaired" although an unknown call is repaired once now; the line separates a
  name that never matched from one that only differed by case.

## v0.1.24

A call to a tool that was never offered is repaired, not handed to the client.


- **A call to a tool that was never offered gets one repair.** The client could
  only reject `bash` when it never offered one, so the model now gets one turn
  naming the tools that exist, while nothing has streamed; the replaced call is
  what ships.

## v0.1.23

The panel opens with the counters already visible.


- **The panel shows the quota line on open.** The counters, plan and credits
  needed a click; they load once when the page does, quietly if the session is
  dead, and still spend no message.

- **A live acceptance record**, `docs/acceptance.md`, lists what was exercised
  against Venice with dates and results, beside the offline matrix.

## v0.1.22

The model list speaks both client shapes.


- **The model list answers the shape the caller parses.** A request with
  `Anthropic-Version` gets `type`, `display_name` and `has_more`; without it the
  OpenAI object stays as it was.

## v0.1.21

Discovery tells you which label to pick.


- **`models discover` names the labels to pick.** With two stored sessions the
  error asked to "select the provider and a credential", though the provider was
  already selected; it now lists the labels, and a test pins the wording.

## v0.1.20

The model list answers a Claude client and a single alias.


- **The model endpoints belong to no protocol.** `GET /v1/models` accepted only a
  Bearer key, so a Claude client sending `x-api-key` got a 401; both header
  shapes work now, and `GET /v1/models/{id}` answers for one alias.

## v0.1.19

The version is one word away.


- **`allaiis version` exists**, also as `-v` and `--version`. A bug report or a
  script reached for it and got "unknown command".

## v0.1.18

Anthropic clients can ask how big a turn is before sending it.


- **`/v1/messages/count_tokens` answers.** Anthropic clients ask for a count
  before a turn; it returns the same character-based estimate the context guard
  uses and spends no message.

## v0.1.17

A wrong passphrase is refused where it used to look like success.


- **`init` refuses a passphrase that does not open the existing vault.** `Init`
  on an existing file is a no-op whatever the passphrase, so a wrong one reported
  success and every later command failed instead; the command now opens the vault
  first and says so.


- **A repeated `init` is pinned as safe.** It keeps an existing config and vault
  and does not rotate the client key, which matters on a host where the first
  command after an upgrade is often `init` again.

## v0.1.16

The notes are written where people read them.


- **Release notes come from the changelog.** The workflow extracts the matching
  `## vX.Y.Z` section and uses it as the release body, instead of a generated
  list of commit subjects.

- **The one-generation-per-account lock is pinned by tests**: two turns on one
  credential never overlap on the wire, and a queued turn gives up when its caller
  does instead of waiting for the first to finish.

## v0.1.15

The model list speaks the usual limit names.


- **`/v1/models` carries the limits under the usual names.** `context_length` and
  `max_output_tokens` are what a client looks for when sizing a prompt; the alias
  limits were only under vendor-prefixed keys.

## v0.1.14

`up --listen` serves where it says.


- **`up --listen` was accepted and ignored.** Only `gui` applied the flag, so
  `up -listen 127.0.0.1:9000` still served on the configured address; it now
  serves where asked and rejects an address without a port.

## v0.1.13

The status line tells the truth, and a BOM no longer breaks the config.


- **`service status` says whether the gateway is up.** It printed the autostart
  entry and nothing else, so "registered" read as "running"; it now also dials the
  configured address.
- **A byte-order mark no longer breaks the config.** A Windows editor can save
  UTF-8 with one, and the loader failed on the first brace with an error that said
  `invalid character 'ï'`.

## v0.1.12

Each command shows only the flags it reads.


- **Each command shows only its own flags.** A fallthrough chain registered the
  smoke and doctor options on `up`, `gui` and `smoke` alike, so `up --help` listed
  `--alias` and `--json` it ignores. The sets are separate now.

## v0.1.11

The service supervisor waits properly.


- **The service script waits with `ping`, not `timeout`.** `timeout` needs a
  console input handle and fails at once under `start /min`, so a crash could
  become a tight restart loop; a kill now yields exactly one restart in thirty
  seconds.

## v0.1.10

`backup --help` no longer writes a directory.


- **`backup --help` printed nothing and wrote a directory named `--help`.** The
  first argument was taken as the target whatever it looked like; help words are
  recognised now and an option-shaped path is refused. Restore rejects unknown
  options too.


- **Two panel paths have tests.** A pasted plain API key for a web provider is
  refused with the message that names `web login`, and the Export button's file
  round-trips a route added through the same panel.


- **The vault has its own tests.** A reopen, a wrong passphrase, a ciphertext
  moved to another label (it must fail closed), a rotated client key and the
  NUL-aware cooldown clear were only covered through other packages. Coverage of
  the package went from 34% to 71%.


- **The trust model is written down.** A security section in the quality dossier
  (both languages) states what binds where, how the vault is keyed, how the two
  tokens are compared, and where the web cookies go.


- **Runbook commands were corrupted.** Ten lines in the two runbooks carried a
  bell byte where `\a` belonged, so `update` and `service` read as
  `.llaiis.exe`. Fixed, and a docs check now fails on any control character in a
  tracked text file.
- **Release steps are written down** in both runbooks: version, changelog, tag,
  the workflow, and the self-update that follows.


- **The key importer has tests.** It was the one package at zero coverage while
  it reads a file from another program, so a bad shape or a name mismatch would
  have surfaced as an empty import rather than an error.


- **Fuzz runs stop when they say.** The minimization tail is capped in CI: a
  five-minute local run spent twenty more minutes minimizing a corpus of
  hundreds of inputs, and two gigabytes, without failing anything.

## v0.1.9

The generated client block no longer loses what an alias can do.


- **`export opencode` carries capabilities.** Each generated model entry now has
  its limits (context, output) and capabilities (tools, vision, reasoning) plus
  the compatibility fields, matching what a hand-written block sets.

## v0.1.8

The plan line warns before the allowance is gone.


- **Doctor warns at the quota floor.** `web plan` uses the same floor as the
  panel badge and prints when the chat allowance resets, so a count of 3 is a
  warning rather than only a number.

## v0.1.7

The last open item closed: a sealed reply was opened live.


- **E2EE verified live.** The catalogue carries free, Pro-only `e2ee-*` models;
  a sealed turn on `e2ee-gpt-oss-120b-p` came back decrypted and a tool call
  inside it parsed. Doctor and the docs stop calling the reply unverified.
- **Free e2ee route.** `e2ee-oss` seals to that model on the `pro` label.

## v0.1.6

A capture now says what it can spend.


- **`web login` says what it captured.** After a signed-in capture it prints the
  plan, the chat allowance and the credits, and names the credit bucket when it
  is empty instead of leaving that to the first paid call.

- **Fuzzing in CI.** The guard, the event decoder and the three request builders
  each run for thirty seconds on every push in their own step, so a failure names
  the target, and a failing input is uploaded as an artifact; the builder target
  stubs the image fetch so no run reaches the network. The quality dossier says
  what they cover.
- **Line endings.** A `.gitattributes` pins LF, so the Windows CI runner's
  checkout no longer fails `gofmt`, and binaries are marked as such.

## v0.1.5

A fuzz target found a marker leak in the tool guard.


- **Stray close marker.** A `<<<END_TOOL_CALL>>>` that arrived without its block
  was passed to the client as prose, and a split marker could leak its prefix.
  The guard now holds a close tag like an open one and drops it. A new fuzz
  target found it; the event decoder has one too.

## v0.1.4

Small fixes that keep the checks honest.


- **Quota button** shows the plan, the credits and the refill date beside the chat
  counter, so an empty credit bucket is visible without a CLI.
- **Status matrix** carries the new work, and the web dossier states the
  Terms-of-Service position plainly: Venice objects to programmatic use of its
  web app, so a web route runs at the operator's risk.
- **Smoke tool probe** asks the route in its own shape. A Messages alias was sent
  through Chat Completions, answered `model_not_found`, and the probe said
  nothing about that route's tools.

## v0.1.3

The quota a client sees is the quota the account really has.


- **Quota tells the truth.** The probe asked the rate-limits endpoint without
  the session token, so a signed-in label looked empty while the account had
  thousands of chats left. It now asks as the account and reports the plan and
  the credit balance beside the counters. The `402` wording says the credit
  bucket is empty, which is what Venice means, instead of calling the plan spent.
- **Doctor plan line.** `web plan` prints each session's chat allowance, plan and
  credits, so a `402` can be read without guessing.

## v0.1.2

The protocol shapes now match each other, and each one says what it can and
cannot honour.

- **Honest refusals.** `n` above one and `logprobs` answer `invalid_request`
  instead of returning one plain answer, and Chat reads `max_completion_tokens`
  as the cap when `max_tokens` is absent.

- **`store: false`.** A Responses reply sent with it stays out of the vault, so a
  follow-up by its id carries only what the client sends.

- **Forced tool choice.** `required` and Messages `any`, a named function and
  the no-parallel switch become an instruction, and `"none"` drops the tools
  from the request; the endpoint has no switch to enforce either.
- **Stop sequences.** Chat `stop`, Messages `stop_sequences` and a `stop` on
  Responses are cut by the gateway, since the endpoint ignores them; the cut
  works on a stream and finds a sequence split across chunks. A route with tools
  leaves them off so a call block is never cut in half.
- **Structured output.** `response_format` on Chat and `text.format` on
  Responses now append the schema to the system turn and declare `json_mode`;
  the web endpoint has no switch behind it, so this is spelled out rather than
  enforced, and a reply can still drift.
- **Responses roles.** A `developer` item arrived as a user message, so its
  rules read like something the user had just said; it joins the system prompt
  now, as the Chat shape already did.
- **Responses images.** `input_image` carries `image_url` as a bare string, but
  the part decoder only knew the `{"url": ...}` object Chat uses. The decode
  failed, so the whole part list came through as JSON text and the model saw no
  picture. Both shapes decode now, and a test sends a real upload through
  `/v1/responses`.
- **Responses history.** A tool result replayed through `/v1/responses` reached
  the model without the blank line the Messages shape puts in front of it, so
  the model tended to call the same tool again. Both shapes now produce the same
  prompt, and a test compares them end to end.

## v0.1.1

Recovery before the client sees a failure, and the last transport gap.

- **Tools.** A call short of a required argument is held to the end of the stream
  and replaced by a repair turn that names the field, also after prose has
  streamed; the prose is not repeated. Label switches, empty replies and repairs
  each have their own single retry budget.
- **Pools.** A failing label (429, 402, expired or challenged session) is parked
  and the request retried on another label while nothing was streamed. The
  response names the serving label.
- **Balance.** A `402` parks one model on its label, not the label: free models on
  the same session keep answering. Later calls answer `402` with `Retry-After`.
- **End-to-end encryption.** `e2ee` routes seal every message to the attested
  enclave (secp256k1, HKDF-SHA256, AES-256-GCM) after checking the nonce, the
  verified flag, debug mode and the TDX verdict; replies are opened per chunk.
  Doctor, the panel and `allaiis web e2ee` attest without quota. Tools and
  attachments travel the same path. A live sealed reply is still unverified.
- **Observation.** `: ping` keep-alives, failover/repair/enclave counters,
  doctor cooldown and guest-enclave warnings, smoke codes with `--allow`.
- **Ops.** `--offline` for doctor, Go 1.26.11, no known vulnerabilities,
  `staticcheck` and `govulncheck` in CI.

## v0.1.0

First public release: the Venice web transport, all three inbound protocol
shapes, tool emulation with repairs, attachments, quotas and cooldowns, the
control panel, service autostart, backup/restore and self-update.
