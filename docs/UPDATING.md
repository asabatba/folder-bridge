# Manual upgrades

FolderBridge is a single `FolderBridge.exe`. It does not check for releases,
download software, replace itself, or run an installation/recovery helper.

1. Download `FolderBridge.exe` from the trusted
   [release page](https://github.com/asabatba/folder-bridge/releases).
2. Choose **Quit app** from the dashboard or tray on each computer being upgraded.
   Closing the browser tab is not enough. Let any important work finish first;
   quitting cancels active synchronization.
3. Optionally copy the previous executable somewhere safe, then replace
   `FolderBridge.exe` in its existing application directory.
4. Start the new executable. Check the version in the dashboard, follow the v3
   transition below if applicable, and confirm a small test file synchronizes.

## v3 to v4: explicit fresh pairing

- Upgrade every participating computer. The equal-peer session protocol does not
  accept old host/guest invitations or mix versions.
- Before starting the new session, manually rename `.folderbridgeignore` files to
  **`.fbignore`**, including nested files. There is no old-name alias. Fresh setup
  blocks old-named rule files outside `.git` rather than silently dropping their
  exclusions. Nothing renames or deletes user files automatically.
- Choose **Create session** on any computer, then **Join session** on the others
  using invitations from any member. Select the existing local folders. The first
  sync merges contents and preserves concurrent versions; old pairwise deletion
  history is not imported.
- Old settings do not silently start the engine. Explicit setup preserves a
  `legacy-config.json` backup; old baseline/index/identity files remain intact.
- Ignore rules are now device-local. Matching ignore files normally synchronize;
  a member with different local exclusions does not block sharing between others.

## Settings and rollback

Leave `%AppData%\FolderBridge` and synchronized folders intact. Apart from the
explicit old-ignore filename rename above, no manual state-file migration is needed.
Configuration, TLS/device identity, causal session history and hash indexes are
separate from the executable. `session-*.json` is correctness state, not a cache:
do not delete it or copy it between devices. Missing/damaged active history blocks
startup rather than silently starting a fresh deletion baseline. Keep backups of important data: FolderBridge is not a backup tool.
If launch-at-login was enabled and you move the executable to another directory,
disable and re-enable that preference to update its path.

There is no automated rollback. If a build fails to start, quit it and manually
restore the trusted executable you retained. Rolling back to v3 also requires an
explicit restoration of the backed-up v3 configuration while the app is stopped;
v4 session configuration/history is not understood by v3. Keep backups of settings
and data before a real upgrade. This manual rollback procedure still needs physical
acceptance testing. Follow endpoint-security/device policy; do not bypass
restrictions or request elevation to force an upgrade.

The former self-updater was removed before deployment. Its helper, signed
manifest format, settings, health protocol and recovery journals are unsupported;
there is no compatibility/migration layer. Existing development artifacts were
not automatically deleted or executed by this change.

For creating releases, see [RELEASING.md](RELEASING.md). Historical validation
reports about the removed updater are not operating instructions for this build.
