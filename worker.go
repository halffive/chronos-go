package chronos

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf16"
)

const (
	claimPath              = "/v1/workers/jobs/claim"
	workerExecutionsPath   = "/v1/workers/executions"
	maxPollWait            = 20 * time.Second
	defaultPollWait        = maxPollWait
	workerRetryDelay       = time.Second
	planLimitPause         = 60 * time.Second
	reportMaxAttempts      = 3
	maxReportedErrorLength = 4096
	maxNameLength          = 255
	maxStuckHandlers       = 10
	defaultHandlerError    = "chronos: handler failed"

	defaultTimedOutHandlerGrace = 30 * time.Second
)

var workerRuntime = "go/" + strings.TrimPrefix(runtime.Version(), "go")

// ClaimedJob is the job a [Worker] passes to a [Handler].
type ClaimedJob struct {
	JobID string `json:"job_id"`
	// ExecutionID identifies this attempt. Each attempt gets a new one, so
	// use JobID as an idempotency key.
	ExecutionID  string    `json:"execution_id"`
	Handler      string    `json:"handler"`
	ScheduledFor time.Time `json:"scheduled_for"`
	// Attempt starts at 1.
	Attempt int `json:"attempt"`
	// Timeout is in seconds.
	Timeout int `json:"timeout"`
	// Payload is the job's payload as sent. For a job without a payload,
	// Chronos sends JSON null, which decodes to json.RawMessage("null"), not
	// nil.
	Payload json.RawMessage `json:"payload"`
	// Schedule is nil for a one-off job.
	Schedule *ScheduleReference `json:"schedule"`
}

// ScheduleReference names the schedule that spawned a [ClaimedJob].
type ScheduleReference struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Handler runs one claimed job. A nil error completes the job and records the
// map as its result, or no result if the map is nil. A non-nil error, a panic
// or a map that cannot be encoded to JSON fails the job, and Chronos retries it
// while attempts remain.
//
// Chronos stores error text, panic values and results and returns them from
// its API, and sends error text to your alert channels. Keep secrets and
// personal data out of them.
//
// ctx ends at the job's timeout and is not cancelled when [Worker.Run] stops,
// so a running job can finish during shutdown. Return when ctx ends. Go cannot
// stop a handler that ignores it, and that handler keeps running and holding
// its memory until it returns, alongside any later jobs.
type Handler func(ctx context.Context, job *ClaimedJob) (map[string]any, error)

// WorkerOption configures a [Worker].
type WorkerOption func(*Worker) error

// WithPollWait sets how long each claim waits for a job, in whole seconds up
// to 20 seconds. 0 claims without waiting, so the worker sends requests
// continuously while the queue is empty. Defaults to 20 seconds.
func WithPollWait(wait time.Duration) WorkerOption {
	return func(w *Worker) error {
		if wait < 0 || wait > maxPollWait || wait%time.Second != 0 {
			return fmt.Errorf("%w: poll wait must be whole seconds from 0 to %s, got %s", ErrInvalidWorker, maxPollWait, wait)
		}
		w.pollWait = wait
		return nil
	}
}

// WithWorkerID sets the id the worker registers under, up to 255 characters.
// Workers sharing an id share one entry in the workers list. Defaults to a
// random id, which is new for each [NewWorker] call, so set a stable id in
// production.
func WithWorkerID(id string) WorkerOption {
	return func(w *Worker) error {
		trimmed := strings.TrimSpace(id)
		if !hasValidNameLength(trimmed) {
			return fmt.Errorf("%w: worker id must be 1 to 255 characters", ErrInvalidWorker)
		}
		w.id = trimmed
		return nil
	}
}

// WithWorkerLogger sets the logger for worker failures, such as a failed claim,
// a handler error or a lost result. Defaults to [slog.Default].
func WithWorkerLogger(logger *slog.Logger) WorkerOption {
	return func(w *Worker) error {
		if logger == nil {
			return fmt.Errorf("%w: logger must not be nil", ErrInvalidWorker)
		}
		w.logger = logger
		return nil
	}
}

