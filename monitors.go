package chronos

import (
	"context"
	"iter"
	"net/http"
	"net/url"
	"time"
)

const monitorsPath = "/v1/monitors"

// MonitorStatus is the state of a [Monitor].
type MonitorStatus string

const (
	// MonitorStatusNew monitors have had no success or fail ping since they
	// were created or resumed. The first one sets NextPingDueAt, and a fail
	// ping alerts.
	MonitorStatusNew MonitorStatus = "new"
	MonitorStatusUp  MonitorStatus = "up"
	// MonitorStatusLate monitors are up but past NextPingDueAt. They go down
	// at AlertAt.
	MonitorStatusLate MonitorStatus = "late"
	// MonitorStatusDown monitors got a fail ping, or no success or fail ping
	// by AlertAt.
	MonitorStatusDown   MonitorStatus = "down"
	MonitorStatusPaused MonitorStatus = "paused"
)

// MonitorPingKind is what a ping reported.
type MonitorPingKind string

const (
	MonitorPingKindStart   MonitorPingKind = "start"
	MonitorPingKindSuccess MonitorPingKind = "success"
	MonitorPingKindFail    MonitorPingKind = "fail"
)

// Monitor watches a job that runs outside Chronos. The job calls PingURL, and
// Chronos alerts when a ping reports failure or none arrives in time.
type Monitor struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	// Cron is nil on interval monitors.
	Cron *string `json:"cron"`
	// Interval is nil on cron monitors.
	Interval *Interval `json:"interval"`
	// Timezone is the IANA name the cron expression runs in.
	Timezone string `json:"timezone"`
	// GraceSeconds is how long after NextPingDueAt the monitor waits before
	// alerting.
	GraceSeconds int           `json:"grace_seconds"`
	Status       MonitorStatus `json:"status"`
	// PingURL embeds the ping token, the only credential a ping needs, so
	// treat it as a secret. A key without the monitors:write scope reads the
	// token as "****", and that URL accepts no pings.
	PingURL string `json:"ping_url"`
	// LastPing is the latest ping of any kind, or nil before the first.
	LastPing      *MonitorPingSummary `json:"last_ping"`
	LastStartedAt *time.Time          `json:"last_started_at"`
	// NextPingDueAt is when the next success or fail ping is expected. It is
	// nil while the monitor is new or paused, and so is AlertAt.
	NextPingDueAt *time.Time `json:"next_ping_due_at"`
	AlertAt       *time.Time `json:"alert_at"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

func (m *Monitor) resourceID() string { return m.ID }

// MonitorPingSummary is the part of a [MonitorPing] a [Monitor] carries.
type MonitorPingSummary struct {
	Kind MonitorPingKind `json:"kind"`
	// ExitCode is set when the ping path carried one.
	ExitCode   *int      `json:"exit_code"`
	ReceivedAt time.Time `json:"received_at"`
}

// MonitorPing is one recorded ping.
type MonitorPing struct {
	ID   string          `json:"id"`
	Kind MonitorPingKind `json:"kind"`
	// ExitCode is set when the ping path carried one.
	ExitCode *int `json:"exit_code"`
	// DurationMS is set on a success or fail ping that directly follows a
	// start ping, as the time since that start. It is nil otherwise, and for a
	// run longer than about 24 days.
	DurationMS *int `json:"duration_ms"`
	// Body is the first 10 KB of a POST body, decoded as UTF-8 with NUL
	// characters removed. Keys with monitors:read can list it and alerts quote
	// it, so keep secrets out of it. Treat it as untrusted text, as with
	// UserAgent.
	Body *string `json:"body"`
	// Method is GET, HEAD or POST.
	Method     string    `json:"method"`
	UserAgent  *string   `json:"user_agent"`
	ReceivedAt time.Time `json:"received_at"`
}

func (p *MonitorPing) resourceID() string { return p.ID }

// MonitorCreateParams is the body of [MonitorsService.Create]. Set exactly one
// of Cron and Interval. Nil fields take the API default.
type MonitorCreateParams struct {
	Name        string  `json:"name"`
	Description *string `json:"description,omitzero"`
	// Cron must fire at most once a minute and at least twice in the next
	// 2 years.
	Cron *string `json:"cron,omitzero"`
	// Interval is 60 seconds to 365 days.
	Interval *Interval `json:"interval,omitzero"`
	// Timezone is an IANA name such as "Europe/London". Defaults to the
	// account timezone.
	Timezone *string `json:"timezone,omitzero"`
	// GraceSeconds is 60 to 604800. Defaults to 300.
	GraceSeconds *int `json:"grace_seconds,omitzero"`
}

// MonitorUpdateParams is the body of [MonitorsService.Update]. Set at least one
// field. Setting Cron clears Interval, and the reverse.
type MonitorUpdateParams struct {
	Name         *string          `json:"name,omitzero"`
	Description  Nullable[string] `json:"description,omitzero"`
	Cron         *string          `json:"cron,omitzero"`
	Interval     *Interval        `json:"interval,omitzero"`
	Timezone     *string          `json:"timezone,omitzero"`
	GraceSeconds *int             `json:"grace_seconds,omitzero"`
}

// MonitorListParams filters [MonitorsService.List]. Zero values are left out.
type MonitorListParams struct {
	// Page starts at 1.
	Page int
	// Size is 1 to 100. Defaults to 20.
	Size   int
	Status MonitorStatus
	// Search matches the monitor name, ignoring case.
	Search string
}

func (p MonitorListParams) query() url.Values {
	q := query{}
	q.setInt("page", p.Page)
	q.setInt("size", p.Size)
	q.set("status", string(p.Status))
	q.set("search", p.Search)
	return url.Values(q)
}

// MonitorPingListParams pages [MonitorsService.ListPings]. Zero values are
// left out.
type MonitorPingListParams struct {
	// Page starts at 1.
	Page int
	// Size is 1 to 100. Defaults to 20.
	Size int
}

func (p MonitorPingListParams) query() url.Values {
	q := query{}
	q.setInt("page", p.Page)
	q.setInt("size", p.Size)
	return url.Values(q)
}

// MonitorsService calls the monitor routes. Use it through [Client].
type MonitorsService service

// Create creates a monitor. It starts as [MonitorStatusNew] and sends no alert
// until its first success or fail ping. It fails with
// [ErrorCodePlanLimitExceeded] at the plan's active monitor limit.
func (s *MonitorsService) Create(ctx context.Context, params MonitorCreateParams) (*Monitor, error) {
	return fetch[Monitor](ctx, s.client, apiRequest{method: http.MethodPost, path: monitorsPath, body: params})
}

// List returns one page of monitors, newest first.
func (s *MonitorsService) List(ctx context.Context, params MonitorListParams) (*Page[*Monitor], error) {
	return fetchPage[Monitor](ctx, s.client, apiRequest{method: http.MethodGet, path: monitorsPath, query: params.query()})
}

// Iterate yields every matching monitor, newest first, in pages of 100 unless
// params.Size is set. params.Page is ignored. Each monitor is yielded once, but
// one deleted or changed mid-walk can be missed, so collect ids before
// changing what you iterate. Iteration stops at the first error.
func (s *MonitorsService) Iterate(ctx context.Context, params MonitorListParams) iter.Seq2[*Monitor, error] {
	params.Size = iteratePageSize(params.Size)
	return iterate(func(page int) (*Page[*Monitor], error) {
		p := params
		p.Page = page
		return s.List(ctx, p)
	})
}

// Get returns one monitor.
func (s *MonitorsService) Get(ctx context.Context, id string) (*Monitor, error) {
	path, err := itemPath(monitorsPath, id)
	if err != nil {
		return nil, err
	}
	return fetch[Monitor](ctx, s.client, apiRequest{method: http.MethodGet, path: path})
}

// Update changes the fields set in params.
func (s *MonitorsService) Update(ctx context.Context, id string, params MonitorUpdateParams) (*Monitor, error) {
	path, err := itemPath(monitorsPath, id)
	if err != nil {
		return nil, err
	}
	return fetch[Monitor](ctx, s.client, apiRequest{method: http.MethodPatch, path: path, body: params})
}

// Delete deletes a monitor and its pings permanently. Its ping URL stops
// working.
func (s *MonitorsService) Delete(ctx context.Context, id string) error {
	path, err := itemPath(monitorsPath, id)
	if err != nil {
		return err
	}
	_, err = s.client.do(ctx, apiRequest{method: http.MethodDelete, path: path}, nil)
	return err
}

// Pause stops alerts until [MonitorsService.Resume]. Pings to a paused monitor
// update LastPing but are not kept.
func (s *MonitorsService) Pause(ctx context.Context, id string) (*Monitor, error) {
	return s.action(ctx, id, "pause")
}

// Resume returns a paused monitor to [MonitorStatusNew], so its next success
// or fail ping sets NextPingDueAt. It fails with [ErrorCodePlanLimitExceeded]
// at the plan's active monitor limit.
func (s *MonitorsService) Resume(ctx context.Context, id string) (*Monitor, error) {
	return s.action(ctx, id, "resume")
}

// RotatePingURL replaces the ping token. The old URL stops accepting pings at
// once, so update your job with the returned PingURL. A failed call may still
// have rotated the token, so read the current URL with [MonitorsService.Get].
func (s *MonitorsService) RotatePingURL(ctx context.Context, id string) (*Monitor, error) {
	return s.action(ctx, id, "ping-url/rotate")
}

// ListPings returns one page of a monitor's pings, newest first. Chronos keeps
// the latest 1000 per monitor.
func (s *MonitorsService) ListPings(ctx context.Context, id string, params MonitorPingListParams) (*Page[*MonitorPing], error) {
	path, err := itemPath(monitorsPath, id)
	if err != nil {
		return nil, err
	}
	return fetchPage[MonitorPing](ctx, s.client, apiRequest{method: http.MethodGet, path: path + "/pings", query: params.query()})
}

// IteratePings yields every ping of a monitor, newest first, in pages of 100
// unless params.Size is set. params.Page is ignored. A ping that arrives
// mid-walk shifts later pages, but no ping is yielded twice. Iteration stops at
// the first error.
func (s *MonitorsService) IteratePings(ctx context.Context, id string, params MonitorPingListParams) iter.Seq2[*MonitorPing, error] {
	params.Size = iteratePageSize(params.Size)
	return iterate(func(page int) (*Page[*MonitorPing], error) {
		p := params
		p.Page = page
		return s.ListPings(ctx, id, p)
	})
}

func (s *MonitorsService) action(ctx context.Context, id, action string) (*Monitor, error) {
	path, err := itemPath(monitorsPath, id)
	if err != nil {
		return nil, err
	}
	return fetch[Monitor](ctx, s.client, apiRequest{method: http.MethodPost, path: path + "/" + action})
}
