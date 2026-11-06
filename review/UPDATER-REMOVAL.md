# Self-updater removal validation

## Scope

Implemented the user's complete-removal decision and release option A. The app
retains its thin launcher and app/syncengine/fileops modules, ordinary sync
behavior and Windows private-settings path-identity fix.

Removed `internal/update`, both commands under `cmd`, update UI/tray/settings,
background downloads, helper/recovery/health/signing tooling, engine quiescence
and installation admission gates, candidate-only baseline checks and tests of
the removed features. No updater compatibility shim or dependency was added.
Ordinary baseline persistence/error reporting and pause/stop cancellation remain.

Version display now comes from standalone `app.Version` in `/api/status`.
Removed update routes return 404. New regressions cover version display, absent
routes/UI/source/commands and six-field synchronization configuration. The engine
still refuses to reconcile or rewrite an unreadable baseline.

The replacement workflow tests/vets on Windows, builds one tagged-version EXE
and creates a **draft**. It does not publish, sign or install anything. Operator
instructions: [RELEASING](../docs/RELEASING.md), [UPDATING](../docs/UPDATING.md).

## Executed Windows checks

Native Windows 11, Go **1.23.12**, isolated toolchain cache under ignored `dist`:

```powershell
$env:GOTOOLCHAIN = 'go1.23.12'
$env:GOMODCACHE = Join-Path $PWD 'dist/toolchain-cache'
go test -count=1 -v -skip '^TestPauseResumeHostLifecycle$' ./...
go test -race -count=1 -v -skip '^TestPauseResumeHostLifecycle$' ./...
go vet ./...
go build -trimpath -ldflags "-s -w -H=windowsgui -X folderbridge/internal/app.Version=3.3.1" -o dist/manual/FolderBridge.exe .
```

**70 tests pass normally and under race detection:** app 21, fileops 7,
syncengine 42. These minimum-Go runs use a genuine NTFS short-name TMP via
`dist/ci-folder-short-temp.ps1`; the retained eight-case private-settings alias
regression passes. The default Go 1.27.3 normal suite also passed with the same
exclusion before documentation edits.

The only Windows exclusion is the existing lifecycle test binding production
TCP 48713, occupied by the running installed app. It was not stopped. CI retains
`go test ./...` with no exclusion. Windows vet/root GUI build and executable
`-h` smoke pass (exit 0).

Logs: `dist/no-updater-windows-tests.log`, `dist/no-updater-min-windows.log`,
`dist/no-updater-min-race.log`, `dist/no-updater-build-vet.log`,
`dist/no-updater-help.{out,err}`.

## Linux / WSL

Using the same Go 1.23.12 toolchain with `GOOS=linux`, `GOARCH=amd64`,
`CGO_ENABLED=0`, cross-build each package using `go test -c`, then execute all
three test binaries under WSL with `-test.v`.

**All 61 tests execute and pass:** app 17, syncengine 39, fileops 5. This includes
the lifecycle test excluded on Windows. Linux root build and vet also pass.
Linux race detection was not rerun; Windows race detection was.

Logs: `dist/no-updater-linux-{app,syncengine,fileops}.log` and the build/vet log.

## Dashboard and workflow

- Native Edge/Playwright against the embedded asset with mocked local APIs:
  arbitrary version `9.8.7` renders from ordinary status; no update panel,
  background update requests or mutation on load; normal configuration sends no
  update settings; saving/starting and pausing work. Desktop and 390px layouts
  pass with no horizontal overflow and zero console errors/warnings.
- This is a fixture smoke, not interaction with the running installed app.
  Screenshots/logs: `dist/no-updater-dashboard{,-mobile}.png`,
  `dist/no-updater-dashboard-snapshot.md`, `dist/no-updater-browser{,-console}.log`.
- Release YAML parses; all three PowerShell run steps parse with the native
  PowerShell parser. Static checks confirm one job, draft-only publication and
  no old signing variable/dispatch inputs. The equivalent versioned GUI build
  passes locally. GitHub upload/draft creation has **not** been executed here.
- Active diagnostics checked ten changed Go paths: six confirmed clean, two with
  reviewed auxiliary false positives (static HTTP response writes and a ticker
  Stop misclassified as WaitGroup.Done), two inconclusive. No claim of universal
  LSP cleanliness; executed compiler/test/vet checks are the primary evidence.

## Build artifact

`dist/manual/FolderBridge.exe` is a development smoke build displaying `3.3.1`,
not a published release or a change to any Git tag:

- Bytes: **7,232,512**
- SHA-256: `8f0448ce549abb47ea19aee70750f34582e74377500547f264dd724472a639b3`

No helper is built. Existing historical binaries, keys, private app settings and
synchronized folders were not deleted or migrated.

## Remaining limits and handoff

- The user authorized a local commit after verification. No push, tag change or
  publication was performed. Hosted verification needs a user-directed push and
  an unused version tag;
  existing `v3.3.0` points to the old updater commit `f6009cc`.
- Windows 10/11 interactive tray/login/notifications, real manual upgrade/restore,
  two-physical-PC LAN and physical power-cut acceptance remain unverified.
- Former signing-key activation/helper rollback gates no longer apply. Historical
  reports remain for evidence, not as current operating/release instructions.
- Earlier path-fix evidence is retained in `review/CI-PATH-VALIDATION.md`.
  Unrelated user work was preserved. The running app was not stopped or replaced.
