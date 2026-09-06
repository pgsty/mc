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
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pgsty/silo-pkg/v3/console"
)

var testParseKVArgsCases = []struct {
	inp    string
	kvmap  map[string]string
	errMsg string
}{
	{"fh=use,rd=|,fd=;,qec=\"", map[string]string{"fh": "use", "rd": "|", "fd": ";", "qec": "\""}, "<nil>"},
	{"", map[string]string{}, "<nil>"},
	{"not the right format", map[string]string{}, "Arguments should be of the form key=value,... "},
	{"k==v", map[string]string{"k": "=v"}, "<nil>"},
	{"k=v1,k=v2", map[string]string{}, "More than one key=value found for k"},
	{"k=v1;k=v2", map[string]string{"k": "v1;k=v2"}, "<nil>"},
}

func TestParseKVArgs(t *testing.T) {
	for _, test := range testParseKVArgsCases {
		kvmap, err := parseKVArgs(test.inp)
		gerr := err.ToGoError()
		if gerr != nil && gerr.Error() != test.errMsg {
			t.Fatalf("Unexpected result for \"%s\", expected: |%s|  got: |%s|\n", test.inp, test.errMsg, gerr)
		}
		if gerr == nil && test.errMsg != "<nil>" {
			t.Fatalf("Unexpected result for \"%s\", expected: |%s|  got: |%s|\n", test.inp, test.errMsg, gerr)
		}
		for k, v := range test.kvmap {
			actual, ok := kvmap[k]
			if !ok {
				t.Fatalf("Unexpected result for \"%s,\" expected %s , found %s for key %s\n", test.inp, v, actual, k)
			}
		}
	}
}

var testParseSerializationCases = []struct {
	inp           string
	validKeys     []string
	validAbbrKeys map[string]string
	parsedOpts    map[string]string
	errMsg        string
}{
	{
		"rd=\n,fd=;,qc=\"",
		append(validCSVCommonKeys, validCSVInputKeys...),
		validCSVInputAbbrKeys,
		map[string]string{"recorddelimiter": "\n", "fielddelimiter": ";", "quotechar": "\""},
		"<nil>",
	},
	{
		"rd=\n,fd=;,qc=\"",
		validCSVInputKeys,
		validCSVInputAbbrKeys,
		map[string]string{},
		"Options should be key-value pairs in the form key=value,... where valid key(s) are ",
	},
	{
		"nokey=\n,fd=;,qc=\"",
		validCSVInputKeys,
		validCSVInputAbbrKeys,
		map[string]string{},
		"Options should be key-value pairs in the form key=value,... where valid key(s) are ",
	},
	{
		"rd=\n\n,fd=|,qc=\",qc='",
		validCSVInputKeys,
		validCSVInputAbbrKeys,
		map[string]string{},
		"More than one key=value found for ",
	},
	{
		"recordDelimiter=\n\n,FieldDelimiter=|,QuoteChAR=\"",
		append(validCSVCommonKeys, validCSVInputKeys...),
		validCSVInputAbbrKeys,
		map[string]string{"recorddelimiter": "\n\n", "fielddelimiter": "|", "quotechar": "\""},
		"<nil>",
	},
	{
		"recordDelimiter=\n\n,FieldDelimiter=|,QuoteChAR=\",fh=use,qrd=;",
		append(validCSVCommonKeys, validCSVInputKeys...),
		validCSVInputAbbrKeys,
		map[string]string{"recorddelimiter": "\n\n", "fielddelimiter": "|", "quotechar": "\"", "quotedrecorddelimiter": ";", "fileheader": "use"},
		"<nil>",
	},
	{
		"recordDelimiter=\n\n,FieldDelimiter=|,QuoteChar=\",qf=;,qec='",
		append(validCSVCommonKeys, validCSVOutputKeys...),
		validCSVOutputAbbrKeys,
		map[string]string{},
		"Options should be key-value pairs in the form key=value,... where valid key(s) are ",
	},
	{
		"FieldDelimiter=|,QuoteChar=\",qf=;,qec='",
		append(validCSVCommonKeys, validCSVOutputKeys...),
		validCSVOutputAbbrKeys,
		map[string]string{"fielddelimiter": "|", "quotechar": "\"", "quotefields": ";", "quoteescchar": "'"},
		"<nil>",
	},
	{
		"type=lines",
		validJSONInputKeys,
		nil,
		map[string]string{"type": "lines"},
		"<nil>",
	},
}

