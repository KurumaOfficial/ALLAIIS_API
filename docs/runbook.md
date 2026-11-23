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
The launcher and script are `0600` and hold the passphrase in plain text; keep
the data directory off shared paths.

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

`service.cmd` runs `gui --no-open --ui 127.0.0.1:8431 --log-file <data>/logs/gateway.log --log-max-mb 32
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
.llaiis.exe update --check    # what is the latest tag
.llaiis.exe update            # download, verify the checksum, swap
```

`update` takes the build for this OS and architecture from the newest GitHub
release, checks it against the release's `SHA256SUMS`, renames the running binary
to `.old` and puts the new one in place. A private repository needs
`GITHUB_TOKEN` in the environment. Restart the gateway afterwards; the service
script does that by itself within five seconds.

Without a release, or to try local changes, rebuild by hand:

```powershell
.llaiis.exe service remove
go build -o allaiis.exe ./core/cmd/allaiis
.llaiis.exe doctor
.llaiis.exe service install
```

The vault and config are untouched by either way. `doctor` runs before the new
binary goes back on autostart.

## When something breaks

- `allaiis doctor` — local checks, no provider quota; parked labels and their wait are listed, so a `429` from a pool is explained; e2ee aliases are attested live, which spends no quota; `--offline` skips that one network check.
- `allaiis web quota` — remaining session counters and reset time.
- `allaiis web e2ee --label <label> --model <e2ee model>` — attests the enclave, prints what passed, and sends one sealed canary turn.
- the panel at `http://127.0.0.1:8431/` — quotas, probes, logs, aliases.
- `allaiis smoke --json` — a full sweep through a throwaway gateway; each result carries an error `code`, so a parked model (`insufficient_balance`) is told apart from a real break; `--allow <code>` (repeatable) counts an expected code, such as the deliberately bogus alias, without failing the run.

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
