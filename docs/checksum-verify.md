# Checksum Verify Design

## Decision

MCLI provides a top-level, read-only verification workflow:

```text
mcli checksum verify ALIAS/BUCKET/OBJECT
mcli checksum verify --recursive ALIAS/BUCKET[/PREFIX]
mcli checksum verify --manifest candidates.jsonl ALIAS
```

The command verifies stored S3 additional checksums against the logical bytes
returned by the object API. It does not identify the historical writer with
certainty, inspect `xl.meta`, repair metadata, or write object data.

The command is intentionally not under `admin`: version one uses only S3 data
plane operations and should work with SILO, inherited MinIO data, and other
compatible endpoints when the caller has sufficient object permissions.

## Historical problem

Older CopyObject paths could attach the server-side checksum hasher after
destination compression. When checksum calculation was required, the stored S3
checksum could therefore describe the compressed storage stream instead of the
logical object bytes returned to clients. The object body could remain readable
while checksum-aware clients reported a mismatch.

Future writes are fixed in SILO Server. This command addresses only the missing
read-only inventory and verification workflow for existing objects.

## V1 scope

Version one supports:

- CRC32, CRC32C, CRC64NVME, SHA1, and SHA256;
- checksums explicitly identified as `FULL_OBJECT`;
- one object, a recursive bucket/prefix scan, all object versions, or an exact
  VersionID;
- a minimal JSON Lines candidate manifest;
- SSE-C through the existing `--enc-c` prefix-to-key mapping;
- dry-run cost estimation, bounded concurrency, download limiting, JSON output,
  time and size filters, and an optional JSON Lines report.

Version one does not support:

- checksum repair, CopyObject, PUT, DELETE, or metadata mutation;
- COMPOSITE checksum verification;
- inferring checksum type from ETag;
- `--rewind`, tier restore, server batch jobs, or automatic historical-cause
  attribution.

## Minimal manifest

The manifest is an input candidate list, not a result or resume file. Each line
contains only the exact object identity available from external audit logs or
deployment records:

```json
{"bucket":"archive","key":"2025/report.json","versionId":"optional"}
```

`bucket` and `key` are required. `versionId` is optional. The endpoint alias is
supplied once on the command line. Manifest mode is mutually exclusive with
recursive, version, and time-selection flags.

This deliberately small schema avoids placing checksums, object metadata, or
encryption secrets in the candidate file. Resume and checkpoint semantics are
deferred.

## Dedicated S3 helper

The command uses a dedicated private S3 helper rather than changing the shared
`ClientContent` structure in the same change.

The existing conversion to `ClientContent` does not retain ChecksumType and may
overwrite earlier checksum values when more than one is present. Changing that
shared behavior would also change existing `stat`, copy, mirror, and JSON output
contracts. The dedicated helper keeps the initial verification patch isolated
and reversible:

1. `StatObject` is called with checksum mode enabled and an exact VersionID.
2. All supported checksum values and ChecksumType are retained.
3. `Core.GetObject` returns the logical object stream without enabling the SDK's
   automatic checksum validator.
4. For an unversioned or null version, GET uses the HEAD ETag as `If-Match`, and
   a second HEAD checks that the object did not change during verification.

The shared `ClientContent` checksum representation can be corrected in a
separate compatibility change with its own `stat` and JSON regression review.

## Result contract

Every candidate produces one stable status. Important groups are:

- `MATCH`: all supported stored checksums equal independently calculated values;
- `MISMATCH`: at least one stored value differs;
- `NO_CHECKSUM`: no additional checksum was stored, so the body is not read;
- `WOULD_VERIFY`: dry-run found a supported full-object checksum;
- `UNKNOWN_*`: verification could not make a reliable statement;
- `SKIPPED_*`: the candidate was intentionally excluded by a filter.

`UNKNOWN` is never converted into `MATCH`. The default `--fail-on any` returns a
non-zero status for a mismatch or incomplete verification. MCLI's existing exit
code convention is preserved: normal completion is 0, command failure is 1,
and signals keep their existing codes. Detailed classification lives in the
per-object JSON Lines records and final summary.

`--fail-on` accepts `mismatch`, `unknown`, `any`, or `none`. It applies to
completed object results, not fatal argument, authentication, enumeration, or
report-write failures. Dry-run is an inventory operation and does not apply
`--fail-on`; unsupported objects remain visible in its counts. `NO_CHECKSUM`
means there was no stored additional checksum to compare and does not by itself
make the command fail. Human and JSON summaries must show this count explicitly
so an all-`NO_CHECKSUM` run cannot be mistaken for a fully verified data set.
`unknown` matches only `UNKNOWN_*` results; use the default `any` to fail on both
unknown results and checksum mismatches.

The result means only:

> The additional checksum returned by the endpoint at verification time does
> or does not match the logical bytes returned at verification time.

It does not by itself prove that the historical compression bug created the
object or establish correctness against an external source of truth.

## Safety and operational controls

- Only LIST, HEAD, and GET are allowed.
- `--dry-run` performs LIST and HEAD but no object-body GET.
- FULL_OBJECT checksums are streamed through bounded hashers; bodies are never
  buffered in full or written to disk.
- The default worker count is four and the global download limit remains
  available. `--max-workers` accepts values from 1 through 64.
- `--max-size` allows large objects to be skipped explicitly.
- `--older-than` and `--newer-than` reuse MCLI's existing relative-duration or
  absolute-time filters. Filtering still requires a HEAD so the decision uses
  the authoritative object version metadata.
- Report files are newly created with mode `0600` and never contain object data
  or SSE-C keys.
- Tests must reject any unexpected write method at the mock S3 boundary.

## Deferred work

- COMPOSITE verification using paginated GetObjectAttributes part data;
- checkpoint/resume and richer manifest generation;
- an optional server-side read-only batch executor for very large estates;
- any repair workflow;
- correction of the shared `ClientContent` checksum representation.

For an exact object's historical versions, the current client API lists the
object-name prefix and filters exact key matches locally. This preserves correct
results but can make a broad key prefix more expensive to enumerate; callers
should prefer a manifest when an external log already provides exact VersionIDs.

## Acceptance invariants

The release test boundary keeps the following behavior explicit:

- both an empty VersionID and the literal `null` version are treated as mutable,
  use `If-Match`, and receive a post-read HEAD; a fixed VersionID uses neither;
- if any one of several stored checksums differs, the object is `MISMATCH`;
- `InvalidObjectState` is reported as a storage-class/restore condition before
  the generic HTTP 403 access-denied classification;
- normal worker completion returns exactly one result per candidate, and worker
  cancellation closes the result stream without deadlock;
- the known SILO missing-SSE-C-parameters response is classified without using
  URL or object-name text, which could otherwise create false KMS/SSE labels.
