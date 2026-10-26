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
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/dustin/go-humanize"
	"github.com/fatih/color"
	"github.com/minio/cli"
	"github.com/minio/madmin-go/v3"
	"github.com/minio/mc/pkg/probe"
	"github.com/olekukonko/tablewriter"
	"github.com/pgsty/silo-pkg/v3/console"
)

var adminReplicateResyncStatusCmd = cli.Command{
	Name:         "status",
	Usage:        "show site replication resync status",
	Action:       mainAdminReplicationResyncStatus,
	OnUsageError: onUsageError,
	Before:       setGlobalsFromContext,
	Flags:        globalFlags,
	CustomHelpTemplate: `NAME:
  {{.HelpName}} - {{.Usage}}

USAGE:
  {{.HelpName}} ALIAS1 ALIAS2

FLAGS:
  {{range .VisibleFlags}}{{.}}
  {{end}}

EXAMPLES:
  1. Display status of resync from silo1 to silo2
     {{.Prompt}} {{.HelpName}} silo1 silo2
`,
}

func mainAdminReplicationResyncStatus(ctx *cli.Context) error {
	// Check argument count
	argsNr := len(ctx.Args())
	if argsNr != 2 {
		showCommandHelpAndExit(ctx, 1) // last argument is exit code
	}

	console.SetColor("UserMessage", color.New(color.FgGreen))
	console.SetColor("THeader", color.New(color.Bold, color.FgHiWhite))
	console.SetColor("THeader2", color.New(color.Bold, color.FgYellow))
	console.SetColor("TDetail", color.New(color.Bold, color.FgCyan))

	// Get the alias parameter from cli
	args := ctx.Args()
	aliasedURL := args.Get(0)

	// Create a new MinIO Admin Client
	client, err := newAdminClient(aliasedURL)
	fatalIf(err, "Unable to initialize admin connection.")
	info, e := client.SiteReplicationInfo(globalContext)
	fatalIf(probe.NewError(e), "Unable to fetch site replication info.")
	if !info.Enabled {
		// Disabled replication is a successful query result, in either format.
		printMsg(srInfo(info))
		return nil
	}

	peerClient := getClient(args.Get(1))
	peerAdmInfo, e := peerClient.ServerInfo(globalContext)
	fatalIf(probe.NewError(e), "Unable to fetch server info of the peer.")

	var peer madmin.PeerInfo
	for _, site := range info.Sites {
		if peerAdmInfo.DeploymentID == site.DeploymentID {
			peer = site
		}
	}
	if peer.DeploymentID == "" {
		fatalIf(errInvalidArgument().Trace(ctx.Args().Tail()...),
			"alias provided is not part of cluster replication.")
	}
	opts := madmin.MetricsOptions{
		Type:    madmin.MetricsSiteResync,
		ByDepID: peer.DeploymentID,
	}
	if globalJSON {
		e = streamResyncMetrics(globalContext, client, opts, func(metrics madmin.RealtimeMetrics) {
			printMsg(metricsMessage{RealtimeMetrics: metrics})
		})
	} else {
		ui := tea.NewProgram(initResyncMetricsUI(peer.DeploymentID))
		e = runResyncMetricsUI(globalContext, client, opts, ui)
	}
	fatalIf(probe.NewError(e).Trace(ctx.Args()...), "Unable to get resync status")
	return nil
}

func resyncStatusTerminal(status string) bool {
	switch strings.ToLower(status) {
	case "completed", "canceled", "failed":
		return true
	default:
		return false
	}
}

func streamResyncMetrics(ctx context.Context, client *madmin.AdminClient, opts madmin.MetricsOptions, out func(madmin.RealtimeMetrics)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	finished := false
	err := client.Metrics(ctx, opts, func(metrics madmin.RealtimeMetrics) {
		// The decoder may have buffered more samples before cancellation.
		if finished || ctx.Err() != nil {
			return
		}
		out(metrics)
		finished = metrics.Final
		if sr := metrics.Aggregated.SiteResync; sr != nil {
			finished = finished || resyncStatusTerminal(sr.ResyncStatus)
		}
		if finished {
			cancel()
		}
	})
	// Once the last event has been delivered, the unused response tail does
	// not belong to this query. Earlier request/stream errors still fail.
	if finished || (errors.Is(err, context.Canceled) && ctx.Err() != nil) {
		return nil
	}
	return err
}

type resyncMetricsDoneMsg struct{}

