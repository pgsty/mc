// Copyright (c) 2015-2025 MinIO, Inc.
// Copyright (c) 2025-2026 PGSTY
//
// This file is part of MinIO Client
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package cmd

import (
	"bufio"
	"context"
	"encoding/base64"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/minio/cli"
	json "github.com/minio/colorjson"
	"github.com/minio/mc/pkg/probe"
	minio "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/encrypt"
)

const (
	checksumVerifySchemaVersion = 1

	checksumResultMatch                    = "MATCH"
	checksumResultMismatch                 = "MISMATCH"
	checksumResultNoChecksum               = "NO_CHECKSUM"
	checksumResultWouldVerify              = "WOULD_VERIFY"
	checksumResultSkippedDeleteMarker      = "SKIPPED_DELETE_MARKER"
	checksumResultSkippedTimeFilter        = "SKIPPED_TIME_FILTER"
	checksumResultSkippedTooLarge          = "SKIPPED_TOO_LARGE"
	checksumResultUnknownComposite         = "UNKNOWN_UNSUPPORTED_COMPOSITE"
	checksumResultUnknownChecksumType      = "UNKNOWN_CHECKSUM_TYPE"
	checksumResultUnknownChecksumAlgorithm = "UNKNOWN_CHECKSUM_ALGORITHM"
	checksumResultUnknownSSECKeyMissing    = "UNKNOWN_SSEC_KEY_MISSING"
	checksumResultUnknownAccessDenied      = "UNKNOWN_ACCESS_DENIED"
	checksumResultUnknownKMSError          = "UNKNOWN_KMS_ERROR"
	checksumResultUnknownStorageClass      = "UNKNOWN_STORAGE_CLASS"
	checksumResultUnknownObjectChanged     = "UNKNOWN_OBJECT_CHANGED"
	checksumResultUnknownShortRead         = "UNKNOWN_SHORT_READ"
	checksumResultUnknownReadError         = "UNKNOWN_READ_ERROR"
	checksumVerifyFullObjectType           = "FULL_OBJECT"
	checksumVerifyCompositeType            = "COMPOSITE"
	checksumVerifyDefaultWorkers           = 4
	checksumVerifyMaximumWorkers           = 64
	checksumVerifyManifestMaximumLineSize  = 2 << 20
)

var checksumVerifyFlags = []cli.Flag{
	cli.BoolFlag{
		Name:  "recursive, r",
		Usage: "verify all objects recursively under a bucket or prefix",
	},
	cli.BoolFlag{
		Name:  "versions",
		Usage: "verify all object versions",
	},
	cli.StringFlag{
		Name:  "version-id, vid",
		Usage: "verify a specific object version",
	},
	cli.StringFlag{
		Name:  "older-than",
		Usage: "verify objects older than value in duration string (e.g. 7d10h31s)",
	},
	cli.StringFlag{
		Name:  "newer-than",
		Usage: "verify objects newer than value in duration string (e.g. 7d10h31s)",
	},
	cli.IntFlag{
		Name:  "max-workers",
		Usage: "maximum number of concurrent object reads",
		Value: checksumVerifyDefaultWorkers,
	},
	cli.BoolFlag{
		Name:  "dry-run",
		Usage: "list and stat candidates without reading object data",
	},
	cli.StringFlag{
		Name:  "max-size",
		Usage: "skip objects larger than this size (e.g. 10GiB)",
	},
	cli.StringFlag{
		Name:  "manifest",
		Usage: "verify bucket/key/versionId entries from a JSON Lines manifest",
	},
	cli.StringFlag{
		Name:  "report",
		Usage: "write object results and summary to a new JSON Lines file",
	},
	cli.StringFlag{
		Name:  "fail-on",
		Usage: "return failure on mismatch, unknown, no-checksum, any, or none",
		Value: "any",
	},
}

