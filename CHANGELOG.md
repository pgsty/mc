# Changelog

## Unreleased

- Update silo-pkg to v3.14.2 and upstream minio-go to v7.4.1. Failed multipart
  uploads are cleaned up after cancellation, retention timestamps are sent in
  UTC, and STS duration requests are preserved. Existing copy error and
  checksum handling is retained. Parallel SDK GET and ComposeObject remain
  opt-in; this update does not enable them in mcli.
- Build and test with Go 1.27.2. Refresh Go x/* modules, compress v1.20.3,
  msgp v1.6.5 and the etcd clients v3.7.2. Update golangci-lint to v2.14.0,
  govulncheck to v1.9.0 and gofumpt to v0.12.0.

## RELEASE.2026-09-16T00-00-00Z — 2026-09-16

Package version: `20260916000000.0.0`.
[GitHub release](https://github.com/pgsty/mc/releases/tag/RELEASE.2026-09-16T00-00-00Z) ·
[Changes since 20260913](https://github.com/pgsty/mc/compare/RELEASE.2026-09-13T00-00-00Z...RELEASE.2026-09-16T00-00-00Z)

- Use silo-pkg v3.14.1 and upstream minio-go
  `v7.3.1-0.20260915093545-32e1f32cb176`. The SDK propagates errors embedded
  in CopyObject HTTP 200 responses so a failed copy cannot authorize `mv`
  to delete the source.
- Report permission failures in `mirror`, including unreadable source files,
  rejected destination writes, and failed local destination removals. Continue
  processing later objects, as before, but return a nonzero exit status when a
  finite mirror finishes with failures. `--skip-errors` is not required to keep
  processing permission failures. Per-object permission failures in watch
  mode do not cancel and restart the entire scan. Listing and watcher
  failures retain their existing cancellation and retry behavior.
- Suppress the final success statistics for failed mirrors. An explicit
  `--summary` still prints statistics; its JSON status is `failure`, and text
  output retains the object error diagnostics before the statistics.
- Keep JSON statistics valid when a fast transfer completes within one clock
  tick, instead of failing to encode an infinite transfer speed.
- Return a nonzero exit status when legal-hold set/clear fails, recursive
  retention set/clear partially fails, or `mv` copies successfully but cannot
  delete a source. Retention failures produce one object diagnostic instead
  of duplicate or misleading URL errors. Successful objects are not rolled back.
- Reject empty retention durations and invalid `find --regex` expressions
  through normal CLI errors instead of panicking.

**Script compatibility:** mirror permission failures, Object Lock failures,
and failed move cleanup that previously returned 0 now return 1. Inspect the
final exit status and error records; existing per-object copy start messages
and progress byte counters are not proof that the operation completed.

## RELEASE.2026-09-13T00-00-00Z — 2026-09-13

Package version: `20260913000000.0.0`. Source:
`4f609a4da3bb8548446715867b68ab6c2d53a097`; Go module version:
`v0.0.0-20260913012246-4f609a4da3bb`.
[GitHub release](https://github.com/pgsty/mc/releases/tag/RELEASE.2026-09-13T00-00-00Z) ·
[Changes since 20260903](https://github.com/pgsty/mc/compare/RELEASE.2026-09-03T07-13-05Z...RELEASE.2026-09-13T00-00-00Z)

- Preserve historical destination versions in `mirror --remove --watch`.
- Make service-restart dry runs report the plan without executing it, and make
  noninteractive restart behavior explicit.
- Return failure for failed transfers and S3 Select errors. Honor an explicit
  checksum on empty uploads and keep `pipe` JSON output under `--quiet`.
- Accept on/off boolean environment values and repair cross-platform CLI/JSON
  behavior. Existing configuration paths and `MC_*` names remain supported.
- Use silo-pkg v3.14.0 directly and upstream minio-go
  `v7.3.1-0.20260910142817-60bd07042d49`. Preserve the policy Deny/NotResource
  and bounded wildcard fixes introduced in pkg v3.13.3. Already-lost policy
  clauses must be recovered from the original policy source.
- Refresh Go x/* modules and the UBI image; build with Go 1.27.1 and scan with
  govulncheck 1.8.0. Retain go-systemd v22.6.0 for NetBSD portability. The scan
  reports no reachable or imported vulnerable package; an unused OpenPGP
  module-only advisory remains.

**Password-policy migration:** pkg v3.14.0 separates ChangeMyPassword from
CreateUser. Keep both actions in the same Deny statement if the previous
combined restriction must survive an upgrade or rollback. Saved policies are
not rewritten. This client release alone does not change a Server's permission
mapping. As of 2026-09-13, matching Server/Console source is merged but not yet
released; the latest Server 20260903 and Console v2.4.0 still use the old mapping.
See the [migration guide](https://silo.pgsty.com/compatibility/password-permissions/)
and [component matrix](https://silo.pgsty.com/compatibility/versions/).

The immutable release has six Linux/macOS/Windows archives, RPM/DEB/APK packages
for both architectures, and checksums (19 assets). Archives and the checksum
manifest have verified build attestations; RPMs carry the PGSTY GPG signature.
The release and `latest` Docker tags resolve to the verified amd64/arm64 manifest
`sha256:aa5cc1401b3e1ab482d215d5717e9e69b4f14970a3656f330ed20a549fe19020`.

Earlier releases: [release archive](https://github.com/pgsty/mc/releases).
