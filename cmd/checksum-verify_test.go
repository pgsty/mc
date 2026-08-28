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
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	minio "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/minio/minio-go/v7/pkg/encrypt"
)

type checksumVerifyFakeBackend struct {
	mu             sync.Mutex
	infos          []checksumVerifyObjectInfo
	statErr        error
	getErr         error
	data           []byte
	statCalls      int
	getCalls       int
	getVersionID   string
	getIfMatchETag string
}

type checksumVerifyBlockingBackend struct {
	started chan struct{}
	once    sync.Once
}

func (b *checksumVerifyBlockingBackend) statObjectForChecksumVerify(ctx context.Context, _, _, _ string, _ encrypt.ServerSide) (checksumVerifyObjectInfo, error) {
	b.once.Do(func() { close(b.started) })
	<-ctx.Done()
	return checksumVerifyObjectInfo{}, ctx.Err()
}

func (b *checksumVerifyBlockingBackend) getObjectForChecksumVerify(context.Context, string, string, string, string, encrypt.ServerSide) (io.ReadCloser, error) {
	return nil, errors.New("unexpected GET from blocking backend")
}

func (f *checksumVerifyFakeBackend) statObjectForChecksumVerify(_ context.Context, _, _, _ string, _ encrypt.ServerSide) (checksumVerifyObjectInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statCalls++
	if f.statErr != nil {
		return checksumVerifyObjectInfo{}, f.statErr
	}
	if len(f.infos) == 0 {
		return checksumVerifyObjectInfo{}, errors.New("missing fake object info")
	}
	index := min(f.statCalls-1, len(f.infos)-1)
	return f.infos[index], nil
}

func (f *checksumVerifyFakeBackend) getObjectForChecksumVerify(_ context.Context, _, _, versionID, ifMatchETag string, _ encrypt.ServerSide) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls++
	f.getVersionID = versionID
	f.getIfMatchETag = ifMatchETag
	if f.getErr != nil {
		return nil, f.getErr
	}
	return io.NopCloser(bytes.NewReader(f.data)), nil
}

