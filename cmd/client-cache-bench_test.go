// Copyright (c) 2026 Feng Ruohang
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program. If not, see <http://www.gnu.org/licenses/>.

package cmd

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"math/big"
	"net/http"
	"net/netip"
	"testing"

	"github.com/minio/mc/pkg/probe"
)

func BenchmarkClientCache(b *testing.B) {
	oldRoots, oldHeaders, oldResolvers := globalRootCAs, globalCustomHeader, globalResolvers
	b.Cleanup(func() { globalRootCAs, globalCustomHeader, globalResolvers = oldRoots, oldHeaders, oldResolvers })
	for _, key := range []string{"MC_STS_ENDPOINT_bench", "MC_REGION", "AWS_REGION", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy", "REQUEST_METHOD"} {
		b.Setenv(key, "")
	}
	roots := x509.NewCertPool()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		b.Fatal(err)
	}
	// A populated trust store exposes accidental per-object certificate copies.
	for i := int64(1); i <= 128; i++ {
		template := &x509.Certificate{SerialNumber: big.NewInt(i), IsCA: true, BasicConstraintsValid: true}
		der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
		if err != nil {
			b.Fatal(err)
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			b.Fatal(err)
		}
		roots.AddCert(cert)
	}
	for _, populated := range []bool{false, true} {
		name := "default"
		globalRootCAs, globalCustomHeader, globalResolvers = nil, nil, nil
		if populated {
			name = "populated"
			globalRootCAs = roots
			globalCustomHeader = http.Header{"X-Application": {"acceptance"}, "X-Region": {"test"}}
			globalResolvers = map[string]netip.Addr{"bench.example:443": netip.MustParseAddr("127.0.0.1")}
			b.Setenv("HTTPS_PROXY", "http://proxy.example:3128")
			b.Setenv("NO_PROXY", "localhost,127.0.0.1,.internal.example")
		}
		b.Run(name, func(b *testing.B) {
			for _, admin := range []bool{false, true} {
				kind := "S3"
				if admin {
					kind = "Admin"
				}
				b.Run(kind, func(b *testing.B) {
					for _, parallel := range []bool{false, true} {
						mode := "serial"
						if parallel {
							mode = "parallel"
						}
						b.Run(mode, func(b *testing.B) {
							s3, ad := newFactory(), NewAdminFactory()
							config := Config{Alias: "bench", HostURL: "https://bench.example/bucket/object", AccessKey: "benchmark-access", SecretKey: "benchmark-secret", Signature: "s3v4", Region: "us-east-1"}
							call := func() *probe.Error {
								c := config
								if admin {
									_, e := ad(&c)
									return e
								}
								_, e := s3(&c)
								return e
							}
							if err := call(); err != nil {
								b.Fatal(err)
							}
							b.ReportAllocs()
							b.ResetTimer()
							if parallel {
								b.RunParallel(func(pb *testing.PB) {
									for pb.Next() {
										if err := call(); err != nil {
											b.Error(err)
										}
									}
								})
							} else {
								for b.Loop() {
									if err := call(); err != nil {
										b.Fatal(err)
									}
								}
							}
						})
					}
				})
			}
		})
	}
}