func TestParseSerializationOpts(t *testing.T) {
	for i, test := range testParseSerializationCases {
		optsMap, err := parseSerializationOpts(test.inp, test.validKeys, test.validAbbrKeys)
		gerr := err.ToGoError()
		if gerr != nil && gerr.Error() != test.errMsg {
			// match partial error message
			if !strings.Contains(gerr.Error(), test.errMsg) {
				t.Fatalf("Test %d: Unexpected result for \"%s\", expected: |%s|  got: |%s|\n", i+1, test.inp, test.errMsg, gerr)
			}
		}
		if gerr == nil && test.errMsg != "<nil>" {
			t.Fatalf("Test %d: Unexpected result for \"%s\", expected: |%s|  got: |%s|\n", i+1, test.inp, test.errMsg, gerr)
		}
		for k, v := range test.parsedOpts {
			actual, ok := optsMap[strings.ToLower(k)]
			if !ok {
				t.Fatalf("Test %d:Unexpected result for \"%s,\" expected %s , found %s for key %s\n", i+1, test.inp, v, actual, k)
			}
		}
	}
}

const sqlCLIHelperEnv = "MC_SQL_CLI_HELPER"

// TestSQLCLIHelper is re-executed as a child process by TestSQLExitStatus. It
// runs the real mcli entry point (Main) so that a failing `sql` run exits with
// the process code the CLI would use in production, mirroring package main's
// `if err := mc.Main(os.Args); err != nil { console.Fatalln(err) }`.
func TestSQLCLIHelper(_ *testing.T) {
	if os.Getenv(sqlCLIHelperEnv) != "1" {
		return
	}
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 {
		console.Fatalln("sql CLI helper is missing --")
	}
	args := append([]string{"mcli"}, os.Args[separator+1:]...)
	if err := Main(args); err != nil {
		console.Fatalln(err)
	}
	os.Exit(0)
}

// runSQLCLI runs `mcli <args...>` in an isolated child process against a
// throwaway config directory and returns its exit code.
func runSQLCLI(t *testing.T, args ...string) int {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	configDir := t.TempDir()
	if err = os.WriteFile(filepath.Join(configDir, globalMCConfigFile), []byte("{\"version\":\"10\",\"aliases\":{}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(configDir, globalSharedURLsDataDir), 0o700); err != nil {
		t.Fatal(err)
	}

	helperArgs := append([]string{"-test.run=^TestSQLCLIHelper$", "--"}, args...)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, executable, helperArgs...)
	command.Args[0] = "mcli"

	// Strip inherited MC_* configuration so the child only sees the temp
	// config directory created above.
	childEnv := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(key), "MC_") {
			continue
		}
		childEnv = append(childEnv, entry)
	}
	command.Env = append(childEnv, sqlCLIHelperEnv+"=1", "MC_CONFIG_DIR="+configDir)

	var stderr bytes.Buffer
	command.Stderr = &stderr
	command.Stdout = &bytes.Buffer{}
	if err = command.Run(); err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("running sql CLI helper: %v (stderr: %s)", err, stderr.String())
	}
	return exitErr.ExitCode()
}

// TestSQLExitStatus is the regression test for issue #25: `mc sql` reported a
// per-object failure through errorIf but always exited 0, so scripts could not
// detect failure. A failing target must now yield a non-zero exit status while
// a run with nothing to fail on still exits 0.
func TestSQLExitStatus(t *testing.T) {
	// A nonexistent local target fails in url2Stat and is reported through the
	// same errorIf/accumulate path that a bad query in sqlSelect takes; no
	// live server is required to exercise the failure exit status.
	badTarget := filepath.Join(t.TempDir(), "does-not-exist.csv")
	if code := runSQLCLI(t, "sql", "--query", "select * from s3object", badTarget); code == 0 {
		t.Fatalf("mc sql on a failing target exited 0, want non-zero (issue #25)")
	}

	// A recursive run over an empty directory has no object to fail on and
	// must still exit 0, guarding against a false-positive exit status.
	emptyDir := t.TempDir()
	if code := runSQLCLI(t, "sql", "--recursive", "--query", "select * from s3object", emptyDir+"/"); code != 0 {
		t.Fatalf("mc sql over an empty directory exited %d, want 0", code)
	}
}