var checksumVerifyCmd = cli.Command{
	Name:         "verify",
	Usage:        "verify stored checksums against logical object data",
	Action:       mainChecksumVerify,
	OnUsageError: onUsageError,
	Before:       setGlobalsFromContext,
	Flags:        append(append(checksumVerifyFlags, encCFlag), globalFlags...),
	CustomHelpTemplate: `NAME:
  {{.HelpName}} - {{.Usage}}

USAGE:
  {{.HelpName}} [FLAGS] ALIAS/BUCKET/OBJECT
  {{.HelpName}} --recursive [FLAGS] ALIAS/BUCKET[/PREFIX]
  {{.HelpName}} --manifest FILE [FLAGS] ALIAS

FLAGS:
  {{range .VisibleFlags}}{{.}}
  {{end}}

EXAMPLES:
  1. Verify one object checksum.
     {{.Prompt}} {{.HelpName}} mysilo/archive/report.json

  2. Estimate the read cost before recursively verifying a prefix.
     {{.Prompt}} {{.HelpName}} --recursive --dry-run mysilo/archive/2025/

  3. Verify all historical versions under a prefix with four workers.
     {{.Prompt}} {{.HelpName}} --recursive --versions --max-workers 4 mysilo/archive/2025/

  4. Verify exact objects supplied by an external candidate manifest.
     {{.Prompt}} {{.HelpName}} --manifest candidates.jsonl --report results.jsonl mysilo
`,
}

type checksumVerifyCandidate struct {
	Alias        string
	Bucket       string
	Key          string
	VersionID    string
	DeleteMarker bool
}

type checksumVerifyManifestEntry struct {
	Bucket    string `json:"bucket"`
	Key       string `json:"key"`
	VersionID string `json:"versionId,omitempty"`
}

type checksumVerifyResult struct {
	SchemaVersion       int               `json:"schemaVersion"`
	Type                string            `json:"type"`
	Timestamp           time.Time         `json:"timestamp"`
	Alias               string            `json:"alias"`
	Bucket              string            `json:"bucket"`
	Key                 string            `json:"key"`
	VersionID           string            `json:"versionId,omitempty"`
	Size                int64             `json:"size"`
	ETag                string            `json:"etag,omitempty"`
	LastModified        *time.Time        `json:"lastModified,omitempty"`
	ChecksumType        string            `json:"checksumType,omitempty"`
	StoredChecksums     map[string]string `json:"storedChecksums,omitempty"`
	CalculatedChecksums map[string]string `json:"calculatedChecksums,omitempty"`
	Result              string            `json:"result"`
	ErrorCode           string            `json:"errorCode,omitempty"`
	ErrorMessage        string            `json:"errorMessage,omitempty"`
	BytesRead           int64             `json:"bytesRead"`
}

func (r checksumVerifyResult) String() string {
	resource := r.Alias + "/" + r.Bucket + "/" + r.Key
	if r.VersionID != "" {
		resource += "?versionId=" + r.VersionID
	}
	if r.ErrorMessage != "" {
		return fmt.Sprintf("%-38s %s (%s)", r.Result, resource, r.ErrorMessage)
	}
	return fmt.Sprintf("%-38s %s", r.Result, resource)
}

func (r checksumVerifyResult) JSON() string {
	b, err := json.MarshalIndent(r, "", " ")
	fatalIf(probe.NewError(err), "Unable to marshal checksum verification result.")
	return string(b)
}

type checksumVerifySummary struct {
	SchemaVersion int              `json:"schemaVersion"`
	Type          string           `json:"type"`
	Timestamp     time.Time        `json:"timestamp"`
	Counts        map[string]int64 `json:"counts"`
	Objects       int64            `json:"objects"`
	Verified      int64            `json:"verified"`
	BytesPlanned  int64            `json:"bytesPlanned"`
	BytesRead     int64            `json:"bytesRead"`
	Duration      time.Duration    `json:"durationNs"`
	DryRun        bool             `json:"dryRun"`
	Incomplete    bool             `json:"incomplete"`
}

func newChecksumVerifySummary(dryRun bool) checksumVerifySummary {
	return checksumVerifySummary{
		SchemaVersion: checksumVerifySchemaVersion,
		Type:          "summary",
		Timestamp:     UTCNow(),
		Counts:        make(map[string]int64),
		DryRun:        dryRun,
	}
}

