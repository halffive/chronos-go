package chronos

import (
	"context"
	"iter"
	"net/http"
	"net/url"
	"time"
)

const executionsPath = "/v1/executions"

// ExecutionStatus is the state of an [Execution].
type ExecutionStatus string

const (
	ExecutionStatusPending   ExecutionStatus = "pending"
	ExecutionStatusRunning   ExecutionStatus = "running"
	ExecutionStatusCompleted ExecutionStatus = "completed"
	ExecutionStatusFailed    ExecutionStatus = "failed"
	ExecutionStatusTimeout   ExecutionStatus = "timeout"
)

// ExecutionTrigger says why an [Execution] ran.
type ExecutionTrigger string

const (
	// ExecutionTriggerSystem is a job's first attempt.
	ExecutionTriggerSystem ExecutionTrigger = "system"
	// ExecutionTriggerSystemRetry is a later attempt.
	ExecutionTriggerSystemRetry ExecutionTrigger = "system_retry"
	// ExecutionTriggerManual is reserved; the API does not set it yet.
	ExecutionTriggerManual ExecutionTrigger = "manual"
)

// Execution is one delivery attempt of a [Job].
type Execution struct {
	ID          string           `json:"id"`
	AccountID   string           `json:"account_id"`
	JobID       string           `json:"job_id"`
	Status      ExecutionStatus  `json:"status"`
	Trigger     ExecutionTrigger `json:"trigger"`
	StartedAt   *time.Time       `json:"started_at"`
	CompletedAt *time.Time       `json:"completed_at"`
	DurationMS  *int             `json:"duration_ms"`
	// Result is the object the handler returned, if any. Its numbers decode as
	// [encoding/json.Number].
	Result map[string]any `json:"result"`
	Error  *string        `json:"error"`
	// ResponseCode is your endpoint's HTTP status, for push delivery.
	ResponseCode *int      `json:"response_code"`
	CreatedAt    time.Time `json:"created_at"`
}

func (e *Execution) resourceID() string { return e.ID }

// ExecutionListParams filters [ExecutionsService.List]. Zero values are left
// out.
type ExecutionListParams struct {
	// Page starts at 1.
	Page int
	// Size is 1 to 100. Defaults to 20.
	Size  int
	JobID string
	// ScheduleID set to [Null] matches jobs with no schedule: one-off jobs
	// and jobs whose schedule was deleted.
	ScheduleID Nullable[string]
	// Handler matches the job's handler name exactly.
	Handler string
	Status  ExecutionStatus
	// CreatedBefore keeps executions created at or before this time.
	CreatedBefore time.Time
	// CreatedAfter keeps executions created at or after this time.
	CreatedAfter time.Time
}

func (p ExecutionListParams) query() url.Values {
	q := query{}
	q.setInt("page", p.Page)
	q.setInt("size", p.Size)
	q.set("job_id", p.JobID)
	q.set("schedule_id", nullableQueryValue(p.ScheduleID))
	q.set("handler", p.Handler)
	q.set("status", string(p.Status))
	q.setTime("created_before", p.CreatedBefore)
	q.setTime("created_after", p.CreatedAfter)
	return url.Values(q)
}

// ExecutionsService reads executions. Use it through [Client].
type ExecutionsService service

// List returns one page of executions, newest first.
func (s *ExecutionsService) List(ctx context.Context, params ExecutionListParams) (*Page[*Execution], error) {
	return fetchPage[Execution](ctx, s.client, apiRequest{method: http.MethodGet, path: executionsPath, query: params.query()})
}

// Iterate yields every matching execution, newest first, in pages of 100
// unless params.Size is set. params.Page is ignored. Each execution is
// yielded once, but one changed mid-walk can be missed. Iteration stops at
// the first error.
func (s *ExecutionsService) Iterate(ctx context.Context, params ExecutionListParams) iter.Seq2[*Execution, error] {
	params.Size = iteratePageSize(params.Size)
	return iterate(func(page int) (*Page[*Execution], error) {
		p := params
		p.Page = page
		return s.List(ctx, p)
	})
}

// Get returns one execution.
func (s *ExecutionsService) Get(ctx context.Context, id string) (*Execution, error) {
	path, err := itemPath(executionsPath, id)
	if err != nil {
		return nil, err
	}
	return fetch[Execution](ctx, s.client, apiRequest{method: http.MethodGet, path: path})
}
