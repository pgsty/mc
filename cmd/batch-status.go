package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/dustin/go-humanize"
	"github.com/mattn/go-isatty"
	"github.com/minio/cli"
	json "github.com/minio/colorjson"
	"github.com/minio/madmin-go/v3"
	"github.com/minio/mc/pkg/probe"
	"github.com/olekukonko/tablewriter"
)

var batchStatusCmd = cli.Command{
	Name:            "status",
	Usage:           "summarize job events on Silo/MinIO server in real-time",
	Action:          mainBatchStatus,
	OnUsageError:    onUsageError,
	Before:          setGlobalsFromContext,
	Flags:           globalFlags,
	HideHelpCommand: true,
	CustomHelpTemplate: `NAME:
  {{.HelpName}} - {{.Usage}}

USAGE:
  {{.HelpName}} TARGET JOBID

DESCRIPTION:
  Watch an existing job until it completes or fails. Missing live metrics are
  reported as pending. After 30 seconds without job metrics, or if the stream
  ends, check the saved status and report an error if no terminal result exists.
  Individual status requests time out after 10 seconds.

  A retrieved complete or failed result exits 0; inspect "status" for the job
  outcome. An unavailable or missing job status exits nonzero. Retry this
  status command to resume observation of a queued job.

FLAGS:
  {{range .VisibleFlags}}{{.}}
  {{end}}
EXAMPLES:
   1. Display current in-progress JOB events.
      {{.Prompt}} {{.HelpName}} mysilo/ KwSysDpxcBU9FNhGkn2dCf
`,
}

// batchJobStatusMessage container for batch job status messages
type batchJobStatusMessage struct {
	Status string           `json:"status"`
	Metric madmin.JobMetric `json:"metric"`
}

// JSON jsonified batchJobStatusMessage message
func (c batchJobStatusMessage) JSON() string {
	batchJobStatusMessageBytes, e := json.MarshalIndent(c, "", " ")
	fatalIf(probe.NewError(e), "Unable to marshal into JSON.")

	return string(batchJobStatusMessageBytes)
}

func (c batchJobStatusMessage) String() string {
	if c.Status == "pending" {
		return fmt.Sprintf("Job %s: pending (waiting for live metrics; the job may be queued)", c.Metric.JobID)
	}
	ui := initBatchJobMetricsUI(c.Metric.JobID)
	ui.metric, ui.status, ui.quitting = c.Metric, c.Status, true
	return ui.View()
}

// checkBatchStatusSyntax - validate all the passed arguments
func checkBatchStatusSyntax(ctx *cli.Context) {
	if len(ctx.Args()) != 2 {
		showCommandHelpAndExit(ctx, 1) // last argument is exit code
	}
}

func mainBatchStatus(ctx *cli.Context) error {
	checkBatchStatusSyntax(ctx)

	aliasedURL := ctx.Args().Get(0)
	jobID := ctx.Args().Get(1)

	// Create a new MinIO Admin Client
	client, err := newAdminClient(aliasedURL)
	fatalIf(err.Trace(aliasedURL), "Unable to initialize admin client.")

	ctxt, cancel := context.WithCancel(globalContext)
	defer cancel()

	useUI := !globalJSON && !globalQuiet && isTerminal() && isatty.IsTerminal(os.Stdin.Fd())
	if !useUI {
		e := watchBatchJobStatus(ctxt, client, jobID, batchStatusIdleTimeout, func(msg batchJobStatusMessage) {
			printMsg(msg)
		})
		fatalIf(probe.NewError(e).Trace(ctx.Args()...), "Unable to get current batch status")
		return nil
	}

	model := initBatchJobMetricsUI(jobID)
	ui := tea.NewProgram(model, tea.WithContext(ctxt), tea.WithoutSignalHandler())
	result := make(chan error, 1)
	go func() {
		e := watchBatchJobStatus(ctxt, client, jobID, batchStatusIdleTimeout, func(msg batchJobStatusMessage) {
			ui.Send(msg)
		})
		result <- e
		ui.Send(batchJobStatusDoneMsg{})
	}()
	_, e := ui.Run()
	cancel()
	statusErr := <-result
	if model.canceled {
		return cli.NewExitError("Batch status observation canceled", globalCancelExitStatus)
	}
	fatalIf(probe.NewError(e).Trace(aliasedURL), "Unable to display batch status")
	fatalIf(probe.NewError(statusErr).Trace(ctx.Args()...), "Unable to get current batch status")
	return nil
}

const (
	batchStatusRequestTimeout = 10 * time.Second
	batchStatusIdleTimeout    = 30 * time.Second
)

type batchStatusClient interface {
	DescribeBatchJob(context.Context, string) (string, error)
	BatchJobStatus(context.Context, string) (madmin.BatchJobStatus, error)
	Metrics(context.Context, madmin.MetricsOptions, func(madmin.RealtimeMetrics)) error
}

func batchStatusMessage(job madmin.JobMetric) batchJobStatusMessage {
	status := "in-progress"
	if job.Failed {
		status = "failed"
	} else if job.Complete {
		status = "complete"
	}
	return batchJobStatusMessage{Status: status, Metric: job}
}

