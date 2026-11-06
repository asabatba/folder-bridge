# FolderBridge v3.3 updater

## Status and bootstrap

The updater implementation is available for Windows 10/11 x64. Production
activation requires a real offline-generated Ed25519 public key and a signed
published GitHub release. **Development builds without a key cannot update.**
No production signing key is generated or stored by the application or CI.

An unmodified v3.2 executable has no discovery code. Quit it and manually install
the first updater-enabled build in a user-writable application directory. Keep
its filename `FolderBridge.exe`. Existing `%AppData%\FolderBridge` settings,
pairing credentials, ignore files, baselines and indexes remain in place. Future
updates use the flow below. Updating one PC does not update its paired PC.

## User controls

- The app checks the latest stable release at startup and every 24 hours by
  default. Checks/downloads do not pause synchronization.
- Downloads happen automatically by default. The tray notification and dashboard
  show **Update ready — Restart to install** after verification.
- Select **Restart to update** in the tray, or approve the dashboard restart.
  Installation never occurs just because a download completed.
- **Skip this version** suppresses that release. A failed version is separately
  quarantined; **Retry failed version** explicitly retries it. A newer version is
  eligible without clearing the quarantine manually.
- The dashboard shows the current/available versions, check time, release-notes
  link, download/install state and last result. It also exposes update settings.
- Disabling updates cancels current downloads and disables scheduled/manual
  checks and installation. Already staged bytes are not executed.
- Unwritable directories and policy restrictions produce an error and a manual
  release link. FolderBridge never elevates, changes ACLs, disables security
  software or works around an endpoint restriction.

Backward-compatible configuration (missing fields use these defaults):

```json
{
  "updates": {
    "enabled": true,
    "checkIntervalHours": 24,
    "autoDownload": true,
    "autoInstall": false,
    "channel": "stable",
    "skippedVersion": ""
  }
}
```

Intervals may be 1–720 hours. Only `stable` is accepted; `autoInstall: true` is
rejected. Ordinary sync-settings requests cannot overwrite update settings.
Clear `skippedVersion` through the settings API/config if you want a skipped
release offered again. Stop the app before manually editing its configuration.

## Trust and artifact contract

The release source is fixed to `https://github.com/asabatba/folder-bridge` at
build time. Peers and dashboard settings cannot select another source or key.
The GitHub `/repos/asabatba/folder-bridge/releases/latest` response is only a
locator. Drafts/prereleases are rejected. Canonical stable versions use `X.Y.Z`
without leading zeroes; tags use `vX.Y.Z`. Older and equal versions cannot be
installed (only recovery can restore a previous executable).

Each published release has these assets, using the full semantic version:

```text
FolderBridge-v3.3.0-Windows.zip
FolderBridge.exe
FolderBridge-Updater.exe
manifest.json
manifest.sig
```

The ZIP contains **exactly** the two root-level executables. The manifest is:

```json
{
  "version": "3.3.0",
  "platform": "windows-amd64",
  "url": "https://github.com/asabatba/folder-bridge/releases/download/v3.3.0/FolderBridge-v3.3.0-Windows.zip",
  "sha256": "<ZIP SHA-256 hex digest>",
  "minimumUpdaterVersion": "1.0.0"
}
```

`manifest.sig` is the raw 64-byte Ed25519 signature of the **exact manifest
bytes**, including its final newline. It is not JSON, hex or base64. Base64 is
used only to transport the signature into the publishing workflow.

Both shipped executables embed the same public key. An empty/invalid embedded
key fails closed. Main-app staging, helper preparation and crash recovery all
verify signed evidence; a journal-supplied hash is not a substitute for a valid
release signature. Publication also verifies that standalone EXEs match the
signed ZIP contents.

Limits and checks:

- HTTPS only, normal system certificate validation and proxy policy. Initial
  artifact URLs must match the exact repository/tag/ZIP name, without userinfo,
  alternate ports, query strings or fragments.
- Redirects are bounded to five hops and these exact HTTPS hosts:
  `github.com`, `api.github.com`, `release-assets.githubusercontent.com`, and
  `objects.githubusercontent.com`. Corporate proxies are not bypassed for release
  traffic. No alternate peer or insecure source is attempted on failure.