func checksumForTest(t *testing.T, typ minio.ChecksumType, data []byte) string {
	t.Helper()
	h := typ.Hasher()
	if h == nil {
		t.Fatal("missing checksum hasher")
	}
	if _, err := h.Write(data); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

func fullObjectInfo(data []byte, checksums map[string]string) checksumVerifyObjectInfo {
	return checksumVerifyObjectInfo{
		Size:         int64(len(data)),
		ETag:         "test-etag",
		LastModified: time.Unix(100, 0).UTC(),
		VersionID:    "version-1",
		ChecksumType: checksumVerifyFullObjectType,
		Checksums:    checksums,
	}
}

func TestVerifyChecksumCandidate(t *testing.T) {
	data := []byte("logical object bytes")
	crc32Value := checksumForTest(t, minio.ChecksumCRC32, data)
	sha256Value := checksumForTest(t, minio.ChecksumSHA256, data)
	candidate := checksumVerifyCandidate{Alias: "play", Bucket: "archive", Key: "object", VersionID: "version-1"}
	baseOptions := checksumVerifyOptions{FailOn: "any", Encryption: map[string][]prefixSSEPair{}}

	tests := []struct {
		name        string
		backend     *checksumVerifyFakeBackend
		opts        checksumVerifyOptions
		wantResult  string
		wantGet     int
		wantVersion string
	}{
		{
			name: "match multiple algorithms",
			backend: &checksumVerifyFakeBackend{
				infos: []checksumVerifyObjectInfo{fullObjectInfo(data, map[string]string{"CRC32": crc32Value, "SHA256": sha256Value})},
				data:  data,
			},
			opts:        baseOptions,
			wantResult:  checksumResultMatch,
			wantGet:     1,
			wantVersion: "version-1",
		},
		{
			name: "mismatch",
			backend: &checksumVerifyFakeBackend{
				infos: []checksumVerifyObjectInfo{fullObjectInfo(data, map[string]string{"CRC32": "AAAAAA=="})},
				data:  data,
			},
			opts:        baseOptions,
			wantResult:  checksumResultMismatch,
			wantGet:     1,
			wantVersion: "version-1",
		},
		{
			name: "one of multiple algorithms mismatches",
			backend: &checksumVerifyFakeBackend{
				infos: []checksumVerifyObjectInfo{fullObjectInfo(data, map[string]string{"CRC32": crc32Value, "SHA256": "AAAAAA=="})},
				data:  data,
			},
			opts:        baseOptions,
			wantResult:  checksumResultMismatch,
			wantGet:     1,
			wantVersion: "version-1",
		},
		{
			name: "no checksum avoids get",
			backend: &checksumVerifyFakeBackend{
				infos: []checksumVerifyObjectInfo{fullObjectInfo(data, nil)},
			},
			opts:       baseOptions,
			wantResult: checksumResultNoChecksum,
		},
		{
			name: "composite avoids get",
			backend: &checksumVerifyFakeBackend{
				infos: []checksumVerifyObjectInfo{{
					Size:         int64(len(data)),
					ChecksumType: checksumVerifyCompositeType,
					Checksums:    map[string]string{"CRC32": crc32Value + "-2"},
				}},
			},
			opts:       baseOptions,
			wantResult: checksumResultUnknownComposite,
		},
		{
			name: "missing checksum type avoids get",
			backend: &checksumVerifyFakeBackend{
				infos: []checksumVerifyObjectInfo{{Size: int64(len(data)), Checksums: map[string]string{"CRC32": crc32Value}}},
			},
			opts:       baseOptions,
			wantResult: checksumResultUnknownChecksumType,
		},
		{
			name: "unsupported checksum avoids get",
			backend: &checksumVerifyFakeBackend{
				infos: []checksumVerifyObjectInfo{{
					Size:                 int64(len(data)),
					ChecksumType:         checksumVerifyFullObjectType,
					UnsupportedChecksums: map[string]string{"SHA512": "value"},
				}},
			},
			opts:       baseOptions,
			wantResult: checksumResultUnknownChecksumAlgorithm,
		},
		{
			name: "dry run avoids get",
			backend: &checksumVerifyFakeBackend{
				infos: []checksumVerifyObjectInfo{fullObjectInfo(data, map[string]string{"CRC32": crc32Value})},
			},
			opts: func() checksumVerifyOptions {
				opts := baseOptions
				opts.DryRun = true
				return opts
			}(),
			wantResult: checksumResultWouldVerify,
		},
		{
			name: "maximum size avoids get",
			backend: &checksumVerifyFakeBackend{
				infos: []checksumVerifyObjectInfo{fullObjectInfo(data, map[string]string{"CRC32": crc32Value})},
			},
			opts: func() checksumVerifyOptions {
				opts := baseOptions
				opts.MaximumSize = int64(len(data) - 1)
				return opts
			}(),
			wantResult: checksumResultSkippedTooLarge,
		},
		{
			name: "short read",
			backend: &checksumVerifyFakeBackend{
				infos: []checksumVerifyObjectInfo{fullObjectInfo(append(data, 'x'), map[string]string{"CRC32": crc32Value})},
				data:  data,
			},
			opts:        baseOptions,
			wantResult:  checksumResultUnknownShortRead,
			wantGet:     1,
			wantVersion: "version-1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := verifyChecksumCandidate(context.Background(), tc.backend, candidate, tc.opts)
			if result.Result != tc.wantResult {
				t.Fatalf("result %q, want %q: %+v", result.Result, tc.wantResult, result)
			}
			if tc.backend.getCalls != tc.wantGet {
				t.Fatalf("GET calls %d, want %d", tc.backend.getCalls, tc.wantGet)
			}
			if tc.backend.getVersionID != tc.wantVersion {
				t.Fatalf("GET version %q, want %q", tc.backend.getVersionID, tc.wantVersion)
			}
			if tc.backend.getIfMatchETag != "" {
				t.Fatalf("version-pinned GET unexpectedly used If-Match %q", tc.backend.getIfMatchETag)
			}
			if tc.backend.statCalls != 1 {
				t.Fatalf("version-pinned candidate used %d HEAD requests, want 1", tc.backend.statCalls)
			}
		})
	}
}

func TestVerifyChecksumCandidateAllAlgorithms(t *testing.T) {
	data := []byte("all supported checksum algorithms")
	candidate := checksumVerifyCandidate{Alias: "play", Bucket: "archive", Key: "object", VersionID: "version-1"}
	for _, algorithm := range checksumVerifyAlgorithms {
		t.Run(algorithm.Name, func(t *testing.T) {
			backend := &checksumVerifyFakeBackend{
				infos: []checksumVerifyObjectInfo{fullObjectInfo(data, map[string]string{
					algorithm.Name: checksumForTest(t, algorithm.Type, data),
				})},
				data: data,
			}
			result := verifyChecksumCandidate(context.Background(), backend, candidate,
				checksumVerifyOptions{Encryption: map[string][]prefixSSEPair{}},
			)
			if result.Result != checksumResultMatch {
				t.Fatalf("result %q, want %q: %+v", result.Result, checksumResultMatch, result)
			}
		})
	}
}

