package chronos

import (
	"context"
	"iter"
	"net/http"
	"net/url"
	"time"
)

const schedulesPath = "/v1/schedules"

// ScheduleStatus is the state of a [Schedule].
type ScheduleStatus string

const (
	ScheduleStatusActive    ScheduleStatus = "active"
	ScheduleStatusPaused    ScheduleStatus = "paused"
	ScheduleStatusCompleted ScheduleStatus = "completed"
	ScheduleStatusArchived  ScheduleStatus = "archived"
)

// Schedule creates a job on every cron or interval occurrence.
type Schedule struct {
	ID          string  `json:"id"`
	AccountID   string  `json:"account_id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	// Handler is nil only on push schedules created without one.
	Handler *string `json:"handler"`
	// Cron is nil on interval schedules.
	Cron *string `json:"cron"`
	// Interval is nil on cron schedules.
	Interval *Interval `json:"interval"`
	// Timezone is the IANA name the cron expression runs in.
	Timezone string `json:"timezone"`
	// Runs is the run limit, or nil for none.
	Runs     *int       `json:"runs"`
	RunCount int        `json:"run_count"`
	StopAt   *time.Time `json:"stop_at"`
	Delivery Delivery   `json:"delivery"`
	// Payload numbers decode as [encoding/json.Number].
	Payload map[string]any `json:"payload"`
	// Timeout is the handler timeout in seconds.
	Timeout    int            `json:"timeout"`
	MaxRetries int            `json:"max_retries"`
	Status     ScheduleStatus `json:"status"`
	// NextRunAt is nil unless the schedule is active.
	NextRunAt *time.Time `json:"next_run_at"`
	LastRunAt *time.Time `json:"last_run_at"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

func (s *Schedule) resourceID() string { return s.ID }

// ScheduleCreateParams is the body of [SchedulesService.Create]. Set exactly
// one of Cron and Interval. Nil fields take the API default.
type ScheduleCreateParams struct {
	Name        string  `json:"name"`
	Description *string `json:"description,omitzero"`
	// Handler is required unless Delivery is push.
	Handler  *string   `json:"handler,omitzero"`
	Cron     *string   `json:"cron,omitzero"`
	Interval *Interval `json:"interval,omitzero"`
	// Timezone is an IANA name such as "Europe/London". Defaults to the
	// account timezone.
	Timezone *string `json:"timezone,omitzero"`
	// Runs stops the schedule after this many runs.
	Runs *int `json:"runs,omitzero"`
	// StopAt is a future time after which no jobs are created.
	StopAt *time.Time `json:"stop_at,omitzero"`
	// Delivery defaults to pull.
	Delivery *Delivery `json:"delivery,omitzero"`
	// Payload is passed to every job: a non-empty JSON object of at most
	// 64 KB.
	Payload map[string]any `json:"payload,omitzero"`
	// Timeout is the handler timeout in seconds. Defaults to 30; your plan
	// sets the maximum.
	Timeout *int `json:"timeout,omitzero"`
	// MaxRetries defaults to 3.
	MaxRetries *int `json:"max_retries,omitzero"`
}

// ScheduleUpdateParams is the body of [SchedulesService.Update]. Set at least
// one field. Setting Cron clears Interval, and the reverse. Delivery replaces
// the whole setting.
type ScheduleUpdateParams struct {
	Name        *string                  `json:"name,omitzero"`
	Description Nullable[string]         `json:"description,omitzero"`
	Handler     Nullable[string]         `json:"handler,omitzero"`
	Cron        *string                  `json:"cron,omitzero"`
	Interval    *Interval                `json:"interval,omitzero"`
	Timezone    *string                  `json:"timezone,omitzero"`
	Runs        Nullable[int]            `json:"runs,omitzero"`
	StopAt      Nullable[time.Time]      `json:"stop_at,omitzero"`
	Delivery    *Delivery                `json:"delivery,omitzero"`
	Payload     Nullable[map[string]any] `json:"payload,omitzero"`
	Timeout     *int                     `json:"timeout,omitzero"`
	MaxRetries  *int                     `json:"max_retries,omitzero"`
}

// ScheduleListParams filters [SchedulesService.List]. Zero values are left
// out.
type ScheduleListParams struct {
	// Page starts at 1.
	Page int
	// Size is 1 to 100. Defaults to 20.
	Size   int
	Status ScheduleStatus
	// Search matches the schedule name, ignoring case.
	Search string
}

func (p ScheduleListParams) query() url.Values {
	q := query{}
	q.setInt("page", p.Page)
	q.setInt("size", p.Size)
	q.set("status", string(p.Status))
	q.set("search", p.Search)
	return url.Values(q)
}

// SchedulesService calls the schedule routes. Use it through [Client].
type SchedulesService service

// Create creates a schedule.
func (s *SchedulesService) Create(ctx context.Context, params ScheduleCreateParams) (*Schedule, error) {
	return fetch[Schedule](ctx, s.client, apiRequest{method: http.MethodPost, path: schedulesPath, body: params})
}

// List returns one page of schedules, newest first.
func (s *SchedulesService) List(ctx context.Context, params ScheduleListParams) (*Page[*Schedule], error) {
	return fetchPage[Schedule](ctx, s.client, apiRequest{method: http.MethodGet, path: schedulesPath, query: params.query()})
}

// Iterate yields every matching schedule, newest first, in pages of 100
// unless params.Size is set. params.Page is ignored. Each schedule is yielded
// once, but one deleted or changed mid-walk can be missed, so collect ids
// before changing what you iterate. Iteration stops at the first error.
func (s *SchedulesService) Iterate(ctx context.Context, params ScheduleListParams) iter.Seq2[*Schedule, error] {
	params.Size = iteratePageSize(params.Size)
	return iterate(func(page int) (*Page[*Schedule], error) {
		p := params
		p.Page = page
		return s.List(ctx, p)
	})
}

// Get returns one schedule.
func (s *SchedulesService) Get(ctx context.Context, id string) (*Schedule, error) {
	path, err := itemPath(schedulesPath, id)
	if err != nil {
		return nil, err
	}
	return fetch[Schedule](ctx, s.client, apiRequest{method: http.MethodGet, path: path})
}

// Update changes the fields set in params.
func (s *SchedulesService) Update(ctx context.Context, id string, params ScheduleUpdateParams) (*Schedule, error) {
	path, err := itemPath(schedulesPath, id)
	if err != nil {
		return nil, err
	}
	return fetch[Schedule](ctx, s.client, apiRequest{method: http.MethodPatch, path: path, body: params})
}

// Delete deletes a schedule permanently. Its jobs remain, with a nil
// ScheduleID.
func (s *SchedulesService) Delete(ctx context.Context, id string) error {
	path, err := itemPath(schedulesPath, id)
	if err != nil {
		return err
	}
	_, err = s.client.do(ctx, apiRequest{method: http.MethodDelete, path: path}, nil)
	return err
}

// Pause stops new jobs until [SchedulesService.Resume]. Only active schedules
// can be paused.
func (s *SchedulesService) Pause(ctx context.Context, id string) (*Schedule, error) {
	return s.action(ctx, id, "pause")
}

// Resume restarts a paused schedule from its next occurrence. Missed
// occurrences are skipped.
func (s *SchedulesService) Resume(ctx context.Context, id string) (*Schedule, error) {
	return s.action(ctx, id, "resume")
}

// Archive ends a schedule for good and cancels its jobs still waiting to run.
func (s *SchedulesService) Archive(ctx context.Context, id string) (*Schedule, error) {
	return s.action(ctx, id, "archive")
}

// Trigger creates a job that runs now. It counts toward Runs and works while
// the schedule is paused.
func (s *SchedulesService) Trigger(ctx context.Context, id string) (*Job, error) {
	path, err := itemPath(schedulesPath, id)
	if err != nil {
		return nil, err
	}
	return fetch[Job](ctx, s.client, apiRequest{method: http.MethodPost, path: path + "/trigger"})
}

func (s *SchedulesService) action(ctx context.Context, id, action string) (*Schedule, error) {
	path, err := itemPath(schedulesPath, id)
	if err != nil {
		return nil, err
	}
	return fetch[Schedule](ctx, s.client, apiRequest{method: http.MethodPost, path: path + "/" + action})
}
