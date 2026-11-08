package cmd

import (
	"bytes"
	"context"
	stdjson "encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

  A retrieved complete, complete-with-skips, or failed result exits 0; inspect
  "status" and the skip counters for the job outcome. A paused job is waiting
  for server retry and is not terminal. Details are read from live metrics
  when available; otherwise saved details are refreshed every five seconds
  and at the terminal result. Unconfirmed terminal details are reported
  explicitly. An unavailable or missing job status exits nonzero.
  Retry this status command to resume observation of a queued or paused job.

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
	Status             string              `json:"status"`
	Metric             madmin.JobMetric    `json:"metric"`
	Details            *batchStatusDetails `json:"details,omitempty"`
	DetailsUnavailable string              `json:"detailsUnavailable,omitempty"`
}

// batchStatusDetails is the additive part of SILO's persisted batch status
// response. It is kept separate from madmin.JobMetric so the client remains
// compatible with older madmin-go and upstream servers.
type batchStatusDetails struct {
	VersionsSkippedLocked int64  `json:"versionsSkippedLocked,omitempty"`
	KeysRefused           int64  `json:"keysRefused,omitempty"`
	FailureReason         string `json:"failureReason,omitempty"`
	PausedReason          string `json:"pausedReason,omitempty"`
}

func (d batchStatusDetails) empty() bool {
	return d.VersionsSkippedLocked == 0 && d.KeysRefused == 0 &&
		d.FailureReason == "" && d.PausedReason == ""
}

