# FolderBridge snapshot review and build validation

> Historical initial review. The three findings below have since been remediated. See [REMEDIATION.md](REMEDIATION.md) for current code changes, tests, build checksum and remaining validation. Build/test artifacts under `dist/` have been refreshed and now describe the remediated snapshot.

## Verdict (initial review)

**Build succeeds, but the snapshot is not fully validated for release.** Three safety/security findings were reproduced using isolated temporary folders and synthetic pairing data. No application source was changed. The existing FolderBridge process and its settings were left untouched.

This is a current-source review against `README.md`, not a commit/PR review: the provided folder has no `.git` metadata. No separate requirements specification or coding-standards document was found. This review is not an exhaustive security audit.

## Build

- Artifact: `dist/FolderBridge.exe`
- Toolchain: `go1.27.3 windows/amd64` (installed Windows Scoop toolchain).
- Command: `go build -trimpath '-ldflags=-s -w -H=windowsgui' -o dist/FolderBridge.exe .`
- Size: 8,861,184 bytes (approximately 8.45 MiB).
- SHA-256: `5F4D4DEDCC23451E904F93CCC054D4171ED9CFBE222BC8480BF790A1B23DEF0B`
- Verified as a Windows x64 GUI executable. Launched with `-h`: exit 0 and expected `-background` usage. This does not validate tray behavior or a live sync session.
- Unsigned; not installed and not substituted for the already-running application.

## Automated verification

Commands below are run from the project directory using Windows PowerShell unless noted.

| Check | Result |
| --- | --- |
| `go test -count=1 ./...` | Blocked/fails at `TestPauseResumeHostLifecycle`: TCP 48713 already belongs to another running FolderBridge instance. |
| `go test -count=1 -v -skip TestPauseResumeHostLifecycle ./...` | PASS: 50 top-level Windows tests, no test-reported skips. Log: `dist/test-results.log`. |
| `go test -race -count=1 -skip TestPauseResumeHostLifecycle ./...` | PASS; no race reports. |
| `go test -count=1 '-coverprofile=coverage.out' -skip TestPauseResumeHostLifecycle ./...` | PASS; 59.3% statement coverage. Profile moved to `dist/coverage.out`. |
| `go vet ./...` | PASS. |
| `gofmt -l .` | No unformatted Go files. |
| Windows release build | PASS. |
| Linux x64 test binary, executed under WSL | PASS: all 46 top-level Linux tests, including the host pause/resume lifecycle test. Log: `dist/linux-test-results.log`. |

Linux test binary creation used `$env:GOOS='linux'; $env:GOARCH='amd64'; $env:CGO_ENABLED='0'; go test -c -o dist/folderbridge-linux.test .` in a separate PowerShell process. WSL then executed `./dist/folderbridge-linux.test -test.v`.

The passing suite includes bidirectional sync/conflict/delete behavior, ignore rules, batching, rename optimizations, integrity/recovery/retries, and Windows-specific locked-file, watcher, move and tray-layout tests. Unit tests for Windows tray layout are not a substitute for interactive tray testing.

The occupied port was confirmed to belong to an executable in a different project directory. The lifecycle test fails reproducibly in isolation as well. No attempt was made to stop that process. An initial coverage invocation also hit PowerShell argument parsing (`.out` interpreted as a package); quoting the complete coverage argument resolved that invocation error.

## Findings

### 1. High — A configured folder replaced by a junction is interpreted as empty and can delete peer files

**Locations:** `main.go:77-100` (`validateFolder`), `files.go:44-119` (`Scanner.ScanManifest`), `sync.go:82-106` (bulk deletion threshold).

`validateFolder` uses `os.Stat`, accepting a directory junction as a directory. `ScanManifest` then skips the junction at the root and returns a successful empty manifest, rather than rejecting it. A previously valid configured directory can be moved and replaced by a junction to the same unchanged contents while retaining the configured pathname and baseline. The next reconciliation interprets its contents as deletions.