func (s *checksumVerifySummary) add(result checksumVerifyResult) {
	s.Objects++
	s.Counts[result.Result]++
	s.BytesRead += result.BytesRead
	switch result.Result {
	// Verified counts objects whose stored checksum was actually recomputed and
	// compared. Objects and a zero exit status do not imply that: a run over a
	// prefix where nothing carries a checksum reports NO_CHECKSUM for every
	// object and still succeeds.
	case checksumResultMatch, checksumResultMismatch:
		s.Verified++
	case checksumResultWouldVerify:
		s.BytesPlanned += result.Size
	case checksumResultUnknownComposite,
		checksumResultUnknownChecksumType,
		checksumResultUnknownChecksumAlgorithm,
		checksumResultUnknownSSECKeyMissing,
		checksumResultUnknownAccessDenied,
		checksumResultUnknownKMSError,
		checksumResultUnknownStorageClass,
		checksumResultUnknownObjectChanged,
		checksumResultUnknownShortRead,
		checksumResultUnknownReadError,
		checksumResultSkippedTooLarge:
		s.Incomplete = true
	}
}

func (s checksumVerifySummary) String() string {
	if s.DryRun {
		return fmt.Sprintf("Checksum verification dry-run: %d objects, %d would verify, %d no-checksum, %d unknown, %d skipped, %s planned",
			s.Objects,
			s.Counts[checksumResultWouldVerify],
			s.Counts[checksumResultNoChecksum],
			s.unknownCount(),
			s.skippedCount(),
			humanize.IBytes(uint64(s.BytesPlanned)),
		)
	}
	return fmt.Sprintf("Checksum verification: %d objects, %d verified, %d match, %d mismatch, %d no-checksum, %d unknown, %d skipped, %s read",
		s.Objects,
		s.Verified,
		s.Counts[checksumResultMatch],
		s.Counts[checksumResultMismatch],
		s.Counts[checksumResultNoChecksum],
		s.unknownCount(),
		s.skippedCount(),
		humanize.IBytes(uint64(s.BytesRead)),
	)
}

func (s checksumVerifySummary) JSON() string {
	b, err := json.MarshalIndent(s, "", " ")
	fatalIf(probe.NewError(err), "Unable to marshal checksum verification summary.")
	return string(b)
}

func (s checksumVerifySummary) unknownCount() int64 {
	var n int64
	for status, count := range s.Counts {
		if strings.HasPrefix(status, "UNKNOWN_") {
			n += count
		}
	}
	return n
}

func (s checksumVerifySummary) skippedCount() int64 {
	var n int64
	for status, count := range s.Counts {
		if strings.HasPrefix(status, "SKIPPED_") {
			n += count
		}
	}
	return n
}

func (s checksumVerifySummary) shouldFail(failOn string, dryRun bool) bool {
	if dryRun {
		return false
	}
	mismatch := s.Counts[checksumResultMismatch] > 0
	unknown := s.unknownCount() > 0
	incomplete := unknown || s.Counts[checksumResultSkippedTooLarge] > 0
	switch failOn {
	case "none":
		return false
	case "mismatch":
		return mismatch
	case "unknown":
		return unknown
	case "no-checksum":
		// For callers that treat "nothing was verifiable" as a failure rather
		// than a clean run. Verified == 0 is part of the condition because an
		// empty manifest, an empty prefix, an all-delete-marker listing and a
		// fully excluding time filter all produce zero NO_CHECKSUM results and
		// zero verifications.
		return mismatch || incomplete ||
			s.Counts[checksumResultNoChecksum] > 0 ||
			s.Verified == 0
	default:
		return mismatch || incomplete
	}
}

type checksumVerifyOptions struct {
	DryRun      bool
	MaximumSize int64
	OlderThan   string
	NewerThan   string
	FailOn      string
	Encryption  map[string][]prefixSSEPair
}

