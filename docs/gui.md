# Control panel

`allaiis gui` starts the gateway and a local control panel in one process:

```powershell
.\allaiis.exe gui
```

The panel opens in your default browser. `--ui 127.0.0.1:8431` pins the panel
port, `--listen 127.0.0.1:9000` overrides the gateway address for this run and
`--no-open` skips the browser.

## What the tabs do

| Tab | Purpose |
|---|---|
| Overview | Gateway health, served alias count, request counters, recent requests, provider readiness with Quota, Recapture and Resume, gateway key reveal/copy |
| Aliases | Register, mute and delete model aliases, including protocol and privacy |
| Keys | Add API keys, remove credentials, capture a Venice web session, rotate the gateway key |
| Discovery | Browse an upstream catalog with a stored credential and create an alias from a model |
| Playground | Send a real probe, or probe every alias at once with status, latency and the upstream model |
| Logs | Follow the structured log live |
| Settings | Diagnostics, privacy floor, client snippets, reload, config export, data paths |

There is no build step: the page is plain HTML, CSS and JavaScript embedded in
the binary.

## Quota

**Quota** probes the remaining chat allowance of every stored session for a web
provider and shows the reset time, without spending a message. It also reveals
whether a session actually resolves to a signed-in account or fell back to
anonymous, which is the first thing to check when a plan quota looks wrong.

## Cooldowns

A `429` cools only the credential that hit the limit; other labels of the same
provider keep working. The Overview shows which label is cooling and until when,
and **Resume** lifts it early once the plan has reset. A provider-wide cooldown
only appears for unpinned pools.

## Capturing a Venice session

1. Open **Keys**.
2. Press **Open capture browser**. This launches the dedicated Chrome profile on
   port 9222, not your everyday windows.
3. Sign in to Venice in that window.
4. Enter a label (for example `pro`) and press **Capture session**.

The captured cookie set is stored as an encrypted vault entry. The button runs
the same `allaiis web login` path as the CLI, so the result is identical.

## Hot reload

Every change made through the panel is written to `config.json` or the vault and
then pushed into the running gateway with an atomic table swap. The watcher also
picks up edits made by the CLI or a text editor within a few seconds. A request
in flight keeps the old snapshot; a failed reload keeps the previous routes and
logs a warning. Pressing **Reload config from disk** forces the check.

## Muting an alias

The checkbox in the alias table mutes a route without deleting it: it leaves
`/v1/models`, routing and smoke runs, and its name stays in the config. Useful
while a provider is degraded or a credential is being refreshed. The same effect
is available as `"disabled": true` on a route in `config.json`.

## Probes and smoke

A single probe answers one alias. **Probe every alias** walks all aliases that
have a credential, one at a time, and shows a pass/fail row per alias — the
fastest way to see which route degraded after a provider change. The headless
equivalent is `allaiis smoke`, which builds a throwaway in-process gateway so a
running one is never disturbed:

```powershell
.\allaiis.exe smoke --alias cc --attempts 20   # repeat one alias
.\allaiis.exe smoke --json                     # CI, exit 1 on any failure
```

## Diagnostics

**Run checks** inspects the config, the vault, the listener, alias-to-credential
coverage and the capture browser. It only uses local checks and spends no
provider quota. The same report is available headless as `allaiis doctor`,
which also runs before the gateway starts in deployment scripts.

## Security

- The panel binds `127.0.0.1` only, on a random port unless `--ui` says otherwise.
- The HTML served at `/` contains a random per-run token. Every `/api` call must
  send it in `X-ALLAIIS-Admin`; a stale tab gets `401`.
- Responses are `no-store`, the page sets a restrictive CSP and uses no external
  assets, so nothing is fetched from the network.
- Provider keys never leave the vault; the panel only shows labels. The gateway
  bearer key can be revealed because it is the key clients already send.
- A new random token is generated on every launch; nothing survives a restart.

The panel is a local convenience, not a remote admin surface. Do not expose the
port beyond loopback without a reverse proxy and TLS.

## Key rotation

**Rotate gateway key** mints a new bearer key, stores it sealed and installs it
in the live gateway immediately. Update clients afterwards; the old key stops
working at once.

## Troubleshooting

- **Gateway down** — the counters still render, but requests fail until the
  listener is up. The panel and the gateway start together, so a bind error is
  printed in the terminal that launched `gui`.
- **Browser does not open** — copy the `Control panel: http://…` line from the
  terminal.
- **Capture fails** — the message says whether Chrome, the sign-in or the
  Cloudflare clearance is missing. Press **Recapture** on the provider in the
  Overview to reopen the browser and refresh that label in one step.
