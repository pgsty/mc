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
	"context"
	"io"
	"strings"
	"time"

	minio "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/encrypt"
)

// checksumVerifyObjectInfo is deliberately separate from ClientContent.
// Verification needs every checksum and the checksum type, while changing the
// shared ClientContent conversion would also change existing stat output.
type checksumVerifyObjectInfo struct {
	Size                 int64
	ETag                 string
	LastModified         time.Time
	VersionID            string
	ChecksumType         string
	Checksums            map[string]string
	UnsupportedChecksums map[string]string
}

func (c *S3Client) statObjectForChecksumVerify(ctx context.Context, bucket, object, versionID string, sse encrypt.ServerSide) (checksumVerifyObjectInfo, error) {
	opts := minio.StatObjectOptions{
		ServerSideEncryption: sse,
		VersionID:            versionID,
		Checksum:             true,
	}
	info, err := c.api.StatObject(ctx, bucket, object, opts)
	if err != nil {
		return checksumVerifyObjectInfo{}, err
	}

	checksums := make(map[string]string, 5)
	unsupported := make(map[string]string, 5)
	set := func(dst map[string]string, algorithm, value string) {
		if value != "" {
			dst[algorithm] = value
		}
	}
	for _, algorithm := range checksumVerifyAlgorithms {
		set(checksums, algorithm.Name, algorithm.Value(info))
	}
	set(unsupported, "MD5", info.ChecksumMD5)
	set(unsupported, "SHA512", info.ChecksumSHA512)
	set(unsupported, "XXHASH64", info.ChecksumXXHash64)
	set(unsupported, "XXHASH3", info.ChecksumXXHash3)
	set(unsupported, "XXHASH128", info.ChecksumXXHash128)

	return checksumVerifyObjectInfo{
		Size:                 info.Size,
		ETag:                 strings.Trim(info.ETag, `"`),
		LastModified:         info.LastModified,
		VersionID:            info.VersionID,
		ChecksumType:         strings.ToUpper(info.ChecksumMode),
		Checksums:            checksums,
		UnsupportedChecksums: unsupported,
	}, nil
}

// getObjectForChecksumVerify returns the raw logical object stream without
// enabling minio-go's automatic checksum verification. The caller must consume
// the stream to EOF and compare independently calculated values.
func (c *S3Client) getObjectForChecksumVerify(ctx context.Context, bucket, object, versionID, ifMatchETag string, sse encrypt.ServerSide) (io.ReadCloser, error) {
	opts := minio.GetObjectOptions{
		ServerSideEncryption: sse,
		VersionID:            versionID,
	}
	opts.Set("Accept-Encoding", "identity")
	if ifMatchETag != "" {
		if err := opts.SetMatchETag(strings.Trim(ifMatchETag, `"`)); err != nil {
			return nil, err
		}
	}

	core := minio.Core{Client: c.api}
	reader, _, _, err := core.GetObject(ctx, bucket, object, opts)
	return reader, err
}
