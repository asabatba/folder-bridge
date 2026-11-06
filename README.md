# FolderBridge v3.3

A portable, two-computer LAN folder synchronizer for Windows 10/11 (x64).

- Local synchronization needs no cloud service or account. Optional update checks contact GitHub; disable them in the dashboard if device policy requires it.
- Native Go executables, no Python, .NET runtime, or additional software required. A separate small helper handles approved updates and recovery.
- Signed Windows self-updates: background download, explicit restart approval, graceful sync drain, authenticated startup health and automatic rollback. **Production activation awaits an offline signing public key and signed published release; source builds without a key fail closed.** See [updater setup and release instructions](docs/UPDATING.md) and [v3.3 validation](review/V33-VALIDATION.md).
- Includes a native Windows system-tray icon with pause/resume, open folder, open dashboard, sync now, launch-at-login toggle and quit. The browser-based control panel remains available.
- Two-way sync, including files in nested folders and ordinary file deletions.
- Detects unambiguous moves and renames of unchanged files, applying the move to the other PC without sending the file payload.
- Dashboard ignore-rule inspector identifies the precise `.gitignore` or `.folderbridgeignore` source and line for a relative path.
- Watches the Windows folder tree for changes, coalesces event bursts (350 ms), and reconciles on demand; performs a **60-second safety scan**. If native watching fails, it falls back to 10-second checks.
- Groups small files into streamed TAR/gzip batches with **adaptive batch sizing and 1–3 concurrent transfers**; skips deflate for already-compressed formats.
- Persists an incremental **SHA-256 hash index** in local application settings, reusing metadata across restarts.
- Receives host-side change notifications using a long-lived HTTPS request rather than repeatedly downloading a full remote manifest while idle.
- Respects nested **`.gitignore`** and **`.folderbridgeignore`** files, checking ignore rules from both PCs.
- Encrypted TLS 1.3 connections with a pinned self-signed certificate and a secret pairing link.
- SHA-256 verification, disk-flushed staging files and guarded same-directory replacement; the previous file stays intact if a transfer fails or a locked destination cannot be replaced.
- Transient network failures receive bounded retries; persistent disconnects stop the current scan rather than repeatedly attempting every queued file.
- On startup, orphaned `.fb-part-*` temporary files older than 24 hours are removed (recent transfers are left untouched).
- Simultaneous edits keep the local version and save the other version as a `.conflict-from-other-PC-...` file; the extra copy is then synced to both computers.
- If >=75% of at least 20 previously synced files would be deleted on one side, sync pauses until you explicitly approve bulk deletions.

## Quick start

1. Extract the Windows release ZIP (`FolderBridge.exe` and `FolderBridge-Updater.exe`) into a user-writable directory on **both** Windows computers. Run `FolderBridge.exe` on both. The app opens the browser dashboard at `http://127.0.0.1:47831/` and stays in the Windows notification area. If the browser doesn't open, navigate there manually.
2. On the first PC choose **Host folder**, paste a dedicated directory path (for example `C:\Users\Alice\Documents\Shared`), and click **Save & start**.
3. Copy one of the displayed pairing links. Use the LAN address that the second computer can reach (typically starts with `192.168.`, `10.`, or `172.`).
4. On the second PC choose **Join another PC**, enter its own directory path, paste the pairing link, and click **Save & start**.
5. Watch **Activity**. Once the second PC shows **Up to date**, create `test.txt` in either directory and confirm it appears in the other. The design targets a 1–2-second response on Windows LANs; actual latency has not yet been benchmarked on Windows and depends on disk activity, network conditions and watcher availability.

To suspend synchronization while preserving all pairing settings, use **Pause syncing** in the dashboard or **Pause synchronization** in the tray menu; both computers keep their local files unchanged while paused. Resuming reconnects to the peer. **Stop syncing** disables the configured session, whereas **Quit app** fully exits the process. To reopen an already running app, double-click its tray icon, run `FolderBridge.exe` again, or visit the local control-panel URL.

### Networking

