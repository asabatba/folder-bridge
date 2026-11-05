# Safety/security remediation and rebuilt artifact

## Status

All three confirmed findings from `VALIDATION.md` have been addressed and covered by permanent regression tests. The user chose to **reject** junction/symlink paths rather than support aliases. Existing application processes, real settings, synced user files, registry startup settings and firewall rules were not changed.

This is not an exhaustive security audit or a claim that all desktop/LAN workflows have been manually validated.

## Changes

- `paths.go`, `links_windows.go`, `links_other.go`: inspect the selected path and its ancestors without following links. Windows also rejects other reparse-point tags, not just those Go identifies as symlinks. Missing suffixes are allowed only during pre-creation validation; an active sync root must exist and be a real directory.
- `main.go`: reject linked paths before creating directories; resolve the private-settings location before checking containment (including when AppData itself is aliased). Validate restored roots before startup cleanup, watchers or network services. All dashboard requests now require the exact authority `127.0.0.1:47831`; existing POST origin/content-type checks remain.
- `files.go`: validate the root before and after manifest walks, fail if the root is no longer a directory, and validate roots/ancestors on transfer paths. An unsupported root no longer produces a successful empty manifest. Nested transfer paths also reject Windows reparse points.
- `reliability.go`: validate the root before stale-transfer cleanup.
- `README.md` and `CHANGELOG.md`: document selected-path restrictions and the exact dashboard URL. No dependencies, protocol, baseline or configuration migration were introduced.

These are path checks around ordinary filesystem operations, not a new handle-relative filesystem sandbox. They do not claim to eliminate every adversarial local filesystem race or every alias mechanism such as hard links or alternate volume mappings.

## Permanent regression coverage

- `paths_windows_test.go`: reject a junction at the root or in an ancestor; do not create new folders through a junction; preserve files and returned baselines when either peer's configured directory is relocated and replaced by a junction; reject aliases of private settings and real parents containing aliased settings.
- `paths_test.go`: reject symbolic-link roots and ancestors; reject missing/file roots rather than returning empty manifests; allow ordinary new directories and normal scans/transfers.
- `security_test.go`: reject foreign, missing, `localhost`, wrong-port and lookalike authorities on dashboard GETs and POSTs; GET checks work without an Origin header; an allowed POST Origin cannot bypass authority checks. Rejected requests do not execute quit. The canonical local authority and local POST Origin still work; foreign/null POST origins remain forbidden.
- Existing dashboard tests in `v31_test.go` and `v32_test.go` now send the actual dashboard authority instead of httptest's default `example.com`.

Before production changes, the new targeted tests were run and reproduced junction acceptance, unintended deletions on both peers, private-settings alias acceptance, and unrestricted dashboard authority. The same targeted cases and the broader suites pass after remediation. The archived diagnostic file remains historical evidence; permanent tests above are the maintained suite.

## Final verification

Native Windows toolchain: `go1.27.3 windows/amd64`. Commands run from the project directory in Windows PowerShell unless noted.

| Check | Result |
| --- | --- |
| `go test -count=1 -v '-coverprofile=dist/coverage.out' -skip TestPauseResumeHostLifecycle ./...` | **PASS: 58 top-level tests**, no test-reported skips; 60.9% statement coverage. Includes real Windows symbolic-link and junction tests. |
| `go test -race -count=1 -skip TestPauseResumeHostLifecycle ./...` | **PASS**, no race reports. |
| `go vet ./...` | **PASS**. |
| `gofmt -l .` | **Clean**, no files listed. |
| Linux x64 test binary executed under WSL | **PASS: all 51 top-level tests**, including host pause/resume lifecycle and symlink tests. |
| Windows release build | **PASS**. |
| Built executable launched with `-h` | **PASS**, exit 0 with expected `-background` help. Does not start or validate the tray. |
| Isolated Windows `TestPauseResumeHostLifecycle` | Still **blocked/failing** because another existing application owns TCP 48713. Confirmed again after fixes; process left running. |

Active LSP probes were attempted on changed Go files. The server returned stale undefined-symbol diagnostics for newly created files and later inconclusive/time-out results; these are not a clean IDE validation. The stale findings were marked with evidence that the real Windows compiler/test run resolves the functions. Native compilation, `go vet`, the race detector and the Linux test execution are the verification evidence used here.

Linux test executable creation:

```powershell
$env:GOOS='linux'
$env:GOARCH='amd64'
$env:CGO_ENABLED='0'
go test -c -o dist/folderbridge-linux.test .
```

These environment variables were set only in a separate PowerShell process. WSL then ran `./dist/folderbridge-linux.test -test.v`.

Current logs/artifacts (refreshed after remediation):

- `dist/test-results.log`
- `dist/race-test-results.log`
- `dist/linux-test-results.log`
- `dist/windows-lifecycle-test.log`
- `dist/coverage.out`
- `dist/build-help.txt`

## Rebuilt executable

- **Path:** `dist/FolderBridge.exe`
- **Target:** Windows x64 GUI, unsigned.
- **Size:** 8,873,984 bytes (approximately 8.46 MiB).
- **SHA-256:** `F2B985D114BF006105838F0893D103088F32BD5D5FBDA22F9AA52069E83CF298`

```powershell
go build -trimpath '-ldflags=-s -w -H=windowsgui' -o dist/FolderBridge.exe .
```

The executable was rebuilt again after final test edits and the checksum remained identical. It was not installed or substituted for the running instance.

## Remaining validation

- Rerun the complete Windows suite with TCP 48713 free. Making the lifecycle test independent of the production port remains proposed follow-up work, not an implemented change.
- Interactive Windows tray/menu, notifications, dashboard, pause/resume, shutdown and opt-in launch-at-login checks.
- Authorized two-physical-computer LAN checks: pairing/firewall behavior, latency, nested transfers, renames, disconnect recovery, conflicts and deletion approval.

Use exactly `http://127.0.0.1:47831/` for the dashboard. If a previously selected folder uses a junction/symlink/reparse point in its path, select its real directory path; this rejection is intentional.