type batchStatusMetric struct {
	madmin.JobMetric
	batchStatusDetails
	detailsPresent bool
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
	ui.metric, ui.status, ui.details, ui.quitting = c.Metric, c.Status, c.Details, true
	ui.detailsUnavailable = c.DetailsUnavailable
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

// batchStatusDetailsClient is implemented by madmin.AdminClient through its
// exported custom-request method. Keeping this optional lets tests and older
// client implementations use the original BatchJobStatus interface.
type batchStatusDetailsClient interface {
	ExecuteMethod(context.Context, string, madmin.RequestData) (*http.Response, error)
}

const batchStatusDetailsPollInterval = 5 * time.Second

func batchStatusLabel(job madmin.JobMetric, details *batchStatusDetails) string {
	if job.Failed {
		return "failed"
	}
	if job.Complete {
		if details != nil && (details.VersionsSkippedLocked != 0 || details.KeysRefused != 0) {
			return "complete-with-skips"
		}
		return "complete"
	}
	if details != nil && details.PausedReason != "" {
		return "paused"
	}
	return "in-progress"
}

func batchStatusMessage(job madmin.JobMetric) batchJobStatusMessage {
	return batchJobStatusMessage{Status: batchStatusLabel(job, nil), Metric: job}
}

func batchStatusMessageWithDetails(job madmin.JobMetric, details batchStatusDetails) batchJobStatusMessage {
	var ptr *batchStatusDetails
	if !details.empty() {
		ptr = &details
	}
	return batchJobStatusMessage{
		Status:  batchStatusLabel(job, ptr),
		Metric:  job,
		Details: ptr,
	}
}

func describeBatchStatus(ctx context.Context, client batchStatusClient, jobID string) error {
	ctx, cancel := context.WithTimeout(ctx, batchStatusRequestTimeout)
	defer cancel()
	_, err := client.DescribeBatchJob(ctx, jobID)
	return err
}

// loadBatchStatus reads the additive SILO response when the concrete admin
// client supports custom requests. For other clients it uses the standard
// madmin response, which simply has no details.
func loadBatchStatus(ctx context.Context, client batchStatusClient, jobID string) (madmin.JobMetric, *batchStatusDetails, error) {
	if detailsClient, ok := client.(batchStatusDetailsClient); ok {
		values := make(url.Values)
		values.Set("jobId", jobID)
		resp, err := detailsClient.ExecuteMethod(ctx, http.MethodGet, madmin.RequestData{
			QueryValues: values,
			RelPath:     "/v3/status-job",
		})
		if err != nil {
			return madmin.JobMetric{}, nil, err
		}
		if resp == nil || resp.Body == nil {
			return madmin.JobMetric{}, nil, errors.New("empty batch status response")
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
		if err != nil {
			return madmin.JobMetric{}, nil, err
		}
		if len(body) > 1<<20 {
			return madmin.JobMetric{}, nil, errors.New("batch status response exceeds 1 MiB")
		}
		if resp.StatusCode != http.StatusOK {
			var apiErr madmin.ErrorResponse
			if err := stdjson.Unmarshal(body, &apiErr); err != nil {
				_ = xml.Unmarshal(body, &apiErr)
			}
			if apiErr.Code == "" {
				apiErr.Code = resp.Status
			}
			if apiErr.Message == "" {
				apiErr.Message = resp.Status
			}
			return madmin.JobMetric{}, nil, apiErr
		}
		var status struct {
			LastMetric batchStatusMetric `json:"LastMetric"`
		}
		decoder := stdjson.NewDecoder(bytes.NewReader(body))
		if err := decoder.Decode(&status); err != nil {
			return madmin.JobMetric{}, nil, err
		}
		if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
			return madmin.JobMetric{}, nil, errors.New("unexpected data after batch status response")
		}
		details := status.LastMetric.batchStatusDetails
		if details.empty() {
			return status.LastMetric.JobMetric, nil, nil
		}
		return status.LastMetric.JobMetric, &details, nil
	}

	res, err := client.BatchJobStatus(ctx, jobID)
	return res.LastMetric, nil, err
}

// A metric and its extra fields form one snapshot. Never attach an earlier
// pause or partial counts to newer live progress, or replace a live terminal
// result with a saved nonterminal result.
//
// The server publishes the live metric from a copy it takes before it stamps
// and persists the job, so the live timestamp never exceeds the saved one and
// usually lags it until the job makes further progress, while the live
// counters are the ones that move. While the job runs, the live sample is
// therefore the progress and the saved status only contributes its details,
// such as a pause reason. A saved terminal snapshot is always reported as
// saved, with its details.
func batchStatusSnapshotMessage(live, persisted madmin.JobMetric, details *batchStatusDetails) (batchJobStatusMessage, bool) {
	if persisted.JobID == "" || live.JobID != "" && persisted.JobID != live.JobID || persisted.LastUpdate.Before(live.LastUpdate) || persisted.Complete && persisted.Failed {
		return batchStatusMessage(live), false
	}
	if (live.Complete || live.Failed) && (persisted.Complete != live.Complete || persisted.Failed != live.Failed) {
		return batchStatusMessage(live), false
	}
	metric := persisted
	if live.JobID != "" && !persisted.Complete && !persisted.Failed {
		metric = live
	}
	return batchStatusMessageWithDetails(metric, detailsValue(details)), true
}

// newestBatchMetric returns the metric with the later timestamp, or a when
// they tie.
func newestBatchMetric(a, b madmin.JobMetric) madmin.JobMetric {
	if b.LastUpdate.After(a.LastUpdate) {
		return b
	}
	return a
}

func terminalBatchStatusMessage(ctx context.Context, client batchStatusClient, jobID string, live madmin.JobMetric) batchJobStatusMessage {
	requestCtx, cancel := context.WithTimeout(ctx, batchStatusRequestTimeout)
	persisted, details, err := loadBatchStatus(requestCtx, client, jobID)
	cancel()
	msg, valid := batchStatusSnapshotMessage(live, persisted, details)
	if err != nil || !valid {
		msg = batchStatusMessage(live)
		msg.DetailsUnavailable = "saved details unavailable; skip counts and reasons could not be confirmed"
	}
	return msg
}

type batchStatusDetailsResult struct {
	metric  madmin.JobMetric
	details *batchStatusDetails
	err     error
}

// pollBatchStatusDetails refreshes fields that are intentionally absent from
// madmin's realtime metric type. It is best effort: a live stream remains the
// source of truth when a compatible status endpoint is unavailable.
func pollBatchStatusDetails(ctx context.Context, client batchStatusClient, jobID string) (<-chan batchStatusDetailsResult, <-chan struct{}) {
	if _, ok := client.(batchStatusDetailsClient); !ok {
		return nil, nil
	}
	results := make(chan batchStatusDetailsResult, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(results)
		ticker := time.NewTicker(batchStatusDetailsPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				requestCtx, cancel := context.WithTimeout(ctx, batchStatusRequestTimeout)
				metric, details, err := loadBatchStatus(requestCtx, client, jobID)
				cancel()
				select {
				case results <- batchStatusDetailsResult{metric: metric, details: details, err: err}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return results, done
}

// watchBatchJobStatus observes only existing jobs. Missing telemetry is not a
// terminal job state; a bounded gap is resolved against the persisted status.
func watchBatchJobStatus(ctx context.Context, client batchStatusClient, jobID string, idleTimeout time.Duration, emit func(batchJobStatusMessage)) error {
	err := describeBatchStatus(ctx, client, jobID)
	if madmin.ToErrorResponse(err).Code == "XMinioAdminNoSuchJob" {
		return batchStatusFallback(ctx, client, jobID, madmin.JobMetric{}, true, errors.New("no active job definition"), emit)
	}
	if err != nil {
		return fmt.Errorf("unable to describe job %q: %w", jobID, err)
	}

	streamCtx, cancel := context.WithCancel(ctx)
	samples := make(chan batchRealtimeMetrics)
	result := make(chan error, 1)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		result <- readBatchLiveMetrics(streamCtx, client, jobID, func(sample batchRealtimeMetrics) {
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
	detailCtx, cancelDetails := context.WithCancel(streamCtx)
	defer cancelDetails()
	detailResults, detailDone := pollBatchStatusDetails(detailCtx, client, jobID)
	if detailDone != nil {
		defer func() {
			cancel()
			<-detailDone
		}()
	}

	timer := time.NewTimer(idleTimeout)
	defer timer.Stop()
	pending := false
	var liveMetric madmin.JobMetric
	var liveDetails bool
	var saved batchStatusDetailsResult
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
		case detail, ok := <-detailResults:
			if !ok {
				detailResults = nil
				continue
			}
			if liveDetails || detail.err != nil || detail.metric.JobID != jobID {
				continue
			}
			msg, valid := batchStatusSnapshotMessage(liveMetric, detail.metric, detail.details)
			if !valid {
				continue
			}
			previous := saved
			saved = detail
			// The saved snapshot does not advance the live floor: the live
			// stream is stamped before the job is saved, so its samples
			// legitimately carry an older timestamp than the saved status,
			// and they must keep flowing (and keep the idle timer reset)
			// after a poll. A saved terminal result is final whether or not
			// it carries details.
			if msg.Metric.Complete || msg.Metric.Failed {
				emit(msg)
				return nil
			}
			if detail.details == nil && previous.details == nil {
				continue
			}
			emit(msg)
		case sample := <-samples:
			metric, found := batchStatusMetric{}, false
			if sample.Aggregated.BatchJobs != nil {
				metric, found = sample.Aggregated.BatchJobs.Jobs[jobID]
			}
			job := metric.JobMetric
			if found && job.JobID != jobID {
				cause = fmt.Errorf("live metrics returned job ID %q instead of %q", job.JobID, jobID)
				break observe
			}
			if found && job.Complete && job.Failed {
				cause = errors.New("live metrics report both complete and failed")
				break observe
			}
			// A delayed sample must not undo progress already observed on the
			// live stream, especially by advertising an older terminal state.
			// Only live samples set this floor; see batchStatusSnapshotMessage
			// for why the saved status cannot.
			if found && !job.LastUpdate.Before(liveMetric.LastUpdate) {
				liveMetric = job
				liveDetails = metric.detailsPresent
				msg, _ := batchStatusSnapshotMessage(job, saved.metric, saved.details)
				if liveDetails {
					msg = batchStatusMessageWithDetails(job, metric.batchStatusDetails)
					cancelDetails()
				} else if job.Complete || job.Failed {
					msg = terminalBatchStatusMessage(streamCtx, client, jobID, job)
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				emit(msg)
				if msg.Metric.Complete || msg.Metric.Failed {
					return nil
				}
				pending = false
				timer.Reset(idleTimeout)
			} else if !found && !pending {
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
	// The saved status read now must not be older than anything already
	// observed, from the live stream or from an earlier poll.
	return batchStatusFallback(ctx, client, jobID, newestBatchMetric(liveMetric, saved.metric), false, cause, emit)
}

func batchStatusFallback(ctx context.Context, client batchStatusClient, jobID string, observed madmin.JobMetric, missingDefinition bool, cause error, emit func(batchJobStatusMessage)) error {
	statusCtx, cancel := context.WithTimeout(ctx, batchStatusRequestTimeout)
	job, details, err := loadBatchStatus(statusCtx, client, jobID)
	cancel()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		if job.JobID != jobID {
			return fmt.Errorf("job %q status unavailable: %w; persisted status returned job ID %q", jobID, cause, job.JobID)
		}
		if job.LastUpdate.Before(observed.LastUpdate) {
			return fmt.Errorf("job %q status unavailable: %w; persisted status is older than observed progress, retry batch status", jobID, cause)
		}
		if job.Complete && job.Failed {
			return fmt.Errorf("job %q status unavailable: %w; persisted status reports both complete and failed", jobID, cause)
		}
		emit(batchStatusMessageWithDetails(job, detailsValue(details)))
		if job.Complete || job.Failed {
			return nil
		}
		return fmt.Errorf("job %q status unavailable: %w; persisted status is still %s, retry batch status", jobID, cause, batchStatusLabel(job, details))
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

func detailsValue(details *batchStatusDetails) batchStatusDetails {
	if details == nil {
		return batchStatusDetails{}
	}
	return *details
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
	metric             madmin.JobMetric
	details            *batchStatusDetails
	spinner            spinner.Model
	quitting           bool
	canceled           bool
	jobID              string
	status             string
	detailsUnavailable string
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
		m.details = msg.Details
		m.detailsUnavailable = msg.DetailsUnavailable
		if msg.Status == "pending" {
			return m, nil
		}
		m.metric = msg.Metric
		if msg.Metric.Complete || msg.Metric.Failed {
			m.quitting = true
			return m, tea.Quit
		}
		return m, nil
	case batchJobStatusDoneMsg:
		m.quitting = true
		return m, tea.Quit
	case madmin.JobMetric:
		m.metric = msg
		m.details = nil
		m.detailsUnavailable = ""
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
	if m.detailsUnavailable != "" {
		addLine("Details: ", m.detailsUnavailable)
	}
	if m.details != nil {
		if m.details.VersionsSkippedLocked != 0 {
			addLine("VersionsSkippedLocked: ", m.details.VersionsSkippedLocked)
		}
		if m.details.KeysRefused != 0 {
			addLine("KeysRefused: ", m.details.KeysRefused)
		}
		if m.details.PausedReason != "" {
			addLine("PausedReason: ", m.details.PausedReason)
		}
		if m.details.FailureReason != "" {
			addLine("FailureReason: ", m.details.FailureReason)
		}
	}
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
