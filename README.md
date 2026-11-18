# ALLAIIS API

ALLAIIS is a self-hosted Go gateway for official AI APIs. It exposes a local
OpenAI-compatible Chat Completions endpoint and lets a client use one ALLAIIS
key for the API providers you configure.

## Quick start

Build with Go 1.26.2 or later.

```sh
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
| `opencode` | `OPENCODE_API_KEY` | [OpenCode Console](https://opencode.ai/docs/console/harnesses#base-urls) |
| `opencode-go` | `OPENCODE_API_KEY` | [OpenCode Go](https://opencode.ai/docs/console/go/#where-can-i-use-it) |

OpenCode Go is intended for coding-agent traffic. The gateway sends its own
user agent and a stable `X-OpenCode-Session` identifier. The `qwen` preset uses
the Singapore-compatible endpoint. For a regional or workspace-specific Qwen
endpoint, add a custom provider with that endpoint.

Use the optional `providers` object to configure a custom OpenAI-compatible
API. The base URL is the part before `/chat/completions`. Choose an HTTPS
endpoint or a numeric loopback HTTP endpoint you control.

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
  select `allaiis/<model-alias>`. [OpenCode custom providers](https://opencode.ai/docs/providers/)

## Routing and API behavior

- `GET /v1/models` lists configured aliases with a key and sufficient privacy.
  `POST /v1/chat/completions` forwards the Chat Completions request and supports
  JSON and native SSE. Model-specific parameters and features depend on the
  selected upstream. The response body contains the provider's model ID.
- For stable routing within a conversation, send `X-ALLAIIS-Session` or
  `X-OpenCode-Session`. Without a session header, the gateway uses the opening
  messages through the first user message to choose a key. It computes a keyed
  hash for routing and stores no conversation text.
- When a provider returns `429`, the gateway pauses that provider's entire
  pool until its `Retry-After` expires. It uses roughly 30 seconds if the
  header is missing or invalid. Cooldowns survive a gateway restart.
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

Phase 0 provides the local daemon, Chat Completions, model discovery, key
management, and basic routing. OAuth, browser adapters, the AAWP worker
protocol, native Anthropic Messages and Gemini endpoints, and Responses API
are outside the current implementation.

Run the test suite and static checks with `go test ./...` and `go vet ./...`.

## License

Distributed under the GNU General Public License, version 3. See [LICENSE](LICENSE).