- The two PCs must be able to reach each other over the network.
- Host listens on **TCP 48713** (TLS); the web control panel binds **only to `127.0.0.1:47831`**. Open it at exactly `http://127.0.0.1:47831/`; requests using `localhost`, other hostnames or a different port are rejected to protect pairing information.
- The host PC must remain on. Native Windows change events notify the joining PC through `GET /v3/changes` long polling, with a 60-second periodic verification. If watching is unavailable, FolderBridge falls back to 10-second change checks. Host edits are only distributed while the joining PC is online.
- Windows Defender Firewall or corporate endpoint protection might block incoming connections or this unsigned executable. Only permit network access if your network administrator and device policy authorize it. This application does not disable or work around any firewall or endpoint protections.
- If more than one IP address is shown, use the one reachable from the other PC. Pairing links are passwords: keep them private.


### New in v3.3

- Check the fixed public `asabatba/folder-bridge` GitHub release source at startup and every 24 hours. Stable releases only; downloads stream in the background with bounded sizes and retries. No updater activity pauses ordinary synchronization before approval.
- Ed25519 verification of exact manifest bytes, SHA-256 verification of the ZIP, exact archive-name/path and Windows x64 checks, and an HTTPS redirect allowlist. Unsigned, modified, older or incompatible releases are rejected.
- Tray controls: **Check for updates**, **Update available: vX.Y.Z**, **Restart to update**, and **Skip this version**. The dashboard adds versions, download status, release notes, check/result history, settings and explicit retry of quarantined failures.
- After approval, wait for host writes and the client cycle/baseline to finish, persist state, then hand off to the standalone helper. Same-volume replacement, a durable backup/journal, authenticated 30-second initialization health and rollback protect failed updates. No administrator elevation or device-policy bypass.
- Configuration and LAN protocol remain compatible. An unmodified v3.2 installation needs **one manual bootstrap upgrade**; it cannot discover an updater it does not contain. Subsequent signed updates use the new flow.
- Modular source layout: a thin root `main.go`, application/UI orchestration in `internal/app`, synchronization in `internal/syncengine`, shared filesystem safety in `internal/fileops`, and the existing independent `internal/update`. The dashboard is embedded from `internal/app/web/dashboard.html`; `go build .` remains supported.

**Validation:** Windows 11-native helper/lock/ACL/junction/recovery tests, real transfer-drain tests, regression tests and race detection have been run. Linux regression/trust tests also pass under WSL. The occupied-port lifecycle exclusion, production key/release setup, unexecuted GitHub publication workflow and remaining Windows 10/interactive/power-cut checks are documented in [v3.3 validation](review/V33-VALIDATION.md). This is not a claim that all release gates are complete.

### New in v3.2

**Rename and move detection (without retransmitting file bytes)**

- When a file that existed in the previous successful baseline disappears from one path and appears at another **with identical SHA-256 content**, FolderBridge can issue a conditional remote move (or perform a local move if the host was renamed) rather than uploading/downloading the file again.
- The rename optimization only applies if the old file is unchanged on the other computer, the new path is absent there, and the hash identifies exactly one previous path and one new path. Duplicate hashes, simultaneous divergent renames, changes made during a move, and ambiguous matches use standard conflict-safe synchronization rather than guessing.
- The server rechecks the source hash and target absence just before the move. A Windows `MoveFileW` operation never overwrites an existing destination; a same-filesystem link/unlink fallback is used on non-Windows. Symlinks/junctions and paths ignored by the receiving computer are rejected. Windows sharing violations receive short retries; persistent failures stop the current sync and are reconciled on the next cycle.
- Rename operations count toward **Moves without transfer** on the dashboard and appear in the activity log. A confirmed bulk rename does not trigger the mass-deletion guard; genuinely missing or ambiguous files are still covered by it.
- A peer advertises the `atomic-rename` capability in the existing manifest. If the other computer runs v3.1 or older, the v3.2 client uses the previous upload/delete behavior instead. For the optimization, upgrade **both** PCs to v3.2.
- File payloads are not transferred for a successful optimized rename, but manifest metadata and the small move request still use the LAN connection. Case-only renames and cross-filesystem moves are not optimized. Empty directories are not synchronized.

**Ignore rule inspector**

- In the dashboard, enter a **relative file path** in the **Ignore rule inspector**, then select **Check ignore rules**. The tool identifies whether the path is excluded, including the source filename, line number and matching glob/negation. It also explains when an ignored parent directory prevents a child rule from re-including a file.
- On the **joining PC**, the inspector reads both devices' `.gitignore` / `.folderbridgeignore` policies and reports the combined decision: a path is ignored if either PC excludes it. The host can only inspect its own rules; if the peer is offline, the UI clearly labels the remote policy unavailable.
- `.git/` and `.fb-part-*` are still excluded by FolderBridge's internal policy regardless of ignore-file content. Ignore semantics, baseline format, secrets and control ports remain unchanged.
- The inspector accepts file paths; directory patterns are recognized through ancestors. It is a diagnostic helper for FolderBridge's supported ignore syntax, not a complete implementation of every Git ignore corner case.

