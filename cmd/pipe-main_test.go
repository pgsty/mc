// Copyright (c) 2026 PGSTY
//
// This file is part of the Silo object storage client.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package cmd

import (
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func TestPreparePipeReader(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		reader, size, err := preparePipeReader(strings.NewReader(""))
		if err != nil {
			t.Fatal(err)
		}
		if size != 0 {
			t.Fatalf("stream size = %d, want 0", size)
		}
		contents, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if len(contents) != 0 {
			t.Fatalf("empty stream returned %d byte(s)", len(contents))
		}
	})

	t.Run("non-empty", func(t *testing.T) {
		reader, size, err := preparePipeReader(strings.NewReader("payload"))
		if err != nil {
			t.Fatal(err)
		}
		if size != -1 {
			t.Fatalf("stream size = %d, want -1", size)
		}
		contents, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if string(contents) != "payload" {
			t.Fatalf("stream contents = %q, want payload", contents)
		}
	})

	t.Run("read error", func(t *testing.T) {
		wantErr := errors.New("read failure")
		_, _, err := preparePipeReader(iotest.ErrReader(wantErr))
		if !errors.Is(err, wantErr) {
			t.Fatalf("preparePipeReader() error = %v, want %v", err, wantErr)
		}
	})
}
