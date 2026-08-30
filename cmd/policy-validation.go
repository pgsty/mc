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
	"errors"

	"github.com/minio/pkg/v3/policy"
)

// Require the v3.12 policy implementation even when a consuming module
// overrides mc's module replacement with its own silo-pkg version.
var _ func(policy.Resource) bool = policy.Resource.IsBareARN

// parsePolicyForWrite applies the strict validation required for new and
// updated policy documents while read paths remain backward-compatible.
func parsePolicyForWrite(policyBytes []byte) (*policy.Policy, error) {
	return policy.ParseConfigStrict(bytes.NewReader(policyBytes))
}

// parseNamedPolicyForWrite also applies the server invariants for persisted
// named policies. Session policies intentionally keep accepting an omitted
// Version, matching SILO's service-account handlers.
func parseNamedPolicyForWrite(policyBytes []byte) (*policy.Policy, error) {
	p, err := parsePolicyForWrite(policyBytes)
	if err != nil {
		return nil, err
	}
	if p.IsEmpty() {
		return nil, errors.New("empty policies are not allowed")
	}
	if p.Version == "" {
		return nil, errors.New("policy version cannot be empty")
	}
	return p, nil
}