type checksumVerifyBackend interface {
	statObjectForChecksumVerify(ctx context.Context, bucket, object, versionID string, sse encrypt.ServerSide) (checksumVerifyObjectInfo, error)
	getObjectForChecksumVerify(ctx context.Context, bucket, object, versionID, ifMatchETag string, sse encrypt.ServerSide) (io.ReadCloser, error)
}

type checksumAlgorithm struct {
	Name  string
	Type  minio.ChecksumType
	Value func(minio.ObjectInfo) string
}

var checksumVerifyAlgorithms = []checksumAlgorithm{
	{Name: "CRC32", Type: minio.ChecksumCRC32, Value: func(info minio.ObjectInfo) string { return info.ChecksumCRC32 }},
	{Name: "CRC32C", Type: minio.ChecksumCRC32C, Value: func(info minio.ObjectInfo) string { return info.ChecksumCRC32C }},
	{Name: "CRC64NVME", Type: minio.ChecksumCRC64NVME, Value: func(info minio.ObjectInfo) string { return info.ChecksumCRC64NVME }},
	{Name: "SHA1", Type: minio.ChecksumSHA1, Value: func(info minio.ObjectInfo) string { return info.ChecksumSHA1 }},
	{Name: "SHA256", Type: minio.ChecksumSHA256, Value: func(info minio.ObjectInfo) string { return info.ChecksumSHA256 }},
}

type checksumVerifyReport struct {
	file    *os.File
	encoder *stdjson.Encoder
}

func openChecksumVerifyReport(path string) (*checksumVerifyReport, error) {
	if path == "" {
		return nil, nil
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err = file.Chmod(0o600); err != nil {
		file.Close()
		return nil, err
	}
	return &checksumVerifyReport{file: file, encoder: stdjson.NewEncoder(file)}, nil
}

func (r *checksumVerifyReport) write(v any) error {
	if r == nil {
		return nil
	}
	return r.encoder.Encode(v)
}

func (r *checksumVerifyReport) close() error {
	if r == nil {
		return nil
	}
	return r.file.Close()
}

func parseChecksumVerifyMaximumSize(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	size, err := humanize.ParseBytes(value)
	if err != nil {
		return 0, err
	}
	if size > uint64(^uint64(0)>>1) {
		return 0, fmt.Errorf("maximum size is too large")
	}
	return int64(size), nil
}

func validateChecksumVerifySelection(manifest, versionID string, versions, recursive bool, olderThan, newerThan string, workers int, failOn string) error {
	if versionID != "" && (versions || recursive) {
		return errors.New("--version-id cannot be combined with --versions or --recursive")
	}
	if manifest != "" && (versionID != "" || versions || recursive || olderThan != "" || newerThan != "") {
		return errors.New("--manifest cannot be combined with object selection flags")
	}
	if workers < 1 || workers > checksumVerifyMaximumWorkers {
		return fmt.Errorf("--max-workers must be between 1 and %d", checksumVerifyMaximumWorkers)
	}
	if failOn != "mismatch" && failOn != "unknown" && failOn != "no-checksum" && failOn != "any" && failOn != "none" {
		return errors.New("--fail-on must be mismatch, unknown, no-checksum, any, or none")
	}
	return nil
}

func validateChecksumVerifySyntax(cliCtx *cli.Context) {
	if len(cliCtx.Args()) != 1 || strings.TrimSpace(cliCtx.Args().First()) == "" {
		showCommandHelpAndExit(cliCtx, 1)
	}
	manifest := cliCtx.String("manifest")
	versionID := cliCtx.String("version-id")
	versions := cliCtx.Bool("versions")
	recursive := cliCtx.Bool("recursive")
	olderThan := cliCtx.String("older-than")
	newerThan := cliCtx.String("newer-than")

	workers := cliCtx.Int("max-workers")
	failOn := strings.ToLower(cliCtx.String("fail-on"))
	selectionErr := validateChecksumVerifySelection(manifest, versionID, versions, recursive, olderThan, newerThan, workers, failOn)
	fatalIf(probe.NewError(selectionErr), "Invalid checksum verification options.")
	// Validate time filters once before workers call the existing skip helpers.
	if olderThan != "" {
		_ = isOlder(UTCNow(), olderThan)
	}
	if newerThan != "" {
		_ = isNewer(UTCNow(), newerThan)
	}
	if manifest != "" && cliCtx.String("report") != "" {
		manifestPath, err := filepath.Abs(manifest)
		fatalIf(probe.NewError(err), "Unable to resolve checksum manifest path.")
		reportPath, err := filepath.Abs(cliCtx.String("report"))
		fatalIf(probe.NewError(err), "Unable to resolve checksum report path.")
		if manifestPath == reportPath {
			fatalIf(errInvalidArgument(), "--manifest and --report must refer to different files.")
		}
	}
}

func readChecksumVerifyManifest(ctx context.Context, path, alias string, out chan<- checksumVerifyCandidate) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), checksumVerifyManifestMaximumLineSize)
	line := 0
	for scanner.Scan() {
		line++
		value := strings.TrimSpace(scanner.Text())
		if value == "" {
			continue
		}
		var entry checksumVerifyManifestEntry
		decoder := stdjson.NewDecoder(strings.NewReader(value))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&entry); err != nil {
			return fmt.Errorf("manifest line %d: %w", line, err)
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			if err == nil {
				err = errors.New("multiple JSON values")
			}
			return fmt.Errorf("manifest line %d: %w", line, err)
		}
		if strings.TrimSpace(entry.Bucket) == "" || entry.Key == "" {
			return fmt.Errorf("manifest line %d: bucket and key are required", line)
		}
		candidate := checksumVerifyCandidate{
			Alias:     alias,
			Bucket:    entry.Bucket,
			Key:       entry.Key,
			VersionID: entry.VersionID,
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case out <- candidate:
		}
	}
	return scanner.Err()
}