**Reproduced:** sync `important.txt`, relocate the client folder, replace its original pathname with a junction to the relocated contents, validate and reconcile the original pathname with the existing baseline. The host's previously synced file is deleted even though the client's content still exists at the junction target. With a one-file baseline, the >=20-file mass-deletion guard does not apply. Larger partial losses below the guard threshold are also not prevented by that threshold.

**Recommendation:** reject unsupported reparse-point roots and ancestors before enabling sync and during scans. Never return a successful empty manifest for an unsupported root. Supporting aliases intentionally instead requires consistent canonical-path, containment and scanner semantics.

### 2. High — Junction ancestors bypass the private-settings exclusion

**Locations:** `main.go:77-100` (`validateFolder`), `files.go:44-119` (`Scanner.ScanManifest`).

The private-settings protection compares lexical paths using `filepath.Rel` without resolving junction ancestors. Selecting `<junction-to-settings-parent>/settings` passes the check against the real settings directory. Because the selected final directory is not itself a junction, the scanner walks it normally and advertises its contents.

**Reproduced:** create a synthetic settings directory containing `config.json`, create a junction alias to its parent, and select `alias/settings`. Validation succeeds and the manifest includes `config.json`. No real user settings or secrets were accessed.

**Impact:** selecting such an alias can synchronize configuration, pairing credentials and TLS private keys, contrary to README's promise that folders containing application settings are refused.

**Recommendation:** validate resolved identity/containment, not just lexical containment, and apply one consistent policy for reparse points across folder selection, scanning and transfers.

### 3. Medium — Status accepts arbitrary Host headers and exposes pairing data

**Locations:** `main.go:409-439` (`status`), `main.go:447-471` (`uiHandler`).

The dashboard's GET endpoints do not validate `Host` or request origin. `/api/status` returns the joining invite or host pairing links. The existing origin/content-type checks protect POST actions only.

**Reproduced:** a handler request for `http://untrusted.example:47831/api/status`, with no `Origin` header, receives HTTP 200 and a synthetic configured invite.

**Impact:** this is a missing defense against DNS-rebinding-style access to a loopback service. Loopback binding and normal browser same-origin restrictions remain useful protections; the test does not establish a working browser exploit. Browser local-network protections may reduce exploitability. The verified defect is unrestricted Host acceptance on a credential-bearing endpoint.

**Recommendation:** validate the expected loopback authority for all dashboard requests and keep the existing POST origin checks. Consider an additional per-instance browser session secret as defense in depth.

## Reproduction source

`review/validation_probes_windows_test.go.txt` preserves four focused tests:

- Foreign-Host status rejection: **FAIL** (finding 3).
- Case-only rename convergence/content preservation over three cycles: **PASS** in this scenario.
- Junction-root rejection/data preservation: **FAIL** (finding 1).
- Private-settings alias rejection: **FAIL** (finding 2).

The file intentionally has a `.txt` suffix so diagnostic tests that assert missing protections do not make the normal suite permanently red. To reproduce on Windows, copy it temporarily to `review_probe_test.go` at the project root, run `go test -count=1 -v -run '^TestReview' ./...`, then remove the temporary root copy. Tests use only generated temporary folders and placeholder data. The junction tests create junctions with `cmd.exe /c mklink /J`; they do not need to stop the live app or use its configured folder.

## Outstanding validation

- Run the complete Windows suite when TCP 48713 is free; do not stop an existing session without permission.
- Interactive Windows tray/menu, pause/resume, error/conflict notifications, dashboard and shutdown checks.
- Opt-in registry launch-at-login and startup after signing out/in. Not changed during this review.
- Two physical Windows computers: authorized firewall behavior, TLS pairing, watcher-driven latency, nested file transfer, rename, disconnect/reconnect, conflicts and deletion approval.
- Fix the findings and promote the diagnostic cases into permanent regression tests before treating this as a validated release.

No bug fixes or additional product scope were implemented as part of the review.
