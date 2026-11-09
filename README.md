# FolderBridge v4 — equal-peer LAN sessions

A portable folder synchronizer for **multiple Windows 10/11 computers** on your
local network. One shared session per app installation; no permanent host or guest.

- Create a session on any computer, then invite other computers. **Any member can
  invite**, serve files and receive changes. Reachable members can keep syncing
  when the creator is offline.
- One native Go executable, no account, cloud, relay, helper or extra runtime.
- Native Windows tray controls, pause/resume, sync now, launch-at-login and a local
  browser dashboard with member connection/pending status.
- Two-way synchronization of nested files and ordinary deletions. Durable causal
  edit/deletion history prevents an unchanged offline copy resurrecting a deletion.
- Concurrent edits retain distinct contents as deterministic conflict copies.
  An edit concurrent with a deletion wins over the deletion.
- Native Windows filesystem notifications, coalescing, per-member HTTPS long polls,
  a 60-second safety scan and a 10-second local fallback if native watching fails.
- Persistent SHA-256 hash index; guarded, disk-flushed, verified file replacement.
- Bounded compressed TAR batches for small files and safe, unambiguous unchanged-
  content moves without retransmitting file payloads.
- **Device-local** ignore rules from nested `.gitignore` and **`.fbignore`** files.
  The dashboard inspector reports the decisive local rule and source line.
- TLS 1.3, pinned device certificates, certificate-bound device identities and a
  secret session invitation. All members of a session must be trusted.
- No private content mirror: files are served from shared folders. Only metadata
  persists in app settings; file transfers use same-directory temporary staging.
- Manual upgrades only. No background release checks or software downloads.

## Upgrading from v3

**All members need v4 and fresh pairing. Old host/guest links are unsupported.**

1. Back up important files. Quit the old app before replacing its executable.
2. Rename existing `.folderbridgeignore` files to **`.fbignore`**, including nested
   files. The old name is not an alias. Fresh session setup refuses to proceed
   while it finds an old-named ignore file outside `.git`.
3. Run the new app. Old settings do not silently start synchronization or import
   a two-computer deletion baseline. Explicitly create/join the new session.
4. The first synchronization merges existing contents. Different contents at the
   same path are preserved, not silently overwritten with one computer's version.

The app preserves existing files and old history; explicit setup backs up old
configuration as `legacy-config.json`. See [manual upgrades](docs/UPDATING.md).

## Quick start

1. Run `FolderBridge.exe` on every participating computer. Open the dashboard at
   exactly **`http://127.0.0.1:47831/`**. The app stays in the notification area.
2. On any computer, choose **Create session**, give the computer a recognizable
   name, select a dedicated local folder, and choose **Save & start**.
3. Copy a reachable LAN invitation under **Invite another computer**.
4. On another computer choose **Join session**, select its own local folder, paste
   the invitation, and start. Repeat for more computers. An invitation from any
   existing member works; that member must be online to complete the initial join.
5. Check **Session members** and try a small file. A third computer can join through
   the second; the first does not have to remain online afterward.

Invitations are passwords. Anyone holding one can join, read/change synchronized
files and invite others. Do not post them publicly. Leaving a session is local:
**it does not revoke invitations, remove other members or delete their files**.
Individual device revocation is not implemented; use a new session if its secret
has been exposed.

### Pause, stop and leave

- **Pause syncing** cancels this computer's work and closes its peer listener,
  preserving configuration and files. Resume to reconnect.
- **Stop syncing** disables synchronization but retains the session; use
  **Resume session** to restart it.
- **Leave session** keeps local files and saved history, and returns to setup.
  Creating a different session starts a fresh merge. Rejoining the same v4 session
  with the same folder resumes its saved history rather than resetting it.
- **Quit app** exits the process. Closing the browser tab does not quit the app.

### Networking

- LAN only. No automatic discovery, cloud account, Internet relay or NAT traversal.
- **Every active member listens on TCP 48713**, not just the creator. Permit inbound
  access only when authorized by your device/network policy. The app does not
  disable firewalls or bypass endpoint protections.
- **Outbound-initiated connections (v4.0.1 and later):** if one
  computer cannot accept inbound connections, it automatically opens persistent,
  pinned-TLS connections to reachable session members. Those members can pull its
  files and change notifications over those connections. Upgrade both computers
  for this capability; existing v4 pairing/history is retained. No setting or
  firewall change is required, but outbound access to another member's TCP 48713
  must be permitted. If peer traffic is blocked in both directions, this cannot
  help; there is no Internet relay, corporate-proxy support or policy bypass.
- The dashboard listens only on `127.0.0.1:47831`; `localhost`, alternate hostnames
  and other ports are rejected to protect session credentials.
- Members exchange their identities and private LAN IP addresses over authenticated
  TLS. They connect directly to one another and can relay available files through
  an intermediate member over successive reconciliations.
