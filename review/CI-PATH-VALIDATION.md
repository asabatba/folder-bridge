# Windows CI private-folder validation fix

## Failure and reproduction

The user supplied Windows Server 2025 Actions logs using Go 1.23.12. Both the
pre-refactor `70ff694` tests and release tag `v3.3.0` at `f6009cc` failed with:

```text
--- FAIL: TestOrdinaryFolderSelectionAndScan
    paths_test.go:41: selected private settings directory
```

The failure was reproduced locally on Windows 11 with the same Go 1.23.12
version and a native temporary directory referenced by its real NTFS 8.3 alias.
The unchanged original test failed with exactly that assertion. This was not
caused by the refactor, a busy port, signing configuration or Node/cache warnings.

Cause: `filepath.EvalSymlinks(home)` expands short Windows names, but the selected
folder retained its original spelling. A string-based `filepath.Rel` check can
miss containment across short/long names or normal/extended Windows prefixes.
This was an actual private-settings exclusion bypass, not a test to skip.

Before-fix evidence: `dist/ci-folder-repro-before.log`.

## Fix and permanent regression

`internal/app/config.go` now compares `os.SameFile` filesystem identities against
the resolved private settings directory and its ancestors. Existing selected
folders that contain the private directory are rejected regardless of spelling.
An absent selected folder cannot contain the already-existing private directory.
Existing link/junction rejection still occurs before selection; mkdir/root
validation and returned configuration path spelling are unchanged.

`TestRejectPrivateSettingsPathSpellings` in `internal/app/paths_windows_test.go`
uses real Windows APIs to cover eight cases: selected settings directory or its
parent, with 8.3 aliases or extended prefixes on either input. Six cases failed
before the fix; all eight pass afterward. On a volume without short-name support,
extended-prefix coverage still runs and absent 8.3 coverage is logged explicitly.
Actual local runs exercised all eight cases, with no skipped aliases.

Logs: `dist/ci-folder-regression-before.log`,
`dist/ci-folder-regression-after.log`.

## Executed verification

The declared minimum Go version was tested through an isolated downloaded Go
1.23.12 toolchain cache under ignored `dist/toolchain-cache`; no default Go
installation, module dependency or `go.mod` setting was changed.

- **88 native Windows tests pass** under Go 1.23.12 with a real short-name TMP:
  22 app, 7 fileops, 45 syncengine and 14 updater tests.
- **The same 88 tests pass with Windows race detection** and short-name TMP.
  The first concurrent attempt exceeded the 240-second harness budget after
  app/fileops/syncengine passed; a serial-package retry (`GOFLAGS=-p=1`) completed
  successfully, including updater integration. No tests were disabled for that
  retry beyond the pre-existing occupied-port exclusion below.
- Windows Go 1.23.12 root build and `go vet ./...` pass.
- **All 77 Linux tests**, cross-built with Go 1.23.12 and executed under WSL,
  pass, including the lifecycle test. Linux vet passes; Linux race was not rerun.
- Both the original failing test and new regression also pass with the default
  native Go 1.27.3 installation.
- Formatting and whitespace checks pass. Active LSP probes returned no findings
  but were inconclusive; they are not treated as proof of clean code.

Windows tests/race retain only the existing explicit
`-skip '^TestPauseResumeHostLifecycle$'` because the installed app owns TCP 48713.
The application was not stopped or replaced. Hosted CI has no such exclusion.

Reproduction runner (ignored local artifact): `dist/ci-folder-short-temp.ps1`.
Logs: `dist/ci-folder-windows-tests.log`,
`dist/ci-folder-windows-race-retry.log`, `dist/ci-folder-build-vet.log`,
`dist/ci-folder-linux-{app,syncengine,fileops,update}.log`.

## Release status

The local fix has not been verified by a new hosted Actions run. The existing
`v3.3.0` tag still selects `f6009cc`; rerunning that tag does not pick up this fix.
The fix is committed in `05a3b3d`; this report retains pre-updater-removal test
counts. Current checks are in [UPDATER-REMOVAL.md](UPDATER-REMOVAL.md).
Push and any release-tag decision require user direction. No agent push,
tag change, publication, production key handling or running-app change occurred.
The user's unrelated README whitespace edit was preserved.
