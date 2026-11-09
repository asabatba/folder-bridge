# Changelog

## v4.0.1 — Outbound-initiated LAN transport

- Add automatic persistent reverse connections so a member that cannot accept inbound TCP can still serve files and change notifications to a reachable member. Both computers need the updated build; outbound access to another member's TCP 48713 remains necessary.
- Reuse pinned TLS, per-request certificate-bound session authentication, existing v4 reconciliation, guarded streaming/batches, and local safety/ignore policies. Reconnect after interruption; cancel and join reverse connections on stop. No dependency, settings, invitation or history migration.
- Retain direct transport for v4.0.0 members. No Internet relay, corporate-proxy support, firewall changes or endpoint-policy bypass.
- Verify bidirectional sync with the restricted listener closed, reconnection, large-file and compressed batch transfers, renames/deletions, authentication and shutdown. See [validation](review/OUTBOUND-TRANSPORT.md). Physical corporate-network acceptance remains unverified; the user explicitly authorized stable publication with this limitation disclosed.

## v4.0.0 — Equal-peer LAN sessions

- Replace permanent host/guest setup with Create session / Join session. Every member serves and receives, any member can invite, and reachable members can continue without the creator. One LAN-only session per installation now; multiple sessions and discovery remain deferred.
- Introduce authenticated v4 membership/roster exchange, pinned certificate-bound device identities, per-member long polls and durable address incarnations. Current same-session invitations can refresh stale LAN addresses without resetting history.
- Replace pairwise baseline inference with per-path causal versions and retained deletion tombstones. Preserve concurrent contents under deterministic conflict names; avoid stale-member deletion resurrection and repeated conflict-copy storms.
- Keep guarded verified replacement, crash-intent recovery, bulk-deletion approval, compressed small-file batches and unambiguous no-payload renames. Serve live shared files, not a second private content store.
- Make ignores device-local and rename the custom rule file to `.fbignore` only, alongside `.gitignore`. No `.folderbridgeignore` alias; fresh setup blocks obsolete rule filenames until manually renamed.
- Require fresh v3 pairing with explicit safe merging. Preserve legacy configuration/history rather than importing deletion state; no mixed-version wire compatibility or legacy app role remains active.
- Add member/pending status, equal-peer invitations, stop/resume/leave controls and responsive detached dashboard snapshots. Tray Sync now works for every member.
- Add real multi-peer TLS, creator-offline, stale-return, conflict/missing-payload, restart, ignore, batch/move and authentication regressions. Use ephemeral listeners so lifecycle tests do not conflict with the running app. See [session validation](review/PEER-SESSIONS.md).

## v3.3 — Modular app and manual releases (unreleased)

- Keep a thin root launcher, `internal/app` for settings/dashboard/tray orchestration, `internal/syncengine` for synchronization lifecycle/baselines, and `internal/fileops` for shared guarded filesystem operations. Embed `internal/app/web/dashboard.html`; preserve `go build .` and dependency direction.
- Remove the pre-deployment self-updater completely: background checks/downloads, settings, tray/dashboard controls, helper, health/recovery/rollback, signing/package tools and updater-only engine handoff/admission code. No updater compatibility layer remains.
- Restore a single-executable release flow: stable tag → Windows tests/vet → `FolderBridge.exe` → unpublished GitHub draft. Publication and upgrades are manual; no signing key or helper is required. See [release instructions](docs/RELEASING.md) and [manual upgrades](docs/UPDATING.md).
- Display standalone build-version metadata through the normal status endpoint. Removed updater routes return 404. Synchronization configuration has only its six ordinary fields.
- Preserve ordinary pause/stop cancellation, pairing, LAN protocol, baselines, guarded file writes and the Windows private-settings identity fix. Retain unreadable-baseline regression coverage without candidate-initialization machinery.
- Current Windows/Linux/race/build/dashboard checks and remaining hosted/interactive acceptance limits are in [removal validation](review/UPDATER-REMOVAL.md). Earlier updater reports are historical, not production release requirements.

## Unreleased — Path safety and dashboard hardening

- Reject selected sync folders whose paths include symbolic links, Windows junctions or other reparse points; select the real directory instead. Recheck roots at startup, during scans and transfers, and before stale-file cleanup. Unsupported or missing roots fail rather than becoming empty manifests that could propagate deletions.
- Resolve the private-settings location before checking containment, preventing junction aliases from bypassing the settings exclusion. Compare filesystem identities rather than path strings so Windows 8.3 names and extended prefixes cannot bypass that exclusion; verified with Go 1.23.12 and permanent native regression tests.
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
