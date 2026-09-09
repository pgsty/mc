// Copyright (c) 2026 Pigsty
// SPDX-License-Identifier: AGPL-3.0-or-later

package cmd

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
)

func TestClientTLSKeyExchangeDefaults(t *testing.T) {
	for _, debug := range []string{"tlsmlkem=0", "tlsmlkem=1"} {
		t.Run(debug, func(t *testing.T) {
			t.Setenv("GODEBUG", debug)
			hellos := make(chan []tls.CurveID, 1)
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, "ok")
			}))
			server.TLS = &tls.Config{GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
				select {
				case hellos <- slices.Clone(hello.SupportedCurves):
				default:
				}
				return nil, nil
			}}
			server.StartTLS()
			defer server.Close()
			roots := x509.NewCertPool()
			roots.AddCert(server.Certificate())
			previousRoots := globalRootCAs
			globalRootCAs = roots
			t.Cleanup(func() { globalRootCAs = previousRoots })
			config := &Config{HostURL: server.URL}
			config.initTransport(false)
			aliasTransport := &http.Transport{DialTLSContext: newCustomDialTLSContext(&tls.Config{RootCAs: roots})}
			defer aliasTransport.CloseIdleConnections()
			for name, transport := range map[string]http.RoundTripper{
				"S3-and-admin": config.Transport,
				"alias-dialer": aliasTransport,
			} {
				t.Run(name, func(t *testing.T) {
					client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
					req, err := http.NewRequest(http.MethodGet, server.URL, nil)
					if err != nil {
						t.Fatal(err)
					}
					// The S3 transport is wrapped; close this connection explicitly.
					req.Close = true
					resp, err := client.Do(req)
					if err != nil {
						t.Fatal(err)
					}
					defer resp.Body.Close()
					if resp.StatusCode != http.StatusOK || len(resp.TLS.VerifiedChains) == 0 {
						t.Fatalf("status %d, verified chains %d", resp.StatusCode, len(resp.TLS.VerifiedChains))
					}
					curves := <-hellos
					if got, want := slices.Contains(curves, tls.X25519MLKEM768), debug == "tlsmlkem=1"; got != want {
						t.Errorf("ML-KEM offered = %v, want %v; curves %v", got, want, curves)
					}
				})
			}
		})
	}
}