- GitHub metadata ≤1 MiB; manifest ≤32 KiB; signature exactly 64 bytes.
- ZIP ≤64 MiB; each executable ≤64 MiB; total extraction ≤128 MiB.
- No extra files, directories, duplicate names, traversal, absolute paths,
  case aliases, alternate data streams, symlinks or non-regular entries.
- Both payloads must have Windows x64 PE headers. Unsupported updater minimums,
  invalid signatures and checksum mismatches are rejected before execution.
- ZIP download uses streaming I/O and three bounded attempts, with 1/2-second
  backoff. Failed partial downloads are discarded; the current EXE is untouched.

This protects against compromised mirrors/CDNs, network tampering, unsafe
archives and untrusted peers. It is not a sandbox against a malicious process
already running as the same Windows user, which could also modify the installed
application, its private settings or a file after validation. Installation paths
and existing ancestors containing Windows reparse points are rejected.

## Approval, shutdown and recovery

1. The app stages verified bytes under `%AppData%\FolderBridge\updates`. Verified
   staged bytes survive ordinary restarts, but are revalidated before reuse.
2. Explicit approval stops admitting new client cycles and host requests. Active
   operations are **not canceled**. Existing host writes/batches finish and the
   current client cycle persists its baseline. Failure to persist sync state
   aborts installation and leaves synchronization available.
3. The app persists configuration and launches a freshly extracted, verified
   helper. It exits only after the helper signals successful preparation.
   Settings such as `paused` are not changed to perform this drain.
4. The helper opens a real process handle and waits for app exit. A per-install
   Windows lease excludes concurrent helpers. It probes write permissions and
   creates `.folderbridge-install-*` on the application volume, with a flushed
   copy of the old EXE, signed release evidence and candidate executables.
5. A durable `.folderbridge-update.json` journal records progress before atomic
   same-volume rename/replacement. The live path is never deleted first. If
   replacement fails, the backup is preserved and restoration is attempted.
6. The helper launches the candidate with a random 256-bit health token and an
   ephemeral IPv4 loopback endpoint. No proxy or redirect is used for health.
   The app reports only after config/baseline load, local dashboard responsiveness
   and engine initialization succeed. A peer need not be online, and a deliberately
   paused or unconfigured installation can still be healthy.
7. The helper waits up to 30 seconds. Health must report the exact signed release
   version as well as the random token, so a mislabeled older binary cannot
   confirm a newer manifest. An unauthenticated signal, failed launch or
   initialization failure cannot confirm the update. It terminates only its own
   failed candidate before restoring and launching the previous EXE.
8. Successful installation retains `FolderBridge.previous.exe` until the next
   successful update. Failed versions/results are recorded separately in
   `%AppData%\FolderBridge\update-result.json` to prevent retry loops. The running
   app never changes pairing, ignore files, TLS keys, baselines or indexes on the
   helper's behalf.

After an interrupted, unconfirmed transaction, ordinary app startup delegates to
an independently verified recovery helper before starting synchronization. A
confirmed transaction is finalized, not downgraded; its installed executable is
remeasured against a fresh extraction of the signed archive before relaunch.
Partial restoration copies
are checked against the preserved backup and recopied if interrupted.

