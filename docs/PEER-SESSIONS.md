# Equal-peer sharing sessions

Status: implemented; verification and remaining acceptance are recorded in
[validation](../review/PEER-SESSIONS.md).

## Agreed scope

- Equal members; no permanent host/guest or creator-availability requirement.
- One session per app now; multiple sessions later.
- LAN only; invite-only now; automatic discovery later.
- Any member can invite. All members are trusted; an invitation is a password.
- Fresh v3 pairing, no mixed-version sync or automatic pairwise-history migration.
- Preserve user files and legacy settings/history. Initial contents merge safely.
- Device-local ignores; `.fbignore` and `.gitignore` only, including nested files.
  `.folderbridgeignore` is not an alias; setup requires manual rename first.
- Serve live shared files, not a private content mirror. Keep durable metadata
  and temporary same-directory staging; retry changing/unavailable sources.
- Preserve module direction: app -> syncengine -> fileops.

## Engine interface and state ownership

`PrepareSession(folder, invite)` durably initializes a new session or rejoins the
same session's existing valid history before application configuration switches.
An empty invitation creates a random session ID/secret. A v4 invitation joins its
session through the pinned inviting member; there is no special creator identity.
`Start(Options)` resumes prepared state; missing/corrupt active state fails closed.
`Stop()` cancels and joins reconciliation, watcher/monitor workers and request
handlers before another session may start.

App configuration stores the active session ID/secret, local folder/name and
pause/enabled flags. Identity and correctness state are in the private settings
directory, not in the synchronized tree. Session IDs are independent of folder
paths and network addresses; per-session files leave room for future multiple
sessions without implementing a session manager now.

The device ID is its pinned certificate's SHA-256 fingerprint. Each session state
stores its folder/device binding, monotonic local event sequence, member roster,
per-path causal versions and applied/pending filesystem state. The header/version,
paths, hashes, clocks and sequence invariants are validated when loading. Old v3
baselines are neither consumed nor rewritten.

## Causal reconciliation

Each path is a multi-value register. A version records its originating device,
sequence, content hash/size/mtime and the per-device version clock it observed.
An empty hash is an explicit deletion, not an absent manifest entry.

Merging retains versions not causally superseded by another version. It is
commutative, associative and idempotent. Equal-content concurrent edits retain
causal information but need only one physical copy. Wall clocks are not used to
choose winners. A live concurrent edit wins over a tombstone; differing concurrent
live versions use stable device/sequence ordering for the original and deterministic
`.conflict-DEVICE-SEQUENCE` copies for the rest.

Local edits advance from the last **materialized** clock, not all metadata heard
from peers. Learning an unavailable version is not equivalent to receiving its
bytes. A member preserves secondary versions before replacing its original.
Independently advertised conflict-copy paths are processed after their originals;
metadata alone cannot satisfy conflict preservation when the payload is absent.

Guarded writes/deletes/moves persist intent before mutating files. On restart, a
matching pending target is acknowledged as our completed operation, not a new
user edit. Other local changes retain their previous materialized context and
reconcile as edits. Tombstones are retained indefinitely; an offline device is
never timed out of causal history. An unchanged stale copy therefore cannot
resurrect an observed deletion.

## Membership, authentication and networking

Every member serves and pulls. The active listener exposes:

- `POST /v4/exchange`: authenticated self/roster exchange and a filtered history /
  available-content snapshot, not a remote write request.
- `GET /v4/file`: guarded live file reads.
- `POST /v4/batch/get`: bounded streamed TAR/gzip reads.
- `GET /v4/changes`: per-member long polling.
- `POST /v4/reverse`: optional authenticated HTTP/1.1 upgrade to reverse HTTP
  over the same pinned TLS connection (v4.0.1 and later).

TLS 1.3 clients pin the destination certificate and present their own certificate.
Requests also require the session secret and ID, and the claimed device ID must
match the client's actual certificate. Holding the secret authorizes joining; no
central approver, account, root server or externally trusted CA is needed.