**Validation:** Automated end-to-end tests cover moves in both directions, 2 MiB rename without a file upload, duplicate-content ambiguity, divergent moves, modified files, bulk renames, collision/changed-source rejection, ignored/unsafe targets, v3.1 peer fallback, nested-rule diagnostics and combined remote policy inspection. Linux tests pass under the race detector, and Windows x64 binaries/test executables cross-compile. The **Windows runtime tests and tray behavior have not yet been executed on a Windows desktop**.

### New in v3.1

**Native Windows integration**

- **Tray icon:** Windows notification-area icon uses built-in system icons (no Electron/.NET, no external dependencies). Right-click for **Open dashboard**, **Open synced folder**, **Pause / Resume synchronization**, **Sync now** (joining PC), **Launch at login** and **Quit FolderBridge**. Double-click opens the dashboard. The icon indicates paused and error/partial-sync conditions.
- **Pause/resume:** `paused` in `config.json` is persisted separately from `enabled`, leaving the selected folder, pairing token, invite and baseline unchanged. Pause cancels in-flight transfers; on the hosting PC, it also closes the listening socket. The joining PC may temporarily show a connection error until the host resumes. Nothing is deleted merely because a peer is paused.
- **Launch at login:** optional **per-user** Windows registry `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` entry. Does not request administrator permissions. Starts the *current path* to `FolderBridge.exe` with `--background`, hiding the browser but enabling the tray icon. If you move the executable later, disable and re-enable startup to update its path. Device policy may prevent modifying startup entries.
- **Notifications:** Windows notification-area alerts for new sync errors and preserved conflict copies. Duplicate consecutive errors are suppressed. Windows notification preferences / Do Not Disturb may suppress pop-ups.
- **Dashboard:** Windows integration settings, pause/resume button, and indicators for paused state. Control requests are accepted only on localhost and respect the same API origin checks as earlier versions.
- **Compatibility:** v3 pairing links, ports, encryption, hash index, ignore rules, batch protocol and baseline format remain unchanged. Upgrade both PCs when feasible.

**Validation:** Linux functional/regression and race-detector tests pass; Windows x64 GUI executable and Windows-native test executable cross-compile. **Actual tray behavior, registry launch-at-login and Windows notifications must still be tested on Windows hardware or a Windows CI runner.** The GitHub Actions configuration runs Windows tests on push.

### New in v3.0

**Event-driven synchronization**

- Native recursive Windows `ReadDirectoryChangesW` watcher on each PC, including newly created nested directories, renames, edits and removals. Events invalidate the cached hashes for their paths. Renamed/deleted directory events invalidate the entire corresponding subtree.
- Local filesystem events are coalesced for 350 ms, capped at 2 seconds during continuous changes. A 60-second full directory walk remains as a safety net. If the Windows watcher cannot initialize or fails later, a 10-second fallback signal is used instead.
- The host exposes an authenticated `/v3/changes` long-poll endpoint over the same TLS connection. The join PC watches for remote change revisions and starts reconciliation after notifications. Network failures back off and retry. It also runs regular verification independently.
- **Scope:** scans still walk the directory tree to compare entries; unchanged files reuse cached hashes. This is incremental hashing, **not** an incremental tree database or block-level transfer engine.

**Persistent indexing**

- `index-*.json` in `%AppData%\FolderBridge` records each file's SHA-256, size and modified time. It is written by durable same-directory replacement and reused after restart.
- Any index corruption is treated as a cache miss, not as an instruction to delete files. The live filesystem and the last successful sync baseline remain authoritative.
- The dashboard shows files indexed, hashes reused, hashes calculated and cache entries restored at startup.
- A file modified outside FolderBridge while the watcher was inactive **and** with its size and timestamp deliberately preserved may escape the cached-hash check. Keep backups of important files.

**Transfer improvements**

