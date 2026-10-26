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
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program. If not, see <http://www.gnu.org/licenses/>.

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/minio/madmin-go/v3"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func TestResyncMetricsUITerminalStatus(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		terminal     bool
		marker       string
		failedCount  int64
	}{
		{status: "Completed", terminal: true, marker: tickCell},
		{status: "Canceled", terminal: true},
		{status: "Failed", terminal: true, marker: crossTickCell},
		{status: "completed", terminal: true, marker: tickCell},
		{status: "cAnCeLeD", terminal: true},
		{status: "FAILED", terminal: true, marker: crossTickCell},
		{name: "completed_with_failed_objects", status: "Completed", terminal: true, marker: crossTickCell, failedCount: 1},
		{status: "Pending"},
		{status: "Ongoing"},
		{status: "Unknown"},
	} {
		name := tc.name
		if name == "" {
			name = tc.status
		}
		t.Run(name, func(t *testing.T) {
			ui := initResyncMetricsUI("peer-site")
			sample := resyncJSONTestSample(tc.status, 0).Aggregated.SiteResync
			// A site can fail without a nonzero object failure counter.
			sample.FailedCount = tc.failedCount
			_, cmd := ui.Update(sample)
			if ui.quitting != tc.terminal {
				t.Errorf("quitting=%t, want %t", ui.quitting, tc.terminal)
			}
			if tc.terminal {
				if cmd == nil {
					t.Error("terminal status did not return a quit command")
				} else if _, ok := cmd().(tea.QuitMsg); !ok {
					t.Error("terminal status did not request a graceful quit")
				}
			} else if cmd != nil {
				t.Error("nonterminal status must keep watching")
			}
			view := reAnsi.ReplaceAllString(ui.View(), "")
			if !strings.Contains(view, tc.status) || !strings.Contains(view, sample.ResyncID) {
				t.Errorf("lost final status: %q", view)
			}
			if tc.marker != "" && !strings.Contains(view, tc.marker) {
				t.Errorf("missing %q marker: %q", tc.marker, view)
			}
			if tc.marker != tickCell && strings.Contains(view, tickCell) {
				t.Errorf("unsuccessful resync rendered as success: %q", view)
			}
			if tc.terminal && strings.Contains(view, reAnsi.ReplaceAllString(ui.spinner.View(), "")) {
				t.Errorf("spinner remains after terminal status: %q", view)
			}
		})
	}
}

func TestResyncMetricsUICtrlC(t *testing.T) {
	ui := initResyncMetricsUI("peer-site")
	ui.current = *resyncJSONTestSample("Ongoing", 0).Aggregated.SiteResync
	_, cmd := ui.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !ui.quitting || cmd == nil {
		t.Fatal("Ctrl+C must stop the UI")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("Ctrl+C must request a graceful quit")
	}
	view := reAnsi.ReplaceAllString(ui.View(), "")
	if !strings.Contains(view, "Ongoing") || strings.Contains(view, tickCell) {
		t.Errorf("interruption lost or misrepresented the last status: %q", view)
	}
}

func TestResyncMetricsUIDone(t *testing.T) {
	ui := initResyncMetricsUI("peer-site")
	sample := resyncJSONTestSample("Ongoing", 0).Aggregated.SiteResync
	_, _ = ui.Update(sample)
	_, cmd := ui.Update(resyncMetricsDoneMsg{})
	if !ui.quitting || cmd == nil {
		t.Fatal("end of stream must stop the UI")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("end of stream must request a graceful quit")
	}
	if !strings.Contains(ui.View(), "Ongoing") || strings.Contains(ui.View(), tickCell) {
		t.Errorf("end of stream lost or misrepresented the last status: %q", ui.View())
	}
	// Queued spinner ticks and samples must not restart or change the final UI.
	for _, msg := range []tea.Msg{ui.spinner.Tick(), resyncJSONTestSample("Completed", 1).Aggregated.SiteResync} {
		if _, cmd := ui.Update(msg); cmd != nil {
			t.Fatal("quitting UI scheduled more work")
		}
	}
	if ui.current.ResyncStatus != "Ongoing" {
		t.Fatal("quitting UI accepted a late sample")
	}
}

func TestResyncMetricsUICancellation(t *testing.T) {
	for _, cancelUI := range []bool{false, true} {
		name := "query_context"
		if cancelUI {
			name = "ui_context"
		}
		t.Run(name, func(t *testing.T) {
			delivered := make(chan struct{})
			disconnected := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(resyncJSONTestSample("Ongoing", 0))
				w.(http.Flusher).Flush()
				close(delivered)
				select {
				case <-r.Context().Done():
					close(disconnected)
				case <-t.Context().Done():
				}
			}))
			defer server.Close()
			client, err := madmin.NewWithOptions(strings.TrimPrefix(server.URL, "http://"), &madmin.Options{
				Creds: credentials.NewStaticV4("access", "secret", ""),
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			uiCtx, cancelProgram := context.WithCancel(t.Context())
			defer cancelProgram()
			ui := tea.NewProgram(initResyncMetricsUI("peer-site"), tea.WithContext(uiCtx),
				tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutSignalHandler())
			go func() {
				select {
				case <-delivered:
					if cancelUI {
						cancelProgram()
					} else {
						cancel()
					}
				case <-ctx.Done():
				}
			}()
			result := make(chan error, 1)
			go func() {
				result <- runResyncMetricsUI(ctx, client, madmin.MetricsOptions{Type: madmin.MetricsSiteResync}, ui)
			}()
			select {
			case err := <-result:
				if cancelUI {
					if !errors.Is(err, tea.ErrProgramKilled) {
						t.Errorf("UI error was lost: %v", err)
					}
				} else if err != nil {
					t.Errorf("canceled query reported an error: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("UI/metrics worker failed to stop after cancellation")
			}
			select {
			case <-disconnected:
			case <-time.After(5 * time.Second):
				t.Fatal("UI returned without closing its metrics request")
			}
		})
	}
}

func TestAdminReplicateResyncStatusDisabled(t *testing.T) {
	for _, mode := range []string{"text", "json", "leaf_json"} {
		t.Run(mode, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/minio/admin/v3/site-replication/info" {
					t.Errorf("disabled replication requested %s %s", r.Method, r.URL)
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"enabled":false}`)
			}))
			defer server.Close()
			// The peer is deliberately not configured: no lookup is needed.
			args := []string{"admin", "replicate", "resync", "status", "b4", "unused-peer"}
			switch mode {
			case "json":
				args = append([]string{"--json"}, args...)
			case "leaf_json":
				args = append(args, "--json")
			}
			result := runChecksumVerifyCLI(t, server.URL, nil, args...)
			if result.exitCode != 0 || len(result.stderr) != 0 {
				t.Fatalf("disabled query failed: exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
			}
			if requests.Load() != 1 {
				t.Errorf("requests=%d, want only the site info query", requests.Load())
			}
			if mode == "text" {
				if !strings.Contains(string(result.stdout), "SiteReplication is not enabled") {
					t.Errorf("missing disabled explanation: %q", result.stdout)
				}
			} else {
				var resultInfo struct {
					Enabled *bool `json:"enabled"`
				}
				if err := json.Unmarshal(result.stdout, &resultInfo); err != nil {
					t.Fatalf("invalid disabled JSON: %v; stdout=%q", err, result.stdout)
				}
				if resultInfo.Enabled == nil || *resultInfo.Enabled {
					t.Errorf("JSON must explicitly report enabled=false: %s", result.stdout)
				}
			}
		})
	}
}