Sharing locks/security software may prevent even restoration. In that case the
journal, backup and signed evidence remain intact; there is no destructive
fallback or privilege escalation. Close only processes you are authorized to
close, then retry startup. If recovery still cannot run, quit FolderBridge and
restore the preserved EXE manually (`FolderBridge.previous.exe`, or `previous.exe`
in the journal's work directory). Never remove private settings or sync data.
Failed work directories are intentionally retained as recovery evidence; they
contain application binaries, not user sync data.

Atomic renames, flushed files and the journal improve crash recoverability but
cannot guarantee durability against hardware/controller failure. The tests inject
interrupted transaction states; actual power-cut testing remains a release check.

## Offline signing and GitHub Actions

The private key stays on the offline signing machine. **Do not put it into
GitHub secrets, Actions, this repository or the application directory.** Keep an
offline backup. Losing the key requires a manual trust/bootstrap upgrade; automatic
key rotation is not implemented.

1. On the offline machine, build/use `cmd/folderbridge-release`, then generate
   the production key outside the repository (example PowerShell path):

   ```powershell
   go run ./cmd/folderbridge-release --mode keygen `
     --private-key E:/offline-keys/folderbridge-release.private.pem
   ```

   The `.pub` sibling contains the base64 public key. Key files are created
   exclusively; existing files are never overwritten.
2. Set the repository **variable** `UPDATE_PUBLIC_KEY` to that public key. It is
   public, not a secret. Configure the GitHub `release` environment with required
   reviewers. No private signing material is configured in Actions.
3. Push an authorized stable version tag, e.g. `v3.3.0`. The release workflow runs
   native Windows tests/vet, embeds the public key and version in both EXEs,
   packages the ZIP, creates the manifest, and creates an **unpublished draft**.
   Release actions are pinned to immutable commits; shared Go caching and checkout
   credential persistence are disabled. Repository tokens are exposed only to the
   steps that need GitHub release access.
4. Download the exact draft assets/Actions artifact for offline review. Check the
   intended source/tag and binaries before signing. Do not reformat the manifest
   or rebuild the ZIP afterward. On the offline machine:

   ```powershell
   go run ./cmd/folderbridge-release --mode sign --dir ./reviewed-release `
     --private-key E:/offline-keys/folderbridge-release.private.pem
   ```

   This verifies the manifest/artifact contract and prints a base64 signature in
   addition to writing raw `manifest.sig`.
5. Run **FolderBridge release (offline signing)** manually in Actions with the
   existing draft tag and the base64 signature. The protected publishing job
   downloads the existing draft bytes (does not rebuild), verifies the embedded
   trust contract/signature/ZIP/standalone EXEs, uploads `manifest.sig`, then
   publishes the release as latest. Unsigned drafts cannot be discovered by users.

Local verification:

```powershell
go run ./cmd/folderbridge-release --mode verify --dir ./reviewed-release `
  --version 3.3.0 --public-key E:/offline-keys/folderbridge-release.private.pem.pub
```

For source builds, preserve `go build .` compatibility:

```powershell
$pin = (Get-Content -Raw E:/offline-keys/folderbridge-release.private.pem.pub).Trim()
$flags = "-s -w -H=windowsgui -X folderbridge/internal/update.PublicKey=$pin -X folderbridge/internal/update.Version=3.3.0"
go build -trimpath -ldflags $flags -o FolderBridge.exe .
go build -trimpath -ldflags $flags -o FolderBridge-Updater.exe ./cmd/folderbridge-updater
```

Without the `-X ...PublicKey=...` flag both binaries build normally, but updates
are unavailable. Merely setting an environment variable cannot change their
embedded trust key. No production key is checked into the source tree.

## Project layout and verification

- `main.go`: executable startup, health reporting and shutdown orchestration.
- `app.go`, `config.go`, `lifecycle.go`: app state, settings and sync lifecycle.
- `dashboard.go`, `ui.go`, `web/dashboard.html`: existing local dashboard and
  embedded assets, without a new UI framework.
- `updates.go`: narrow app adapter for updater controls and graceful drain.
- `internal/update`: trust, streaming download, staging, helper transaction,
  authenticated health and recovery. No dependency on the synchronization engine.
- `cmd/folderbridge-updater`: standalone installation/recovery executable.
- `cmd/folderbridge-release`: offline signing and artifact verification tooling.
- Existing synchronization/network/file modules remain in the root package.

`go test ./...` includes Windows-native subprocess, real executable-lock, ACL,
junction, helper-replacement, authenticated-health and recovery integration tests
on Windows. Tests build only disposable fixtures and never replace an installed
copy, alter production configuration, terminate unrelated processes or request
administrator rights. Real host/client transfer tests verify update drain and
baseline persistence. General trust/archive/network tests also run on Linux.

See `review/V33-VALIDATION.md` for executed checks and outstanding release gates.
The GitHub publication workflow, actual Windows 10 desktop, interactive tray/
notification checks and physical power-cut validation must not be claimed as
verified until performed.
