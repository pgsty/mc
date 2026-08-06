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
	"strings"
	"testing"
)

// The Silo fork ships permanently offline: no code path may contact MinIO
// SUBNET. These tests pin the fork-level guarantees so upstream merges
// cannot silently re-enable connectivity.

func TestSubnetServicesDisabled(t *testing.T) {
	if subnetServicesEnabled() {
		t.Fatal("subnetServicesEnabled() = true; the Silo fork must ship with SUBNET permanently disabled")
	}
}

func TestSubnetRequestsReturnDisabledError(t *testing.T) {
	// The URL must never be contacted; using an unroutable address makes an
	// accidental regression fail loudly instead of silently succeeding.
	const unroutable = "http://192.0.2.1/api/test"

	if _, e := subnetGetReq(unroutable, nil); e == nil || e.Error() != subnetDisabledMessage {
		t.Fatalf("subnetGetReq error = %v, want %q", e, subnetDisabledMessage)
	}
	if _, e := subnetHeadReq(unroutable, nil); e == nil || e.Error() != subnetDisabledMessage {
		t.Fatalf("subnetHeadReq error = %v, want %q", e, subnetDisabledMessage)
	}
	if _, e := SubnetPostReq(unroutable, nil, nil); e == nil || e.Error() != subnetDisabledMessage {
		t.Fatalf("SubnetPostReq error = %v, want %q", e, subnetDisabledMessage)
	}
}

func TestFreshConfigHasNoThirdPartyDemoAlias(t *testing.T) {
	cfg := newMcConfig()

	if _, ok := cfg.Aliases["play"]; ok {
		t.Fatal(`fresh config contains a "play" alias; new configurations must not point at MinIO's demo cluster`)
	}
	for _, alias := range []string{"local", "s3", "gcs"} {
		if _, ok := cfg.Aliases[alias]; !ok {
			t.Fatalf("fresh config is missing the default %q alias", alias)
		}
	}
	for alias, cfgV10 := range cfg.Aliases {
		if strings.Contains(cfgV10.URL, "min.io") {
			t.Fatalf("fresh config alias %q points at %s; MinIO-operated endpoints are not allowed in new configs", alias, cfgV10.URL)
		}
	}
}

func TestAGPLMessageHasNoCommercialPitch(t *testing.T) {
	msg := getAGPLMessage()
	for _, needle := range []string{"min.io", "commercial", "subscription"} {
		if strings.Contains(strings.ToLower(msg), needle) {
			t.Fatalf("getAGPLMessage() contains %q; commercial upsell text must not appear: %q", needle, msg)
		}
	}
}
