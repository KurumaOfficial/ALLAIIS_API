# ALLAIIS API

ALLAIIS is a self-hosted Go gateway for AI APIs. It gives tools such as OpenCode
one local API key for streaming Chat Completions, OpenAI Responses, and Anthropic
Messages. Provider-specific model aliases let you choose where each request
runs. Provider API keys are stored in an encrypted local vault.

[Project roadmap](ROADMAP.md) · [Native API setup](#native-apis) · [Browser bridge example](docs/browser-bridge.md) · [Related projects](docs/related-projects.md)

## Quick start

Build with Go 1.26.2 or later.

```sh
git clone https://github.com/KurumaOfficial/ALLAIIS_API.git
cd ALLAIIS_API
go build -o allaiis ./core/cmd/allaiis
./allaiis init
```

On Windows, use `go build -o allaiis.exe ./core/cmd/allaiis` and run
`.\allaiis.exe` in place of `./allaiis`.

Choose a vault passphrase of at least 16 bytes. The CLI prompts for it without
echoing it. For unattended execution, supply `ALLAIIS_VAULT_PASSPHRASE` through
your process environment. The default data directory is `%APPDATA%\allaiis` on
Windows, `~/Library/Application Support/allaiis` on macOS, and
`${XDG_CONFIG_HOME:-~/.config}/allaiis` on Linux. Set `ALLAIIS_DATA_DIR` to
use another directory.

Edit the `config.json` printed by `init` and add a model alias. This example
uses the DeepSeek API.

```json
{
  "listen": "127.0.0.1:8420",
  "min_privacy": "consumer_cloud",
  "models": [
    {
      "id": "deepseek",
      "provider": "deepseek",
      "upstream_model": "deepseek-flash"
    }
  ]
}
```

Set `DEEPSEEK_API_KEY` in the current shell using a key from your provider.
Import it, capture the gateway key into your environment, and start the server.

```sh
./allaiis import --from env --provider deepseek
export ALLAIIS_API_KEY="$(./allaiis key)"
./allaiis up
```

```powershell
.\allaiis.exe import --from env --provider deepseek
$env:ALLAIIS_API_KEY = .\allaiis.exe key
.\allaiis.exe up
```

Point your client at `http://127.0.0.1:8420/v1` with `ALLAIIS_API_KEY` as its
bearer token. To make a streaming request from a second shell, make the same
environment variable available there.

```sh
curl --no-buffer http://127.0.0.1:8420/v1/chat/completions \
  -H "Authorization: Bearer $ALLAIIS_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"deepseek","messages":[{"role":"user","content":"Say hello."}],"stream":true}'
```

```powershell
$request = @{
  model = 'deepseek'
  messages = @(@{ role = 'user'; content = 'Say hello.' })
} | ConvertTo-Json -Depth 5
Invoke-RestMethod -Method Post -Uri 'http://127.0.0.1:8420/v1/chat/completions' `
  -Headers @{ Authorization = "Bearer $env:ALLAIIS_API_KEY" } `
  -ContentType 'application/json' -Body $request
```

## Providers

Pick an exact upstream model ID from the provider documentation and configure
an alias for each model to expose. Use provider-issued API keys and plans that
authorize the traffic you send.

| Provider ID | Default API key variable | Documentation |
| --- | --- | --- |
| `openai` | `OPENAI_API_KEY` | [OpenAI Chat Completions](https://platform.openai.com/docs/api-reference/chat) |
| `deepseek` | `DEEPSEEK_API_KEY` | [DeepSeek API](https://api-docs.deepseek.com/) |
| `zai` | `ZAI_API_KEY` | [Z.ai Chat Completions](https://docs.z.ai/api-reference/llm/chat-completion) |
| `venice` | `VENICE_API_KEY` | [Venice Chat Completions](https://docs.venice.ai/api-reference/endpoint/chat/completions) |
| `qwen` | `DASHSCOPE_API_KEY` | [Alibaba Cloud Model Studio](https://help.aliyun.com/en/model-studio/compatibility-of-openai-with-dashscope) |
| `gemini` | `GEMINI_API_KEY` | [Gemini OpenAI compatibility](https://ai.google.dev/gemini-api/docs/openai) |
| `anthropic` | `ANTHROPIC_API_KEY` | [Claude OpenAI compatibility](https://platform.claude.com/docs/en/api/openai-sdk) |
| `arena-ai` | `ARENA_API_KEY` | [Arena API](https://platform.arena.ai/docs/api) |
| `opencode` | `OPENCODE_API_KEY` | [OpenCode Console](https://opencode.ai/docs/console/harnesses#base-urls) |
| `opencode-go` | `OPENCODE_API_KEY` | [OpenCode Go](https://opencode.ai/docs/console/go/#where-can-i-use-it) |

OpenCode Go is intended for coding-agent traffic. The gateway sends its own
user agent and a stable `X-OpenCode-Session` identifier. The `qwen` preset uses
the Singapore-compatible endpoint. For a regional or workspace-specific Qwen
endpoint, add a custom provider with that endpoint. The `anthropic` preset
uses Claude's OpenAI compatibility layer for chat and its native API for
model discovery.

## Discover provider models

```sh
./allaiis models discover --all
./allaiis models discover --all --with-keys
./allaiis models discover --provider arena-ai --match DeepSeek
./allaiis models discover --provider opencode-go --label workspace
```

`--all` checks built-in and configured providers. It reads the public Venice,
OpenCode Console, and OpenCode Go catalogs without unlocking the vault. Venice's
result includes all catalog types. Use `--with-keys` to query catalogs using
stored credentials. A single private provider can also be queried directly
with `--provider`. When a provider has multiple stored keys, select a label
to get the catalog for that account or workspace.

The JSON report groups results by provider and identifies public and
credential-specific results. It includes a status for each provider, including
missing keys, ambiguous credential selection, and upstream errors. Use
`--match` to filter by ID or name. Catalogs can contain models for capabilities
other than Chat Completions. Register a chat-capable model separately with
`models add` to expose it through the gateway's `/v1/models`.

For Qwen Model Studio workspace catalogs, use a custom provider with your
regional workspace URL ending in `/compatible-mode/v1` and set
`"catalog_format": "dashscope"`. Discovery uses the native list endpoint on
that host and follows its pages. [Alibaba Cloud model catalog](https://help.aliyun.com/en/model-studio/list-models)

Use the optional `providers` object to configure a custom OpenAI-compatible
API. The base URL is the part before `/chat/completions`. Choose an HTTPS
endpoint or a numeric loopback HTTP endpoint you control. Custom providers use
`<base_url>/models` by default. Set `"catalog_public": true` if that model
list accepts anonymous requests.

```json
{
  "providers": {
    "local": {
      "base_url": "http://127.0.0.1:11434/v1",
      "key_env": "LOCAL_LLM_API_KEY"
    }
  },
  "models": [
    {
      "id": "local-model",
      "provider": "local",
      "upstream_model": "your-installed-model"
    }
  ]
}
```

The gateway expects a stored API key for every route. For a local inference
server that ignores bearer credentials, you may supply a placeholder in its
dedicated key variable.

### DeepSeek V4.1 Flash on Venice and OpenCode Go

Use the public catalog commands to find each provider's exact model ID. These
commands can run before `init` and use no API key.

```sh
./allaiis models discover --provider venice --match "DeepSeek V4.1 Flash"
./allaiis models discover --provider opencode-go --match "DeepSeek V4.1 Flash"
```

After `init`, add an inference key from each provider. The prompts hide the
keys. Use OpenCode Console's **Account → Personal API keys** to issue a key
for the workspace with your Go plan. [OpenCode API keys](https://opencode.ai/docs/console/api/)

```sh
./allaiis account add --provider venice --label personal
./allaiis account add --provider opencode-go --label workspace
./allaiis models add --provider venice --upstream-model deepseek-v4-1-flash-1017 --name DeepSeek-v4.1-flash --label personal
./allaiis models add --provider opencode-go --upstream-model deepseek-v4.1-flash --name DeepSeek-v4.1-flash --label workspace
```

The aliases `DeepSeek-v4.1-flash-venice` and
`DeepSeek-v4.1-flash-opencode-go` point to the corresponding 1017 offerings.
Select either alias with the same ALLAIIS bearer key. In OpenCode, select
`allaiis/DeepSeek-v4.1-flash-venice` or
`allaiis/DeepSeek-v4.1-flash-opencode-go` after importing the exported
configuration. [Venice model](https://cdn.venice.ai/models/deepseek-v4-1-flash-1017) ·
[OpenCode model catalog](https://opencode.ai/docs/console/api/models)

OpenCode Go lists DeepSeek V4.1 Flash under restricted models. The workspace's
privacy settings determine its availability. [OpenCode Go model access](https://opencode.ai/docs/console/go/#enable-restricted-models)

`models add` appends the provider ID to the name and saves the route in
`config.json`. With `--label`, the route uses only that named credential.
Without a label it uses the provider's configured pool. Workspace permissions,
allowances, and billing apply to the credential that serves the request.
Use `--provider opencode` for OpenCode's pay-as-you-go catalog.

## Keys and OpenCode

```sh
./allaiis import --from env
./allaiis import --from opencode --path /path/to/opencode/auth.json
./allaiis account add --provider deepseek --label work --key-env WORK_DEEPSEEK_API_KEY
./allaiis status
./allaiis export opencode
```

- Environment import uses the providers listed in `config.json`. Use
  `--provider` to select one explicitly. OpenCode import reads API-key entries
  of type `api` for recognized provider IDs. If you omit `--path`, it looks
  under `XDG_DATA_HOME/opencode/auth.json` or
  `~/.local/share/opencode/auth.json`. Use distinct `--label` values to import
  keys from multiple files into the same pool.
- Each pool is bound to one provider ID and base URL. Restart `allaiis up`
  after changing configuration or keys. To remove a credential, use
  `allaiis account remove --provider <provider> --label <label>`.
- In automation, set the provider's key in a dedicated environment variable
  and pass its variable name with `account add --key-env`.
- To use ALLAIIS in OpenCode, merge the exported provider entry into
  `opencode.json`, expose `ALLAIIS_API_KEY` to the OpenCode process, and
  select `allaiis/<model-alias>`. The export includes Chat Completions aliases.
  [OpenCode custom providers](https://opencode.ai/docs/providers/)

## Native APIs

Each alias serves one endpoint. The default protocol is `chat`. Register
separate aliases for Responses and Messages with `models add --protocol`.
The built-in provider for Responses is `openai`, and the built-in provider for
Messages is `anthropic`. A custom provider can serve either protocol at
`<base_url>/responses` or `<base_url>/messages`.

After `init`, set `OPENAI_API_KEY` and `ANTHROPIC_API_KEY` in your environment.
Set `OPENAI_MODEL_ID` and `ANTHROPIC_MODEL_ID` to model IDs available to those
keys. Use `models discover --provider` after adding the keys if you need to look
up the models.

```sh
./allaiis account add --provider openai --label work --key-env OPENAI_API_KEY
./allaiis account add --provider anthropic --label work --key-env ANTHROPIC_API_KEY
./allaiis models add --provider openai --upstream-model "$OPENAI_MODEL_ID" --name responses --protocol responses --label work
./allaiis models add --provider anthropic --upstream-model "$ANTHROPIC_MODEL_ID" --name messages --protocol messages --label work
export ALLAIIS_API_KEY="$(./allaiis key)"
./allaiis up
```

With the gateway running, send requests using the local ALLAIIS key.

```sh
curl --no-buffer http://127.0.0.1:8420/v1/responses \
  -H "Authorization: Bearer $ALLAIIS_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"responses-openai","input":"Say hello.","stream":true}'

curl --no-buffer http://127.0.0.1:8420/v1/messages \
  -H "x-api-key: $ALLAIIS_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"messages-anthropic","max_tokens":32,"messages":[{"role":"user","content":"Say hello."}],"stream":true}'
```

- Responses aliases require a credential label. Use the same alias and upstream
  project for follow-ups with `previous_response_id`. The provider manages
  response state and retention. ALLAIIS supports foreground creation over
  HTTP. [OpenAI Responses](https://developers.openai.com/api/reference/resources/responses/methods/create)
- Messages accepts `x-api-key` or `Authorization: Bearer` at the gateway.
  ALLAIIS forwards `Anthropic-Version` and `Anthropic-Beta` and uses
  `2026-10-21` if no version is specified.
  [Claude Messages](https://platform.claude.com/docs/en/api/messages/create)
- Both endpoints pass through native JSON and SSE, including tool events and
  usage when the provider returns them. `GET /v1/models` identifies native
  aliases with `allaiis_protocol`.

## Routing and API behavior

- `GET /v1/models` lists configured aliases with a key and sufficient privacy.
  The generation endpoints support JSON and native SSE. Model-specific
  parameters and features depend on the selected upstream. The response body
  contains the provider's model ID.
- For stable routing within a conversation, send `X-ALLAIIS-Session` or
  `X-OpenCode-Session` when using a key pool. Without a session header, Chat
  Completions and Messages use the opening messages through the first user
  message to choose a key. The gateway computes a keyed hash and stores no
  conversation text.
- When a provider returns `429`, the gateway pauses that provider's entire
  pool until its `Retry-After` expires. It uses roughly 30 seconds if the
  header is missing or invalid. Cooldowns survive a gateway restart.
- The gateway accepts request bodies up to 8 MiB. If a provider returns HTTP
  `413`, ALLAIIS preserves that status and lets the client decide how to
  reduce the request.
- Routes default to `consumer_cloud`. Administrators can assign each model
  alias a classification in `privacy` after verifying its data handling.
  From lowest to highest, the values are `public_research`, `consumer_cloud`,
  `zdr`, and `e2ee`. `X-ALLAIIS-Min-Privacy` can raise the configured floor
  for a request. The gateway uses the configured labels to filter routes.
- `GET /healthz` is a local liveness probe. Routed responses identify the
  provider and requested alias in `X-ALLAIIS-Provider` and `X-ALLAIIS-Model`
  and include `X-ALLAIIS-Transport-Tier: tier_a`.

The listener is loopback-only. The local bearer key and provider API keys are
encrypted in SQLite with Argon2id and AES-256-GCM. Keep the vault passphrase
separate from your data directory. The gateway logs operational metadata and
omits prompts, provider error bodies, and keys.

## Project status

The local gateway supports Chat Completions, Responses creation, Messages
creation, model discovery, and key management. The browser bridge example
connects a web application that opts in to its worker protocol. Check the
[roadmap](ROADMAP.md) for upcoming work.

Run the test suite and static checks with `go test ./...` and `go vet ./...`.

## License

Distributed under the GNU General Public License, version 3 (`GPL-3.0-only`).
See [LICENSE](LICENSE).