func describeBatchStatus(ctx context.Context, client batchStatusClient, jobID string) error {
	ctx, cancel := context.WithTimeout(ctx, batchStatusRequestTimeout)
	defer cancel()
	_, err := client.DescribeBatchJob(ctx, jobID)
	return err
}

// watchBatchJobStatus observes only existing jobs. Missing telemetry is not a
// terminal job state; a bounded gap is resolved against the persisted status.
func watchBatchJobStatus(ctx context.Context, client batchStatusClient, jobID string, idleTimeout time.Duration, emit func(batchJobStatusMessage)) error {
	err := describeBatchStatus(ctx, client, jobID)
	if madmin.ToErrorResponse(err).Code == "XMinioAdminNoSuchJob" {
		return batchStatusFallback(ctx, client, jobID, true, errors.New("no active job definition"), emit)
	}
	if err != nil {
		return fmt.Errorf("unable to describe job %q: %w", jobID, err)
	}

	streamCtx, cancel := context.WithCancel(ctx)
	samples := make(chan madmin.RealtimeMetrics)
	result := make(chan error, 1)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		result <- client.Metrics(streamCtx, madmin.MetricsOptions{
			Type:     madmin.MetricsBatchJobs,
			ByJobID:  jobID,
			Interval: time.Second,
		}, func(sample madmin.RealtimeMetrics) {
			select {
			case samples <- sample:
			case <-streamCtx.Done():
			}
		})
	}()
	defer func() {
		cancel()
		<-stopped
	}()

	timer := time.NewTimer(idleTimeout)
	defer timer.Stop()
	pending := false
	var cause error
observe:
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			cause = fmt.Errorf("no live metrics for job %q for %s", jobID, idleTimeout)
			break observe
		case err := <-result:
			if err == nil {
				err = errors.New("metrics stream ended before a terminal job status")
			} else {
				err = fmt.Errorf("live metrics stream failed: %w", err)
			}
			cause = err
			break observe
		case sample := <-samples:
			job, found := madmin.JobMetric{}, false
			if sample.Aggregated.BatchJobs != nil {
				job, found = sample.Aggregated.BatchJobs.Jobs[jobID]
			}
			if found {
				if job.JobID != jobID {
					cause = fmt.Errorf("live metrics returned job ID %q instead of %q", job.JobID, jobID)
					break observe
				}
				emit(batchStatusMessage(job))
				if job.Complete || job.Failed {
					return nil
				}
				pending = false
				timer.Reset(idleTimeout)
			} else if !pending {
				emit(batchJobStatusMessage{Status: "pending", Metric: madmin.JobMetric{JobID: jobID}})
				pending = true
			}
			if len(sample.Errors) > 0 {
				cause = fmt.Errorf("batch metrics unavailable: %s", strings.Join(sample.Errors, "; "))
				break observe
			}
			if sample.Final {
				cause = errors.New("metrics stream ended before a terminal job status")
				break observe
			}
		}
	}
	cancel()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return batchStatusFallback(ctx, client, jobID, false, cause, emit)
}

func batchStatusFallback(ctx context.Context, client batchStatusClient, jobID string, missingDefinition bool, cause error, emit func(batchJobStatusMessage)) error {
	statusCtx, cancel := context.WithTimeout(ctx, batchStatusRequestTimeout)
	res, err := client.BatchJobStatus(statusCtx, jobID)
	cancel()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		job := res.LastMetric
		if job.JobID != jobID {
			return fmt.Errorf("job %q status unavailable: %w; persisted status returned job ID %q", jobID, cause, job.JobID)
		}
		emit(batchStatusMessage(job))
		if job.Complete || job.Failed {
			return nil
		}
		return fmt.Errorf("job %q status unavailable: %w; persisted status is still in-progress, retry batch status", jobID, cause)
	}
	if madmin.ToErrorResponse(err).Code == "XMinioAdminNoSuchJob" {
		if !missingDefinition {
			describeErr := describeBatchStatus(ctx, client, jobID)
			missingDefinition = madmin.ToErrorResponse(describeErr).Code == "XMinioAdminNoSuchJob"
			if describeErr != nil && !missingDefinition {
				return fmt.Errorf("job %q status unavailable: %w", jobID, errors.Join(cause, err, describeErr))
			}
		}
		if missingDefinition {
			return fmt.Errorf("job %q is missing or expired: %w", jobID, errors.Join(cause, err))
		}
		return fmt.Errorf("job %q exists but its status is unavailable (it may be queued): %w; retry batch status", jobID, cause)
	}
	return fmt.Errorf("job %q persisted status unavailable: %w", jobID, errors.Join(cause, err))
}

type batchJobStatusDoneMsg struct{}

func initBatchJobMetricsUI(jobID string) *batchJobMetricsUI {
	s := spinner.New()
	s.Spinner = spinner.Points
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))
	return &batchJobMetricsUI{
		spinner: s,
		jobID:   jobID,
		status:  "pending",
	}
}

type batchJobMetricsUI struct {
	metric   madmin.JobMetric
	spinner  spinner.Model
	quitting bool
	canceled bool
	jobID    string
	status   string
}

