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

// Command check-dependency-floors rejects module requirements that are lower
// than the most recent published release. It protects runtime version floors
// from disappearing when development tools leave the main module graph.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"
)

var allowedDowngrades = map[string]string{
	// SILO's shared portability pin. The v22.7.0 daemon package does not
	// compile on NetBSD; do not permit this exception to drift any lower.
	"github.com/coreos/go-systemd/v22": "v22.6.0",
	// Keep the public module graph compatible with servers that embed mc or
	// Console. Standalone SILO builds still select silo-pkg v3.12.2 through the
	// top-level replacement, while downstream modules ignore that replacement.
	"github.com/minio/pkg/v3": "v3.6.1",
}

type floorSet struct {
	goVersion string
	toolchain string
	modules   map[string]string
}

func main() {
	baseline := strings.TrimSpace(os.Getenv("DEPENDENCY_FLOOR_BASELINE"))
	if baseline == "" {
		out, err := exec.Command("git", "describe", "--tags", "--abbrev=0", "--match", "RELEASE.*", "HEAD^").Output()
		if err != nil {
			fatalf("find previous release tag: %v", err)
		}
		baseline = strings.TrimSpace(string(out))
	}

	currentData, err := os.ReadFile("go.mod")
	if err != nil {
		fatalf("read current go.mod: %v", err)
	}
	baselineData, err := exec.Command("git", "show", baseline+":go.mod").Output()
	if err != nil {
		fatalf("read go.mod from %s: %v", baseline, err)
	}

	current := requirements("go.mod", currentData)
	previous := requirements(baseline+":go.mod", baselineData)

	regressions := compareFloors(current, previous)
	if len(regressions) > 0 {
		fmt.Fprintf(os.Stderr, "dependency or Go requirements fell below %s:\n  %s\n", baseline, strings.Join(regressions, "\n  "))
		os.Exit(1)
	}

	fmt.Printf("dependency floors are not below %s\n", baseline)
}

func compareFloors(current, previous floorSet) []string {
	var regressions []string
	if semver.Compare("v"+current.goVersion, "v"+previous.goVersion) < 0 {
		regressions = append(regressions, fmt.Sprintf("go: %s -> %s", previous.goVersion, current.goVersion))
	}
	if current.toolchain != "" && previous.toolchain != "" &&
		semver.Compare(toolchainVersion(current.toolchain), toolchainVersion(previous.toolchain)) < 0 {
		regressions = append(regressions, fmt.Sprintf("toolchain: %s -> %s", previous.toolchain, current.toolchain))
	}
	for path, version := range current.modules {
		oldVersion, ok := previous.modules[path]
		if !ok || semver.Compare(version, oldVersion) >= 0 {
			continue
		}
		if allowedVersion, ok := allowedDowngrades[path]; ok && version == allowedVersion {
			continue
		}
		regressions = append(regressions, fmt.Sprintf("%s: %s -> %s", path, oldVersion, version))
	}
	sort.Strings(regressions)
	return regressions
}

func requirements(name string, data []byte) floorSet {
	parsed, err := modfile.Parse(name, data, nil)
	if err != nil {
		fatalf("parse %s: %v", name, err)
	}
	if parsed.Go == nil || !semver.IsValid("v"+parsed.Go.Version) {
		fatalf("%s has an invalid or missing go directive", name)
	}
	result := make(map[string]string, len(parsed.Require))
	for _, requirement := range parsed.Require {
		version := requirement.Mod.Version
		if !semver.IsValid(version) {
			fatalf("%s has invalid version %q for %s", name, version, requirement.Mod.Path)
		}
		result[requirement.Mod.Path] = version
	}
	for _, replacement := range parsed.Replace {
		if replacement.Old.Path != replacement.New.Path || replacement.New.Version == "" {
			continue
		}
		version, ok := result[replacement.Old.Path]
		if !ok || (replacement.Old.Version != "" && replacement.Old.Version != version) {
			continue
		}
		if !semver.IsValid(replacement.New.Version) {
			fatalf("%s has invalid replacement version %q for %s", name, replacement.New.Version, replacement.Old.Path)
		}
		result[replacement.Old.Path] = replacement.New.Version
	}

	toolchain := ""
	if parsed.Toolchain != nil {
		toolchain = parsed.Toolchain.Name
		if !semver.IsValid(toolchainVersion(toolchain)) {
			fatalf("%s has invalid toolchain %q", name, toolchain)
		}
	}
	return floorSet{goVersion: parsed.Go.Version, toolchain: toolchain, modules: result}
}

func toolchainVersion(name string) string {
	return "v" + strings.TrimPrefix(name, "go")
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
