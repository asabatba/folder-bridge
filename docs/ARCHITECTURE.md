# Source architecture

FolderBridge keeps `go build .` as its application entry point. The root contains
only `main.go`, which calls `internal/app.Run()`. Executable names, command-line
flags, LAN protocol, dashboard API and persisted formats are unchanged by the
module extraction. There are no new module dependencies.

## Ownership and dependency direction

```text
main → app → syncengine → fileops
        ├──────────────→ fileops
        └──────────────→ update

cmd/folderbridge-updater → update
cmd/folderbridge-release → update
```

| Module | Owns | Does not own |
|---|---|---|
| `internal/app` | Process startup/shutdown, recovery/health orchestration, configuration, dashboard, tray/autostart, updater controls | Peer transports, scanners, transfer locks or baseline implementation |
| `internal/syncengine` | Session lifecycle, TLS peers, synchronization, watchers, ignore rules, indexes, transfers, bounded history and baseline persistence | Dashboard/tray, updater trust or persisted app settings |
| `internal/fileops` | Checked paths, link rejection, hashes, guarded staging, durable metadata replacement and no-overwrite moves | Networking, UI, session state or application policy |
| `internal/update` | Release trust/download/staging, helper installation, authenticated health, recovery and rollback | Synchronization engine or app/UI |

Tray, settings and dashboard stay together in `app`; splitting each into another
package would expose orchestration state without creating a useful independent
module. `update` retains its own independently verified installation-path and
recovery protections rather than depending on the sync engine.

## Application boundary

`app.Run()` is the launcher-facing entry point. The application and configuration
types remain private. `run.go` coordinates startup recovery, CLI flags, dashboard
readiness, candidate health reporting and shutdown. The update adapter is in
`updates.go`; configuration persistence is in `config.go` and session controls
are in `lifecycle.go`.

`ui.go` embeds `web/dashboard.html` relative to `internal/app`, so the repository
asset is `internal/app/web/dashboard.html`. No runtime asset installation or new
frontend framework is required. Tray/platform variants remain in this package.

## Synchronization boundary

- `New(home, notify)` creates an idle engine. Notifications are callbacks, not an
  import of application or Windows tray code.
- `Start(Options)` / `Stop()` own contexts, peer serving, scanners and watchers.
  `Options` describes a session; it does not expose the application's persisted
  configuration type. `CheckBaseline` requires synchronous baseline validation
  before an updated candidate can report initialization health.
- `Snapshot()` returns detached status/history and pairing links. Callers do not
  acquire engine locks or receive live scanner/server pointers.
- `RequestSync()` and `ApproveBulkDelete()` express operations, not transport or
  scheduler internals. `ReportError()` and `RecordEvent()` retain shared status
  and bounded activity history.
- `ValidateInvite()` and `ExplainIgnore()` support app validation/inspection
  without exporting the concrete peer, host, scanner or manifest types.

### Update drain contract

`Quiesce(handoff)` owns synchronization admission and drain guarantees:

1. Mark the engine draining so new client work and host requests cannot start.
2. Wait for the active client cycle and admitted host handlers; do not cancel
   their contexts. The client cycle finishes its baseline persistence first.
3. Reject handoff if synchronization state could not be persisted.
4. Invoke the app's handoff callback while new work is gated. The callback saves
   app configuration, revalidates/extracts the helper and performs safe handoff.
5. Resume synchronization if preparation/handoff fails. After success, keep
   admission closed until shutdown.

The callback must not call engine lifecycle transitions; `Quiesce` holds the
lifecycle lock. Ordinary pause/stop behavior remains separate from update drain.

## Filesystem boundary

`fileops` centralizes shared correctness primitives rather than routing app
configuration writes through the synchronization package:

- `CheckSyncRoot`, `RejectPathLinks`, `CheckedPath`: existing real-directory,
  ancestor and relative-path checks, including Windows reparse-point rejection.
- `HashFile`, `HashStableFile`, `CurrentHash`, `CheckExpected`: hashing and
  expected-content/absence checks.
- `StagedWriteGuarded`: streamed hash verification, flush and guarded replacement.
- `WriteAtomically`: durable same-directory replacement of metadata bytes.
- `MoveUnchanged`: checked unchanged-content moves without replacing a target.

Callers still own domain serialization and must validate untrusted relative paths
before filesystem access. These functions do not introduce a new storage format.

## Tests and verification

Tests live beside the behavior they exercise. Former version-named mixed tests
are split into app controls/ignore inspection, engine renames/index/watching and
filesystem replacement tests. Engine public-boundary tests compile as an external
consumer; implementation tests can stay inside their owning package without
exporting internals solely for testing.

`internal/app/architecture_test.go` checks the thin root and permitted module
imports, including platform variants not compiled on the current OS. Real-transfer
quiesce tests belong to `syncengine`; app tests verify updater integration and
configuration failures. Native helper/rollback tests remain in `update`.

See [current validation](../review/REFACTOR-VALIDATION.md) for executed checks and
[updater acceptance](../review/V33-VALIDATION.md) for remaining release gates.