func checksumVerifyBucketKey(content *ClientContent) (bucket, key string) {
	path := strings.TrimPrefix(filepath.ToSlash(content.URL.Path), "/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) == 0 {
		return "", ""
	}
	bucket = parts[0]
	if len(parts) == 2 {
		key = parts[1]
	}
	return bucket, key
}

func scanChecksumVerifyCandidates(ctx context.Context, client *S3Client, target, alias, versionID string, recursive, versions bool, out chan<- checksumVerifyCandidate) error {
	bucket, object := client.url2BucketAndObject()
	if bucket == "" {
		return errors.New("checksum verification requires an S3 bucket")
	}
	if versionID != "" {
		if object == "" {
			return errors.New("--version-id requires an object target")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case out <- checksumVerifyCandidate{Alias: alias, Bucket: bucket, Key: object, VersionID: versionID}:
			return nil
		}
	}

	isPrefix := object == "" || strings.HasSuffix(target, "/") || recursive
	if !versions && !isPrefix {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case out <- checksumVerifyCandidate{Alias: alias, Bucket: bucket, Key: object}:
			return nil
		}
	}
	if isPrefix && !recursive && object == "" {
		return errors.New("bucket and prefix verification requires --recursive")
	}
	if strings.HasSuffix(target, "/") && !recursive {
		return errors.New("prefix verification requires --recursive")
	}

	exactObject := versions && !recursive && object != "" && !strings.HasSuffix(target, "/")
	listOptions := ListOptions{
		Recursive:         true,
		WithOlderVersions: versions,
		WithDeleteMarkers: versions,
		ShowDir:           DirNone,
	}
	for content := range client.List(ctx, listOptions) {
		if content.Err != nil {
			return content.Err.ToGoError()
		}
		listedBucket, key := checksumVerifyBucketKey(content)
		if listedBucket == "" || key == "" || content.Type.IsDir() {
			continue
		}
		if exactObject && key != object {
			continue
		}
		candidate := checksumVerifyCandidate{
			Alias:        alias,
			Bucket:       listedBucket,
			Key:          key,
			VersionID:    content.VersionID,
			DeleteMarker: content.IsDeleteMarker,
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case out <- candidate:
		}
	}
	return nil
}

