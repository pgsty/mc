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
	"encoding/json"
	"flag"
	"testing"

	"github.com/minio/cli"
)

func TestSelfUpdateDisabled(t *testing.T) {
	cmd := disabledUpdateCmd()
	if cmd.Name != "update" {
		t.Fatalf("disabled command name = %q, want %q", cmd.Name, "update")
	}
	if len(cmd.Flags) != 1 || cmd.Flags[0].GetName() != "json" {
		t.Fatalf("disabled command flags = %#v, want compatibility flag json", cmd.Flags)
	}

	set := flag.NewFlagSet("update", flag.ContinueOnError)
	ctx := cli.NewContext(cli.NewApp(), set, nil)
	err := mainUpdate(ctx)
	if err == nil {
		t.Fatal("mc update unexpectedly succeeded")
	}

	exitErr, ok := err.(cli.ExitCoder)
	if !ok {
		t.Fatalf("mc update returned %T, want cli.ExitCoder", err)
	}
	if exitErr.ExitCode() != globalErrorExitStatus {
		t.Fatalf("mc update exit code = %d, want %d", exitErr.ExitCode(), globalErrorExitStatus)
	}
	if err.Error() != "" {
		t.Fatalf("mc update error = %q, want an empty exit status", err)
	}

	var msg disabledUpdateMessage
	if jsonErr := json.Unmarshal([]byte((disabledUpdateMessage{
		Status:  "error",
		Message: selfUpdateDisabledMessage,
	}).JSON()), &msg); jsonErr != nil {
		t.Fatalf("disabled update JSON is invalid: %v", jsonErr)
	}
	if msg.Status != "error" || msg.Message != selfUpdateDisabledMessage {
		t.Fatalf("disabled update JSON = %#v", msg)
	}

	data, downloadErr := DownloadReleaseData("https://updates.example.invalid/", 0)
	if downloadErr == nil || data != "" {
		t.Fatalf("DownloadReleaseData returned data %q and error %v", data, downloadErr)
	}
	if got := downloadErr.ToGoError().Error(); got != selfUpdateDisabledMessage {
		t.Fatalf("DownloadReleaseData error = %q, want %q", got, selfUpdateDisabledMessage)
	}
}
