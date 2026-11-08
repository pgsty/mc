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
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func shareTestShell(t *testing.T) string {
	t.Helper()
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("requires a POSIX shell")
	}
	return shell
}

func TestMakeCurlCmdShellArguments(t *testing.T) {
	shell := shareTestShell(t)
	for _, recursive := range []bool{false, true} {
		for _, value := range []string{
			"text/plain; charset=utf-8", "Robert O'Neil \"quoted\"", "$(printf substituted)",
			"`printf substituted`", "back\\slash", "line\nbreak\tspace", "@not-a-file", "<not-a-file",
			"A&B<Revision>|(1);#", "", "${HOME}",
		} {
			t.Run(value+map[bool]string{false: "/object", true: "/recursive"}[recursive], func(t *testing.T) {
				url := "https://example.com/upload?literal=" + value + "&other=$HOME"
				field := "field ' \" $() `literal` \\ \n"
				key := "prefix/" + value
				command, err := makeCurlCmd("unused-key", url, recursive, map[string]string{field: value, "key": key})
				if err != nil {
					t.Fatal(err)
				}
				// Replace curl with a shell function to observe precisely what the
				// generated command passes, without performing a network request.
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				output, e := exec.CommandContext(ctx, shell, "-c", `curl() { printf '%s\000' "$@"; }; `+command).Output()
				if e != nil {
					t.Fatalf("shell failed: %v; command=%s", e, command)
				}
				if recursive {
					key += "<NAME>"
				}
				want := []string{url, "--form-string", field + "=" + value, "--form-string", "key=" + key, "--form", "file=@<FILE>"}
				got := strings.Split(strings.TrimSuffix(string(output), "\x00"), "\x00")
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("argv=%q want=%q", got, want)
				}
			})
		}
	}
}

func TestMakeCurlCmdUploadsLiteralFormValues(t *testing.T) {
	shell := shareTestShell(t)
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("requires curl")
	}
	for _, recursive := range []bool{false, true} {
		for _, value := range []string{
			"text/plain; charset=utf-8", "quotes ' \" $() `literal` \\ \n", "@not-a-file", "<not-a-file", "a&b;type=text/plain",
		} {
			t.Run(value+map[bool]string{false: "/object", true: "/recursive"}[recursive], func(t *testing.T) {
				payload := []byte("uploaded payload\n")
				file := filepath.Join(t.TempDir(), "upload file.txt")
				if err := os.WriteFile(file, payload, 0o600); err != nil {
					t.Fatal(err)
				}
				key := "prefix/" + value
				wantKey := key
				if recursive {
					wantKey += "uploaded.txt"
				}
				received := make(chan struct{}, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodPost || r.URL.RawQuery != "first=$()&second='quoted'" {
						t.Errorf("unexpected request: %s %s", r.Method, r.URL)
					}
					if err := r.ParseMultipartForm(1 << 20); err != nil {
						t.Errorf("multipart: %v", err)
						w.WriteHeader(400)
						return
					}
					defer r.MultipartForm.RemoveAll()
					for name, want := range map[string]string{"key": wantKey, "Content-Type": value, "policy": value} {
						if got := r.FormValue(name); got != want {
							t.Errorf("%s=%q want=%q", name, got, want)
						}
					}
					uploaded, _, err := r.FormFile("file")
					if err != nil {
						t.Errorf("file: %v", err)
						w.WriteHeader(400)
						return
					}
					defer uploaded.Close()
					data, err := io.ReadAll(uploaded)
					if err != nil || !bytes.Equal(data, payload) {
						t.Errorf("upload=%q error=%v", data, err)
					}
					received <- struct{}{}
					w.WriteHeader(http.StatusNoContent)
				}))
				defer server.Close()
				command, err := makeCurlCmd(key, server.URL+"?first=$()&second='quoted'", recursive, map[string]string{"Content-Type": value, "policy": value})
				if err != nil {
					t.Fatal(err)
				}
				command = strings.ReplaceAll(command, "<FILE>", file)
				command = strings.ReplaceAll(command, "<NAME>", "uploaded.txt")
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				output, e := exec.CommandContext(ctx, shell, "-c", command).CombinedOutput()
				if e != nil {
					t.Fatalf("curl failed: %v: %s", e, output)
				}
				select {
				case <-received:
				default:
					t.Fatal("curl did not upload the expected multipart data")
				}
			})
		}
	}
}
