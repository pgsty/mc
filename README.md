<p align="center">
  <a href="https://silo.pgsty.com/">
    <img src=".github/silo.svg" alt="Silo emblem" width="112"><br>
    <img src=".github/silo-word.svg" alt="SILO" height="40">
  </a>
</p>

<h1 align="center">mcli</h1>

<p align="center">
  <strong>The command-line for Silo — a MinIO Client fork maintained by PIGSTY</strong>
</p>

<p align="center">
  <a href="https://silo.pgsty.com/">Website</a> ·
  <a href="https://silo.pgsty.com/reference/minio-mc/">Documentation</a> ·
  <a href="https://silo.pgsty.com/download/#client">Download</a> ·
  <a href="https://silo.pgsty.com/tags/mcli/">Release Notes</a> ·
  <a href="https://silo.pgsty.com/compatibility/mcli/">Compatibility</a> ·
  <a href="https://silo.pgsty.com/about/security/">Security</a> ·
  <a href="README_ZH.md">中文</a>
</p>
<p align="center">
  <a href="https://silo.pgsty.com/"><img alt="Website" src="https://img.shields.io/badge/Website-silo.pgsty.com-1d588c"></a>
  <a href="https://github.com/pgsty/mc/releases"><img alt="GitHub Release" src="https://img.shields.io/github/v/release/pgsty/mc?include_prereleases&label=release&logo=github"></a>
  <a href="https://hub.docker.com/r/pgsty/mc"><img alt="Docker Pulls" src="https://img.shields.io/docker/pulls/pgsty/mc?logo=docker"></a>
  <a href="go.mod"><img alt="Go Version" src="https://img.shields.io/github/go-mod/go-version/pgsty/mc?logo=go"></a>
  <a href="LICENSE"><img alt="License" src="https://img.shields.io/badge/license-AGPLv3-blue"></a>
</p>