// Worker claims pull jobs from the API and runs them one at a time. Create one
// with [NewWorker], register handlers with [Worker.Handle], then call
// [Worker.Run]. It is safe for concurrent use, and Run can be called again
// after it returns.
//
// Claims and result reports ignore [WithMaxRequestRetries]; the worker retries
// them itself. A Timeout on a client passed to [WithHTTPClient] also bounds
// each claim, so set it above the poll wait, or claims fail while waiting.
type Worker struct {
	client   *Client
	pollWait time.Duration
	id       string
	logger   *slog.Logger

	mu           sync.Mutex
	running      bool
	handlers     map[string]Handler
	handlerNames []string
	setupErrs    []error

	stuckHandlers atomic.Int64

	jobTimeoutUnit       time.Duration
	timedOutHandlerGrace time.Duration
}

// NewWorker returns a worker that uses client for its requests. [Worker.Run]
// returns any problem with client or opts.
func NewWorker(client *Client, opts ...WorkerOption) *Worker {
	w := &Worker{
		client:               client,
		pollWait:             defaultPollWait,
		id:                   generateWorkerID(),
		handlers:             map[string]Handler{},
		jobTimeoutUnit:       time.Second,
		timedOutHandlerGrace: defaultTimedOutHandlerGrace,
	}
	if client == nil {
		w.setupErrs = append(w.setupErrs, fmt.Errorf("%w: client must not be nil", ErrInvalidWorker))
	}
	for _, opt := range opts {
		if err := opt(w); err != nil {
			w.setupErrs = append(w.setupErrs, err)
		}
	}
	return w
}

func generateWorkerID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return "w_" + hex.EncodeToString(b)
}

// Handle registers h for jobs whose handler name matches name exactly, after
// trimming whitespace from name. [Worker.Run] returns an error for an invalid
// or duplicate name, or a nil h. A call made while the worker is running is
// logged and ignored.
func (w *Worker) Handle(name string, h Handler) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.running {
		w.log().Error("chronos: handler not registered; the worker is already running", "handler", name)
		return
	}
	trimmed := strings.TrimSpace(name)
	if err := w.checkHandler(trimmed, h); err != nil {
		w.setupErrs = append(w.setupErrs, err)
		return
	}
	w.handlers[trimmed] = h
	w.handlerNames = append(w.handlerNames, trimmed)
}

func (w *Worker) checkHandler(name string, h Handler) error {
	if !hasValidNameLength(name) {
		return fmt.Errorf("%w: handler name must be 1 to 255 characters, got %q", ErrInvalidWorker, name)
	}
	if h == nil {
		return fmt.Errorf("%w: handler %q must not be nil", ErrInvalidWorker, name)
	}
	if _, exists := w.handlers[name]; exists {
		return fmt.Errorf("%w: handler %q is already registered", ErrInvalidWorker, name)
	}
	return nil
}

func hasValidNameLength(name string) bool {
	length := len(utf16.Encode([]rune(name)))
	return length >= 1 && length <= maxNameLength
}

// Run claims and runs jobs until ctx ends, then returns nil. Cancelling ctx
// abandons the claim in flight, lets a running handler finish within its
// timeout, and reports its result. If a handler ignores its timeout, Run waits
// up to 30 more seconds after reporting it, and the handler keeps running after
// Run returns.
//
// Run returns an error before sending any request if the worker is
// misconfigured, which matches [ErrInvalidWorker], or already running. It also
// stops and returns the [*APIError] when the API rejects the key with a 401,
// which matches [ErrUnauthorized], or a 403 with [ErrorCodeInsufficientScope].
// It returns [ErrTooManyStuckHandlers] when 10 handlers have ignored their
// timeouts and are still running. It logs every other failure and keeps going.
func (w *Worker) Run(ctx context.Context) error {
	if err := w.begin(); err != nil {
		return err
	}
	defer w.end()

	startedAt := time.Now()
	for ctx.Err() == nil {
		if w.stuckHandlers.Load() >= maxStuckHandlers {
			w.log().Error("chronos: worker stopped; too many stuck handlers", "stuck_handlers", maxStuckHandlers)
			return ErrTooManyStuckHandlers
		}
		job, err := w.claim(ctx, startedAt)
		if stopsWorker(err) {
			return stopRun(ctx, w.log(), err)
		}
		if err != nil {
			w.pauseAfterClaimError(ctx, err)
			continue
		}
		if job == nil {
			continue
		}
		if err := w.process(ctx, job); err != nil {
			return stopRun(ctx, w.jobLog(job).With("result_recorded", false), err)
		}
	}
	return nil
}