- Batches remain limited to at most 128 entries, 8 MiB uncompressed, 128 KiB per file, and at least 8 entries. The scheduler chooses a 1–8 MiB target based on observed throughput and runs 1–3 independent batch requests at once.
- TAR archives retain gzip framing for protocol compatibility, but select fast gzip or no compression (gzip level 0) based on the extensions and byte weight of the files (e.g. PNG, JPEG, ZIP, MP4). Large files remain individual streaming transfers.
- All per-file integrity checks, ignore checks, guarded overwrites, retries and conflict/deletion protections are preserved. The existing v2 transfer endpoints remain available.

### Reliability improvements (v2.1)

- **Safe replacement:** incoming contents are written to a temporary file in the destination directory, SHA-256 verified and flushed to storage before a rename/replacement. If the checksum, stream, access permissions, or expected previous hash check fails, the existing destination is not intentionally removed or overwritten.
- **Windows sharing locks:** replacement attempts are retried briefly for Windows sharing/lock violations, then deferred to the next event-driven or periodic sync. The application never deletes the destination to force a rename.
- **Network recovery:** temporary HTTP failures (408, 429, 5xx, connection resets, truncated streams) retry up to three total attempts with 200/400 ms delays. Authentication, conflicts and integrity errors are not retried automatically within the same operation. An unreachable peer stops the scan early and is retried on the next cycle.
- **Crash recovery:** interrupted transfers never publish half-written files. During startup, regular `.fb-part-*` files older than 24 hours are cleaned from the shared folder. The sync baseline remains the last safely saved snapshot, and the next scan reconciles the files on both PCs even if a network response was lost. Previously transferred bytes **are not resumed**; interrupted individual files are sent again.
- **Durable metadata:** config and baselines are written via a flushed temporary file and replacement, so a process crash while saving does not replace the previous file with partial JSON. This is not a guarantee against sudden hardware failure or power loss.
- **Testing:** interruption, checksum, retry, baseline, recovery, and stale-temp tests run on Linux. `internal/fileops/replace_windows_test.go` contains the real Windows locked-destination test. GitHub Actions configuration runs the suite on Windows and Linux; Windows runtime validation must still be performed on a Windows device or CI runner.

### Compressed batching (v2)

- Automatic for **8 or more** eligible files, each at most **128 KiB**. A batch contains up to **128 files** and **8 MiB** of uncompressed contents.
- Each group becomes one **gzip-compressed TAR stream**, transferred directly from disk without creating a ZIP or TAR file on disk. Compression uses fast gzip for compressible files and gzip level 0 for mostly already-compressed batches.
- Only ordinary one-sided additions and modifications are bundled. Large files, conflicts, and deletions use their original, per-file logic.
- Each file still has SHA-256 verification and an expected-previous-version check. Interrupted or rejected batches fall back to individual transfers; successful operations remain on disk and later scans reconcile any partial uploads.
- The dashboard shows compressed batch counts and activity summaries. **Install v3 on both computers** for native change notifications; v2 endpoints remain supported for file transfer.

### Ignoring files

Place a `.folderbridgeignore` in either synced folder, or reuse a project's `.gitignore` (including nested `.gitignore` files). For example:

```gitignore
# .folderbridgeignore
node_modules/
build/
*.log
.env*
!important.log
```

- Supports ordinary glob syntax (`*`, `?`, `**`, character classes), comments, `!` negation, directory patterns ending in `/`, and `/`-anchored patterns. Matching is case-sensitive.
- Rules are **combined from both PCs**. If either computer ignores a path, it is excluded from transfers on both sides. A nested ignore file can override its parent's rules, but a negation cannot restore files within an ignored parent directory.
- Ignore files themselves normally sync; `.git/` directories and temporary `.fb-part-*` files are **always excluded**.
- **Ignoring never deletes an existing copy**, including one already synchronized. Removing an ignore rule treats the affected path as a new merge. A synchronized ignore-file change can require an additional reconciliation cycle to take effect on both devices.
- Files already transferred before they became ignored **remain on the destination**; delete such copies manually if needed. Ignoring is not a secure-delete operation.
- Each individual ignore file is capped at 64 KiB, combined ignore text at 1 MiB.

### Conflict and deletion rules

- First sync **merges** contents. If both sides already have the same path with different contents, the host's version becomes a conflict copy while the joining PC's version keeps the original name.
- After the first completed sync, deletions propagate in both directions. A file that was never synced will not be deleted merely because it is missing on the other PC.
- If both sides modify the same file before the next scan, both versions are retained. If one side edits and the other deletes a file, the modified version wins.
- A major change such as an accidentally emptied folder may trigger a **Safety stop**. Verify your selected folders, then use **Approve bulk deletions** only if intended.
- FolderBridge is not a backup system. Keep backups of important files.

