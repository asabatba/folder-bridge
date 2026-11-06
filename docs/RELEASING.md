# Releasing FolderBridge

Releases contain one unsigned Windows x64 `FolderBridge.exe`. There is no signing
key, update manifest, helper, protected signing environment or application-side
release discovery. The GitHub workflow creates a **draft only**; publication is
always manual.

## Publish a release

1. Commit/review the intended source and push `main` to
   `asabatba/folder-bridge`. Ensure GitHub Actions is enabled.
2. Create and push an **unused** stable version tag on that commit. Example:

   ```powershell
   git push origin main
   git tag -a v3.3.1 -m "FolderBridge v3.3.1"
   git push origin v3.3.1
   ```

   Use the intended version, not an already-used tag. Rerunning an old tag still
   builds its old commit. Tags must use `vX.Y.Z` without leading zeroes.
3. Open **Actions → FolderBridge release**. The Windows job runs `go test ./...`
   and `go vet ./...`, builds the GUI executable with the tag's version, prints
   its SHA-256 and creates an unpublished draft with the EXE attached.
4. In **Releases**, open the draft. Review the source/tag, generated notes and
   attached `FolderBridge.exe`; download and test the candidate on disposable
   data. Confirm expected version, pairing/sync, pause/resume and tray behavior.
5. When acceptance is complete, click **Publish release**. Users download the
   EXE and follow [manual upgrade instructions](UPDATING.md).

The workflow uses pinned checkout/setup-go actions, no Go cache and no persisted
checkout credentials. Its repository token is provided only to the draft-release
command. It never publishes a draft automatically. A failed existing draft/tag
may need operator cleanup before rerunning; nothing is overwritten by force.

No custom Actions secrets or variables are needed. Any old `UPDATE_PUBLIC_KEY`
repository variable or signing-only `release` environment is unused by this
workflow and may be removed manually in GitHub Settings. Do not upload private
keys. Existing offline key files are not needed for this release flow and are
not deleted by the source change.

## Local build

Requires Go 1.23 or newer:

```powershell
go test ./...
go vet ./...
go build -trimpath -ldflags="-s -w -H=windowsgui" -o FolderBridge.exe .
```

To set the displayed version explicitly:

```powershell
$flags = '-s -w -H=windowsgui -X folderbridge/internal/app.Version=3.3.1'
go build -trimpath -ldflags $flags -o FolderBridge.exe .
```

No private settings, credentials, TLS keys, developer logs or test data belong
in a release. Unsigned executables may trigger Windows/device-policy warnings;
only run/distribute software that the applicable policy permits.
