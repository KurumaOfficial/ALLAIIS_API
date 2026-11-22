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
| Overview | Gateway health, served alias count, request counters, recent requests, provider readiness, gateway key reveal/copy |
| Aliases | Register and delete model aliases, including protocol and privacy |
| Keys | Add API keys, remove credentials, capture a Venice web session, rotate the gateway key |
| Discovery | Browse an upstream catalog with a stored credential and create an alias from a model |
| Playground | Send a real probe through the running gateway and see status, latency and the upstream model |
| Logs | Follow the structured log live |
| Settings | Privacy floor, client snippets, reload, config export, data paths |

There is no build step: the page is plain HTML, CSS and JavaScript embedded in
the binary.

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
  Cloudflare clearance is missing. Reopen the capture browser and retry.
