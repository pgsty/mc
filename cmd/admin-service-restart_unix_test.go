//go:build darwin || linux

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
	"bytes"
	"fmt"
	"strings"
	"syscall"
	"testing"
)

func TestServiceRestartWithoutControllingTTY(t *testing.T) {
	for _, quiet := range []bool{false, true} {
		for _, dryRun := range []bool{false, true} {
			t.Run(fmt.Sprintf("quiet=%t/dry-run=%t", quiet, dryRun), func(t *testing.T) {
				server := newServiceRestartTestServer(t, true)
				defer server.Close()
				args := []string{"admin", "service", "restart", "--wait"}
				if quiet {
					args = append([]string{"--quiet"}, args...)
				}
				if dryRun {
					args = append(args, "--dry-run")
				}
				command := checksumVerifyCLICommand(t, server.URL, nil, append(args, "b4")...)
				command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
				var stdout, stderr bytes.Buffer
				command.Stdout, command.Stderr = &stdout, &stderr
				if code := checksumVerifyCLIExitCode(t, command); code != 0 {
					t.Fatalf("exit=%d stdout=%s stderr=%s", code, &stdout, &stderr)
				}
				text := stdout.String()
				action := "Restart request sent to"
				if dryRun {
					action = "Dry run completed for"
				}
				if strings.Count(text, "\n") != 1 || !strings.HasPrefix(text, action+" b4") || !strings.Contains(text, "1 online, 1 offline, 1 hung") || !strings.Contains(text, "initialization wait") {
					t.Errorf("unexpected summary: %q", text)
				}
				if strings.Contains(text, "\x1b") || strings.Contains(text, `"status"`) || stderr.Len() != 0 {
					t.Errorf("unexpected UI/JSON output: stdout=%q stderr=%q", text, stderr.String())
				}
			})
		}
	}
}