func (w *Worker) begin() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.running {
		return errors.New("chronos: worker is already running")
	}
	errs := slices.Clone(w.setupErrs)
	if len(w.handlerNames) == 0 {
		errs = append(errs, fmt.Errorf("%w: register at least one handler before calling Run", ErrInvalidWorker))
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	w.running = true
	return nil
}

func (w *Worker) end() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.running = false
}

func (w *Worker) log() *slog.Logger {
	if w.logger != nil {
		return w.logger
	}
	return slog.Default()
}

func (w *Worker) jobLog(job *ClaimedJob) *slog.Logger {
	return w.log().With("job_id", job.JobID, "execution_id", job.ExecutionID, "handler", job.Handler)
}

func stopRun(ctx context.Context, log *slog.Logger, err error) error {
	log.Error("chronos: worker stopped; the API rejected the key", "error", err)
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func stopsWorker(err error) bool {
	return errors.Is(err, ErrUnauthorized) || isForbiddenWithCode(err, ErrorCodeInsufficientScope)
}

func isForbiddenWithCode(err error, code ErrorCode) bool {
	apiErr, ok := errors.AsType[*APIError](err)
	return ok && apiErr.StatusCode == http.StatusForbidden && apiErr.Code == code
}

func (w *Worker) pauseAfterClaimError(ctx context.Context, err error) {
	if ctx.Err() != nil {
		return
	}
	if isForbiddenWithCode(err, ErrorCodePlanLimitExceeded) {
		w.log().Error("chronos: claims paused; execution quota used up", "error", err)
		w.client.sleep(ctx, planLimitPause)
		return
	}
	w.log().Error("chronos: claim failed", "error", err)
	w.client.sleep(ctx, workerRetryDelayFor(err))
}

func workerRetryDelayFor(err error) time.Duration {
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.RetryAfter <= 0 {
		return workerRetryDelay
	}
	return min(apiErr.RetryAfter, maxRetryAfter)
}

type claimRequest struct {
	WaitTimeSeconds int            `json:"wait_time_seconds"`
	Handlers        []string       `json:"handlers"`
	Worker          workerIdentity `json:"worker"`
}

type workerIdentity struct {
	ID         string `json:"id"`
	SDKVersion string `json:"sdk_version"`
	Runtime    string `json:"runtime"`
	UptimeMS   int64  `json:"uptime_ms"`
}

func (w *Worker) claim(ctx context.Context, startedAt time.Time) (*ClaimedJob, error) {
	var job *ClaimedJob
	err := w.client.sendOnce(ctx, apiRequest{
		method:        http.MethodPost,
		path:          claimPath,
		allowNullData: true,
		timeout:       w.pollWait + w.client.timeout,
		body: claimRequest{
			WaitTimeSeconds: int(w.pollWait / time.Second),
			Handlers:        w.handlerNames,
			Worker: workerIdentity{
				ID:         w.id,
				SDKVersion: Version,
				Runtime:    workerRuntime,
				UptimeMS:   time.Since(startedAt).Milliseconds(),
			},
		},
	}, &job)
	return job, err
}

type handlerReturn struct {
	result map[string]any
	err    error
}

func (w *Worker) process(ctx context.Context, job *ClaimedJob) error {
	handler, ok := w.handlers[job.Handler]
	if !ok {
		message := fmt.Sprintf("chronos: received a job for unregistered handler %q", job.Handler)
		w.jobLog(job).Error(message)
		return w.report(ctx, job, failedResult(message))
	}

	handlerCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Duration(job.Timeout)*w.jobTimeoutUnit)
	defer cancel()

	returned := make(chan executionResult, 1)
	go w.runHandler(handlerCtx, handler, *job, returned)

	select {
	case result := <-returned:
		if result.Status == ExecutionStatusTimeout {
			w.logTimeout(job)
		}
		return w.report(ctx, job, result)
	case <-handlerCtx.Done():
	}

	w.logTimeout(job)
	err := w.report(ctx, job, executionResult{Status: ExecutionStatusTimeout})
	w.awaitTimedOutHandler(job, returned)
	return err
}