Members forward authenticated session roster data. Each member increments a
persisted incarnation on restart; newer self-descriptions replace old addresses,
while stale gossip cannot restore them. Addresses must be private/link-local/
loopback literal IP endpoints. Redirects are not followed. Every member listens on
TCP 48713 in normal use; tests use isolated ephemeral listeners, never the running
installed app's port.

### Outbound-initiated transport (v4.0.1)

Each member maintains two outbound reverse connections to each known member,
with bounded connection attempts and automatic reconnect after disconnection.
After the authenticated upgrade, the dialing member serves the existing v4
handlers on that same encrypted socket. The accepting member uses it as an HTTP
client connection, preferring reverse transport for exchanges, live-file/batch
reads and change polling, with ordinary direct connections as fallback. Two
sockets allow a long poll and reconciliation to progress independently.

Certificate pinning is performed before sending the upgrade credentials. Every
inner request still requires the session ID/secret and a device ID matching the
actual TLS peer certificate; the transport does not trust a forwarded identity
header. The route cannot dial arbitrary targets and is not a general proxy.
Socket admission and cleanup use a separate lock from reconciliation. Stop cancels
outbound workers, closes hijacked inbound sockets and joins handlers/workers;
address changes cancel and replace the associated outbound workers.

This permits two-way synchronization when only one member can initiate TCP
connections. It does not circumvent outbound application/network restrictions,
provide corporate HTTP proxy support, or add Internet routing/relays. Existing
v4.0.0 members reject the optional route and retain direct syncing; upgrade both
sides to benefit. No settings, invitation or durable-history migration is needed.
Closed-listener, reconnect, streaming/batch/move/delete, authentication and shutdown
evidence is in [outbound transport validation](../review/OUTBOUND-TRANSPORT.md).

Filesystem events and per-member long polls trigger debounced reconciliation; a
60-second safety scan and watcher fallback remain. Offline connections have bounded
dial/exchange attempts and retry delays. A current same-session invitation can
refresh a stale member address without resetting history. If no reachable member
holds a required version, status remains pending until its source returns.

## Ignore and filesystem safety

Policies are local; remote rule text is not combined into the local matcher.
Histories and available content are filtered by the sending device. Receivers
apply and recheck their own rules before mutations. Filtered absence never emits
a tombstone. Ignoring does not delete existing files. Removing an ignore rule does
not turn an ignored interval into local deletions.

Ignore files can synchronize as ordinary files; include `.fbignore` in its own
rules to intentionally keep that policy local. Fresh setup blocks obsolete
`.folderbridgeignore` files outside `.git` instead of silently exposing formerly
excluded data.

Existing fileops primitives retain unsafe-root/path/link rejection, SHA-256
verification, durable replacement, locked-file retries and no-overwrite moves.
Small ordinary receives reuse bounded compressed batches and joined transfer
workers; ambiguous/conflicting files remain individual guarded transfers. Safe,
unambiguous unchanged-content moves need no payload. Bulk-deletion approval is
local to each affected computer, excluding confirmed unchanged-content moves.

Dashboard snapshots use a detached member view rather than waiting on the
reconciliation lock or a long-running hash/transfer operation.

## Compatibility and limits

The application has no active v1-v3 listener or host/join runtime. Existing low-level
pairwise test helpers remain for regression coverage of reused file/transfer
primitives; they are not supported mixed-version compatibility.

Explicit v3 setup backs up old configuration as `legacy-config.json`. Files, old
baselines and identity remain intact. Leaving is local and does not revoke shared
invitations. Rejoining the same v4 session/folder resumes its history; creating a
different session performs a new merge.

Deferred: multiple sessions, LAN discovery, Internet connectivity, automatic legacy
migration, individual device revocation and tombstone garbage collection.

The user subsequently authorized v4.0.0 commit/push/public stable publication
following successful hosted Windows checks, explicitly accepting the remaining
manual-testing gaps. See the release status in [validation](../review/PEER-SESSIONS.md).
This does not authorize stopping or replacing the running installed app.
