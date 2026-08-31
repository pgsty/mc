// Copyright (c) 2026 PGSTY
//
// This file is part of the Silo object storage client.
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
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

type selectCloseTestReader struct {
	io.Reader
	closeCalls atomic.Int32
}

type selectBlockingReader struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *selectBlockingReader) Read([]byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	<-r.release
	return 0, io.EOF
}

func (r *selectCloseTestReader) Close() error {
	r.closeCalls.Add(1)
	return nil
}

func TestSelectResultsReadCloserDoesNotDoubleCloseAfterEOF(t *testing.T) {
	underlying := &selectCloseTestReader{Reader: bytes.NewReader([]byte("rows\n"))}
	var cancelCalls atomic.Int32
	reader := newSelectResultsReadCloser(underlying, func() { cancelCalls.Add(1) })

	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "rows\n" {
		t.Fatalf("output = %q, want rows\\n", got)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if underlying.closeCalls.Load() != 0 {
		t.Fatalf("underlying Close calls = %d, want 0", underlying.closeCalls.Load())
	}
	if cancelCalls.Load() != 1 {
		t.Fatalf("cancel calls = %d, want 1", cancelCalls.Load())
	}
}

func TestSelectResultsReadCloserEarlyCloseUnblocksRead(t *testing.T) {
	blocking := &selectBlockingReader{started: make(chan struct{}), release: make(chan struct{})}
	underlying := &selectCloseTestReader{Reader: blocking}
	var cancelCalls atomic.Int32
	reader := newSelectResultsReadCloser(underlying, func() {
		cancelCalls.Add(1)
		close(blocking.release)
	})

	readDone := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(reader)
		readDone <- err
	}()
	select {
	case <-blocking.started:
	case <-time.After(5 * time.Second):
		t.Fatal("Read did not enter the blocking source")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- reader.Close() }()

	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not cancel and drain the in-flight Select read")
	}
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Read remained blocked after Close")
	}
	if underlying.closeCalls.Load() != 0 {
		t.Fatalf("underlying Close calls = %d, want 0", underlying.closeCalls.Load())
	}
	if cancelCalls.Load() != 1 {
		t.Fatalf("cancel calls = %d, want 1", cancelCalls.Load())
	}
}

func selectEventHeader(name, value string) []byte {
	var out bytes.Buffer
	out.WriteByte(byte(len(name)))
	out.WriteString(name)
	out.WriteByte(7) // AWS event-stream string header
	_ = binary.Write(&out, binary.BigEndian, uint16(len(value)))
	out.WriteString(value)
	return out.Bytes()
}

func selectEvent(headers, payload []byte) []byte {
	var out bytes.Buffer
	totalLength := uint32(12 + len(headers) + len(payload) + 4)
	_ = binary.Write(&out, binary.BigEndian, totalLength)
	_ = binary.Write(&out, binary.BigEndian, uint32(len(headers)))
	_ = binary.Write(&out, binary.BigEndian, crc32.ChecksumIEEE(out.Bytes()))
	out.Write(headers)
	out.Write(payload)
	_ = binary.Write(&out, binary.BigEndian, crc32.ChecksumIEEE(out.Bytes()))
	return out.Bytes()
}

func selectEventHeaders(eventType string) []byte {
	return bytes.Join([][]byte{
		selectEventHeader(":message-type", "event"),
		selectEventHeader(":event-type", eventType),
		selectEventHeader(":content-type", "application/octet-stream"),
	}, nil)
}

func TestS3SelectZstdResponseHasSingleCloseOwner(t *testing.T) {
	// The SDK closes its pipe before it drains the remainder of an End event.
	// A sizeable, incompressible tail makes the old caller-side second Close
	// reliably overlap that zstd drain under the race detector.
	tail := make([]byte, 256<<10)
	state := uint32(0x9e3779b9)
	for i := range tail {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		tail[i] = byte(state)
	}
	stream := append(selectEvent(selectEventHeaders("Records"), []byte("1,alice\n")),
		selectEvent(selectEventHeaders("End"), tail)...)
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest))
	if err != nil {
		t.Fatal(err)
	}
	compressed := encoder.EncodeAll(stream, nil)
	encoder.Close()

	var sawZstd atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Query().Has("location"):
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`)
		case r.Method == http.MethodPost && r.URL.Query().Has("select"):
			sawZstd.Store(strings.Contains(r.Header.Get("Accept-Encoding"), "zstd"))
			w.Header().Set("Content-Encoding", "zstd")
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(compressed)
		default:
			http.Error(w, "unexpected request", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	client, perr := S3New(&Config{
		HostURL:   server.URL + "/bucket/rows.csv",
		AccessKey: "access",
		SecretKey: "secretsecret",
		Signature: "S3v4",
	})
	if perr != nil {
		t.Fatal(perr)
	}
	for range 50 {
		reader, perr := client.Select(context.Background(), "select * from s3object", nil, SelectObjectOpts{})
		if perr != nil {
			t.Fatal(perr)
		}
		got, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "1,alice\n" {
			t.Fatalf("select output = %q, want 1,alice\\n", got)
		}
		if err := reader.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if !sawZstd.Load() {
		t.Fatal("test server never received a zstd Select request")
	}
}

func TestS3SelectZstdEarlyCloseCancelsOpenStream(t *testing.T) {
	record := selectEvent(selectEventHeaders("Records"), []byte("1,alice\n"))
	requestCanceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Query().Has("location"):
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`)
		case r.Method == http.MethodPost && r.URL.Query().Has("select"):
			w.Header().Set("Content-Encoding", "zstd")
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(http.StatusOK)
			encoder, err := zstd.NewWriter(w, zstd.WithEncoderLevel(zstd.SpeedFastest))
			if err != nil {
				return
			}
			_, _ = encoder.Write(record)
			_ = encoder.Flush()
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			<-r.Context().Done()
			close(requestCanceled)
			_ = encoder.Close()
		default:
			http.Error(w, "unexpected request", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	client, perr := S3New(&Config{
		HostURL:   server.URL + "/bucket/rows.csv",
		AccessKey: "access",
		SecretKey: "secretsecret",
		Signature: "S3v4",
	})
	if perr != nil {
		t.Fatal(perr)
	}
	reader, perr := client.Select(context.Background(), "select * from s3object", nil, SelectObjectOpts{})
	if perr != nil {
		t.Fatal(perr)
	}
	line, err := bufio.NewReader(reader).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if line != "1,alice\n" {
		t.Fatalf("select output = %q, want 1,alice\\n", line)
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- reader.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not cancel and drain the open zstd Select stream")
	}
	select {
	case <-requestCanceled:
	case <-time.After(5 * time.Second):
		t.Fatal("Select request context was not canceled")
	}
}