func (w *Worker) runHandler(ctx context.Context, handler Handler, job ClaimedJob, returned chan<- executionResult) {
	log := w.jobLog(&job)
	result := failedResult("chronos: handler exited without returning")
	defer func() {
		_ = recover()
		returned <- result
	}()
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		result = failedResult("chronos: handler panicked")
		message := fmt.Sprintf("chronos: handler panicked: %v", r)
		result = failedResult(message)
		log.Error(message, "stack", string(debug.Stack()))
	}()
	value, err := handler(ctx, &job)
	result = resultOf(ctx, log, handlerReturn{result: value, err: err})
}

func (w *Worker) logTimeout(job *ClaimedJob) {
	w.jobLog(job).Error("chronos: handler timed out", "timeout_seconds", job.Timeout)
}

func (w *Worker) awaitTimedOutHandler(job *ClaimedJob, returned <-chan executionResult) {
	timer := time.NewTimer(w.timedOutHandlerGrace)
	defer timer.Stop()
	select {
	case <-returned:
	case <-timer.C:
		w.jobLog(job).Error("chronos: handler ignored its timeout and is still running")
		w.stuckHandlers.Add(1)
		go func() {
			<-returned
			w.stuckHandlers.Add(-1)
		}()
	}
}

type executionResult struct {
	Status ExecutionStatus `json:"status"`
	Result json.RawMessage `json:"result,omitzero"`
	Error  string          `json:"error,omitzero"`
}

func failedResult(message string) executionResult {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		trimmed = defaultHandlerError
	}
	return executionResult{Status: ExecutionStatusFailed, Error: truncateUTF16(trimmed, maxReportedErrorLength)}
}

func truncateUTF16(s string, limit int) string {
	units := 0
	for i, r := range s {
		units += utf16.RuneLen(r)
		if units > limit {
			return s[:i]
		}
	}
	return s
}

func resultOf(handlerCtx context.Context, log *slog.Logger, ret handlerReturn) executionResult {
	if ret.err != nil && handlerCtx.Err() != nil {
		return executionResult{Status: ExecutionStatusTimeout}
	}
	if ret.err != nil {
		log.Error("chronos: handler failed", "error", ret.err)
		return failedResult(ret.err.Error())
	}
	if ret.result == nil {
		return executionResult{Status: ExecutionStatusCompleted}
	}
	encoded, err := json.Marshal(ret.result)
	if err != nil {
		log.Error("chronos: handler returned an invalid result", "error", err)
		return failedResult("chronos: handler result cannot be encoded to JSON: " + err.Error())
	}
	return executionResult{Status: ExecutionStatusCompleted, Result: encoded}
}

func (w *Worker) report(ctx context.Context, job *ClaimedJob, result executionResult) error {
	log := w.jobLog(job).With("status", result.Status)
	path, err := itemPath(workerExecutionsPath, job.ExecutionID)
	if err != nil {
		log.Error("chronos: result not reported", "error", err)
		return nil
	}

	req := apiRequest{method: http.MethodPost, path: path + "/result", body: result}
	sendCtx := context.WithoutCancel(ctx)
	for attempt := 1; ; attempt++ {
		err := w.client.sendOnce(sendCtx, req, nil)
		if err == nil {
			return nil
		}
		if stopsWorker(err) {
			return err
		}
		if isExecutionConflict(err) {
			log.Warn("chronos: result discarded; execution already finished", "error", err)
			return nil
		}
		if isTerminalReportError(err) {
			log.Error("chronos: result rejected by the API", "error", err)
			return nil
		}
		if attempt == reportMaxAttempts {
			log.Error("chronos: result report failed after retries", "error", err)
			return nil
		}
		log.Warn("chronos: result report failed; retrying", "error", err, "attempt", attempt)
		w.client.sleep(ctx, workerRetryDelayFor(err))
	}
}

func isExecutionConflict(err error) bool {
	apiErr, ok := errors.AsType[*APIError](err)
	return ok && apiErr.StatusCode == http.StatusConflict
}

func isTerminalReportError(err error) bool {
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok {
		return false
	}
	switch apiErr.StatusCode {
	case http.StatusOK:
		return true
	case http.StatusRequestTimeout, http.StatusTooManyRequests:
		return false
	}
	return apiErr.StatusCode >= 400 && apiErr.StatusCode < 500
}
