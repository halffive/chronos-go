package chronos

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"net/url"
	"time"
)

const (
	jobsPath              = "/v1/jobs"
	defaultJobWaitTimeout = 60 * time.Second
	defaultJobWaitPoll    = time.Second
)

// JobStatus is the state of a [Job].
type JobStatus string

const (
	JobStatusPending   JobStatus = "pending"
	JobStatusAdmitting JobStatus = "admitting"
	JobStatusQueued    JobStatus = "queued"
	JobStatusReady     JobStatus = "ready"
	JobStatusRunning   JobStatus = "running"
	JobStatusRetrying  JobStatus = "retrying"
	JobStatusCompleted JobStatus = "completed"
	JobStatusFailed    JobStatus = "failed"
	JobStatusCancelled JobStatus = "cancelled"
	JobStatusSkipped   JobStatus = "skipped"
)

var terminalJobStatuses = map[JobStatus]bool{
	JobStatusCompleted: true,
	JobStatusFailed:    true,
	JobStatusCancelled: true,
	JobStatusSkipped:   true,
}

// JobOrigin says what created a [Job].
type JobOrigin string

const (
	// JobOriginAPI jobs come from [JobsService.Create].
	JobOriginAPI      JobOrigin = "api"
	JobOriginSchedule JobOrigin = "schedule"
)

