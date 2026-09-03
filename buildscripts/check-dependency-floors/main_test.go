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

package main

import (
	"slices"
	"testing"
)

func TestRequirementsApplySamePathReplacement(t *testing.T) {
	floors := requirements("test.mod", []byte(`module example.com/test

go 1.27.1

require example.com/dependency v1.2.0

replace example.com/dependency => example.com/dependency v1.1.0
`))
	if got := floors.modules["example.com/dependency"]; got != "v1.1.0" {
		t.Fatalf("replacement floor = %s, want v1.1.0", got)
	}
}

func TestCompareFloors(t *testing.T) {
	previous := floorSet{
		goVersion: "1.27.1",
		modules: map[string]string{
			"example.com/dependency":           "v1.2.0",
			"github.com/coreos/go-systemd/v22": "v22.7.0",
		},
	}
	current := floorSet{
		goVersion: "1.27.0",
		modules: map[string]string{
			"example.com/dependency":           "v1.1.0",
			"github.com/coreos/go-systemd/v22": "v22.6.0",
		},
	}

	want := []string{"example.com/dependency: v1.2.0 -> v1.1.0", "go: 1.27.1 -> 1.27.0"}
	if got := compareFloors(current, previous); !slices.Equal(got, want) {
		t.Fatalf("regressions = %v, want %v", got, want)
	}
}