- If an address becomes stale and the members cannot reconnect, paste a **current
  invitation from that member** into **Refresh a member's changed LAN address**.
  Restarting the member refreshes its advertised adapter addresses. No history reset
  is required. Invitations accept private, link-local or loopback IP endpoints,
  not public Internet addresses or arbitrary DNS names.
- “Up to date with reachable members” does not mean offline members have received
  changes. Their pending status remains visible when the session is active.

## Ignore rules

Place `.fbignore` or `.gitignore` in the synchronized folder or a subfolder:

```gitignore
node_modules/
build/
*.log
.env*
!important.log
```

Rules affect **this computer's sending and receiving only**. One member's exclusion
never blocks two other members from sharing a path. Ignored absence is not a deletion;
ignoring never deletes an existing copy. Removing an ignore rule reconciles safely
without treating the ignored interval as local deletions.

Ignore files themselves normally synchronize, so matching rule files naturally give
members matching policies. To deliberately keep `.fbignore` local, include
`.fbignore` in that file's own rules. Rules are not a substitute for backups or secure
removal of files already shared.

Supports common Git glob syntax, negation, directory patterns and nested rules,
including the ignored-parent restriction. Matching is case-sensitive. Each rule
file is limited to 64 KiB and the combined policy to 1 MiB. `.git/` and `.fb-part-*`
are always excluded. `.folderbridgeignore` is **not** recognized.

## Conflicts, deletions and recovery

- Each path has durable causal versions, including deletion tombstones. Wall-clock
  timestamps and a member being unreachable are not used to infer deletions.
- Concurrent different contents converge on the same original-name version and
  `.conflict-DEVICE-SEQUENCE` copies on every member. Ordering uses stable device
  identities, not a privileged computer, arrival order or the newest clock time.
- A member must preserve available conflicting contents before replacing its
  original. If needed bytes are unavailable, synchronization stays pending rather
  than claiming completion. Changes to a live transfer source cause a safe retry.
- If at least 75% of at least 20 synchronized files would be deleted on a computer,
  it stops for local approval. Other members may independently need approval.
  Unambiguous unchanged-content renames are excluded from that tally.
- Interrupted transfers never intentionally publish half-written files. Durable
  write intents distinguish an installed transfer from a new user edit on restart.
  Read errors, damaged/missing history and failed folder scans fail closed.
- Files are transferred in full, not resumed or block-delta synchronized. Successful
  files in a partially completed batch remain committed; batches are not transactions.
- Regular orphan `.fb-part-*` files older than 24 hours are cleaned at startup.

## Private settings and limitations

Settings live in `%AppData%\FolderBridge`:

- `config.json`: current session ID/secret, local folder/name and enabled/pause flags.
- `peer-cert.pem`, `peer-key.pem`: stable TLS/device identity. Keep these private.
- `session-*.json`: membership, version clocks, tombstones and write intents. These
  are correctness state, **not disposable caches**. Do not delete them to fix a
  connection problem. Missing/corrupt active history blocks startup.
- `index-*.json`: best-effort hash cache; corruption triggers rehashing, not deletion.
- `legacy-config.json` and old `baseline-*.json`: preserved v3 data, not imported.

Do not put private settings inside the shared folder. The app rejects that selection,
including Windows short-name/extended-path aliases. Do not copy one device's identity
or session-state files to another device.

One session per app is supported now; multiple sessions and LAN discovery are deferred.
Empty directories, symlinks, junctions and other reparse-point paths are not replicated.
Paths through linked ancestors are rejected. Case-only moves are not optimized.
Locked Windows files retry briefly, then remain pending. There is no content cache,
so a version whose only copy is on an offline computer must wait for that computer.
Deletion history is retained rather than expiring offline members; metadata grows
with the paths and device identities used by a session.

Hash reuse depends on size/mtime; deliberately modifying a file with both preserved
while the watcher is inactive may evade the cache. Tray/login notifications require
an interactive Windows session. This is an unsigned executable; respect corporate
security restrictions. **FolderBridge is not a backup system.**

## Build from source

Requires Go 1.23 or newer:

```powershell
go test ./...
go build -trimpath -ldflags="-s -w -H=windowsgui" -o FolderBridge.exe .
```

Cross-compile for Windows x64:

```bash
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags='-s -w -H=windowsgui' -o FolderBridge.exe .
```

See [architecture](docs/ARCHITECTURE.md), [session design](docs/PEER-SESSIONS.md),
[validation](review/PEER-SESSIONS.md) and [manual draft releases](docs/RELEASING.md).
No signing key, helper or dependency was added.

**Acceptable use:** Only synchronize files/devices you are authorized to connect.
If device policy restricts file synchronization, use an approved solution instead.
