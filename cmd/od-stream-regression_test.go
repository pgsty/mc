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
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program. If not, see <http://www.gnu.org/licenses/>.

package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/minio/mc/pkg/probe"
)

type trackingPart struct {
	*bytes.Reader
	closed bool
}

func (r *trackingPart) Close() error {
	r.closed = true
	return nil
}

func TestMultiPartReaderPreservesOrderAndClosesParts(t *testing.T) {
	first := &trackingPart{Reader: bytes.NewReader([]byte("first"))}
	second := &trackingPart{Reader: bytes.NewReader([]byte("second"))}
	reader := &multiPartReader{
		readers: []io.ReadCloser{first, second},
		reader:  io.MultiReader(first, second),
	}
	data, err := io.ReadAll(reader)
	if err != nil || string(data) != "firstsecond" {
		t.Fatalf("read=%q error=%v", data, err)
	}
	if err := reader.Close(); err != nil || !first.closed || !second.closed {
		t.Fatalf("close error=%v first=%v second=%v", err, first.closed, second.closed)
	}
}

type partDownloadClient struct {
	Client
	requested []int
	opened    []*trackingPart
	failPart  int
}

func (c *partDownloadClient) GetPart(_ context.Context, part int) (io.ReadCloser, *probe.Error) {
	c.requested = append(c.requested, part)
	if part == c.failPart {
		return nil, probe.NewError(errors.New("part unavailable"))
	}
	r := &trackingPart{Reader: bytes.NewReader([]byte(fmt.Sprintf("part%d", part)))}
	c.opened = append(c.opened, r)
	return r, nil
}

func TestMultiGetRequestsDistinctParts(t *testing.T) {
	for _, skip := range []int{0, 1} {
		c := &partDownloadClient{}
		r, err := multiGet(t.Context(), c, 3, skip)
		if err != nil {
			t.Fatal(err)
		}
		data, e := io.ReadAll(r)
		want := "part1part2part3"
		if skip == 1 {
			want = "part2part3"
		}
		if e != nil || string(data) != want {
			t.Fatalf("read=%q error=%v want=%q", data, e, want)
		}
		if e = r.Close(); e != nil {
			t.Fatal(e)
		}
		for _, r := range c.opened {
			if !r.closed {
				t.Fatal("part body was not closed")
			}
		}
	}
}

func TestMultiGetClosesPartsOnFailure(t *testing.T) {
	c := &partDownloadClient{failPart: 2}
	if _, err := multiGet(t.Context(), c, 3, 0); err == nil {
		t.Fatal("missing part error")
	}
	if len(c.opened) != 1 || !c.opened[0].closed {
		t.Fatal("part body leaked on later failure")
	}
}

func TestODMultipartDownloadContent(t *testing.T) {
	parts := []string{"first-part", "second-part"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Last-Modified", "Thu, 01 Jan 2026 00:00:00 GMT")
		w.Header().Set("ETag", `"test-etag-2"`)
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", fmt.Sprint(len(strings.Join(parts, ""))))
			return
		}
		part, err := strconv.Atoi(r.URL.Query().Get("partNumber"))
		if err != nil || part < 1 || part > len(parts) {
			t.Errorf("unexpected part request: %s %s", r.Method, r.URL)
			http.Error(w, "bad part", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(parts[part-1])))
		_, _ = io.WriteString(w, parts[part-1])
	}))
	defer server.Close()
	for _, skip := range []int{0, 1} {
		target := filepath.Join(t.TempDir(), "download")
		result := runChecksumVerifyCLI(t, server.URL, []string{"MC_REGION=us-east-1"}, "--json", "od", "if=b4/bucket/source", "of="+target, "parts=2", fmt.Sprintf("skip=%d", skip))
		want := strings.Join(parts[skip:], "")
		data, err := os.ReadFile(target)
		if result.exitCode != 0 || err != nil || string(data) != want {
			t.Fatalf("exit=%d data=%q error=%v stdout=%s stderr=%s", result.exitCode, data, err, result.stdout, result.stderr)
		}
	}
}