func TestVerifyChecksumCandidateUnversionedConsistency(t *testing.T) {
	data := []byte("unversioned data")
	value := checksumForTest(t, minio.ChecksumCRC32C, data)
	for _, versionID := range []string{"", "null"} {
		versionName := versionID
		if versionName == "" {
			versionName = "empty"
		}
		for _, changed := range []bool{false, true} {
			name := versionName + "/unchanged"
			if changed {
				name = versionName + "/changed"
			}
			t.Run(name, func(t *testing.T) {
				before := fullObjectInfo(data, map[string]string{"CRC32C": value})
				before.VersionID = versionID
				after := before
				if changed {
					after.ETag = "changed"
				}
				backend := &checksumVerifyFakeBackend{infos: []checksumVerifyObjectInfo{before, after}, data: data}
				result := verifyChecksumCandidate(context.Background(), backend,
					checksumVerifyCandidate{Alias: "play", Bucket: "archive", Key: "object", VersionID: versionID},
					checksumVerifyOptions{Encryption: map[string][]prefixSSEPair{}},
				)
				wantResult := checksumResultMatch
				if changed {
					wantResult = checksumResultUnknownObjectChanged
				}
				if result.Result != wantResult {
					t.Fatalf("result %q, want %q", result.Result, wantResult)
				}
				if backend.getVersionID != versionID {
					t.Fatalf("GET version %q, want %q", backend.getVersionID, versionID)
				}
				if backend.getIfMatchETag != before.ETag {
					t.Fatalf("If-Match %q, want %q", backend.getIfMatchETag, before.ETag)
				}
				if backend.statCalls != 2 {
					t.Fatalf("stat calls %d, want 2", backend.statCalls)
				}
			})
		}
	}
}