func checksumVerifyResultFor(candidate checksumVerifyCandidate) checksumVerifyResult {
	return checksumVerifyResult{
		SchemaVersion: checksumVerifySchemaVersion,
		Type:          "object",
		Timestamp:     UTCNow(),
		Alias:         candidate.Alias,
		Bucket:        candidate.Bucket,
		Key:           candidate.Key,
		VersionID:     candidate.VersionID,
	}
}

func checksumVerifyErrorResult(candidate checksumVerifyCandidate, err error) checksumVerifyResult {
	return applyChecksumVerifyError(checksumVerifyResultFor(candidate), err)
}

func applyChecksumVerifyError(result checksumVerifyResult, err error) checksumVerifyResult {
	response := minio.ToErrorResponse(err)
	result.ErrorCode = response.Code
	// Server-controlled text, printed and written to --report: an endpoint
	// that echoes a request header into its message must not put an SSE-C
	// key or a token there.
	result.ErrorMessage = scrubSecretsFromOutput(err.Error())
	code := strings.ToLower(response.Code)
	message := strings.ToLower(response.Message)
	switch {
	case code == "invalidobjectstate":
		result.Result = checksumResultUnknownStorageClass
	case response.StatusCode == 403 || code == "accessdenied":
		result.Result = checksumResultUnknownAccessDenied
	case response.StatusCode == http.StatusPreconditionFailed || code == "preconditionfailed":
		result.Result = checksumResultUnknownObjectChanged
	case strings.Contains(code, "kms") || strings.Contains(message, "kms"):
		result.Result = checksumResultUnknownKMSError
	case strings.Contains(code, "sse") ||
		strings.Contains(message, "server side encryption") ||
		strings.Contains(message, "customer key") ||
		strings.Contains(message, "sse-c"):
		result.Result = checksumResultUnknownSSECKeyMissing
	default:
		result.Result = checksumResultUnknownReadError
	}
	return result
}

