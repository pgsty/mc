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

// These helpers remain for source compatibility with external consumers of
// cmd. They perform local process/environment inspection only and have no
// release-feed or update behavior.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/minio/mc/pkg/probe"
)

// Check whether stdout and stderr are terminals. This is used by interactive
// prompts and pager selection.
var isTerminal = func() bool {
	return isatty.IsTerminal(os.Stdout.Fd()) && isatty.IsTerminal(os.Stderr.Fd())
}

// mcVersionToReleaseTime - parses a standard official release
// mc --version string.
//
// An official binary's version string is the release time formatted
// with RFC3339 (in UTC) - e.g. `2017-09-29T19:16:56Z`
func mcVersionToReleaseTime(version string) (releaseTime time.Time, err *probe.Error) {
	var e error
	releaseTime, e = time.Parse(time.RFC3339, version)
	return releaseTime, probe.NewError(e)
}

// getModTime - get the file modification time of `path`
func getModTime(path string) (t time.Time, err *probe.Error) {
	var e error
	path, e = filepath.EvalSymlinks(path)
	if e != nil {
		return t, probe.NewError(fmt.Errorf("Unable to get absolute path of %s. %w", path, e))
	}

	// Version is mc non-standard, we will use mc binary's
	// ModTime as release time.
	var fi os.FileInfo
	fi, e = os.Stat(path)
	if e != nil {
		return t, probe.NewError(fmt.Errorf("Unable to get ModTime of %s. %w", path, e))
	}

	// Return the ModTime
	return fi.ModTime().UTC(), nil
}

// GetCurrentReleaseTime - returns this process's release time.  If it
// is official mc --version, parsed version is returned else mc
// binary's mod time is returned.
func GetCurrentReleaseTime() (releaseTime time.Time, err *probe.Error) {
	if releaseTime, err = mcVersionToReleaseTime(Version); err == nil {
		return releaseTime, nil
	}

	// Looks like version is mc non-standard, we use mc
	// binary's ModTime as release time:
	path, e := os.Executable()
	if e != nil {
		return releaseTime, probe.NewError(e)
	}
	return getModTime(path)
}

// IsDocker - returns if the environment mc is running in docker or
// not. The check is a simple file existence check.
func IsDocker() bool {
	_, e := os.Stat("/.dockerenv")
	if os.IsNotExist(e) {
		return false
	}

	return e == nil
}

// IsDCOS returns true if mc is running in DCOS.
func IsDCOS() bool {
	// Mesos docker containerizer sets this value
	return os.Getenv("MESOS_CONTAINER_NAME") != ""
}

// IsKubernetes returns true if MinIO is running in kubernetes.
func IsKubernetes() bool {
	return os.Getenv("KUBERNETES_SERVICE_HOST") != ""
}

// IsSourceBuild - returns if this binary is a non-official build from
// source code.
func IsSourceBuild() bool {
	_, err := mcVersionToReleaseTime(Version)
	return err != nil
}

// DownloadReleaseData is retained for source compatibility. The Silo fork
// does not contact a release feed or download self-update metadata.
func DownloadReleaseData(_ string, _ time.Duration) (data string, err *probe.Error) {
	return "", probe.NewError(errors.New(selfUpdateDisabledMessage))
}
