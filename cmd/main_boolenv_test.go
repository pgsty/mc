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
	"strconv"
	"strings"
	"testing"

	"github.com/minio/cli"
)

// neutralizeBoolEnvVars empties every variable the pass inspects, so an
// inherited setting can neither fail the run before the case under test is
// reached nor be rewritten outside the test's own cleanup. An empty value is
// the one the CLI library already reads as false.
func neutralizeBoolEnvVars(t *testing.T, flags []cli.Flag) {
	t.Helper()
	for _, f := range flags {
		bf, ok := f.(cli.BoolFlag)
		if !ok || bf.EnvVar == "" {
			continue
		}
		for _, name := range strings.Split(bf.EnvVar, ",") {
			t.Setenv(strings.TrimSpace(name), "")
		}
	}
}

// TestNormalizeBoolEnvVars checks the three classes of value: the literals
// strconv.ParseBool already accepts are left byte for byte alone, the SILO
// boolean spellings are rewritten into those literals, and anything else is
// reported against the variable that carries it. The production flag list is
// used so a boolean flag added later is covered without touching this test.
func TestNormalizeBoolEnvVars(t *testing.T) {
	flags := append(mcFlags, globalFlags...)
	neutralizeBoolEnvVars(t, flags)

	for _, tc := range []struct {
		value string
		want  string // "" means the call must fail
	}{
		{value: "true", want: "true"},
		{value: "false", want: "false"},
		{value: "1", want: "1"},
		{value: "0", want: "0"},
		{value: "T", want: "T"},
		{value: "on", want: "true"},
		{value: "ON", want: "true"},
		{value: "On", want: "true"},
		{value: "off", want: "false"},
		{value: "OFF", want: "false"},
		{value: "enabled", want: "true"},
		{value: "Disabled", want: "false"},
		{value: " on ", want: ""},
		{value: " true ", want: ""},
		{value: "yes", want: ""},
		{value: "no", want: ""},
		{value: "bogus", want: ""},
	} {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv(envPrefix+"JSON", tc.value)

			err := normalizeBoolEnvVars(flags)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("%s=%q was accepted, want an error", envPrefix+"JSON", tc.value)
				}
				if !strings.Contains(err.Error(), envPrefix+"JSON") {
					t.Errorf("error %q does not name %s", err, envPrefix+"JSON")
				}
				return
			}
			if err != nil {
				t.Fatalf("%s=%q: %v", envPrefix+"JSON", tc.value, err)
			}
			if got := os.Getenv(envPrefix + "JSON"); got != tc.want {
				t.Fatalf("%s = %q, want %q", envPrefix+"JSON", got, tc.want)
			}
			// Whatever is left must be something the CLI library can parse,
			// which is the only reason this pass exists.
			if _, e := strconv.ParseBool(os.Getenv(envPrefix + "JSON")); e != nil {
				t.Fatalf("normalized value is still unparseable: %v", e)
			}
		})
	}

	t.Run("every bool flag with an env var is covered", func(t *testing.T) {
		neutralizeBoolEnvVars(t, flags)
		for _, f := range flags {
			bf, ok := f.(cli.BoolFlag)
			if !ok || bf.EnvVar == "" {
				continue
			}
			for _, name := range strings.Split(bf.EnvVar, ",") {
				name = strings.TrimSpace(name)
				t.Setenv(name, "on")
				if err := normalizeBoolEnvVars(flags); err != nil {
					t.Fatalf("%s=on: %v", name, err)
				}
				if got := os.Getenv(name); got != "true" {
					t.Errorf("%s = %q, want \"true\"", name, got)
				}
			}
		}
	})

	t.Run("unset and empty are untouched", func(t *testing.T) {
		neutralizeBoolEnvVars(t, flags)
		t.Setenv(envPrefix+"JSON", "")
		if err := normalizeBoolEnvVars(flags); err != nil {
			t.Fatalf("empty value: %v", err)
		}
		if got, ok := os.LookupEnv(envPrefix + "JSON"); !ok || got != "" {
			t.Errorf("%s = %q %v, want empty and set", envPrefix+"JSON", got, ok)
		}
	})
}
