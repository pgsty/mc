// Copyright (c) 2015-2022 MinIO, Inc.
//
// This file is part of MinIO Object Storage stack
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package cmd

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"time"

	humanize "github.com/dustin/go-humanize"
	"github.com/minio/mc/pkg/probe"
)

// odSetSizes sets necessary values for object transfer.
func odSetSizes(odURLs URLs, args argKVS) (combinedSize int64, partSize uint64, parts int, skip int64, e error) {
	// If parts not specified, set to 0, else scan for integer.
	p := args.Get("parts")
	if p == "" {
		parts = 0
	} else {
		parts, e = strconv.Atoi(p)
		if e != nil {
			return 0, 0, 0, 0, e
		}
		if parts < 0 {
			return 0, 0, 0, 0, fmt.Errorf("parts must be non-negative")
		}
	}

	// Get number of parts to skip, defaults to 0.
	sk := args.Get("skip")
	var skipInt int
	if sk == "" {
		skipInt = 0
	} else {
		skipInt, e = strconv.Atoi(sk)
		if e != nil {
			return 0, 0, 0, 0, e
		}
		if skipInt < 0 {
			return 0, 0, 0, 0, fmt.Errorf("skip must be non-negative")
		}
	}

	sourceSize := odURLs.SourceContent.Size

	// If neither parts nor size is specified, copy full file in 1 part.
	s := args.Get("size")
	if parts <= 1 && s == "" {
		return sourceSize, uint64(sourceSize), 1, 0, nil
	}

	// If size is not specified, calculate the size based on parts and upload full file.
	if s == "" {
		partSize = odPartSize(sourceSize, parts)
		if sourceSize > 0 {
			// A requested part count can exceed the number of non-empty parts
			// available in a finite source. Report the effective count instead
			// of carrying an impossible count into the transfer summary.
			parts = odPartCount(sourceSize, partSize)
		}
		if skipInt > parts {
			return 0, 0, 0, 0, fmt.Errorf("skip exceeds source parts")
		}
		skip, e = odPartBytes(skipInt, partSize, "skip")
		if e != nil {
			return 0, 0, 0, 0, e
		}
		parts = parts - skipInt
		return -1, partSize, parts, skip, nil
	}

	partSize, e = humanize.ParseBytes(s)
	if e != nil {
		return 0, 0, 0, 0, e
	}
	if partSize == 0 {
		return 0, 0, 0, 0, fmt.Errorf("size must be greater than zero")
	}

	// Convert skipInt to bytes.
	skip, e = odPartBytes(skipInt, partSize, "skip")
	if e != nil {
		return 0, 0, 0, 0, e
	}
	if sourceSize > 0 && skip > sourceSize {
		return 0, 0, 0, 0, fmt.Errorf("skip exceeds source size")
	}

	// If source has no content, calculate combined size and return.
	// This is needed for when source is /dev/zero.
	if sourceSize == 0 {
		if parts == 0 {
			parts = 1
		}
		combinedSize, e = odPartBytes(parts, partSize, "parts")
		if e != nil {
			return 0, 0, 0, 0, e
		}
		return combinedSize, partSize, parts, skip, nil
	}

	// If source is smaller than part size, upload in 1 part.
	if partSize > uint64(sourceSize) {
		return sourceSize, uint64(sourceSize), 1, 0, nil
	}

	// If parts is not specified, calculate number of parts and upload full file.
	if parts < 1 {
		parts = odPartCount(sourceSize, partSize) - skipInt
		return sourceSize - skip, partSize, parts, skip, nil
	}

	combinedSize, e = odPartBytes(parts, partSize, "parts")
	if e != nil {
		return 0, 0, 0, 0, e
	}

	// If combined size is larger than source after skip, recalculate number of parts.
	if sourceSize-skip < combinedSize {
		combinedSize = sourceSize - skip
		parts = odPartCount(sourceSize, partSize) - skipInt
	}
	return combinedSize, partSize, parts, skip, nil
}

const maxODInt64 = uint64(1<<63 - 1)

func odPartSize(sourceSize int64, parts int) uint64 {
	if sourceSize <= 0 || parts <= 0 {
		return 0
	}
	size := uint64(sourceSize)
	partSize := size / uint64(parts)
	if size%uint64(parts) != 0 {
		partSize++
	}
	return partSize
}

func odPartBytes(count int, partSize uint64, name string) (int64, error) {
	if count < 0 {
		return 0, fmt.Errorf("%s must be non-negative", name)
	}
	if count == 0 || partSize == 0 {
		return 0, nil
	}
	if uint64(count) > maxODInt64/partSize {
		return 0, fmt.Errorf("%s and size exceed the maximum supported transfer size", name)
	}
	return int64(uint64(count) * partSize), nil
}

func odPartCount(sourceSize int64, partSize uint64) int {
	if sourceSize <= 0 || partSize == 0 {
		return 0
	}
	size := uint64(sourceSize)
	parts := size / partSize
	if size%partSize != 0 {
		parts++
	}
	return int(parts)
}