### Limitations

- Intended for **two computers and one shared folder pair**, not a distributed multi-device cluster.
- Empty directories are not replicated. Symlinks and Windows directory junctions are not intended to be synced. The selected folder and every ancestor in its path must be real directories, not symlinks, junctions or other Windows reparse points; select the real directory path instead. If a configured path becomes linked later, synchronization fails rather than interpreting it as an empty folder.
- Windows files that are in use or cannot be overwritten may produce an error; the app attempts a brief locked-file retry, then tries again on the next sync cycle.
- Launch at login is opt-in and occurs only after a user signs in, not as a pre-login Windows service. Windows Do Not Disturb can suppress notifications.
- Watcher-triggered scans still enumerate the directory tree; unchanged contents generally reuse their hash. Changed files are transferred in full (no block-level delta synchronization).
- Ignore pattern support covers the common Git syntax, but is not a full implementation of Git wildmatch (e.g. it does not read `.git/info/exclude` or global Git excludes).
- You may see Windows security prompts because this is a custom **unsigned executable**. Do not override a corporate restriction to run it.
- The current validation build was built with native Windows Go. Automated Windows tests, including the locked-destination test, and Linux tests pass; see [module-refactor validation](review/REFACTOR-VALIDATION.md) for current commands and the occupied-port test exclusion. The Windows tray menu and login startup have **not been interactively verified on a Windows desktop**.
- A process crash during a multi-file batch may leave some files already committed; the next scan reconciles them. **Multi-file batches are not all-or-nothing transactions.**
- The temporary-file namespace `.fb-part-*` is reserved anywhere inside the synchronized tree; do not use it for user files.

### Private settings

Per-user application data is stored in `%AppData%\FolderBridge\`:

- `config.json`: role, folder, peer invite or host pairing secret, optional persisted pause state and backward-compatible `updates` settings. Startup preference is stored in the Windows per-user Run registry key, not here.
- `peer-cert.pem`, `peer-key.pem`: TLS identity for hosting.
- `baseline-*.json`: content hashes recorded at the last successful sync.
- `index-*.json`: best-effort persistent SHA-256 hash cache keyed by local folder; deleting it only makes the next scan rehash files.
- `update-state.json`, `update-result.json`, `updates/`: check/download status, installation results/quarantine and signed staging evidence. These are independent of pairing and synchronization state.

Do not copy the configuration directory into the shared folder; the app refuses a selected folder that contains its own settings directory. Deleting a baseline starts a fresh **merge**, not a new round of deletions.

### First upgrade from v3.2 or earlier

1. In the FolderBridge browser dashboard, click **Quit app** on both PCs (do not just close the browser tab).
2. Extract the first trusted v3.3 Windows release into each application directory and start `FolderBridge.exe` again. An old v3.2 executable cannot perform this bootstrap automatically.
3. Confirm **v3.3.0** in the control panel and that the joining PC says **Up to date**. Your configuration and baselines under `%AppData%\FolderBridge` remain unchanged.

### Build from source

Requires Go 1.23 or newer. In the source directory:

```powershell
go test ./...
go build -trimpath -ldflags="-s -w -H=windowsgui" -o FolderBridge.exe .
go build -trimpath -ldflags="-s -w -H=windowsgui" -o FolderBridge-Updater.exe ./cmd/folderbridge-updater
```

On Linux/macOS, cross-compile for Windows x64:

```bash
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags='-s -w -H=windowsgui' -o FolderBridge.exe .
```

These source builds deliberately have no update trust key and cannot self-update. For key-pinned production builds, offline signing, release workflows, recovery and the reorganized source layout, see [docs/UPDATING.md](docs/UPDATING.md).

See [source architecture](docs/ARCHITECTURE.md) for ownership, interfaces and dependency direction, and [module-refactor validation](review/REFACTOR-VALIDATION.md) for current checks. Tests live alongside their owning modules: app controls/update integration in `internal/app`, transfers/renames/watchers/draining in `internal/syncengine`, filesystem integrity/replacement in `internal/fileops`, and signature/archive/helper recovery in `internal/update`.

**Acceptable use:** Only synchronize files and devices you are authorized to connect. A restriction on Syncthing or external file transfer may be intentional; if so, use an approved corporate transfer solution instead.
