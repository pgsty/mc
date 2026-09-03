# SILO mcli Guide

`pgsty/mc` is the maintained client for `pgsty/silo`. Its administration
commands are release-tested against SILO. Operation against unmodified upstream
MinIO and other S3-compatible services is best effort, not a release guarantee.
Retained command names, `MC_*` variables, `x-minio-*` protocol fields, config
formats, and historical import paths do not by themselves create an upstream
support commitment.

Use `github.com/pgsty/silo-pkg/v3` directly and reject new maintained-source
imports of `github.com/minio/pkg/v3`. Do not lower or replace the SILO package to
make an upstream dependency graph compile. `github.com/minio/minio-go/v7` is the
explicit exception and should track the verified upstream release/commit.

Keep generic S3 and upstream-MinIO compatibility when inexpensive, but record
failures as advisory compatibility findings unless the user explicitly makes
them release-blocking. Functional and release gates should use `pgsty/silo`.
