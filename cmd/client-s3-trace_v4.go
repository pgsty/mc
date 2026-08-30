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
	"net/http"
	"net/http/httputil"

	"github.com/minio/mc/pkg/httptracer"
	"github.com/pgsty/silo-pkg/v3/console"
)

// traceV4 - tracing structure for signature version '4'.
type traceV4 struct{}

// newTraceV4 - initialize Trace structure
func newTraceV4() httptracer.HTTPTracer {
	return traceV4{}
}

// Request - Trace HTTP Request
func (t traceV4) Request(req *http.Request) error {
	reqTrace, err := httputil.DumpRequestOut(redactRequestForTrace(req), false) // Only display header
	if err != nil {
		return err
	}
	console.Debug(string(reqTrace))
	return nil
}

// Response - Trace HTTP Response
func (t traceV4) Response(resp *http.Response) error {
	respTrace, err := dumpResponseForTrace(resp)
	if err != nil {
		return err
	}
	console.Debug(string(respTrace))

	if resp.TLS != nil {
		printTLSCertInfo(resp.TLS)
	}

	return nil
}