// Job is one unit of work. Each delivery attempt is an [Execution].
type Job struct {
	ID        string `json:"id"`
	AccountID string `json:"account_id"`
	// ScheduleID is nil for one-off jobs and jobs whose schedule was deleted.
	ScheduleID     *string   `json:"schedule_id"`
	Origin         JobOrigin `json:"origin"`
	Name           string    `json:"name"`
	Handler        *string   `json:"handler"`
	IdempotencyKey *string   `json:"idempotency_key"`
	ScheduledFor   time.Time `json:"scheduled_for"`
	// RunAt is when the next attempt is due.
	RunAt    time.Time `json:"run_at"`
	Delivery Delivery  `json:"delivery"`
	// Payload numbers decode as [encoding/json.Number].
	Payload map[string]any `json:"payload"`
	// Timeout is the handler timeout in seconds.
	Timeout      int        `json:"timeout"`
	MaxRetries   int        `json:"max_retries"`
	AttemptCount int        `json:"attempt_count"`
	Status       JobStatus  `json:"status"`
	LastError    *string    `json:"last_error"`
	StartedAt    *time.Time `json:"started_at"`
	CompletedAt  *time.Time `json:"completed_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

func (j *Job) resourceID() string { return j.ID }

// JobCreateParams is the body of [JobsService.Create]. Set Delay or
// ScheduledFor, or neither to run now. Nil fields take the API default.
type JobCreateParams struct {
	Name string `json:"name"`
	// Handler is required unless Delivery is push.
	Handler *string `json:"handler,omitzero"`
	// Delay runs the job after this long.
	Delay *Interval `json:"delay,omitzero"`
	// ScheduledFor is a future time to run the job at.
	ScheduledFor *time.Time `json:"scheduled_for,omitzero"`
	// Delivery defaults to pull.
	Delivery *Delivery `json:"delivery,omitzero"`
	// Payload is passed to the handler: a non-empty JSON object of at most
	// 64 KB.
	Payload map[string]any `json:"payload,omitzero"`
	// Timeout is the handler timeout in seconds. Defaults to 30; your plan
	// sets the maximum.
	Timeout *int `json:"timeout,omitzero"`
	// MaxRetries defaults to 3.
	MaxRetries *int `json:"max_retries,omitzero"`
	// IdempotencyKey is up to 255 characters. Reusing one returns a 409
	// [ErrorCodeIdempotencyKeyConflict] instead of creating a duplicate.
	IdempotencyKey *string `json:"idempotency_key,omitzero"`
}

// JobListParams filters [JobsService.List]. Zero values are left out.
type JobListParams struct {
	// Page starts at 1.
	Page int
	// Size is 1 to 100. Defaults to 20.
	Size       int
	ScheduleID string
	Origin     JobOrigin
	// Handler matches the handler name exactly.
	Handler string
	// Search matches the job name, ignoring case.
	Search string
	Status JobStatus
	// ScheduledBefore keeps jobs scheduled at or before this time.
	ScheduledBefore time.Time
	// ScheduledAfter keeps jobs scheduled at or after this time.
	ScheduledAfter time.Time
}

func (p JobListParams) query() url.Values {
	q := query{}
	q.setInt("page", p.Page)
	q.setInt("size", p.Size)
	q.set("schedule_id", p.ScheduleID)
	q.set("origin", string(p.Origin))
	q.set("handler", p.Handler)
	q.set("search", p.Search)
	q.set("status", string(p.Status))
	q.setTime("scheduled_before", p.ScheduledBefore)
	q.setTime("scheduled_after", p.ScheduledAfter)
	return url.Values(q)
}

// JobWaitOptions configures [JobsService.Wait].
type JobWaitOptions struct {
	// PollInterval is the time between checks. Defaults to 1 second.
	PollInterval time.Duration
}

// JobsService calls the job routes. Use it through [Client].
type JobsService service

// Create creates a job. It is not retried after a network error or a 5xx,
// because the job may already exist. To retry it yourself, set an
// IdempotencyKey and treat a 409 [ErrorCodeIdempotencyKeyConflict] as
// success.
func (s *JobsService) Create(ctx context.Context, params JobCreateParams) (*Job, error) {
	return fetch[Job](ctx, s.client, apiRequest{method: http.MethodPost, path: jobsPath, body: params})
}

// List returns one page of jobs, newest first.
func (s *JobsService) List(ctx context.Context, params JobListParams) (*Page[*Job], error) {
	return fetchPage[Job](ctx, s.client, apiRequest{method: http.MethodGet, path: jobsPath, query: params.query()})
}

// Iterate yields every matching job, newest first, in pages of 100 unless
// params.Size is set. params.Page is ignored. Each job is yielded once, but
// one deleted or changed mid-walk can be missed, so collect ids before
// changing what you iterate. Iteration stops at the first error.
func (s *JobsService) Iterate(ctx context.Context, params JobListParams) iter.Seq2[*Job, error] {
	params.Size = iteratePageSize(params.Size)
	return iterate(func(page int) (*Page[*Job], error) {
		p := params
		p.Page = page
		return s.List(ctx, p)
	})
}

// Get returns one job.
func (s *JobsService) Get(ctx context.Context, id string) (*Job, error) {
	path, err := itemPath(jobsPath, id)
	if err != nil {
		return nil, err
	}
	return fetch[Job](ctx, s.client, apiRequest{method: http.MethodGet, path: path})
}

// Cancel cancels a job that is not running or finished.
func (s *JobsService) Cancel(ctx context.Context, id string) (*Job, error) {
	return s.action(ctx, id, "cancel")
}

// Skip skips one occurrence of a schedule. Only pending or queued jobs
// created by a schedule can be skipped.
func (s *JobsService) Skip(ctx context.Context, id string) (*Job, error) {
	return s.action(ctx, id, "skip")
}

// Retry queues a failed job to run again now.
func (s *JobsService) Retry(ctx context.Context, id string) (*Job, error) {
	return s.action(ctx, id, "retry")
}

// Wait polls a job until it is completed, failed, cancelled or skipped, and
// returns it. A failed job is returned, not an error; check its Status.
//
// Without a deadline on ctx, Wait gives up after 60 seconds. At the deadline
// it returns a [*WaitTimeoutError] holding the last state seen.
func (s *JobsService) Wait(ctx context.Context, id string, opts JobWaitOptions) (*Job, error) {
	if _, err := itemPath(jobsPath, id); err != nil {
		return nil, err
	}
	if opts.PollInterval < 0 {
		return nil, fmt.Errorf("chronos: poll interval must not be negative, got %s", opts.PollInterval)
	}
	interval := opts.PollInterval
	if interval == 0 {
		interval = defaultJobWaitPoll
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.client.jobWaitTimeout)
		defer cancel()
	}

	var last *Job
	for {
		job, err := s.Get(ctx, id)
		if err != nil {
			return nil, waitError(ctx, err, last)
		}
		if terminalJobStatuses[job.Status] {
			return job, nil
		}
		last = job
		if err := s.client.sleep(ctx, interval); err != nil {
			return nil, waitError(ctx, err, last)
		}
	}
}

func waitError(ctx context.Context, err error, last *Job) error {
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() != nil {
		return &WaitTimeoutError{Job: last}
	}
	if _, skipped := errors.AsType[*retrySkippedError](err); skipped {
		return &WaitTimeoutError{Job: last}
	}
	return err
}

func (s *JobsService) action(ctx context.Context, id, action string) (*Job, error) {
	path, err := itemPath(jobsPath, id)
	if err != nil {
		return nil, err
	}
	return fetch[Job](ctx, s.client, apiRequest{method: http.MethodPost, path: path + "/" + action})
}
