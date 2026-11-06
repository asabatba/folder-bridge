# Module-refactor validation

## Scope and status

The user-approved option B is implemented: one thin root launcher, `internal/app`,
`internal/syncengine`, shared `internal/fileops`, and the existing independent
`internal/update`. See [architecture](../docs/ARCHITECTURE.md).

This refactor follows implementation commit `3871d66`. Mechanical relocation,
filesystem extraction and engine extraction were each checked before proceeding.
It does not activate production updates, change protocol/state formats, introduce
module dependencies or fix the deferred fixed-port test. The running installed
application was not stopped or replaced. No tag, push or publication was performed.

## Staged checks

- Stage 1: mechanical relocation and thin root launcher; Windows tests with the
  known port exclusion, root build and vet pass (`dist/refactor-stage1-tests.log`).
- Stage 2: shared guarded filesystem/persistence extraction; Windows app/fileops
  tests and vet pass (`dist/refactor-stage2-tests.log`).
- Stage 3: engine lifecycle/status/drain ownership and app integration; Windows
  tests, root build and vet pass (`dist/refactor-stage3-tests.log`).
- Final checks below include private implementation types, behavior-owned tests,
  external-consumer engine tests and module-dependency guards.

## Final Windows checks

Native Windows 11 Pro x64, version 10.0.26300, using Go 1.27.3 windows/amd64:

```powershell
go test -v -count=1 -skip '^TestPauseResumeHostLifecycle$' ./...
go test -race -count=1 -skip '^TestPauseResumeHostLifecycle$' ./...
go vet ./...
go test -count=1 -run '^TestPauseResumeHostLifecycle$' ./internal/app
```

| Package | Passing top-level tests |
|---|---:|
| `internal/app` | 21 |
| `internal/fileops` | 7 |
| `internal/syncengine` | 45 |
| `internal/update` | 14 |
| Total | 87 |

The normal suite, race detector and vet pass with the one explicit exclusion.
The separately rerun lifecycle test still fails to bind TCP 48713, occupied by the
existing application. It was not stopped; CI has no exclusion and continues to
run `go test ./...` on its own runner. Root/command packages compile and have no
separate test files.

Logs: `dist/refactor-windows-tests.log`, `dist/refactor-windows-race.log`,
`dist/refactor-windows-vet.log`, `dist/refactor-windows-lifecycle.log`.

## Final Linux / WSL checks

Using a child PowerShell environment with `GOOS=linux`, `GOARCH=amd64`,
`CGO_ENABLED=0`, cross-build each test package and the root application:

```powershell
go test -c -o dist/refactor-linux-app.test ./internal/app
go test -c -o dist/refactor-linux-engine.test ./internal/syncengine
go test -c -o dist/refactor-linux-fileops.test ./internal/fileops
go test -c -o dist/refactor-linux-update.test ./internal/update
go build -o dist/refactor-linux-app .
go vet ./...
```

Execute all four test binaries from the repository directory under WSL using
`-test.v`. **All 77 top-level tests pass:** app 18, engine 42, fileops 5, updater
12. This includes the lifecycle test excluded on Windows. These tests were
executed, not merely cross-compiled. Linux build/vet pass. Linux race detection
was not run; Windows race detection was.

Logs: `dist/refactor-linux-{app,engine,fileops,update}.log`.
The project still declares Go 1.23; an actual Go 1.23 CI run is not claimed.

## Boundary and behavior coverage

- App controls/configuration preserve pairing, pause/update settings and exact
  dashboard authority/origin checks. Ignore inspection is exercised through the
  HTTP adapter against local rules and a real pinned-TLS peer fixture.
- Engine concrete host/peer/scanner/manifest types are private. External-consumer
  tests cover paused lifecycle, detached bounded history and synchronous rejection
  of an unreadable legacy-named baseline during candidate initialization.
- Real client and host transfers drain without cancellation before handoff;
  baseline persistence precedes the callback. Failed handoff resumes admission,
  unpersisted state refuses installation, and successful handoff gates new work.
- Filesystem tests follow their owner, covering interrupted streams, checksum
  failure, temporary-file cleanup, concurrent-edit guards, path validation,
  Windows locked replacement and no-overwrite moves. Engine tests retain linked
  roots/scans/transfers and protocol/reconciliation coverage.
- Dependency guards parse all platform variants and reject reverse module
  dependencies or additional root Go sources.
- The independent updater/helper/release-tool implementation and workflow build
  targets remain unchanged; both executables still build from their existing
  targets and linker trust-key/version symbols.

## Builds and release-tool smoke

Development builds are separate from the running executable and historical
`dist/v3.3` artifacts:

```powershell
go build -trimpath -ldflags="-s -w -H=windowsgui" -o dist/refactor/FolderBridge.exe .
go build -trimpath -ldflags="-s -w -H=windowsgui" -o dist/refactor/FolderBridge-Updater.exe ./cmd/folderbridge-updater
```

| File | Bytes | SHA-256 |
|---|---:|---|
| `FolderBridge.exe` | 9,104,896 | `1c95788271c9633d13f18e18cd3b365c905f1f1944c0e4ac7d8097e6d06baded` |
| `FolderBridge-Updater.exe` | 7,178,752 | `26e50fd930db0a0b4209f705203fd6cbb31157f870f61daf8937bd860328c542` |

Both native `-h` smoke checks exit 0 (`dist/refactor-help.log`). These development
builds lack a production trust key and fail closed.

A separate build of both executables with the existing **test-only** public key
and version 3.4.0 was packaged, signed offline with the existing ignored test key,
and verified through `cmd/folderbridge-release`. Exact signed ZIP/standalone
payload verification passes (`dist/refactor-release-smoke.log`;
`dist/refactor-release-smoke/`). No production key was generated or stored, and
nothing was uploaded or published.

## Browser smoke

The moved dashboard asset was loaded in an isolated native Microsoft Edge
Playwright session against a disposable HTTP fixture, not the installed app.
Version/ready state renders, loading causes no mutation, dismissing approval
causes no install, acceptance produces exactly one fixture installation request,
and waiting disables repeat installation. Desktop and 390px-wide layouts have
no horizontal overflow. Browser console: zero errors/warnings.

Artifacts: `dist/refactor-dashboard.png`,
`dist/refactor-dashboard-mobile.png`, `dist/refactor-dashboard-snapshot.md`,
`dist/refactor-browser-validation.log`, `dist/refactor-browser-console.log`.
The CLI briefly reported stale modal state; an extra dismiss found no active
dialog. Subsequent explicit assertions confirm the single approved request and
disabled button. This is a CLI trace issue, not an application-console error.
The browser and owned fixture were closed; the fixture exited after its final
keep-alive connection drained. No installed app actions were performed.

## Diagnostics and remaining gates

Active LSP probes covered 19 critical source files: 11 confirmed clean, six
unconfirmed (timeout or silent push-only server), and two auxiliary audit hits.
The hits were individually reviewed: the helper launch uses a fixed executable
freshly extracted from a signed/hash-verified archive without a shell; the SQL
rule matched an activity-log string, not database execution. Their dispositions
are session-only false positives, with no suppression comments or disabled rules.
Incomplete LSP coverage is not evidence of universal cleanliness; compiler,
tests, race detection and vet provide the executed verification above.

Production signing activation, actual Actions publication, Windows 10/11 real-app
upgrade/rollback, interactive tray/login/notifications, two-physical-computer LAN
acceptance and physical power-cut testing remain outstanding as recorded in
[V33-VALIDATION.md](V33-VALIDATION.md). The module refactor does not complete those
release gates or authorize stopping the existing application.