func runResyncMetricsUI(ctx context.Context, client *madmin.AdminClient, opts madmin.MetricsOptions, ui *tea.Program) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		err := streamResyncMetrics(ctx, client, opts, func(metrics madmin.RealtimeMetrics) {
			if sr := metrics.Aggregated.SiteResync; sr != nil {
				ui.Send(sr)
			}
		})
		// Quit through Update so EOF, errors and cancellation stop the
		// spinner and retain the last frame before restoring the terminal.
		ui.Send(resyncMetricsDoneMsg{})
		done <- err
	}()
	_, uiErr := ui.Run()
	cancel()
	streamErr := <-done
	if uiErr != nil {
		return uiErr
	}
	return streamErr
}

func initResyncMetricsUI(deplID string) *resyncMetricsUI {
	s := spinner.New()
	s.Spinner = spinner.Points
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))
	return &resyncMetricsUI{
		spinner: s,
		deplID:  deplID,
	}
}

type resyncMetricsUI struct {
	current  madmin.SiteResyncMetrics
	spinner  spinner.Model
	quitting bool
	deplID   string
}

func (m *resyncMetricsUI) Init() tea.Cmd {
	return m.spinner.Tick
}

func (m *resyncMetricsUI) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.quitting {
		return m, nil
	}
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		default:
			return m, nil
		}
	case *madmin.SiteResyncMetrics:
		m.current = *msg
		if resyncStatusTerminal(msg.ResyncStatus) {
			m.quitting = true
			return m, tea.Quit
		}
		return m, nil
	case resyncMetricsDoneMsg:
		m.quitting = true
		return m, tea.Quit
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	default:
		return m, nil
	}
}

func (m *resyncMetricsUI) View() string {
	var s strings.Builder

	// Set table header
	table := tablewriter.NewWriter(&s)
	table.SetAutoWrapText(false)
	table.SetAutoFormatHeaders(true)
	table.SetHeaderAlignment(tablewriter.ALIGN_LEFT)
	table.SetAlignment(tablewriter.ALIGN_LEFT)
	table.SetCenterSeparator("")
	table.SetColumnSeparator("")
	table.SetRowSeparator("")
	table.SetHeaderLine(false)
	table.SetBorder(false)
	table.SetTablePadding("\t") // pad with tabs
	table.SetNoWhiteSpace(true)

	var data [][]string
	addLine := func(prefix string, value any) {
		data = append(data, []string{
			prefix,
			whiteStyle.Render(fmt.Sprint(value)),
		})
	}

	if !m.quitting {
		s.WriteString(m.spinner.View())
	} else {
		if strings.EqualFold(m.current.ResyncStatus, "failed") || (m.current.Complete() && m.current.FailedCount != 0) {
			s.WriteString(m.spinner.Style.Render(crossTickCell + crossTickCell + crossTickCell))
		} else if m.current.Complete() {
			s.WriteString(m.spinner.Style.Render(tickCell + tickCell + tickCell))
		}
	}
	s.WriteString("\n")
	if m.current.ResyncID != "" {
		accElapsedTime := m.current.LastUpdate.Sub(m.current.StartTime)
		addLine("ResyncID: ", m.current.ResyncID)
		addLine("Status: ", m.current.ResyncStatus)

		addLine("Objects: ", m.current.ReplicatedCount)
		addLine("Versions: ", m.current.ReplicatedCount)
		addLine("FailedObjects: ", m.current.FailedCount)
		if accElapsedTime > 0 {
			elapsedSeconds := accElapsedTime.Seconds()
			bytesTransferredPerSec := float64(m.current.ReplicatedSize) / elapsedSeconds
			objectsPerSec := float64(m.current.ReplicatedCount) / elapsedSeconds
			addLine("Throughput: ", fmt.Sprintf("%s/s", humanize.IBytes(uint64(bytesTransferredPerSec))))
			addLine("IOPs: ", fmt.Sprintf("%.2f objs/s", objectsPerSec))
		}
		addLine("Transferred: ", humanize.IBytes(uint64(m.current.ReplicatedSize)))
		addLine("Elapsed: ", accElapsedTime.String())
		addLine("CurrObjName: ", fmt.Sprintf("%s/%s", m.current.Bucket, m.current.Object))
	}
	table.AppendBulk(data)
	table.Render()

	if m.quitting {
		s.WriteString("\n")
	}

	return s.String()
}