func TestReadChecksumVerifyManifest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.jsonl")
	data := []byte("\n" +
		`{"bucket":"archive","key":"one","versionId":"v1"}` + "\n" +
		`{"bucket":"archive","key":"two"}` + "\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	out := make(chan checksumVerifyCandidate, 2)
	if err := readChecksumVerifyManifest(context.Background(), path, "play", out); err != nil {
		t.Fatal(err)
	}
	close(out)
	var got []checksumVerifyCandidate
	for candidate := range out {
		got = append(got, candidate)
	}
	want := []checksumVerifyCandidate{
		{Alias: "play", Bucket: "archive", Key: "one", VersionID: "v1"},
		{Alias: "play", Bucket: "archive", Key: "two"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("manifest entries %#v, want %#v", got, want)
	}

	badPath := filepath.Join(dir, "bad.jsonl")
	if err := os.WriteFile(badPath, []byte(`{"bucket":"archive","key":"one","unexpected":true}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := readChecksumVerifyManifest(context.Background(), badPath, "play", make(chan checksumVerifyCandidate, 1)); err == nil {
		t.Fatal("expected unknown manifest field to fail")
	}
	trailingPath := filepath.Join(dir, "trailing.jsonl")
	if err := os.WriteFile(trailingPath, []byte(`{"bucket":"archive","key":"one"} {"bucket":"archive","key":"two"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := readChecksumVerifyManifest(context.Background(), trailingPath, "play", make(chan checksumVerifyCandidate, 1)); err == nil {
		t.Fatal("expected multiple JSON values on one manifest line to fail")
	}
}

func TestChecksumVerifyReportPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.jsonl")
	report, err := openChecksumVerifyReport(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = report.write(checksumVerifyResult{SchemaVersion: 1, Type: "object", Result: checksumResultMatch}); err != nil {
		t.Fatal(err)
	}
	if err = report.close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("report mode %o, want 600", got)
	}
	if _, err = openChecksumVerifyReport(path); err == nil {
		t.Fatal("expected existing report file to be rejected")
	}
}

func TestChecksumVerifySummaryFailOn(t *testing.T) {
	summary := newChecksumVerifySummary(false)
	summary.add(checksumVerifyResult{Result: checksumResultMismatch})
	summary.add(checksumVerifyResult{Result: checksumResultUnknownReadError})
	for _, tc := range []struct {
		failOn string
		want   bool
	}{
		{failOn: "none", want: false},
		{failOn: "mismatch", want: true},
		{failOn: "unknown", want: true},
		{failOn: "any", want: true},
	} {
		if got := summary.shouldFail(tc.failOn, false); got != tc.want {
			t.Fatalf("fail-on %q = %t, want %t", tc.failOn, got, tc.want)
		}
	}
	if summary.shouldFail("any", true) {
		t.Fatal("dry-run findings must not trigger --fail-on")
	}
	noChecksum := newChecksumVerifySummary(false)
	noChecksum.add(checksumVerifyResult{Result: checksumResultNoChecksum})
	if !strings.Contains(noChecksum.String(), "1 no-checksum") {
		t.Fatalf("human summary does not disclose no-checksum count: %q", noChecksum.String())
	}
}

func TestApplyChecksumVerifyError(t *testing.T) {
	base := checksumVerifyResult{SchemaVersion: 1, Type: "object", Bucket: "archive", Key: "object", Size: 42}
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "access denied",
			err:  minio.ErrorResponse{Code: "AccessDenied", StatusCode: http.StatusForbidden, Message: "denied"},
			want: checksumResultUnknownAccessDenied,
		},
		{
			name: "invalid object state precedes generic 403",
			err:  minio.ErrorResponse{Code: "InvalidObjectState", StatusCode: http.StatusForbidden, Message: "restore required"},
			want: checksumResultUnknownStorageClass,
		},
		{
			name: "nonstandard 412",
			err:  minio.ErrorResponse{Code: "VendorObjectChanged", StatusCode: http.StatusPreconditionFailed, Message: "changed"},
			want: checksumResultUnknownObjectChanged,
		},
		{
			name: "kms service error",
			err:  minio.ErrorResponse{Code: "KMSNotConfigured", StatusCode: http.StatusInternalServerError, Message: "kms unavailable"},
			want: checksumResultUnknownKMSError,
		},
		{
			name: "kms in URL does not change network classification",
			err:  &url.Error{Op: "GET", URL: "https://example.test/kms-object", Err: errors.New("connection reset")},
			want: checksumResultUnknownReadError,
		},
		{
			name: "silo missing SSE-C parameters",
			err: minio.ErrorResponse{
				Code:       "InvalidRequest",
				StatusCode: http.StatusBadRequest,
				Message:    "The object was stored using a form of Server Side Encryption. The correct parameters must be provided to retrieve the object.",
			},
			want: checksumResultUnknownSSECKeyMissing,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := applyChecksumVerifyError(base, tc.err)
			if got.Result != tc.want {
				t.Fatalf("result %q, want %q", got.Result, tc.want)
			}
			if got.Size != base.Size {
				t.Fatalf("error classification dropped prior HEAD metadata: %+v", got)
			}
		})
	}
}

func TestValidateChecksumVerifySelection(t *testing.T) {
	tests := []struct {
		name      string
		manifest  string
		versionID string
		versions  bool
		recursive bool
		older     string
		workers   int
		failOn    string
		wantErr   bool
	}{
		{name: "default", workers: 4, failOn: "any"},
		{name: "manifest", manifest: "candidates.jsonl", workers: 4, failOn: "none"},
		{name: "version recursive conflict", versionID: "v1", recursive: true, workers: 4, failOn: "any", wantErr: true},
		{name: "manifest version conflict", manifest: "candidates.jsonl", versions: true, workers: 4, failOn: "any", wantErr: true},
		{name: "manifest time conflict", manifest: "candidates.jsonl", older: "7d", workers: 4, failOn: "any", wantErr: true},
		{name: "zero workers", workers: 0, failOn: "any", wantErr: true},
		{name: "too many workers", workers: checksumVerifyMaximumWorkers + 1, failOn: "any", wantErr: true},
		{name: "bad fail-on", workers: 4, failOn: "everything", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateChecksumVerifySelection(tc.manifest, tc.versionID, tc.versions, tc.recursive, tc.older, "", tc.workers, tc.failOn)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error %v, wantErr=%t", err, tc.wantErr)
			}
		})
	}
}

