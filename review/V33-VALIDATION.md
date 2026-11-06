# v3.3 implementation and validation

This records the initial v3.3 implementation at commit `3871d66`; root-package
paths and test counts below are historical. The subsequent approved module
extraction and current checks are in [REFACTOR-VALIDATION.md](REFACTOR-VALIDATION.md).

## Status

The self-updater and approved focused refactor are implemented and automated
checks below pass. **This is not a completed production release.** Production
trust-key activation, GitHub publication and the manual acceptance gates below
remain outstanding. No production private key was generated or stored here.

Agreed decisions:

- Fixed public GitHub repository: `asabatba/folder-bridge`.
- Offline Ed25519 signing. CI builds an unpublished draft, then verifies an
  externally signed manifest before publishing; it never receives the private key.
- Fail-closed source/development builds without an embedded public key.
- Focused refactor, retaining `go build .`, the root sync-engine package, existing
  pairing/protocol/baseline formats and the existing dashboard styling.

See [docs/UPDATING.md](../docs/UPDATING.md) for architecture, artifact format,
configuration, recovery, bootstrap and operator instructions.

## Environment and executed checks

Native Windows execution used **Windows 11 Pro x64, 10.0.26300**, and native Go
**1.27.3 windows/amd64**. Linux test executables were cross-compiled with that Go
installation and executed under WSL, not merely cross-compiled. The repository
still declares Go 1.23; the workflow uses `go-version-file: go.mod`. An actual
Go 1.23 CI run is not claimed here.

### Windows

```powershell
go test -v -count=1 -skip '^TestPauseResumeHostLifecycle$' ./...
go test -race -count=1 -skip '^TestPauseResumeHostLifecycle$' ./...
go vet ./...
```

- **76 top-level tests pass:** 62 in the main package, 14 in `internal/update`.
  Native integration subtests are additional to that top-level count.
- Windows race detection passes with the same single exclusion.
- `go vet ./...` passes. Formatting and `git diff --check` are clean.
- Commands under `cmd/` compile successfully; they have no separate unit-test
  package. Their release operations are exercised by the native smoke test below.
- The excluded existing lifecycle test was run separately again and still fails
  because another FolderBridge instance owns TCP 48713. It was **not stopped**.
  No exclusion is built into CI; CI still runs the entire suite on its own runner.

Current logs:

- `dist/v33-windows-tests.log`
- `dist/v33-windows-race.log`
- `dist/v33-windows-vet.log`
- `dist/v33-windows-lifecycle.log`

The earlier full-run log also contains a now-fixed test assertion for the old
hard-coded `v3.2` dashboard label. `v31_test.go` now checks the dynamic version
label element; `updates_test.go` verifies the application version in update status.
The latest normal/race logs above are the current regression evidence.

### Linux / WSL

Using a child PowerShell environment with `GOOS=linux`, `GOARCH=amd64`,
`CGO_ENABLED=0`:

```powershell
go test -c -o dist/v33-linux-root.test .
go test -c -o dist/v33-linux-update.test ./internal/update
go vet ./...
```

Then execute under WSL:

```bash
./dist/v33-linux-root.test -test.v
./dist/v33-linux-update.test -test.v
```

**All 67 top-level tests pass:** 55 main-package tests, including the host lifecycle
case excluded on Windows, plus 12 platform-neutral updater tests. Windows-native
subprocess/ACL/junction tests are correctly Windows-only. Linux race detection
was not run in this validation; Windows race detection was.

Logs: `dist/v33-linux-root.log`, `dist/v33-linux-update.log`.

## Acceptance coverage

