# Project validation

## In progress
- None; the three confirmed findings have been remediated and the executable rebuilt.

## Blocked
- Full Windows suite: `TestPauseResumeHostLifecycle` cannot bind TCP 48713 because another FolderBridge instance is running. Existing process was not stopped. Other 58 Windows tests pass; the isolated lifecycle failure was reconfirmed after remediation.
- Interactive Windows tray/login/notification checks and two-physical-computer LAN validation remain manual and unverified.

## Next
- Await user-directed manual validation or follow-up work. No process shutdown is authorized.

## Backlog
- Proposed: make the lifecycle test independent of the fixed production port.

## Done
- Git housekeeping: initialized `main`; added ignore rules for builds, logs/coverage and local secrets, plus LF line-ending attributes. Moved stray root coverage output to `dist/coverage-initial.out` without deleting artifacts. Verified the staged source-only snapshot: no whitespace errors, all files mode `100644`, no common private-key/token pattern hits, and Windows/WSL Git compatibility. No remote configured or running process changed.
- Remediated all three findings using the user-approved rejection policy for junction/symlink paths (including ancestors). Added startup/scan/transfer/cleanup checks and exact dashboard authority validation; no protocol/configuration migration or new dependency.
- Added permanent regression tests in `paths_windows_test.go`, `paths_test.go` and `security_test.go`; updated existing dashboard tests to use the canonical authority. Targeted tests reproduced failures before fixes.
- Final checks: 58 Windows tests pass (one explicit port-conflict exclusion), Windows race detector passes with the same exclusion, all 51 Linux tests pass, statement coverage is 60.9%, `go vet` passes, formatting is clean. LSP probes were stale/inconclusive; compiler and test evidence is recorded in `review/REMEDIATION.md`.
- Rebuilt `dist/FolderBridge.exe` (8,873,984 bytes), help smoke test exits 0. SHA-256: `F2B985D114BF006105838F0893D103088F32BD5D5FBDA22F9AA52069E83CF298`. Current logs are under `dist/`; manual desktop/two-PC checks remain unverified.
- Updated README/CHANGELOG and recorded remediation in `review/REMEDIATION.md`. Initial review evidence below is historical; `review/VALIDATION.md` links to current results.

### Initial review (superseded by remediation above)
- Reviewed the current snapshot against README; no Git metadata or separate spec/standards document was present.
- Built `dist/FolderBridge.exe` (Windows x64 GUI, 8,861,184 bytes); executable `-h` smoke test exits 0. Build checksum and exact commands are in `review/VALIDATION.md`.
- Windows: 50 top-level tests pass excluding the occupied-port lifecycle test; race detector passes with the same exclusion; statement coverage is 59.3%; `go vet` passes and `gofmt -l` is empty.
- Linux: cross-compiled the Linux test executable with the installed Windows Go toolchain and executed all 46 top-level tests successfully under WSL, including the lifecycle test.
- Reproduced three safety/security findings with isolated temporary data. Diagnostic test source is retained as `review/validation_probes_windows_test.go.txt` so known-failing probes do not affect normal tests. No application source changed.
- Saved test logs and coverage under `dist/`; wrote findings and remaining manual checks to `review/VALIDATION.md`.
