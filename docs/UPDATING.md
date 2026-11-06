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
4. Start the new executable. Check the version in the dashboard, verify the
   configured folders/pairing and confirm a small test file synchronizes.

Leave `%AppData%\FolderBridge`, ignore files and synchronized folders alone.
Configuration, TLS pairing identity, baselines and hash indexes are separate from
the executable. Keep backups of important data: FolderBridge is not a backup tool.
If launch-at-login was enabled and you move the executable to another directory,
disable and re-enable that preference to update its path.

There is no automated rollback. If a build fails to start, quit it and manually
restore the trusted executable you retained. Follow endpoint-security/device
policy; do not bypass restrictions or request elevation to force an upgrade.

The former self-updater was removed before deployment. Its helper, signed
manifest format, settings, health protocol and recovery journals are unsupported;
there is no compatibility/migration layer. Existing development artifacts were
not automatically deleted or executed by this change.

For creating releases, see [RELEASING.md](RELEASING.md). Historical validation
reports about the removed updater are not operating instructions for this build.
