# Equal-peer session validation

Status: implementation/local verification complete; v4.0.0 public release authorized
and being prepared. Physical/manual acceptance remains unverified and is explicitly
accepted by the user for this publication after successful hosted Windows checks.

## Implemented scope

Equal peers, one LAN-only session per installation, invite-only joining from any
member, fresh v3 pairing, device-local `.fbignore`/`.gitignore`, no old-name alias
and no private content cache. Running installed app and user folders/settings
were not stopped, replaced or migrated. At the initial local-validation checkpoint,
no commit, push, tag or release had been made. Release progress is tracked below.

The active listener exposes v4 only. Legacy pairwise helpers remain exercised by
the existing low-level regression suite but are not a parallel application mode
or supported wire protocol. Old host/join configuration cannot start a listener.

## Final executed checks

### Native Windows

Go **1.23.12**, x64, genuine NTFS short-name TMP, full suite with **no exclusion**:

- **97 tests pass**: app 26, syncengine 64, fileops 7.
- All **97 pass under the race detector**.
- Windows `go vet ./...` and root GUI build pass; executable `-h` exits 0.
- Automatic three-engine lifecycle additionally passes **12 repeated race runs**.
- `gofmt` / `git diff --check` pass.

Commands and isolated environment are retained in ignored
`dist/peer-session-final.ps1`. It sets `GOTOOLCHAIN=go1.23.12`,
`GOMODCACHE=dist/toolchain-cache`, short-name TMP/TEMP, Windows amd64 and CGO, then
runs `go test -v -count=1 ./...`, `go test -race -v -count=1 ./...`, vet and the
versioned GUI build. The full script completes successfully, including subsequent
Linux cross-builds. Normal/race package timeouts are 240/300 seconds.

Logs: `dist/peer-session-final-windows.log`, `dist/peer-session-final-race.log`,
`dist/peer-session-final.log`, `dist/peer-session-lifecycle-repeat.log`.

The former `TestPauseResumeHostLifecycle` fixed-production-port dependency is
replaced by real session tests on isolated ephemeral listeners. Nothing binds or
stops the installed app's occupied port during validation.

### Linux / WSL

The same Go 1.23.12 toolchain cross-builds each package with `GOOS=linux`,
`GOARCH=amd64`, `CGO_ENABLED=0`; all binaries execute under WSL with `-test.v`:

- **87 tests pass**: app 22, syncengine 60, fileops 5.
- Linux `go vet ./...` and root build pass.
- Linux race detection was not run; native Windows race detection was.

Logs: `dist/peer-session/linux-{app,syncengine,fileops}-final.log`.

### Dashboard

Native Edge / Playwright fixture against the actual dashboard asset, with local
APIs mocked (not the running installed app):

- Create/join/leave, stop/resume, pause/resume and Sync now send expected actions.
- Three-member roster includes connected and offline/pending states.
- Local ignore inspector displays `.fbignore` source and device-local semantics.
- Initial load makes no mutation request.
- Desktop (1280px) and narrow (390px) screenshots pass; no horizontal overflow.
- **Zero browser console errors or warnings**.

Fixture/log/screenshots: `dist/peer-session-browser.js`,
`dist/peer-session/browser-final.log`, `dist/peer-session/dashboard-{desktop,mobile}.png`.
The fixture replaces confirmation dialogs with affirmative responses to allow
unattended control checks; native dialog interaction is not claimed.

### Diagnostics

The final active LSP probe covers 27 changed Go paths and reports no visible
errors / 27 clean outcomes. It also reports one previously marked false-positive
cross-file missing-method finding suppressed from the result, so this is **not**
a blanket proof of diagnostic cleanliness. Earlier probes had push-only/time-out
coverage and stale missing-method reports while new files were indexed. The
executed native compiler, full tests/race and Windows/Linux vet are the primary
evidence; no inline suppression was added to source.

## Safety coverage and failures resolved

Real isolated loopback TLS peers cover three-member membership propagation,
creator-offline synchronization, stable identity/restart, returning stale files
without deletion resurrection, concurrent edits/equal-byte causality, edit/delete
conflicts, stable conflict names, device-local ignore/unignore, bulk deletion
approval, compressed batches, bulk unchanged-content moves, guarded live-source
changes, certificate/device/session authentication, rejected legacy routes,
stale-address gossip, idle long-poll stability and automatic three-engine watching.

The missing-conflict-payload regression failed before its fix: a remotely known
conflict-copy path was incorrectly enough to skip preserving its locally available
version. Originals now materialize before derived copy paths and an absent live
copy must be preserved before original replacement. The test passes afterward.
A separate regression holds the transfer lock and checks that detached dashboard
status remains responsive.

One intermediate race suite failed its automatic-lifecycle assertion, **not** the
race detector: it waited only for computer C's initial receive, then edited B
before B had necessarily received the original. Correctly preserved concurrent
contents made that test's expected original-name winner invalid. The test now
waits for both recipients before the sequential edit. Twelve repeated race runs
and the final full normal/race suites pass. Failed evidence remains in
`dist/peer-session-race-test-ordering-failure.log` and
`dist/peer-session-test-ordering-failure.log`.

The first combined verification command also timed out late in Linux compilation
after earlier checks passed. Missing compilation/execution was completed separately;
the final combined script and final Linux execution both pass. Early 95/85-test
results are superseded by the final 97/87 counts above.

Application tests cover explicit create/join/stop/resume/leave/rejoin, preserved
legacy configuration/history, obsolete-ignore blocking, session-only pause/tray
controls, member/status fields, rejected forged/old credentials and cross-session
address refresh rejection. Existing filesystem/path/index/transfer regressions
remain in the full suite.

## Development artifact

- `dist/peer-session/FolderBridge.exe`, displaying **4.0.0-dev**.
- Bytes: **7,288,320**.
- SHA-256: `55cf90de27ac5f937462b6174dad300492432ef980a7939eee21fbd24c84db0a`.
- Help smoke outputs: `dist/peer-session/help.{out,err}`.

This is a local development build, not a published release or an installed upgrade.

## v4.0.0 publication authorization

The user chose public stable publication (not draft-only), after GitHub's Windows
release checks pass, explicitly accepting the manual gaps below. Target tag:
`v4.0.0`, verified unused before preparation. The existing v3.3.1 draft is unrelated
and will be left unchanged. The tagged workflow must build the single EXE from the
expected source commit before its draft is published. Fresh pairing, `.fbignore`
rename requirements and the known gaps will be included in release notes.

Hosted execution, tag/commit/asset evidence and public URL will be recorded after
completion. No installed-app change is authorized by this release request.

## Remaining acceptance

Physical three-computer LAN/firewall/DHCP scenarios, Windows 10/11 interactive
tray/login/notification and real manual v3→v4 upgrade/rollback remain unverified.
Crash-intent recovery is simulated; physical power-loss durability is not proven.
Invitations trust all members and have no per-device revocation. Deletion history
is retained without automatic expiry. These are documented limitations, not
claims covered by loopback tests. Multiple sessions and LAN discovery remain
user-deferred; no cloud/relay feature or new dependency was added.