| Requirement | Executed evidence |
|---|---|
| Valid signed release | Exact-byte signature verification, streamed signed fixture download, real standalone helper install and authenticated health confirmation |
| Invalid/missing signature | Rejected, including altered exact manifest bytes and a build without a valid embedded public key |
| Modified archive | SHA-256 rejection; cached staged release is revalidated across restarts and modified bytes cannot become ready |
| Network interruption | Interrupted response retried from byte zero; cancellation and bounded-response tests pass; installed app is untouched |
| Unsafe ZIP / extraction bounds | Traversal, absolute/Windows/ADS names, duplicates, aliases, extra files, symlinks, excessive size and non-x64/non-executable headers rejected |
| Same/older/incompatible versions | Stable version ordering, same-version suppression, platform/minimum-updater checks; an actual candidate reporting an older version than its signed manifest rolls back |
| Skip / quarantine / retry | Skipped versions do not download/prompt; failed versions are blocked; explicit retry survives restart but a subsequent failure has a new transaction ID and is quarantined again |
| Graceful client shutdown | A real HTTPS file transfer finishes and its baseline is persisted before handoff; active context is not canceled |
| Graceful host shutdown | A real admitted host write finishes before handoff; new work is gated; no partial published file |
| State persistence error | Installation is refused if the last baseline could not be persisted; failed preparation resumes ordinary synchronization |
| Startup failure / authentication | Real failed-process and wrong-token cases restore and launch the preserved old executable |
| Health deadline | Cancellation/deadline path kills only the helper's candidate and rolls back; the production deadline is 30 seconds, with accelerated context used in the test |
| Interrupted replacement | Prepared/replacing/testing phases and a partial restoration copy recover to the preserved old EXE |
| Interrupted confirmation | Confirmed transactions finish without downgrade, both before and after moving the retained backup; a modified confirmed candidate is refused before relaunch |
| Locked executable | Real native sharing lock produces controlled failure, leaving live bytes, backup and recovery evidence intact |
| Unwritable directory | Real temporary Windows ACL denial produces manual-upgrade guidance without elevation or permission bypass |
| Linked installation path | Real Windows junction ancestor is rejected |
| Existing v3.2 configuration | Defaults are added without changing pairing/secret/pause/history; saving update settings and sync settings preserves their separation |
| Last installation result | The new app can read a helper result written after app startup, without requiring another restart |

Permanent tests:

- `updates_test.go`
- `internal/update/verify_test.go`
- `internal/update/install_windows_test.go`
- Disposable native process source: `internal/update/testdata/health-app/main.go`

**Limits of this evidence:** native helper integration uses disposable Go app
fixtures with real Windows executable/process behavior, not the installed running
FolderBridge instance. Real synchronization drain/baseline behavior is tested
separately through the app adapter and sync engine. A complete real-app upgrade
between two released FolderBridge versions remains a manual release gate. Crash
states are injected; no physical power-cut test is claimed.

## Dashboard browser checks

The final embedded dashboard was loaded in an isolated headless **Microsoft Edge**
context through Playwright on Windows. Its HTTP fixture used disposable mocked
status/actions, not the installed application's settings or ports.

Verified:

- Current/available versions, release links and last installation result render.
- Loading the page causes no installation/settings mutation.
- Dismissing restart approval sends no installation request; accepting it sends
  exactly one request and disables repeat installation while waiting.
- A verified staged update remains installable after a check/network error.
- Saving disabled updates and a 48-hour interval preserves `autoInstall: false`,
  the stable channel and the skipped-version field; check actions become disabled.
- No browser console errors/warnings. At 390px width there is no horizontal
  overflow; desktop and narrow layouts retain the existing styling.

Artifacts (ignored, local): `dist/v33-dashboard.png`,
`dist/v33-dashboard-mobile.png`, `dist/v33-dashboard-snapshot.md`,
`dist/v33-browser-validation.log`, `dist/v33-browser-console.log`.
All fixture/browser processes created for this check were closed. No production
tray, login/startup registry or actual OS notification was interacted with.

## Release tooling and workflow

A local **test-only** Ed25519 key, stored under ignored `dist/`, was used to:

