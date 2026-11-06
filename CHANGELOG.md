# Changelog

## v3.3 — Self-updater (release gates pending)

- Discover stable releases from the fixed public `asabatba/folder-bridge` GitHub repository at startup and every 24 hours; stream background downloads with bounded sizes/retries.
- Verify exact Ed25519-signed manifest bytes, ZIP SHA-256, minimum updater version, Windows x64 PE headers, strict archive entries and HTTPS download/redirect origins. Builds without an embedded public key fail closed.
- Add tray and dashboard update controls, explicit restart approval, skip-version settings, update status/history and explicit retry of quarantined failures. Automatic installation is not supported.
- Drain real host writes and client sync cycles without cancellation, require baseline/config persistence, then hand off to a standalone Go helper. Preserve config, pairing, ignore files, history, pause state and protocol compatibility.
- Add same-volume replacement, durable old-EXE backup and transaction journal, authenticated initialization health with a 30-second deadline, rollback and independently verified crash-recovery execution. Respect ACLs, sharing locks and device policy without elevation.
- Add offline key/sign/package/verify tooling and a two-phase GitHub Actions workflow: unsigned draft build, then verification/publication of an externally signed manifest. The private key never enters CI.
- Refactor into deep modules: a thin root launcher, `internal/app` for settings/dashboard/tray/update orchestration, `internal/syncengine` for synchronization lifecycle and drain/baseline guarantees, and `internal/fileops` for shared guarded filesystem operations. Keep `internal/update` independent and commands in `cmd/`; embed `internal/app/web/dashboard.html`. Preserve `go build .`, wire/state formats and dependencies. Tests follow their owning behavior and enforce dependency direction.
- Add Windows-native executable/helper, health, rollback, interrupted transaction, ACL, junction and sharing-lock integration tests; general trust/download/archive tests; real transfer-drain and v3.2 configuration regressions. Update the dashboard test to check its dynamic version element rather than a hard-coded old version.
- Production signing key/release activation, GitHub publication execution, Windows 10/interactive desktop and physical power-loss checks remain release gates. See [updater instructions](docs/UPDATING.md) and [validation evidence](review/V33-VALIDATION.md). An unmodified v3.2 app requires one manual bootstrap installation.

## Unreleased — Path safety and dashboard hardening

- Reject selected sync folders whose paths include symbolic links, Windows junctions or other reparse points; select the real directory instead. Recheck roots at startup, during scans and transfers, and before stale-file cleanup. Unsupported or missing roots fail rather than becoming empty manifests that could propagate deletions.
- Resolve the private-settings location before checking containment, preventing junction aliases from bypassing the settings exclusion.
- Require the exact dashboard authority `127.0.0.1:47831` on every request, including credential-bearing GET endpoints; retain POST origin/content-type checks.
- Add permanent regression tests for junction replacements on both peers, settings aliases, symlink paths, invalid roots, ordinary paths and dashboard authority/origin validation. No protocol, configuration or baseline migration.

## v3.2 — No-payload renames and ignore explanations

- Optimizes safe, unambiguous renames/moves of files without reuploading unchanged file bytes. SHA-256-matched source and target paths must be unique, and the old file must still match the previous baseline.
- Adds authenticated `POST /v3/move` with server-side SHA-256, path, ignore-rule and no-overwrite validation. Invalid or conflicting operations never delete the original file.
- Windows uses MoveFileW (no overwrite), with brief retries on transient sharing errors; non-Windows testing uses same-filesystem hard-link/unlink.
- Preserves mass-deletion protection for actual deletions but excludes confirmed rename pairs from the safety tally.
- Adds protocol capability advertisement: a v3.2 joiner falls back to ordinary transfers when paired with a v3.1 host.
- Adds a rule inspector to the dashboard for a relative file path, reporting effective ignore status and decisive source file, line, pattern and ancestor directory. A joiner checks both computers; a host checks its local policy.
- Adds end-to-end and regression tests for client/host renames, large-file optimization, duplicate content, simultaneous divergent moves, modified renamed files, bulk renames, refused collision and unsafe/ignored moves, v3.1 compatibility, and remote-rule inspection.
- No configuration or baseline migration; upgrade both peers for rename optimization.

## v3.1 — Native Windows integration

- Native Win32 notification-area (system-tray) icon; context menu opens dashboard/folder, pauses/resumes, syncs now, toggles autostart and quits. Double-click opens the dashboard.
- Opt-in, per-user HKCU Run registry entry launches the portable executable with `--background` after sign-in; no administrator rights or additional runtimes.
- Persisted `paused` config flag; pausing cancels transfers and shuts the host socket without losing pairing information. Resuming restarts the previous role.
- Best-effort Windows notification-area alerts for sync errors and conflict copies. Consecutive duplicate error alerts are suppressed.
- Local browser dashboard adds pause/resume and Windows autostart controls, plus paused status.
- New platform-neutral tests for pause lifecycle, persistence, dashboard API and notifications; Windows-only tests check Win32 struct layouts and the quoted autostart command.
- No changes to LAN protocol, ignore-file rules, sync baselines or file hash index.


## v3.0 — Change-driven synchronization and faster LAN batching

- Native recursive Windows `ReadDirectoryChangesW` watching, per-path hash invalidation and 350 ms coalescing, with 60-second safety scans and a 10-second fallback when native watching fails.
- Authenticated long-poll host change notifications via `GET /v3/changes`, enabling near-immediate remote-triggered sync without a frequent idle manifest transfer.
- Durable per-folder JSON hash index reused across process restarts. Invalid indices are rebuilt from the live filesystem rather than treated as a deletion signal.
- Adaptive TAR batch target (1–8 MiB), 1–3 concurrent independent batch requests, gzip level 0 for largely compressed assets.
- The dashboard now reports hash reuse/index statistics and the v3 version.
- New tests for cache recovery and preserved-mtime edits, long-poll wakeups, compression decisions, parallel batches, debounce, and Windows-only native filesystem notifications.
- No changes to pairing identity, baseline format, credentials, ports, file format, ignore semantics, or the TLS model. Files can still be exchanged through v2 endpoints.

## v2.1 — Reliability

- Flush verified temporary transfers to disk before guarded destination replacement.
- Retry Windows sharing/lock violations without ever deleting the old file first.
- Retry transient HTTP/transport failures; preserve permanent error handling for 409 conflicts, 401 failures and checksum mismatches.
- Stop a failing sync scan early when the LAN peer is unreachable; reconcile again in the next cycle.
- Remove orphaned `.fb-part-*` files older than 24 hours at startup; newer partials are retained and all temporary-part files are ignored during sync.
- Flush config and baseline writes before atomic replacement.
- Tests for interrupted file copy, checksum errors, orphan cleanup, baseline rewrites, transport retries and recovery after a lost response.
- Windows-only regression test for replacing a file held open without FILE_SHARE_DELETE.
- GitHub Actions CI configuration covering Windows and Linux.

No protocol or configuration migration. Install on both PCs for full protection.
