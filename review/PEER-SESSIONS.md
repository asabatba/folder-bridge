# Equal-peer session validation

Status: implementation/local verification complete; **v4.0.0 publicly released**
after successful hosted checks and source/asset verification. Physical/manual
acceptance remains unverified and was explicitly accepted by the user for this
publication. The running installed app remains untouched.

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
- Linux race detection was not run locally; it subsequently passed on hosted
  Ubuntu for both the source branch and release tag (see publication below).

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

## v4.0.0 publication

The user chose public stable publication (not draft-only), explicitly accepting
the manual gaps below after hosted checks pass. Source commit and annotated tag
were pushed; the tag was verified unused beforehand. Existing v3 tags and the
v3.3.1 draft were left unchanged. No installed-app change was authorized or made.

- Source commit: `e918eee1954e05083ecf7417150c4216421d7325`.
- Annotated tag: `v4.0.0` (tag object `212adad1e89d21350e200befa9b4c5694b93fea2`).
- [Public stable release](https://github.com/asabatba/folder-bridge/releases/tag/v4.0.0),
  published **2026-11-06 23:19:24 UTC**; draft/prerelease both false.
- [Tagged Windows release job](https://github.com/asabatba/folder-bridge/actions/runs/42308065424):
  tests, vet, versioned GUI build and single-EXE draft creation all pass.
- [Tagged Windows/Ubuntu test jobs](https://github.com/asabatba/folder-bridge/actions/runs/42308065441)
  and [source main test jobs](https://github.com/asabatba/folder-bridge/actions/runs/42308060336)
  all pass, including Ubuntu race detection.

### Published asset verification

- Exactly one attached asset: unsigned Windows x64 `FolderBridge.exe`.
- [Public download](https://github.com/asabatba/folder-bridge/releases/download/v4.0.0/FolderBridge.exe).
- Bytes: **7,288,320**.
- SHA-256: `6b9158c2a2be53aa507e9d275205f6a6212db471942e0e98780e3c7319a1f99a`.
- Downloaded draft asset hash/size match GitHub's asset digest. Go build metadata
  confirms Go 1.23.12, Windows amd64, exact source revision above and
  `vcs.modified=false`; hosted executable `-h` exits 0.
- The successful tagged workflow sets application version **4.0.0** via its
  tag-derived linker flag. Go intentionally omits linker flags from build metadata
  under `-trimpath`; no runtime dashboard-version probe against the installed
  app's occupied port is claimed.
- After publication, both the release API and the complete EXE download succeeded
  **without authentication**. Public bytes match the same SHA-256. Upgrade and
  known-gap notes were checked on the published release.

Initial publication attempts stalled locally while Windows PowerShell serialized
filesystem-provider metadata attached to a `Get-Content` string; read-back checks
confirmed the release remained a draft. Reading plain text with .NET resolved the
helper issue. Publication used a bounded HTTP request after rechecking source,
all hosted checks, draft state and downloaded-asset hash. No application source,
workflow, tag or asset was changed to resolve the helper issue.

Local operational evidence (ignored `dist/`): `release-v4-hosted-checks.log`,
`release-v4-asset-verification.log`, `release-v4-publication-final.log`,
`release-v4-final-verification.log`, `release-v4-public-verification.json` and
`release-v4-hosted/{build-info.txt,help.out,help.err}`. Release notes disclose fresh
pairing, manual ignore-file rename, trusted membership and remaining manual gaps.

## Remaining acceptance

Physical three-computer LAN/firewall/DHCP scenarios, Windows 10/11 interactive
tray/login/notification and real manual v3→v4 upgrade/rollback remain unverified.
Crash-intent recovery is simulated; physical power-loss durability is not proven.
Invitations trust all members and have no per-device revocation. Deletion history
is retained without automatic expiry. These are documented limitations, not
claims covered by loopback tests. Multiple sessions and LAN discovery remain
user-deferred; no cloud/relay feature or new dependency was added.
