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
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Every local support artifact starts life as an os.CreateTemp file, which is
// 0600. moveFile used to recreate it with os.Create, so the file the operator
// keeps ended up 0644 under the usual 022 umask.
func TestMoveFilePreservesRestrictiveSourceMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on Windows")
	}

	dir := t.TempDir()
	source := filepath.Join(dir, "mc-profile-source")
	dest := filepath.Join(dir, "profile.zip")

	if err := os.WriteFile(source, []byte("profile payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := moveFile(source, dest); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("destination mode %o, want 600", got)
	}
	if _, err = os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("source should have been removed, stat error: %v", err)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "profile payload" {
		t.Fatalf("destination content %q", string(data))
	}
}

// The rotate-then-move callers overwrite an existing destination; a stale
// world-readable file must not keep its mode.
func TestMoveFileTightensExistingDestination(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on Windows")
	}

	dir := t.TempDir()
	source := filepath.Join(dir, "mc-inspect-source")
	dest := filepath.Join(dir, "inspect.zip")

	if err := os.WriteFile(source, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("stale and world readable"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dest, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := moveFile(source, dest); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("destination mode %o, want 600", got)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Fatalf("destination was not truncated: %q", string(data))
	}
}

// tarGZ writes the health report the command itself warns may contain
// sensitive environment detail.
func TestTarGZWritesPrivateHealthReport(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on Windows")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "health.zip")
	// Pre-create the file world-readable: WriteFile only applies its mode when
	// it creates the file, so an existing report must be tightened too.
	if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := tarGZ(map[string]string{"probe": "value"}, "3", path); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("health report mode %o, want 600", got)
	}
}