> [!IMPORTANT]
> `pgsty/mc` is an independent, community-maintained fork of the open-source [MinIO Client](https://github.com/minio/mc), published by [Pigsty](https://pigsty.io). It is not affiliated with, endorsed by, or sponsored by MinIO, Inc. “MinIO” is used only to identify the upstream project and compatibility lineage.

## Overview

`pgsty/mc` maintains one downstream release line based on the final upstream MinIO Client commit, [`77f82e18`](https://github.com/minio/mc/commit/77f82e18b5401a65958f1619df6ebb994634bd88). After the upstream repository was archived, this fork keeps the client built, patched, tested, and released through the same maintained supply chain as the [Silo](https://silo.pgsty.com/) object storage server, while remaining a general-purpose client for filesystems and Amazon S3-compatible object stores.

The fork follows one rule: **the shipped artifact and its distribution channels are renamed; the tool you use is not.** Standalone archives and Linux packages ship the binary as `mcli`, while commands, flags, configuration, and wire behavior stay unchanged from upstream. The complete, versioned list of differences is maintained in the [compatibility notes](https://silo.pgsty.com/compatibility/mcli/).

The official project portal is [silo.pgsty.com](https://silo.pgsty.com/). It brings the command reference, downloads, release and security notes, and project legal information together.

## Find the Right Resource

| Looking for | Canonical location |
| :-- | :-- |
| Project overview and navigation | [Silo Website](https://silo.pgsty.com/) |
| `mcli` installation methods and downloads | [Download & Install](https://silo.pgsty.com/download/#client) |
| Client and administration command reference | [`mcli` reference](https://silo.pgsty.com/reference/minio-mc/) · [`mcli admin` reference](https://silo.pgsty.com/reference/minio-mc-admin/) |
| Release notes for this client | [`mcli` release notes](https://silo.pgsty.com/tags/mcli/) |
| Differences from upstream `mc` | [`mcli` compatibility notes](https://silo.pgsty.com/compatibility/mcli/) |
| Project news and security advisories | [Blog](https://silo.pgsty.com/blog/) · [release](https://silo.pgsty.com/blog/release/) and [security](https://silo.pgsty.com/blog/security/) notes |
| Versioned archives, packages, checksums, and source | [GitHub Releases](https://github.com/pgsty/mc/releases) |
| Bug reports and feature discussions | [GitHub Issues](https://github.com/pgsty/mc/issues) |
| Vulnerability reporting | [Security Policy](https://silo.pgsty.com/about/security/) |
| License, attribution, and trademark information | [`LICENSE`](LICENSE) · [`NOTICE`](NOTICE) · [`CREDITS`](CREDITS) · portal [license](https://silo.pgsty.com/about/license/), [attribution](https://silo.pgsty.com/about/attribution/), and [trademark](https://silo.pgsty.com/about/trademark/) pages |

## Related Projects

| Repository | Description |
| :-- | :-- |
| [`pgsty/silo`](https://github.com/pgsty/silo) | Silo object storage server — the S3-compatible MinIO fork this client accompanies |
| [`pgsty/mc`](https://github.com/pgsty/mc) | This repository — the Silo client, shipped as `mcli` with the `mc` command name |
| [`pgsty/silo-console`](https://github.com/pgsty/silo-console) | Admin web console for the Silo server |
| [`pgsty/silo-pkg`](https://github.com/pgsty/silo-pkg) | Shared Go packages maintained for the Silo forks |
| [`pgsty/pigsty`](https://github.com/pgsty/pigsty) | Pigsty — the PostgreSQL distribution that ships Silo as its object storage |

## Maintenance Policy

The active `main` release line covers:

- build, toolchain, and dependency maintenance;
- applicable security fixes and sensitive-output hardening;
- focused fixes for reproducible defects and Silo interoperability;
- versioned archives, Linux packages, checksums, and multi-architecture images;
- release automation, documentation, and Pigsty integration.

Changes are kept narrow and tested where practical. Maintenance is best effort; no response, remediation, or release schedule is guaranteed.

### Out of scope

- a separate client roadmap, new object-storage protocol, or speculative commands;
- broad rewrites or changes that materially expand the downstream delta;
- historical releases or multiple support branches;
- commercial support, SLAs, 24×7 coverage, or SUBNET services;
- guaranteed compatibility with every S3 implementation or future proprietary MinIO API.

## Governance

The client is maintained together with the [Silo server](https://github.com/pgsty/silo) under one release process: DCO-signed commits, reviewed pull requests, and versioned `RELEASE.YYYY-MM-DDTHH-MM-SSZ` tags with checksummed artifacts. Each release is announced with a [release note](https://silo.pgsty.com/tags/mcli/) on the portal; security advisories follow the [security policy](https://silo.pgsty.com/about/security/). Upstream copyright, license, and third-party notices are preserved in [`LICENSE`](LICENSE), [`NOTICE`](NOTICE), and [`CREDITS`](CREDITS).

## Compatibility

This fork aims to preserve:

- the `github.com/minio/mc` module path, `~/.mc` configuration, and familiar command behavior;
- S3 Signature V2/V4 access and common MinIO/Silo administration workflows;
- `RELEASE.YYYY-MM-DDTHH-MM-SSZ` tags and the `mc` container entrypoint;
- the `mc` name for source builds, while standalone archives and Linux packages use `mcli`; the Silo image provides both names.

Connections to MinIO-operated services — the update feed, SUBNET, telemetry, and the pre-seeded `play` alias — are severed; affected commands remain for script compatibility and fail with a stable error. In particular, `mc update` is intentionally disabled: upgrade through the [Pigsty package repository](https://pigsty.io/docs/repo/infra/list/#object-storage) or [GitHub Releases](https://github.com/pgsty/mc/releases).

Compatibility is a goal, not a guarantee. Server-specific administration commands may vary across versions. Pin releases, review the [release notes](https://silo.pgsty.com/tags/mcli/) and [compatibility notes](https://silo.pgsty.com/compatibility/mcli/), keep a rollback path, and test the client against the target service before production use.

## Downloads and Release Artifacts

Use [Download & Install](https://silo.pgsty.com/download/#client) to choose a client installation method. GitHub Releases remains the source for versioned archives, packages, checksums, and source archives.

| Artifact | Location |
| :-- | :-- |
| Source | [`github.com/pgsty/mc`](https://github.com/pgsty/mc) |
| Standalone archives | [GitHub Releases](https://github.com/pgsty/mc/releases), providing `mcli` for Linux, macOS, and Windows on `amd64` and `arm64` |
| Linux packages | RPM, DEB, and APK for `x86_64`/`aarch64`, also distributed through the [Pigsty repository](https://pigsty.io/docs/repo/infra/list/#object-storage) |
| Container image | [`pgsty/mc`](https://hub.docker.com/r/pgsty/mc), multi-arch for `linux/amd64` and `linux/arm64`, with `mc` as the entrypoint |
| Silo bundle | [`pgsty/silo`](https://hub.docker.com/r/pgsty/silo) includes the client as `mcli` with an `mc` compatibility alias |

## Quick Start

Standalone archives and Linux packages expose the command as `mcli`:

```bash
mcli alias set local http://127.0.0.1:9000 ACCESS_KEY SECRET_KEY
mcli mb local/demo
mcli cp README.md local/demo/
mcli ls local/demo
```

The container retains the familiar `mc` entrypoint:

```bash
docker run --rm pgsty/mc:latest --version
docker run --rm pgsty/mc:latest alias ls
```

> [!WARNING]
> For production, pin a release instead of `latest`, verify checksums, use TLS and least-privilege credentials, and test destructive commands against non-production data first.

Build from source:

```bash
git clone https://github.com/pgsty/mc.git
cd mc
make build
./mc --version
```

## Common Commands

| Command | Purpose |
| :-- | :-- |
| `alias` | Configure credentials and endpoints |
| `ls`, `tree`, `stat`, `du` | Inspect buckets, objects, and local files |
| `mb`, `rb` | Create or remove buckets |
| `cp`, `get`, `put`, `mv`, `rm` | Transfer and manage objects |
| `mirror`, `diff`, `find` | Synchronize, compare, and search data |
| `anonymous`, `share` | Manage anonymous access and temporary URLs |
| `version`, `retention`, `legalhold`, `ilm` | Manage data-protection policies |
| `replicate` | Configure bucket replication |
| `admin` | Operate compatible MinIO/Silo servers |

Run `mcli --help`, `mcli <command> --help`, or consult the [client reference](https://silo.pgsty.com/reference/minio-mc/) for the complete command set.

## Contributing

Useful contributions include security and dependency updates, reproducible bug fixes, interoperability tests, release automation, packaging, and documentation.

Issues and pull requests should include the affected client and server versions, reproduction steps, impact, expected behavior, tests, and compatibility notes. Discuss large changes in an issue first.

There is no CLA: contributions are accepted inbound=outbound under the project license (AGPL-3.0-or-later) and contributors keep their copyright. Every commit must be signed off (`git commit -s`) per the [Developer Certificate of Origin](https://developercertificate.org/); see [`CONTRIBUTING.md`](CONTRIBUTING.md).

## Background

The upstream [`minio/mc`](https://github.com/minio/mc) repository was archived after its final 2025 development line. Pigsty maintains this fork because Silo needs a reproducible companion client release channel rather than depending on an archived upstream project.

The broader upstream changes, alternatives considered, and early fork maintenance record are documented below:

| Essay | Subject |
| :-- | :-- |
| [MinIO Is Dead](https://silo.pgsty.com/blog/post/minio-is-dead/) | Changes to the upstream project and distribution model |
| [MinIO Is Dead, Who Takes Over?](https://silo.pgsty.com/blog/post/minio-alternative/) | Alternatives considered |
| [MinIO Is Dead, Long Live MinIO](https://silo.pgsty.com/blog/post/minio-resurrect/) | Establishing the server and client release pipeline |
| [Two months into maintaining a MinIO fork](https://silo.pgsty.com/blog/post/minio-promise-kept/) | Initial security and maintenance work |

## License, Attribution, and Trademark

The client source is distributed under the [GNU Affero General Public License v3.0 or later](LICENSE). This fork derives from [`minio/mc`](https://github.com/minio/mc): [`NOTICE`](NOTICE) retains the upstream product notice, [`CREDITS`](CREDITS) records licenses and notices for included third-party components, and the Git history records downstream modifications.

MinIO is a trademark of MinIO, Inc. The name is used here only to identify the upstream project and compatibility lineage. Pigsty, Silo, `pgsty/mc`, and `mcli` are independent community efforts and are not affiliated with, endorsed by, or sponsored by MinIO, Inc.

The portal separately publishes the project [license summary](https://silo.pgsty.com/about/license/), [documentation attribution](https://silo.pgsty.com/about/attribution/), and [trademark notice](https://silo.pgsty.com/about/trademark/).