func TestScanChecksumVerifyCandidatesDirectModes(t *testing.T) {
	tests := []struct {
		name      string
		targetURL string
		target    string
		versionID string
		recursive bool
		versions  bool
		want      []checksumVerifyCandidate
		wantErr   bool
	}{
		{
			name:      "one current object",
			targetURL: "https://example.test/archive/object",
			target:    "play/archive/object",
			want:      []checksumVerifyCandidate{{Alias: "play", Bucket: "archive", Key: "object"}},
		},
		{
			name:      "one exact version",
			targetURL: "https://example.test/archive/object",
			target:    "play/archive/object",
			versionID: "v1",
			want:      []checksumVerifyCandidate{{Alias: "play", Bucket: "archive", Key: "object", VersionID: "v1"}},
		},
		{
			name:      "bucket requires recursive",
			targetURL: "https://example.test/archive",
			target:    "play/archive",
			wantErr:   true,
		},
		{
			name:      "prefix requires recursive",
			targetURL: "https://example.test/archive/prefix/",
			target:    "play/archive/prefix/",
			wantErr:   true,
		},
		{
			name:      "version requires object",
			targetURL: "https://example.test/archive",
			target:    "play/archive",
			versionID: "v1",
			wantErr:   true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := &S3Client{targetURL: newClientURL(tc.targetURL)}
			out := make(chan checksumVerifyCandidate, 4)
			err := scanChecksumVerifyCandidates(context.Background(), client, tc.target, "play", tc.versionID, tc.recursive, tc.versions, out)
			close(out)
			var got []checksumVerifyCandidate
			for candidate := range out {
				got = append(got, candidate)
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("error %v, wantErr=%t", err, tc.wantErr)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("candidates %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestScanChecksumVerifyCandidatesExactVersions(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !r.URL.Query().Has("versions") {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, `<ListVersionsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`+
			`<Name>archive</Name><Prefix>object</Prefix><KeyMarker></KeyMarker><VersionIdMarker></VersionIdMarker>`+
			`<MaxKeys>1000</MaxKeys><IsTruncated>false</IsTruncated>`+
			`<Version><Key>object</Key><VersionId>v2</VersionId><IsLatest>true</IsLatest>`+
			`<LastModified>2026-08-28T00:00:00Z</LastModified><ETag>"etag-v2"</ETag><Size>12</Size><StorageClass>STANDARD</StorageClass></Version>`+
			`<Version><Key>object-child</Key><VersionId>child</VersionId><IsLatest>true</IsLatest>`+
			`<LastModified>2026-08-28T00:00:00Z</LastModified><ETag>"etag-child"</ETag><Size>12</Size><StorageClass>STANDARD</StorageClass></Version>`+
			`<DeleteMarker><Key>object</Key><VersionId>deleted</VersionId><IsLatest>false</IsLatest>`+
			`<LastModified>2026-08-27T00:00:00Z</LastModified></DeleteMarker>`+
			`</ListVersionsResult>`)
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	api, err := minio.New(endpoint.Host, &minio.Options{
		Creds:  credentials.NewStaticV4("access", "secret", ""),
		Secure: false,
		Region: "us-east-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	client := &S3Client{api: api, targetURL: newClientURL(server.URL + "/archive/object")}
	out := make(chan checksumVerifyCandidate, 4)
	if err = scanChecksumVerifyCandidates(context.Background(), client, "play/archive/object", "play", "", false, true, out); err != nil {
		t.Fatal(err)
	}
	close(out)
	var got []checksumVerifyCandidate
	for candidate := range out {
		got = append(got, candidate)
	}
	want := []checksumVerifyCandidate{
		{Alias: "play", Bucket: "archive", Key: "object", VersionID: "v2"},
		{Alias: "play", Bucket: "archive", Key: "object", VersionID: "deleted", DeleteMarker: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates %#v, want %#v", got, want)
	}
}

func TestRunChecksumVerifyWorkersReturnsEveryResult(t *testing.T) {
	data := []byte("worker pool data")
	value := checksumForTest(t, minio.ChecksumCRC32, data)
	backend := &checksumVerifyFakeBackend{
		infos: []checksumVerifyObjectInfo{fullObjectInfo(data, map[string]string{"CRC32": value})},
		data:  data,
	}
	const candidateCount = 32
	candidates := make(chan checksumVerifyCandidate, candidateCount)
	for i := range candidateCount {
		candidates <- checksumVerifyCandidate{
			Alias:     "play",
			Bucket:    "archive",
			Key:       fmt.Sprintf("object-%d", i),
			VersionID: "version-1",
		}
	}
	close(candidates)

	results := runChecksumVerifyWorkers(context.Background(), backend, candidates, 4,
		checksumVerifyOptions{Encryption: map[string][]prefixSSEPair{}},
	)
	var got int
	for result := range results {
		if result.Result != checksumResultMatch {
			t.Fatalf("result %q, want MATCH", result.Result)
		}
		got++
	}
	if got != candidateCount {
		t.Fatalf("received %d results, want %d", got, candidateCount)
	}
	if backend.getCalls != candidateCount {
		t.Fatalf("GET calls %d, want %d", backend.getCalls, candidateCount)
	}
}

func TestRunChecksumVerifyWorkersCancelTerminates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &checksumVerifyBlockingBackend{started: make(chan struct{})}
	candidates := make(chan checksumVerifyCandidate, 1)
	candidates <- checksumVerifyCandidate{Alias: "play", Bucket: "archive", Key: "object"}
	close(candidates)
	results := runChecksumVerifyWorkers(ctx, backend, candidates, 1,
		checksumVerifyOptions{Encryption: map[string][]prefixSSEPair{}},
	)
	done := make(chan struct{})
	var drained int
	go func() {
		for range results {
			drained++
		}
		close(done)
	}()
	select {
	case <-backend.started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not start verification")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker pool did not terminate after cancellation")
	}
	if drained > 1 {
		t.Fatalf("drained %d results from one canceled candidate, want at most 1", drained)
	}
}

func TestS3ChecksumVerifyHelpersAreReadOnly(t *testing.T) {
	data := []byte("logical body")
	checksum := checksumForTest(t, minio.ChecksumCRC32, data)
	var mu sync.Mutex
	var methods []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, r.Method)
		mu.Unlock()
		if r.Method != http.MethodHead && r.Method != http.MethodGet {
			t.Errorf("unexpected write method %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != "/archive/object" {
			t.Errorf("path %q, want /archive/object", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if got := r.URL.Query().Get("versionId"); got != "v1" {
			t.Errorf("versionId %q, want v1", got)
		}
		switch r.Method {
		case http.MethodHead:
			if got := r.Header.Get("x-amz-checksum-mode"); got != "ENABLED" {
				t.Errorf("HEAD checksum mode %q, want ENABLED", got)
			}
			w.Header().Set("Content-Length", "12")
			w.Header().Set("Last-Modified", time.Unix(100, 0).UTC().Format(http.TimeFormat))
			w.Header().Set("ETag", `"test-etag"`)
			w.Header().Set("X-Amz-Version-Id", "v1")
			w.Header().Set("X-Amz-Checksum-Type", checksumVerifyFullObjectType)
			w.Header().Set("X-Amz-Checksum-Crc32", checksum)
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			if got := r.Header.Get("x-amz-checksum-mode"); got != "" {
				t.Errorf("GET unexpectedly enabled automatic checksum mode: %q", got)
			}
			if got := r.Header.Get("Accept-Encoding"); got != "identity" {
				t.Errorf("Accept-Encoding %q, want identity", got)
			}
			if got := r.Header.Get("If-Match"); got != `"test-etag"` {
				t.Errorf("If-Match %q, want quoted ETag", got)
			}
			w.Header().Set("Content-Length", "12")
			w.Header().Set("Last-Modified", time.Unix(100, 0).UTC().Format(http.TimeFormat))
			w.Header().Set("ETag", `"test-etag"`)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
		}
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	api, err := minio.New(endpoint.Host, &minio.Options{
		Creds:  credentials.NewStaticV4("access", "secret", ""),
		Secure: false,
		Region: "us-east-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	client := &S3Client{api: api, targetURL: newClientURL(server.URL)}
	info, err := client.statObjectForChecksumVerify(context.Background(), "archive", "object", "v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if info.ChecksumType != checksumVerifyFullObjectType || info.Checksums["CRC32"] != checksum {
		t.Fatalf("stat info %+v does not preserve checksum metadata", info)
	}
	reader, err := client.getObjectForChecksumVerify(context.Background(), "archive", "object", "v1", "test-etag", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err = reader.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("body %q, want %q", got, data)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(methods, []string{http.MethodHead, http.MethodGet}) {
		t.Fatalf("methods %v, want only HEAD and GET", methods)
	}
}
