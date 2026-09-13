# Changelog

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
