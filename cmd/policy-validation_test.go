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
	"strings"
	"testing"

	"github.com/pgsty/silo-pkg/v3/policy"
)

func TestParsePolicyForWriteRejectsBareARNs(t *testing.T) {
	testCases := []string{
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::"]}]}`,
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["*arn:aws:s3:::"]}]}`,
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"NotResource":["arn:aws:s3:::"]}]}`,
	}

	for _, document := range testCases {
		if _, err := policy.ParseConfig(strings.NewReader(document)); err != nil {
			t.Fatalf("compatibility parser rejected stored policy: %v", err)
		}
		if _, err := parsePolicyForWrite([]byte(document)); err == nil {
			t.Fatalf("strict write parser accepted bare ARN policy: %s", document)
		}
	}
}

func TestParsePolicyForWriteAcceptsExplicitResources(t *testing.T) {
	document := []byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::bucket/*"]}]}`)

	p, err := parsePolicyForWrite(document)
	if err != nil {
		t.Fatalf("strict write parser rejected explicit resource: %v", err)
	}
	if p.IsEmpty() {
		t.Fatal("parsed policy is empty")
	}
}

func TestParseNamedPolicyForWriteAppliesNamedPolicyInvariants(t *testing.T) {
	testCases := []struct {
		name     string
		document string
		wantErr  bool
	}{
		{
			name:     "valid named policy",
			document: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::bucket/*"]}]}`,
		},
		{
			name:     "empty policy",
			document: `{"Version":"2012-10-17","Statement":[]}`,
			wantErr:  true,
		},
		{
			name:     "missing version",
			document: `{"Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::bucket/*"]}]}`,
			wantErr:  true,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := parseNamedPolicyForWrite([]byte(testCase.document))
			if (err != nil) != testCase.wantErr {
				t.Fatalf("parseNamedPolicyForWrite() error = %v, wantErr %v", err, testCase.wantErr)
			}
		})
	}
}

func TestParsePolicyForWriteKeepsSessionPolicyVersionCompatibility(t *testing.T) {
	document := []byte(`{"Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::bucket/*"]}]}`)

	if _, err := parsePolicyForWrite(document); err != nil {
		t.Fatalf("strict session-policy parser rejected an omitted version: %v", err)
	}
}
