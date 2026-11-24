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
launcher in plain text, so the vault is only as protected as that file. The
launcher sits in the per-user Startup folder, which Windows restricts to that
user and administrators; the `0600` bits Go sets on the file do not restrict
other Windows accounts. Keep the data directory off shared paths.

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
.\allaiis.exe update --check    # what is the latest tag
.\allaiis.exe update            # download, verify the checksum, swap
```

`update` takes the build for this OS and architecture from the newest GitHub
release, checks it against the release's `SHA256SUMS`, renames the running binary
to `.old` and puts the new one in place. A private repository needs
`GITHUB_TOKEN` in the environment. Restart the gateway afterwards; the service
script does that by itself within five seconds.

Without a release, or to try local changes, rebuild by hand:

```powershell
.\allaiis.exe service remove
go build -o allaiis.exe ./core/cmd/allaiis
.\allaiis.exe doctor
.\allaiis.exe service install
```

The vault and config are untouched by either way. `doctor` runs before the new
binary goes back on autostart.

## Cutting a release

1. Set the version in `core/cmd/allaiis/gui.go` (`var version`).
2. Turn the top `Unreleased` section of `CHANGELOG.md` into `## vX.Y.Z` with one
   sentence saying what the release is about.
3. Commit, then tag and push both: `git tag vX.Y.Z` and `git push origin main vX.Y.Z`.
4. The release workflow builds five targets plus `SHA256SUMS`; wait until the
   release page lists them.
5. Run `update --check` and then `update`; the service swaps the binary and
   restarts within five seconds.

## When something breaks

- `allaiis doctor` — local checks, no provider quota; parked labels and their wait are listed, so a `429` from a pool is explained; The `web plan` line prints each stored session's chat allowance, plan and
credit balance, so a `402` can be read without guessing: paid models need
credits, free models do not. e2ee aliases are attested live, which spends no
quota, and a guest session pinned to one is flagged since it cannot run e2ee; `--offline` skips that one network check.
- `allaiis web quota` — remaining session counters and reset time.
- `allaiis web e2ee --model <e2ee model>` — attests the enclave for every stored session (or one with `--label`), prints what passed, and sends one sealed canary turn.
- the panel at `http://127.0.0.1:8431/` — quotas, probes, logs, aliases.
- `allaiis smoke --json` - a full sweep through a throwaway gateway; each result carries an error `code`, so a parked model (`insufficient_balance`) is told apart from a real break; `--allow <code>` (repeatable) counts an expected code, such as the deliberately bogus alias, without failing the run. The tool probe asks the route in its own shape (Chat, Messages or Responses), so a Messages alias is not judged by a Chat request. A sweep is not free: every alias that answers spends one message, and the tool probe spends one more, so `--no-tools` or a single `--alias` keeps it cheap.

A `403` with `cloudflare_challenge` means the attestation went stale: run
`allaiis web chrome` and `web login` again for that label.

## Switching a client over

```powershell
.\allaiis.exe service status     # the gateway must already be up
.\allaiis.exe doctor             # no errors, and the alias you want is listed
.\allaiis.exe export opencode
```

`export opencode` prints the provider block for OpenCode's config with the local
key, and each alias carries the limits and capabilities it declares (context,
output, tools, vision, reasoning), so the generated block needs no hand-tuning. The gateway serves the OpenAI, Responses and Anthropic Messages shapes on
the same port, so any client that speaks one of them only needs the base URL
`http://127.0.0.1:8420/v1` and the key from `allaiis key`.

Pick an alias that answers: free models keep working when the plan is spent,
while a paid or `e2ee` alias answers `402` until the label has balance. An `e2ee`
alias also needs a signed-in label and spends an attestation round trip; its
sealed reply is verified on a free `e2ee-*` model, so start with a free alias and
move over when the first `allaiis web e2ee` canary decrypts.