func mergeChecksumMaps(a, b map[string]string) map[string]string {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func checksumVerifyHashers(stored map[string]string) (map[string]hash.Hash, []io.Writer) {
	hashers := make(map[string]hash.Hash, len(stored))
	writers := make([]io.Writer, 0, len(stored))
	for _, algorithm := range checksumVerifyAlgorithms {
		if stored[algorithm.Name] == "" {
			continue
		}
		h := algorithm.Type.Hasher()
		if h == nil {
			continue
		}
		hashers[algorithm.Name] = h
		writers = append(writers, h)
	}
	return hashers, writers
}

func checksumVerifyObjectChanged(before, after checksumVerifyObjectInfo) bool {
	return before.ETag != after.ETag || before.Size != after.Size || !before.LastModified.Equal(after.LastModified)
}

func verifyChecksumCandidate(ctx context.Context, backend checksumVerifyBackend, candidate checksumVerifyCandidate, opts checksumVerifyOptions) checksumVerifyResult {
	result := checksumVerifyResultFor(candidate)
	if candidate.DeleteMarker {
		result.Result = checksumResultSkippedDeleteMarker
		return result
	}

	resource := candidate.Alias + "/" + candidate.Bucket + "/" + candidate.Key
	sse := getSSE(resource, opts.Encryption[candidate.Alias])
	info, err := backend.statObjectForChecksumVerify(ctx, candidate.Bucket, candidate.Key, candidate.VersionID, sse)
	if err != nil {
		return checksumVerifyErrorResult(candidate, err)
	}
	result.Size = info.Size
	result.ETag = info.ETag
	result.LastModified = &info.LastModified
	result.ChecksumType = info.ChecksumType
	result.StoredChecksums = mergeChecksumMaps(info.Checksums, info.UnsupportedChecksums)
	if info.VersionID != "" {
		result.VersionID = info.VersionID
	}

	if (opts.OlderThan != "" && isOlder(info.LastModified, opts.OlderThan)) ||
		(opts.NewerThan != "" && isNewer(info.LastModified, opts.NewerThan)) {
		result.Result = checksumResultSkippedTimeFilter
		return result
	}
	if len(info.Checksums) == 0 && len(info.UnsupportedChecksums) == 0 {
		result.Result = checksumResultNoChecksum
		return result
	}
	if len(info.UnsupportedChecksums) > 0 {
		result.Result = checksumResultUnknownChecksumAlgorithm
		result.ErrorMessage = "object uses a checksum algorithm not supported by this command"
		return result
	}
	switch info.ChecksumType {
	case checksumVerifyFullObjectType:
	case checksumVerifyCompositeType:
		result.Result = checksumResultUnknownComposite
		return result
	default:
		result.Result = checksumResultUnknownChecksumType
		result.ErrorMessage = "checksum type is missing or unsupported"
		return result
	}
	if info.Size < 0 {
		result.Result = checksumResultUnknownReadError
		result.ErrorMessage = "object size is unknown"
		return result
	}
	if opts.MaximumSize > 0 && info.Size > opts.MaximumSize {
		result.Result = checksumResultSkippedTooLarge
		return result
	}
	if opts.DryRun {
		result.Result = checksumResultWouldVerify
		return result
	}

	readVersionID := candidate.VersionID
	if readVersionID == "" && info.VersionID != "" {
		readVersionID = info.VersionID
	}
	mutableVersion := readVersionID == "" || readVersionID == "null"
	if mutableVersion && info.ETag == "" {
		result.Result = checksumResultUnknownObjectChanged
		result.ErrorMessage = "unversioned object did not return an ETag for If-Match"
		return result
	}
	ifMatch := ""
	if mutableVersion {
		ifMatch = info.ETag
	}
	hashers, writers := checksumVerifyHashers(info.Checksums)
	if len(writers) == 0 || len(hashers) != len(info.Checksums) {
		result.Result = checksumResultUnknownChecksumAlgorithm
		return result
	}
	reader, err := backend.getObjectForChecksumVerify(ctx, candidate.Bucket, candidate.Key, readVersionID, ifMatch, sse)
	if err != nil {
		return applyChecksumVerifyError(result, err)
	}
	read, readErr := io.Copy(io.MultiWriter(writers...), reader)
	closeErr := reader.Close()
	result.BytesRead = read
	if readErr != nil {
		result.Result = checksumResultUnknownReadError
		result.ErrorMessage = scrubSecretsFromOutput(readErr.Error())
		return result
	}
	if closeErr != nil {
		result.Result = checksumResultUnknownReadError
		result.ErrorMessage = scrubSecretsFromOutput(closeErr.Error())
		return result
	}
	if read != info.Size {
		result.Result = checksumResultUnknownShortRead
		result.ErrorMessage = fmt.Sprintf("read %d bytes, expected %d", read, info.Size)
		return result
	}

	if mutableVersion {
		after, statErr := backend.statObjectForChecksumVerify(ctx, candidate.Bucket, candidate.Key, readVersionID, sse)
		if statErr != nil {
			return applyChecksumVerifyError(result, statErr)
		}
		if checksumVerifyObjectChanged(info, after) {
			result.Result = checksumResultUnknownObjectChanged
			return result
		}
	}

	result.CalculatedChecksums = make(map[string]string, len(hashers))
	result.Result = checksumResultMatch
	for algorithm, hasher := range hashers {
		calculated := base64.StdEncoding.EncodeToString(hasher.Sum(nil))
		result.CalculatedChecksums[algorithm] = calculated
		if calculated != info.Checksums[algorithm] {
			result.Result = checksumResultMismatch
		}
	}
	return result
}

func runChecksumVerifyWorkers(ctx context.Context, backend checksumVerifyBackend, candidates <-chan checksumVerifyCandidate, workers int, opts checksumVerifyOptions) <-chan checksumVerifyResult {
	results := make(chan checksumVerifyResult)
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case candidate, ok := <-candidates:
					if !ok {
						return
					}
					result := verifyChecksumCandidate(ctx, backend, candidate, opts)
					select {
					case <-ctx.Done():
						return
					case results <- result:
					}
				}
			}
		}()
	}
	go func() {
		wg.Wait()
		close(results)
	}()
	return results
}