func (m *batchJobMetricsUI) Init() tea.Cmd {
	return m.spinner.Tick
}

func (m *batchJobMetricsUI) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			m.quitting = true
			m.canceled = true
			return m, tea.Quit
		default:
			return m, nil
		}
	case batchJobStatusMessage:
		m.status = msg.Status
		if msg.Status == "pending" {
			return m, nil
		}
		return m.Update(msg.Metric)
	case batchJobStatusDoneMsg:
		m.quitting = true
		return m, tea.Quit
	case madmin.JobMetric:
		m.metric = msg
		m.status = batchStatusMessage(msg).Status
		if msg.Complete || msg.Failed {
			m.quitting = true
			return m, tea.Quit
		}
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	default:
		return m, nil
	}
}

func (m *batchJobMetricsUI) View() string {
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
		if m.metric.Complete {
			s.WriteString(m.spinner.Style.Render((tickCell + tickCell + tickCell)))
		} else if m.metric.Failed {
			s.WriteString(m.spinner.Style.Render((crossTickCell + crossTickCell + crossTickCell)))
		}
	}
	s.WriteString("\n")

	addLine("JobID: ", m.jobID)
	addLine("Status: ", m.status)
	if m.status == "pending" {
		addLine("Waiting: ", "live metrics unavailable; the job may be queued")
	}
	switch {
	case m.metric.JobType == string(madmin.BatchJobReplicate) && m.metric.Replicate != nil:
		accElapsedTime := m.metric.LastUpdate.Sub(m.metric.StartTime)

		addLine("JobType: ", m.metric.JobType)
		addLine("Objects: ", m.metric.Replicate.Objects)
		addLine("Versions: ", m.metric.Replicate.Objects)
		addLine("FailedObjects: ", m.metric.Replicate.ObjectsFailed)
		addLine("DeleteMarker: ", m.metric.Replicate.DeleteMarkers)
		addLine("FailedDeleteMarker: ", m.metric.Replicate.DeleteMarkersFailed)
		if accElapsedTime > 0 {
			bytesTransferredPerSec := float64(m.metric.Replicate.BytesTransferred) / accElapsedTime.Seconds()
			objectsPerSec := float64(m.metric.Replicate.Objects) / accElapsedTime.Seconds()
			addLine("Throughput: ", fmt.Sprintf("%s/s", humanize.IBytes(uint64(bytesTransferredPerSec))))
			addLine("IOPs: ", fmt.Sprintf("%.2f objs/s", objectsPerSec))
		}
		addLine("Transferred: ", humanize.IBytes(uint64(m.metric.Replicate.BytesTransferred)))
		addLine("Elapsed: ", accElapsedTime.Round(time.Second).String())
		addLine("CurrObjName: ", m.metric.Replicate.Object)
	case m.metric.JobType == string(madmin.BatchJobExpire) && m.metric.Expired != nil:
		addLine("JobType: ", m.metric.JobType)
		addLine("Objects: ", m.metric.Expired.Objects)
		addLine("FailedObjects: ", m.metric.Expired.ObjectsFailed)
		addLine("DeleteMarker: ", m.metric.Expired.DeleteMarkers)
		addLine("FailedDeleteMarker: ", m.metric.Expired.DeleteMarkersFailed)
		addLine("CurrObjName: ", m.metric.Expired.Object)

		if !m.metric.LastUpdate.IsZero() {
			accElapsedTime := m.metric.LastUpdate.Sub(m.metric.StartTime)
			addLine("Elapsed: ", accElapsedTime.String())
		}
	case m.metric.JobType == string(madmin.BatchJobCatalog) && m.metric.Catalog != nil:
		addLine("JobType: ", m.metric.JobType)
		addLine("ObjectsScannedCount: ", m.metric.Catalog.ObjectsScannedCount)
		addLine("ObjectsMatchedCount: ", m.metric.Catalog.ObjectsMatchedCount)
		lastScanned := fmt.Sprintf("%s/%s", m.metric.Catalog.LastBucketScanned, m.metric.Catalog.LastObjectScanned)
		addLine("LastScanned: ", lastScanned)
		lastMatched := fmt.Sprintf("%s/%s", m.metric.Catalog.LastBucketMatched, m.metric.Catalog.LastObjectMatched)
		addLine("LastMatched: ", lastMatched)
		accElapsedTime := m.metric.LastUpdate.Sub(m.metric.StartTime)
		addLine("RecordsWrittenCount: ", m.metric.Catalog.RecordsWrittenCount)
		addLine("OutputObjectsCount: ", m.metric.Catalog.OutputObjectsCount)
		addLine("Elapsed: ", accElapsedTime.Round(time.Second).String())
		addLine("Scan Speed: ", fmt.Sprintf("%f objects/s", float64(m.metric.Catalog.ObjectsScannedCount)/accElapsedTime.Seconds()))
		if m.metric.Catalog.ErrorMsg != "" {
			addLine("Error: ", m.metric.Catalog.ErrorMsg)
		}
	}

	table.AppendBulk(data)
	table.Render()

	if m.quitting {
		s.WriteString("\n")
	}
	return s.String()
}
