# Source architecture

FolderBridge builds one portable Windows executable with `go build .`. The root
contains only `main.go`, which calls `internal/app.Run()`. There are no additional
runtime/module dependencies, helper executables or in-app software downloads.

## Ownership and dependency direction

```text
main → app → syncengine → fileops
        └──────────────→ fileops
```

| Module | Owns | Does not own |
|---|---|---|
| `internal/app` | Startup/shutdown, configuration, dashboard, tray/autostart, build-version display | Peer transports, scanners or baseline implementation |
| `internal/syncengine` | Session lifecycle, TLS peers, reconciliation, watchers, ignore rules, indexes, transfers, bounded history and baseline persistence | Dashboard/tray or persisted app settings |
| `internal/fileops` | Checked paths, link rejection, hashes, guarded staging, durable metadata replacement and no-overwrite moves | Networking, UI or session state |

Tray, settings and dashboard stay together in `app`; a package for each would
expose orchestration state without creating a useful independent module.

## Application boundary

`app.Run()` is the launcher-facing entry point. Application and configuration
types stay private. `run.go` coordinates CLI flags, the local dashboard, tray,
engine startup and shutdown. Configuration persistence is in `config.go`; session
controls are in `lifecycle.go`.

`ui.go` embeds `web/dashboard.html` relative to `internal/app`, so the repository
asset is `internal/app/web/dashboard.html`. No runtime asset installation or new
frontend framework is required. Platform variants remain beside their owner.

The dashboard reads `version` from ordinary `/api/status` responses. Release
builds set `folderbridge/internal/app.Version` through Go's `-X` linker flag;
version display does not require a network request or separate service.

## Synchronization boundary

- `New(home, notify)` creates an idle engine. Notifications use callbacks rather
  than imports of application or Windows tray code.
- `Start(Options)` / `Stop()` own contexts, peer serving, scanners and watchers.
  `Options` describes a session, not the application's persisted configuration.
  Pausing/stopping cancels active synchronization as before.
- `Snapshot()` returns detached status/history and pairing links, not live
  scanner/server pointers or locks.
- `RequestSync()` and `ApproveBulkDelete()` express operations rather than
  transport internals. `ReportError()` and `RecordEvent()` retain status/history.
- `ValidateInvite()` and `ExplainIgnore()` support validation/inspection without
  exporting concrete peer, host, scanner or manifest implementations.
- Reconciliation loads the persisted baseline before transfers. Unreadable state
  reports an error and is not rewritten as an empty baseline. Successful state
  is persisted before reporting a completed cycle.

The engine has no software-installation admission gate, handoff callback,
candidate-health option, or dependency on a release mechanism.

## Filesystem boundary

`fileops` centralizes shared correctness primitives rather than routing app
configuration writes through the synchronization package:

- `CheckSyncRoot`, `RejectPathLinks`, `CheckedPath`: real-directory, ancestor and
  relative-path checks, including Windows reparse-point rejection.
- `HashFile`, `HashStableFile`, `CurrentHash`, `CheckExpected`: hashing and
  expected-content/absence checks.
- `StagedWriteGuarded`: streamed hash verification, flush and guarded replacement
  of synchronized files, not application executables.
- `WriteAtomically`: durable same-directory replacement of metadata bytes.
- `MoveUnchanged`: checked unchanged-content moves without replacing a target.

Callers own domain serialization and validate untrusted relative paths before
filesystem access. App folder selection additionally compares filesystem
identities to exclude its private settings, including Windows path aliases.

## Releases and verification

The release workflow is separate from runtime: a stable version tag runs Windows
tests/vet, builds one EXE and creates a draft. A human publishes the draft and
users replace their executable manually. See [RELEASING.md](RELEASING.md) and
[UPDATING.md](UPDATING.md).

Tests follow their owning behavior. Dependency guards parse every platform
variant and protect the thin root. App tests cover controls, configuration,
version display and absent software-update routes. Engine tests cover real
transfers/reconciliation, watchers, retained baseline safety and the public API.
Native filesystem locking/path tests stay with `fileops`.

See [current validation](../review/UPDATER-REMOVAL.md). Older updater/refactor
reports are historical evidence only, not current release instructions.