1. Build both native Windows executables with a test public key and version 3.4.0.
2. Package the two EXEs and exact manifest through `cmd/folderbridge-release`.
3. Sign offline through the signing command, then verify the exact signature,
   ZIP, x64 payloads and equality of standalone EXEs with signed ZIP contents.
4. Verify again using the base64 signature transport with an existing identical
   `manifest.sig`, covering a publication retry without overwriting different bytes.

These operations pass. This key is **not** a production key and is not tracked.
`dist/v33-release-smoke.log` and `dist/v33-release-smoke-path.txt` identify the
current local smoke output.

The release workflow YAML parses; all six embedded PowerShell run steps pass the
native PowerShell parser (`dist/v33-release-syntax.log`). Action references were
resolved from their upstream Git tags and pinned to immutable commit SHAs. Go
caching and checkout credential persistence are disabled for release jobs; the
repository token is exposed only to steps using GitHub releases. Workflow static
checks were rerun after fixing the unpinned-action/cache findings.

**No tag, draft, release, push or actual GitHub Actions run was performed.** Local
syntax/tool smoke checks are not evidence of successful GitHub publication.

## Final development builds

Native Windows x64 GUI executables are under `dist/v3.3/`, deliberately separate
from the existing `dist/FolderBridge.exe` so the running instance is not replaced.
Both build from the root repository with no new module dependency:

```powershell
go build -trimpath -ldflags="-s -w -H=windowsgui" -o dist/v3.3/FolderBridge.exe .
go build -trimpath -ldflags="-s -w -H=windowsgui" -o dist/v3.3/FolderBridge-Updater.exe ./cmd/folderbridge-updater
```

| File | Bytes | SHA-256 |
|---|---:|---|
| `FolderBridge.exe` | 9,082,368 | `43414278880c7b3a91064df3cecdb6ae0b3fd0feb3fc0dbdbbe233e2d41b6abc` |
| `FolderBridge-Updater.exe` | 7,178,752 | `50df65967861552cb6c297b588dda6bce46b7b857063718b34724ed42f6ffe90` |

The app's `-h` smoke check exits successfully. **These two development builds have
no production public key; their updater fails closed.** They are not signed
release assets and have not replaced the running application.

## Diagnostic notes

Active LSP probes were performed repeatedly on changed files. Some completed
cleanly; others timed out, were push-only/silent on clean, or temporarily retained
stale missing-symbol findings after the file split. Empty/inconclusive probes are
not treated as proof of clean code. Native compiler, full applicable tests, race
detection and `go vet` are the definitive verification recorded above.

Generic command-execution audit findings were reviewed individually: launches use
fixed canonical executable names, independently signed/checksummed extractions,
remeasured confirmed candidates, or the hash-checked authorized previous backup.
No shell is used. Their false-positive dispositions are session-only; no blanket
rule-disable config or inline suppression was added. An existing SQL rule hit on
an activity-log string was also not SQL execution. Publication action/cache
findings, unlike these generic launch audits, were fixed in the workflow.

## Remaining release gates

- Generate the **production** Ed25519 key on the offline signing machine; supply
  only its public key as `UPDATE_PUBLIC_KEY` and configure required reviewers on
  the GitHub `release` environment.
- Run the real draft build, offline review/signing and protected publishing flow.
  Verify discovery against the resulting public signed stable release.
- Manually bootstrap an existing v3.2 installation once, then perform a complete
  real-app approved upgrade/rollback on Windows 10 and Windows 11. Native Windows
  11 test execution above is not a substitute for the Windows 10 desktop check.
- Interactively check tray actions, OS notifications, dashboard reconnect after
  restart, preserved autostart behavior and both paired computers' sync state.
- Validate actual power-loss recovery on disposable data/hardware and authorized
  corporate endpoint restrictions. Filesystem/journal tests cannot establish
  hardware/controller durability or every enterprise policy.
- Rerun the existing Windows host lifecycle test when TCP 48713 is free, only
  after the user authorizes any required shutdown. Making this test port-independent
  remains proposed backlog, not an unapproved scope expansion.
