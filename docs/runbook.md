# Running it yourself

This is the one-machine setup: the gateway starts with your session, writes a
rotated log, and can be backed up. It targets a personal machine, not a server
farm.

## Before the first start

```powershell
.\allaiis.exe init
.\allaiis.exe web chrome
# log in at the opened tab, then capture the session:
.\allaiis.exe web login --label personal
```

Set the passphrase for the current shell so the background task can start
without a prompt:

```powershell
$env:ALLAIIS_VAULT_PASSPHRASE = "your long passphrase"
```

The same passphrase is used again by `service install`; it is written into the
launcher, so the vault has to be protected by file permissions, which it is.

## Start at logon

```powershell
.\allaiis.exe service install
.\allaiis.exe service status
.\allaiis.exe service remove
```

On Windows this writes `service.cmd` into the data directory and a launcher into
the per-user Startup folder — no elevation, which is why it is not a scheduled
task. On Linux and macOS it writes a unit file and prints the two root commands
left (`systemctl enable --now …`, `launchctl load …`).

## Logs

`service.cmd` runs `up --log-file <data>/logs/gateway.log --log-max-mb 32
--log-keep 5`. The file rotates at 32 MiB and five older files stay. The same
flags work when you start it by hand:

```powershell
.\allaiis.exe up --log-file .\logs\gateway.log
```

## Backup and restore

```powershell
.\allaiis.exe backup D:\allaiis-backup
.\allaiis.exe backup restore D:\allaiis-backup --force
```

The vault is already encrypted; the backup adds a manifest with a SHA-256 per
file and `restore` refuses a file whose hash changed. Stop the gateway before a
restore.

## Upgrade

```powershell
.\allaiis.exe service remove
go build -o allaiis.exe ./core/cmd/allaiis
.\allaiis.exe doctor
.\allaiis.exe service install
```

The vault and config are untouched by a rebuild. `doctor` runs before you put
the new binary back on the task, and the service is registered again with the
new executable path.

## When something breaks

- `allaiis doctor` — local checks, no provider quota.
- `allaiis web quota` — remaining session counters and reset time.
- the panel at `http://127.0.0.1:8431/` — quotas, probes, logs, aliases.
- `allaiis smoke --json` — a full sweep through a throwaway gateway.

A `403` with `cloudflare_challenge` means the attestation went stale: run
`allaiis web chrome` and `web login` again for that label.

## Switching a client over

```powershell
.\allaiis.exe export opencode
```

That prints the provider block for OpenCode's config with the local key. The
gateway serves the OpenAI, Responses and Anthropic Messages shapes on the same
port, so any client that speaks one of them only needs the base URL
`http://127.0.0.1:8420/v1` and the key from `allaiis key`.
