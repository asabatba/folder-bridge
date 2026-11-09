# Original Windows tray icon validation

## Scope and changes

The user supplied `Teal and Blue Folder Link Icon.png` after requesting an
original tray icon and discussing pause/error variants. The source PNG has real
alpha transparency, generous padding and faint low-alpha generation speckles.
The original root upload is unchanged; a copy is retained as
`internal/app/icons/folder-link-source.png`.

Embedded `tray-normal.ico`, `tray-paused.ico` and `tray-error.ico` preserve the
folder-link design/colors/proportions, trim transparent padding and remove
near-invisible speckles. Paused adds an amber pause badge; error adds a red
exclamation badge. Each ICO has 16/20/24/32/48/64 pixel 32-bit DIB frames. See
`internal/app/icons/README.md` for preparation details.

`tray_icons_windows.go` selects a suitable frame and creates private native
HICONs at the system's small-icon size. The tray loads its three icons once on
its locked OS thread, reuses them for timer/state changes and Explorer restarts,
and destroys them on tray shutdown after normal notification-icon removal.
If custom loading fails, it logs the issue and retains the previous stock-icon
fallback. No UI framework, Go dependency, resource compiler, external asset or
runtime image processing is introduced. The retained source PNG is not embedded.

Scope is the notification-area icon only: Explorer's EXE icon, dashboard, sync
behavior/settings/history and release automation are unchanged. The running app
was not stopped/replaced. The user subsequently authorized commit, push and
stable v4.0.2 release after the pending interactive checks were disclosed.
Publication is gated on hosted main/tag checks and tagged EXE verification;
installation is not authorized by this release request.

## Verification

- All **103 native Windows tests and race tests pass**, zero skips, on minimum
  Go **1.23.12**, using genuine short-name TMP and no production-port exclusions.
- All **89 Linux tests execute successfully under WSL**; the new icon code is
  Windows-only and does not affect the Linux build.
- Windows/Linux `go vet ./...` and root builds pass on Go 1.23.12.
- Four new Windows tests validate normal/paused/error state mapping; embedded
  frame format, size selection and malformed ICO bounds; creation of all three
  native HICONs at six sizes with native 32-bit bitmap dimensions; actual HICON
  destruction and idempotent cleanup; and reuse of the cached artwork through
  engine status changes.
- Those four tests also pass **20 repeated native race runs** on Go 1.27.3.
- GUI build help smoke exits zero. Only `-h` was launched; no new sync instance.
- Inspected the prepared light/dark enlarged-pixel preview at
  `dist/tray-icon-preview.png` (16/20/24/32 pixel artwork). This is an asset preview,
  **not a screenshot of the live Windows notification area**.
- `gofmt` and `git diff --check` pass.
- Active LSP probes are incomplete: two files time out; the test file reports
  only a stylistic `rangeint` modernization hint. Compiler/vet/test results are
  the verification evidence, not an assertion of a clean LSP scan.

Local scripts/logs: `dist/generate-tray-icons.py`, `dist/tray-icon-verify.ps1`,
`dist/tray-icon-verification.log`, `dist/tray-icon-windows.log`,
`dist/tray-icon-race.log`, `dist/tray-icon-repeat.log`, and
`dist/tray-icon-linux-{app,syncengine,fileops}.log`. Windows test logs are UTF-16.

## Separate development build

- `dist/tray-icon/FolderBridge.exe`
- Version `4.0.2-dev` (not a release)
- Windows x64 GUI, **7,453,696 bytes**
- SHA-256: `D8F87F504DE2727E3E3D135ED609150842DA9D83453B8253C77EF2F764DC2549`
- Built on Go 1.23.12 with `-trimpath` and
  `-ldflags '-s -w -H=windowsgui -X folderbridge/internal/app.Version=4.0.2-dev'`.

## Remaining manual acceptance

With user authorization to replace/run the app, check the actual Windows tray on
light/dark taskbars at 100/125/150/200% scaling, paused/error badges, menu and
double-click behavior, Explorer restart restoration and exit. Existing native
HICON/state/lifetime tests do not replace interactive visual acceptance. Live
per-monitor DPI changes are not separately handled; the icon size follows the
system small-icon metric when the tray starts. No running-app installation or
release publication is implied by preparing these assets.
