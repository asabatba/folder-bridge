# Outbound-initiated LAN transport validation

## Scope and status

Implemented at the user's request after a member was reported as `Pending /
unavailable` with a TCP dial timeout to `192.168.1.235:48713`. A corporate inbound
firewall is suspected, not established: the affected physical network is not
available to this verification session. No firewall, endpoint policy, installed
executable or running app was changed. The user subsequently authorized committing
and pushing these source changes to `origin/main`, then explicitly chose stable
v4.0.1 publication over a prerelease despite the unverified physical corporate-
network acceptance. Stable v4.0.1 is now publicly published after passing hosted
checks and tagged asset verification; the release notes disclose the remaining
gaps. Installation remains outside scope.

The restricted computer automatically opens two persistent, certificate-pinned
TLS connections to each known reachable member. An authenticated `/v4/reverse`
HTTP/1.1 upgrade lets that member issue ordinary v4 exchange, file/batch and change
requests back over those same sockets. Two connections keep long polling from
blocking file reconciliation. Direct v4 transport remains available as fallback.
No dependencies, settings, secrets, invitations or durable history formats change.

The transport retains per-request session authentication and device/certificate
binding; nested reverse upgrades are rejected. It does not proxy arbitrary
connections or use public endpoints. Stop cancels and joins outbound workers and
handlers and explicitly closes hijacked inbound connections. Connections retry
following a network interruption.

## Regression and test evidence

Before implementation, this native Windows invocation failed:

```text
go test ./internal/syncengine -run '^TestSessionOutboundOnlyBidirectional$' -count=1 -timeout=45s
FAIL: reachable member cannot pull from outbound-only member
```

The restricted node's actual listener is closed, while it can still dial the
reachable member. The direct connection fails with connection-refused rather than
the user's network timeout; this deterministically verifies the essential
one-way-connectivity constraint, not the cause of the physical firewall issue.

The final regression verifies restricted-to-reachable file transfer, forced
reverse-socket disconnection/reconnection, a large streaming file, reachable-to-
restricted transfer, 24 small files, rename, deletion, cleared unavailable status
and bounded shutdown. It cannot accidentally succeed by connecting to the
restricted node's listener because that listener remains closed throughout.

`TestSessionReverseAuthentication` verifies wrong session secret/ID and wrong
certificate pin cannot establish a reverse connection, requests inside a valid
connection still reject incorrect authorization and device identity, valid
requests work, and nested upgrades are refused. A new test initially closed an
unconsumed response body before attempting connection reuse; draining that test
response fixed the fixture, with no production transport change needed.

Final checks:

- **99 native Windows tests pass, zero skips**, Go **1.23.12**, using a genuine
  Windows 8.3 short-name TMP directory. No production-port exclusion.
- **99 native Windows race tests pass, zero skips**, same minimum Go toolchain.
- All **89 Linux tests execute successfully under WSL**, using Linux binaries
  cross-compiled with Go 1.23.12 (not merely compiled).
- Windows and Linux `go vet ./...` and root builds pass on Go 1.23.12.
- Final closed-listener/reconnection, reverse authentication and automatic
  three-engine lifecycle tests pass **five repeated race runs** on native
  Go 1.27.3. Reverse authentication separately passes ten repeated race runs.
- GUI build `-h` smoke exits zero. Only help was launched, not a new sync instance.
- `gofmt` and `git diff --check` pass.
- Active LSP probes of all six changed/new Go files time out/inconclusive. They
  are not claimed clean; compiler, vet and test results provide verification.

Reproduction script: `dist/outbound-verify.ps1` (local ignored artifact). Logs:
`dist/outbound-windows.log`, `dist/outbound-race.log`,
`dist/outbound-verification.log`, `dist/outbound-repeat.log` and
`dist/outbound-linux-{app,syncengine,fileops}.log`. Windows test logs are UTF-16
PowerShell Tee-Object output.

## Stable v4.0.1 publication

- Published [FolderBridge v4.0.1](https://github.com/asabatba/folder-bridge/releases/tag/v4.0.1)
  as the latest stable release (not draft or prerelease).
- Annotated tag `v4.0.1` selects clean source commit
  `bbab9657c6cf8547595d713248c8bcc5b02180a2`; implementation commit is `982d5a4`.
- Hosted source checks on main: [42588828352](https://github.com/asabatba/folder-bridge/actions/runs/42588828352),
  Windows/Ubuntu tests pass. Tag checks: [42588832950](https://github.com/asabatba/folder-bridge/actions/runs/42588832950),
  Windows/Ubuntu tests pass, including the workflow's Linux race check.
- Tagged Windows tests/vet/build and draft creation:
  [42588832956](https://github.com/asabatba/folder-bridge/actions/runs/42588832956), successful.
- Single unsigned Windows x64 `FolderBridge.exe`, **7,342,592 bytes**.
- SHA-256: `1a3d398beb9dc8cc392536d9ca5bec2756e03e59bfe2992c88bdd1ed7640482a`.
- Downloaded the hosted asset before publication; checked hash/size against GitHub
  metadata, embedded `vcs.revision` against the tag, `vcs.modified=false`, Windows
  amd64 target, tag-derived version linker flag and help smoke (exit zero).
- After publication, independently downloaded the public EXE **without credentials**
  and verified its hash/size. Unauthenticated latest-release API confirms v4.0.1
  is public, stable and contains exactly the expected single asset.
- Verification copies: `dist/release-v401-hosted/FolderBridge.exe` and
  `FolderBridge-public.exe`. Logs/metadata: `dist/release-v401-hosted-checks.log`,
  `dist/release-v401-asset-verification.log`, `dist/release-v401-publication.log`,
  `dist/release-v401-final-verification.log`, `dist/release-v401-public-metadata.json`.
- Public notes explicitly disclose unverified corporate-network and earlier v4
  physical/manual acceptance gaps, outbound TCP 48713 requirements, upgrading both
  members and preserving existing v4 history. The user accepted stable publication
  with those limitations. No installed/running app or firewall changes were made;
  existing v3 tags/draft and v4.0.0 release remain untouched.

## Development artifact

- Path: `dist/outbound/FolderBridge.exe`
- Build version: `4.0.1-dev` (not a release)
- Windows x64 GUI, 7,342,592 bytes
- SHA-256: `BB40813C00BC0C919AC90938347F61C55F0C154A647D91C3EDB4356A5FE0301A`
- Built with Go 1.23.12, `-trimpath -ldflags '-s -w -H=windowsgui -X folderbridge/internal/app.Version=4.0.1-dev'`.

## Remaining acceptance and use

Upgrade both participating computers to benefit; v4.0.0 members continue to use
ordinary direct connections and cannot serve over reverse connections. Existing
v4 settings and session history should be retained; no fresh pairing or history
reset is required. Initial pairing should use an invitation from the reachable
computer, pasted on the restricted one. Existing sessions need no new invitation
unless a known LAN address is stale.

Physical testing remains required: run the released v4.0.1 build on both actual
computers, confirm both member statuses connect, create/edit/delete a disposable
file in both directions, then test disconnect/reconnect and pause/resume. Back up
important data before testing. Do not start a second instance alongside an
existing instance or delete session history to resolve a connection problem.

The restricted member must be awake/running and allowed to initiate outbound TCP
48713 to at least one reachable member. This cannot solve blocked outbound peer
traffic, corporate HTTP-proxy-only access, network isolation in both directions or
endpoint policy prohibiting the application. It adds no relay, public Internet
routing, firewall exception or policy bypass. The original v4 physical acceptance
gaps remain as recorded in `PEER-SESSIONS.md`.