// odCopy copies a file/object from local to server, server to server, or local to local.
func odCopy(ctx context.Context, odURLs URLs, args argKVS, odType string) (odMessage, error) {
	// Set sizes.
	combinedSize, partSize, parts, skip, e := odSetSizes(odURLs, args)
	if e != nil {
		return odMessage{}, e
	}

	sourceAlias := odURLs.SourceAlias
	sourceURL := odURLs.SourceContent.URL
	sourcePath := filepath.ToSlash(filepath.Join(sourceAlias, sourceURL.Path))
	targetAlias := odURLs.TargetAlias
	targetURL := odURLs.TargetContent.URL
	targetPath := filepath.ToSlash(filepath.Join(targetAlias, targetURL.Path))

	getOpts := GetOptions{}

	// Skip given number of parts.
	if skip > 0 {
		getOpts.RangeStart = skip
	}

	// Placeholder encryption key database.
	var encKeyDB map[string][]prefixSSEPair

	// Create reader from source.
	reader, err := getSourceStreamFromURL(ctx, sourcePath, encKeyDB, getSourceOpts{GetOptions: getOpts})
	fatalIf(err.Trace(sourcePath), "Unable to get source stream")
	defer reader.Close()

	putOpts := PutOptions{
		storageClass:  odURLs.TargetContent.StorageClass,
		md5:           odURLs.MD5,
		multipartSize: partSize,
	}

	// Disable multipart on files too small for multipart upload.
	if combinedSize < 5242880 && combinedSize > 0 {
		putOpts.disableMultipart = true
	}

	// Used to get transfer time
	pg := newAccounter(combinedSize)

	// Write to target.
	targetClnt, err := newClientFromAlias(targetAlias, targetURL.String())
	fatalIf(err.Trace(targetURL.String()), "Unable to initialize target client")

	// Put object.
	total, err := targetClnt.PutPart(ctx, reader, combinedSize, pg, putOpts)
	fatalIf(err.Trace(targetURL.String()), "Unable to upload")

	// Get upload time.
	elapsed := time.Since(pg.startTime)

	skippedParts := 0
	if partSize > 0 {
		skippedParts = int(uint64(skip) / partSize)
	}
	message := odMessage{
		Status:    "success",
		Type:      odType,
		Source:    sourcePath,
		Target:    targetPath,
		PartSize:  partSize,
		TotalSize: total,
		Parts:     parts,
		Skip:      skippedParts,
		Elapsed:   elapsed.Milliseconds(),
	}

	return message, nil
}

// odSetParts sets parts for object download.
func odSetParts(args argKVS) (parts, skip int, e error) {
	if args.Get("size") != "" {
		return 0, 0, fmt.Errorf("size cannot be specified getting from server")
	}

	p := args.Get("parts")
	if p == "" {
		return 0, 0, nil
	}
	parts, e = strconv.Atoi(p)
	if e != nil {
		return 0, 0, e
	}
	if parts < 1 {
		return 0, 0, fmt.Errorf("parts must be at least 1")
	}

	sk := args.Get("skip")
	if sk == "" {
		skip = 0
	} else {
		skip, e = strconv.Atoi(sk)
	}
	if e != nil {
		return 0, 0, e
	}
	if skip < 0 {
		return 0, 0, fmt.Errorf("skip must be non-negative")
	}

	return parts, skip, nil
}

// odDownload copies an object from server to local.
func odDownload(ctx context.Context, odURLs URLs, args argKVS) (odMessage, error) {
	/// Set number of parts to get.
	parts, skip, e := odSetParts(args)
	if e != nil {
		return odMessage{}, e
	}

	targetPath := odURLs.TargetContent.URL.Path
	sourceAlias := odURLs.SourceAlias
	sourceURL := odURLs.SourceContent.URL
	sourcePath := filepath.ToSlash(filepath.Join(sourceAlias, sourceURL.Path))

	// Get server client.
	cli, err := newClientFromAlias(sourceAlias, sourceURL.String())
	if err != nil {
		return odMessage{}, err.ToGoError()
	}

	var reader io.ReadCloser
	if parts == 0 {
		// Get the full file.
		reader, err = cli.GetPart(ctx, 0)
	} else {
		// Get the file in parts.
		reader, err = multiGet(ctx, cli, parts, skip)
	}
	if err != nil {
		return odMessage{}, err.ToGoError()
	}
	defer reader.Close()

	// Accounter to get transfer time.
	pg := newAccounter(-1)

	// Upload the file.
	total, err := putTargetStream(ctx, "", targetPath, "", "", "",
		reader, -1, pg, PutOptions{})
	if err != nil {
		return odMessage{}, err.Trace(targetPath).ToGoError()
	}

	// Get upload time.
	elapsed := time.Since(pg.startTime)

	message := odMessage{
		Status:    "success",
		Type:      "S3toFS",
		Source:    sourcePath,
		Target:    targetPath,
		TotalSize: total,
		Parts:     parts,
		Skip:      skip,
		Elapsed:   elapsed.Milliseconds(),
	}

	return message, nil
}

// multiPartReader retains the closers that io.MultiReader does not expose.
type multiPartReader struct {
	readers []io.ReadCloser
	reader  io.Reader
}

func (r *multiPartReader) Read(p []byte) (int, error) { return r.reader.Read(p) }

func (r *multiPartReader) Close() error {
	var firstErr error
	for _, reader := range r.readers {
		if err := reader.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func multiGet(ctx context.Context, cli Client, parts, skip int) (io.ReadCloser, *probe.Error) {
	var readers []io.ReadCloser

	// Get reader for each part.
	for i := 1 + skip; i <= parts; i++ {
		reader, err := cli.GetPart(ctx, i)
		if err != nil {
			for _, opened := range readers {
				_ = opened.Close()
			}
			return nil, err
		}
		readers = append(readers, reader)
	}
	genericReaders := make([]io.Reader, len(readers))
	for i, reader := range readers {
		genericReaders[i] = reader
	}
	return &multiPartReader{readers: readers, reader: io.MultiReader(genericReaders...)}, nil
}