// checksumVerifyFlagEnabled checks every command level because GlobalBool stops
// at the nearest ancestor flag set and cannot see an app-level flag here.
func checksumVerifyFlagEnabled(cliCtx *cli.Context, name string) bool {
	for ctx := cliCtx; ctx != nil; ctx = ctx.Parent() {
		if ctx.Bool(name) {
			return true
		}
	}
	return false
}

func mainChecksumVerify(cliCtx *cli.Context) error {
	validateChecksumVerifySyntax(cliCtx)
	quiet := checksumVerifyFlagEnabled(cliCtx, "quiet")
	if checksumVerifyFlagEnabled(cliCtx, "json") && !isTerminal() {
		// A nested Before hook can reset this after an app-level --json.
		globalJSONLine = true
	}
	ctx, cancel := context.WithCancel(globalContext)
	defer cancel()

	target := cliCtx.Args().First()
	alias, path := url2Alias(target)
	manifest := cliCtx.String("manifest")
	if manifest != "" && path != "" {
		fatalIf(errInvalidArgument(), "Manifest mode requires an alias without a bucket or prefix.")
	}
	client, err := newClient(target)
	fatalIf(err.Trace(target), "Unable to initialize checksum verification target.")
	s3Client, ok := client.(*S3Client)
	if !ok {
		fatalIf(errInvalidArgument(), "Checksum verification requires an S3-compatible alias.")
	}

	maximumSize, sizeErr := parseChecksumVerifyMaximumSize(cliCtx.String("max-size"))
	fatalIf(probe.NewError(sizeErr), "Unable to parse --max-size.")
	encryption, encErr := validateAndCreateEncryptionKeys(cliCtx)
	fatalIf(encErr, "Unable to parse encryption keys.")
	report, reportErr := openChecksumVerifyReport(cliCtx.String("report"))
	fatalIf(probe.NewError(reportErr), "Unable to create checksum verification report.")

	started := time.Now()
	candidates := make(chan checksumVerifyCandidate)
	producerErr := make(chan error, 1)
	go func() {
		defer close(candidates)
		if manifest != "" {
			producerErr <- readChecksumVerifyManifest(ctx, manifest, alias, candidates)
			return
		}
		producerErr <- scanChecksumVerifyCandidates(ctx, s3Client, target, alias,
			cliCtx.String("version-id"), cliCtx.Bool("recursive"), cliCtx.Bool("versions"), candidates)
	}()

	opts := checksumVerifyOptions{
		DryRun:      cliCtx.Bool("dry-run"),
		MaximumSize: maximumSize,
		OlderThan:   cliCtx.String("older-than"),
		NewerThan:   cliCtx.String("newer-than"),
		FailOn:      strings.ToLower(cliCtx.String("fail-on")),
		Encryption:  encryption,
	}
	summary := newChecksumVerifySummary(opts.DryRun)
	var outputErr error
	for result := range runChecksumVerifyWorkers(ctx, s3Client, candidates, cliCtx.Int("max-workers"), opts) {
		summary.add(result)
		if outputErr == nil {
			outputErr = report.write(result)
			if outputErr != nil {
				cancel()
			}
		}
		if !quiet {
			printMsg(result)
		}
	}
	enumerationErr := <-producerErr
	if enumerationErr != nil {
		summary.Incomplete = true
	}
	summary.Duration = time.Since(started)
	summary.Timestamp = UTCNow()
	if outputErr == nil {
		outputErr = report.write(summary)
	}
	if !quiet {
		printMsg(summary)
	}
	if closeErr := report.close(); outputErr == nil {
		outputErr = closeErr
	}
	if outputErr != nil {
		errorIf(probe.NewError(outputErr), "Unable to write checksum verification report")
		return exitStatus(globalErrorExitStatus)
	}
	if enumerationErr != nil && !errors.Is(enumerationErr, context.Canceled) {
		errorIf(probe.NewError(enumerationErr), "Unable to enumerate checksum verification candidates")
		return exitStatus(globalErrorExitStatus)
	}
	if summary.shouldFail(opts.FailOn, opts.DryRun) {
		return exitStatus(globalErrorExitStatus)
	}
	return nil
}
