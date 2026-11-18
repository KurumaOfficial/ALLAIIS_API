<h1 align="center">ALLAIIS API</h1>

<p align="center"><strong>One local key for the AI APIs you choose.</strong></p>

<p align="center">
  <a href="go.mod"><img alt="Go 1.26.2+" src="https://img.shields.io/badge/Go-1.26.2%2B-00ADD8?logo=go"></a>
  <a href="LICENSE"><img alt="GPL-3.0-only" src="https://img.shields.io/badge/license-GPL--3.0--only-4158A6"></a>
</p>

<p align="center"><strong>English</strong> · <a href="README.ru.md">Русский</a></p>

[Quick start](#quick-start) · [API](#api) · [Providers](#providers) · [Status](#status) · [Integration blockers](docs/BLOCKED.md)

ALLAIIS is a self-hosted Go gateway for AI clients such as OpenCode. Register model aliases, store provider keys in an encrypted local vault, and use one loopback endpoint for native JSON and streaming responses.

## Quick start

Use Go 1.26.2 or later. Pick an exact model ID available to your DeepSeek account and set `DEEPSEEK_API_KEY` and `DEEPSEEK_MODEL_ID` in your shell before running the commands. `deepseek-flash` is the model used in this example.

<details open>
<summary>Linux and macOS</summary>

```sh
git clone https://github.com/KurumaOfficial/ALLAIIS_API.git
cd ALLAIIS_API
go build -o allaiis ./core/cmd/allaiis
./allaiis init
./allaiis import --from env --provider deepseek
./allaiis models add --provider deepseek --upstream-model "${DEEPSEEK_MODEL_ID:-deepseek-flash}" --name flash
export ALLAIIS_API_KEY="$(./allaiis key)"
./allaiis up
```

</details>

<details>
<summary>Windows PowerShell</summary>

```powershell
git clone https://github.com/KurumaOfficial/ALLAIIS_API.git
cd ALLAIIS_API
go build -o allaiis.exe ./core/cmd/allaiis
.\allaiis.exe init
.\allaiis.exe import --from env --provider deepseek
.\allaiis.exe models add --provider deepseek --upstream-model $env:DEEPSEEK_MODEL_ID --name flash
$env:ALLAIIS_API_KEY = .\allaiis.exe key
.\allaiis.exe up
```

</details>

In your client, set the base URL to `http://127.0.0.1:8420/v1`, supply the local `ALLAIIS_API_KEY`, and select `flash-deepseek`. `allaiis up` runs in the foreground. For a second terminal, obtain the same local key with `allaiis key`.

<details>
<summary>Send a test request</summary>

```sh
curl --no-buffer http://127.0.0.1:8420/v1/chat/completions \
  -H "Authorization: Bearer $ALLAIIS_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"flash-deepseek","messages":[{"role":"user","content":"Say hello."}],"stream":true}'
```

```powershell
$body = @{
  model = 'flash-deepseek'
  messages = @(@{ role = 'user'; content = 'Say hello.' })
} | ConvertTo-Json -Depth 5
Invoke-RestMethod http://127.0.0.1:8420/v1/chat/completions -Method Post `
  -Headers @{ Authorization = "Bearer $env:ALLAIIS_API_KEY" } `
  -ContentType 'application/json' -Body $body
```

</details>

## API

| Endpoint | Purpose |
| --- | --- |
| `GET /healthz` | Local liveness probe |
| `GET /v1/models` | Aliases available under the configured privacy policy |
| `POST /v1/chat/completions` | Chat Completions with native JSON or SSE |
| `POST /v1/responses` | Foreground Responses creation with native JSON or SSE |
| `POST /v1/messages` | Anthropic Messages creation with native JSON or SSE |

Each model alias serves one generation endpoint. `models add` defaults to `chat`. `GET /v1/models` uses `allaiis_protocol` to identify Responses and Messages aliases. The gateway forwards native tool calls and usage when the configured API returns them.

<details>
<summary>Set up Responses and Messages</summary>

Set `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `OPENAI_MODEL_ID`, and `ANTHROPIC_MODEL_ID` in your shell.

```sh
./allaiis account add --provider openai --label work --key-env OPENAI_API_KEY
./allaiis account add --provider anthropic --label work --key-env ANTHROPIC_API_KEY
./allaiis models add --provider openai --upstream-model "$OPENAI_MODEL_ID" --name responses --protocol responses --label work
./allaiis models add --provider anthropic --upstream-model "$ANTHROPIC_MODEL_ID" --name messages --protocol messages --label work
```

Restart the gateway after updating its configuration. Send `{"model":"responses-openai","input":"Say hello."}` to `/v1/responses`. A Responses alias requires a credential label. Use the same alias and upstream project for `previous_response_id` follow-ups. [OpenAI Responses](https://developers.openai.com/api/reference/resources/responses/methods/create)

Send `{"model":"messages-anthropic","max_tokens":32,"messages":[{"role":"user","content":"Say hello."}]}` to `/v1/messages`. Supply the local key via `x-api-key` or `Authorization: Bearer`. ALLAIIS forwards `Anthropic-Version` and `Anthropic-Beta` and defaults to version `2026-10-21`. [Claude Messages](https://platform.claude.com/docs/en/api/messages/create)

</details>

## Providers

| Provider ID | API key variable | Reference |
| --- | --- | --- |
| `openai` | `OPENAI_API_KEY` | [OpenAI](https://platform.openai.com/docs/api-reference/chat) |
| `deepseek` | `DEEPSEEK_API_KEY` | [DeepSeek](https://api-docs.deepseek.com/) |
| `zai` | `ZAI_API_KEY` | [Z.ai](https://docs.z.ai/api-reference/llm/chat-completion) |
| `venice` | `VENICE_API_KEY` | [Venice](https://docs.venice.ai/api-reference/endpoint/chat/completions) |
| `qwen` | `DASHSCOPE_API_KEY` | [Alibaba Cloud](https://help.aliyun.com/en/model-studio/compatibility-of-openai-with-dashscope) |
| `gemini` | `GEMINI_API_KEY` | [Gemini](https://ai.google.dev/gemini-api/docs/openai) |
| `anthropic` | `ANTHROPIC_API_KEY` | [Claude](https://platform.claude.com/docs/en/api/openai-sdk) |
| `arena-ai` | `ARENA_API_KEY` | [Arena](https://platform.arena.ai/docs/api) |
| `opencode` | `OPENCODE_API_KEY` | [OpenCode Console](https://opencode.ai/docs/console/harnesses#base-urls) |
| `opencode-go` | `OPENCODE_API_KEY` | [OpenCode Go](https://opencode.ai/docs/console/go/#where-can-i-use-it) |

```sh
./allaiis models discover --all
./allaiis models discover --all --with-keys
./allaiis models discover --provider venice --match 'DeepSeek'
./allaiis models discover --provider opencode-go --label workspace
```

Discovery reports per-provider results. Public catalogs can be queried before `init`. Use `--with-keys` to include stored credentials in bulk discovery and `--label` to select a key for one provider. Register a chat-capable model separately with `models add`.

<details>
<summary>Expose the same model from Venice and OpenCode Go</summary>

```sh
./allaiis models discover --provider venice --match 'DeepSeek V4.1 Flash'
./allaiis models discover --provider opencode-go --match 'DeepSeek V4.1 Flash'
./allaiis account add --provider venice --label personal
./allaiis account add --provider opencode-go --label workspace
./allaiis models add --provider venice --upstream-model "$VENICE_MODEL_ID" --name deepseek-flash --label personal
./allaiis models add --provider opencode-go --upstream-model "$OPENCODE_MODEL_ID" --name deepseek-flash --label workspace
```

Set `VENICE_MODEL_ID` and `OPENCODE_MODEL_ID` to the IDs reported by discovery before registering the aliases. Select `deepseek-flash-venice` or `deepseek-flash-opencode-go` using the same local key. OpenCode Go is for coding-agent traffic and access to restricted models depends on the workspace. [OpenCode Go model access](https://opencode.ai/docs/console/go/#enable-restricted-models)

</details>

<details>
<summary>Custom providers and regional endpoints</summary>

Use HTTPS or a numeric loopback HTTP address. `base_url` is the prefix before the final API path. For example, a compatible local server can be registered in `config.json` as follows.

```json
{
  "providers": {
    "local": {
      "base_url": "http://127.0.0.1:11434/v1",
      "key_env": "LOCAL_LLM_API_KEY"
    }
  },
  "models": [
    { "id": "local-model", "provider": "local", "upstream_model": "your-installed-model" }
  ]
}
```

Import a key before using a route. For a local server that ignores authorization, set a dedicated placeholder key. Custom catalogs default to `<base_url>/models`. Set `"catalog_public": true` for an anonymous catalog. A custom Qwen provider with a regional workspace URL ending in `/compatible-mode/v1` can set `"catalog_format": "dashscope"` for paginated native discovery. [Alibaba Cloud model catalog](https://help.aliyun.com/en/model-studio/list-models)

</details>

## Keys and OpenCode

```sh
./allaiis import --from opencode --path /path/to/opencode/auth.json
./allaiis account add --provider deepseek --label work --key-env WORK_DEEPSEEK_API_KEY
./allaiis status
./allaiis export opencode
```

Merge the exported provider into `opencode.json`, set `ALLAIIS_API_KEY` for OpenCode, and select `allaiis/<model-alias>`. The export includes Chat Completions aliases. [OpenCode provider configuration](https://opencode.ai/docs/providers/)

- **Vault.** Use a passphrase of at least 16 bytes. For unattended operation, set `ALLAIIS_VAULT_PASSPHRASE` in the process environment. Credentials in SQLite are encrypted with AES-256-GCM using an Argon2id-derived key.
- **Data directory.** Set `ALLAIIS_DATA_DIR` to override the default. The defaults are `%APPDATA%\allaiis` on Windows, `~/Library/Application Support/allaiis` on macOS, and `${XDG_CONFIG_HOME:-~/.config}/allaiis` on Linux.
- **Key pools.** A pool is bound to a provider ID and base URL. Pin a route with `--label`. To remove a key, run `allaiis account remove --provider <provider> --label <label>`. Restart after configuration or key changes.
- **Routing.** Use `X-ALLAIIS-Session` or `X-OpenCode-Session` for stable key selection. `X-ALLAIIS-Min-Privacy` can raise the configured privacy floor. A provider `429` pauses its pool until `Retry-After`. The gateway accepts up to 8 MiB per request.
- **OpenCode import.** Recognized `api` entries are imported from `auth.json`. By default the CLI checks `$XDG_DATA_HOME/opencode/auth.json` or `~/.local/share/opencode/auth.json`.

## Status

**Live provider tests recorded for this matrix: 0.**

| Feature | Implemented | Tested offline | Tested live | Blocked |
| --- | :---: | --- | :---: | --- |
| Model routing, privacy and cooldowns | ✓ | `TestRoutingAndProviderCooldown` | — | — |
| Native Responses and Messages | ✓ | `TestNativeProtocols` | — | — |
| SSE and disconnect cancellation | ✓ | `TestStreamFlushAndCancel` | — | — |
| Local opt-in browser worker | ✓ | `TestBrowserWorkerBoundaryAndStreaming`, `TestCanceledRequestReleasesWorker` | — | — |
| Private Venice web integration | — | Not covered | — | [Details](docs/BLOCKED.md) |

```sh
go test ./...
go vet ./...
```

[Roadmap](ROADMAP.md) · [Browser worker example](docs/browser-bridge.md) · [Related projects](docs/related-projects.md)

## License

GNU General Public License v3.0 only (`GPL-3.0-only`). [Full license](LICENSE)
