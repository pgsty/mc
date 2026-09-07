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
	"encoding/json"
	"testing"

	"github.com/minio/cli"
)

func TestCLIOnUsageError(t *testing.T) {
	var checkOnUsageError func(cli.Command, string)
	checkOnUsageError = func(cmd cli.Command, parentCmd string) {
		if cmd.Subcommands != nil {
			for _, subCmd := range cmd.Subcommands {
				if cmd.Hidden {
					continue
				}
				checkOnUsageError(subCmd, parentCmd+" "+cmd.Name)
			}
			return
		}
		if !cmd.Hidden && cmd.OnUsageError == nil {
			t.Errorf("On usage error for `%s` not found", parentCmd+" "+cmd.Name)
		}
	}

	for _, cmd := range appCmds {
		if cmd.Hidden {
			continue
		}
		checkOnUsageError(cmd, "")
	}
}

func TestCLIJSONUsageErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		env  []string
	}{
		{name: "app flag", args: []string{"--json", "--bad-flag"}},
		{name: "global flag", args: []string{"--json", "cp", "--bad-flag"}},
		{name: "command flag", args: []string{"cp", "--json", "--bad-flag"}},
		{name: "group global flag", args: []string{"--json", "admin", "--bad-flag"}},
		{name: "group local flag", args: []string{"admin", "--json", "--bad-flag"}},
		{name: "nested group flag", args: []string{"admin", "user", "--json", "--bad-flag"}},
		{name: "nested command flag", args: []string{"admin", "user", "add", "--json", "--bad-flag"}},
		{name: "flag value", args: []string{"--json", "cp", "--max-workers", "invalid"}},
		{name: "no command", args: []string{"--json"}},
		{name: "missing global args", args: []string{"--json", "cp"}},
		{name: "missing local args", args: []string{"cp", "--json"}},
		{name: "missing nested args", args: []string{"admin", "user", "add", "--json"}},
		{name: "unknown command", args: []string{"--json", "not-a-command"}},
		{name: "environment", args: []string{"admin", "--bad-flag"}, env: []string{"MC_JSON=on"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := runChecksumVerifyCLI(t, "http://127.0.0.1:1", tc.env, tc.args...)
			if result.exitCode != 1 {
				t.Fatalf("exit = %d, want 1; stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
			}
			var msg struct {
				Status string       `json:"status"`
				Error  errorMessage `json:"error"`
			}
			if err := json.Unmarshal(result.stdout, &msg); err != nil {
				t.Fatalf("expected one JSON error: %v; stdout=%q stderr=%q", err, result.stdout, result.stderr)
			}
			if msg.Status != "error" || msg.Error.Message == "" || msg.Error.Type != "fatal" {
				t.Fatalf("incomplete JSON error: %+v", msg)
			}
			if len(bytes.TrimSpace(result.stderr)) != 0 {
				t.Fatalf("unexpected human error on stderr: %q", result.stderr)
			}
		})
	}
}

func TestCLIUsageHelpRemainsHuman(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code int
		text string
	}{
		{args: []string{"--json", "--help"}, text: "USAGE:"},
		{args: []string{"cp", "--json", "--help"}, text: "USAGE:"},
		{args: []string{"admin", "--json", "--help"}, text: "USAGE:"},
		{args: []string{"--version"}, text: "Silo object storage client"},
		{args: []string{"--json=false", "cp", "--bad-flag"}, code: 1, text: "SUPPORTED FLAGS:"},
		{args: []string{"cp"}, code: 1, text: "USAGE:"},
	} {
		t.Run(tc.args[0]+" "+tc.text, func(t *testing.T) {
			result := runChecksumVerifyCLI(t, "http://127.0.0.1:1", nil, tc.args...)
			if result.exitCode != tc.code || !bytes.Contains(append(result.stdout, result.stderr...), []byte(tc.text)) {
				t.Fatalf("exit=%d stdout=%q stderr=%q; want exit=%d and %q", result.exitCode, result.stdout, result.stderr, tc.code, tc.text)
			}
		})
	}
}
