package chronos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
)

const (
	scheduleID = "0192f3a0-7c1e-7b3a-9d2e-1f4a5b6c7d8e"
	jobID      = "0192f3a1-0000-7000-8000-000000000001"
	execID     = "0192f3a2-0000-7000-8000-000000000002"
)

func TestOperationsSendTheDocumentedRequest(t *testing.T) {
	scheduledAfter := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		call     func(ctx context.Context, c *Client) error
		response func(t *testing.T) fakeResponse
		method   string
		uri      string
		body     string
	}{
		{
			name: "schedules create",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.Schedules.Create(ctx, ScheduleCreateParams{
					Name:       "Nightly report",
					Handler:    new("send-report"),
					Cron:       new("0 2 * * *"),
					MaxRetries: new(0),
				})
				return err
			},
			response: scheduleResponse,
			method:   http.MethodPost,
			uri:      "/v1/schedules",
			body:     `{"name":"Nightly report","handler":"send-report","cron":"0 2 * * *","max_retries":0}`,
		},
		{
			name: "schedules list",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.Schedules.List(ctx, ScheduleListParams{Page: 2, Size: 50, Status: ScheduleStatusPaused, Search: "a b&c"})
				return err
			},
			response: schedulePageResponse,
			method:   http.MethodGet,
			uri:      "/v1/schedules?page=2&search=a+b%26c&size=50&status=paused",
		},
		{
			name: "schedules list with no filters",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.Schedules.List(ctx, ScheduleListParams{})
				return err
			},
			response: schedulePageResponse,
			method:   http.MethodGet,
			uri:      "/v1/schedules",
		},
		{
			name:     "schedules get",
			call:     func(ctx context.Context, c *Client) error { _, err := c.Schedules.Get(ctx, scheduleID); return err },
			response: scheduleResponse,
			method:   http.MethodGet,
			uri:      "/v1/schedules/" + scheduleID,
		},
		{
			name: "schedules update",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.Schedules.Update(ctx, scheduleID, ScheduleUpdateParams{Name: new("Renamed"), Runs: Null[int]()})
				return err
			},
			response: scheduleResponse,
			method:   http.MethodPatch,
			uri:      "/v1/schedules/" + scheduleID,
			body:     `{"name":"Renamed","runs":null}`,
		},
		{
			name:     "schedules delete",
			call:     func(ctx context.Context, c *Client) error { return c.Schedules.Delete(ctx, scheduleID) },
			response: func(*testing.T) fakeResponse { return fakeResponse{status: http.StatusNoContent} },
			method:   http.MethodDelete,
			uri:      "/v1/schedules/" + scheduleID,
		},
		{
			name:     "schedules pause",
			call:     func(ctx context.Context, c *Client) error { _, err := c.Schedules.Pause(ctx, scheduleID); return err },
			response: scheduleResponse,
			method:   http.MethodPost,
			uri:      "/v1/schedules/" + scheduleID + "/pause",
		},
		{
			name:     "schedules resume",
			call:     func(ctx context.Context, c *Client) error { _, err := c.Schedules.Resume(ctx, scheduleID); return err },
			response: scheduleResponse,
			method:   http.MethodPost,
			uri:      "/v1/schedules/" + scheduleID + "/resume",
		},
		{
			name:     "schedules archive",
			call:     func(ctx context.Context, c *Client) error { _, err := c.Schedules.Archive(ctx, scheduleID); return err },
			response: scheduleResponse,
			method:   http.MethodPost,
			uri:      "/v1/schedules/" + scheduleID + "/archive",
		},
		{
			name:     "schedules trigger",
			call:     func(ctx context.Context, c *Client) error { _, err := c.Schedules.Trigger(ctx, scheduleID); return err },
			response: jobResponse,
			method:   http.MethodPost,
			uri:      "/v1/schedules/" + scheduleID + "/trigger",
		},
		{
			name: "jobs create",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.Jobs.Create(ctx, JobCreateParams{
					Name:           "Send welcome email",
					Handler:        new("send-email"),
					Delay:          &Interval{Value: 10, Unit: IntervalUnitMinute},
					Payload:        map[string]any{"user_id": "usr_123"},
					IdempotencyKey: new("welcome-usr_123"),
				})
				return err
			},
			response: jobResponse,
			method:   http.MethodPost,
			uri:      "/v1/jobs",
			body:     `{"name":"Send welcome email","handler":"send-email","delay":{"value":10,"unit":"minute"},"payload":{"user_id":"usr_123"},"idempotency_key":"welcome-usr_123"}`,
		},
		{
			name: "jobs list",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.Jobs.List(ctx, JobListParams{
					ScheduleID:     scheduleID,
					Origin:         JobOriginSchedule,
					Handler:        "send-report",
					Status:         JobStatusFailed,
					ScheduledAfter: scheduledAfter,
				})
				return err
			},
			response: jobPageResponse,
			method:   http.MethodGet,
			uri:      "/v1/jobs?handler=send-report&origin=schedule&schedule_id=" + scheduleID + "&scheduled_after=2026-01-01T00%3A00%3A00Z&status=failed",
		},
		{
			name:     "jobs get",
			call:     func(ctx context.Context, c *Client) error { _, err := c.Jobs.Get(ctx, jobID); return err },
			response: jobResponse,
			method:   http.MethodGet,
			uri:      "/v1/jobs/" + jobID,
		},
		{
			name:     "jobs cancel",
			call:     func(ctx context.Context, c *Client) error { _, err := c.Jobs.Cancel(ctx, jobID); return err },
			response: jobResponse,
			method:   http.MethodPost,
			uri:      "/v1/jobs/" + jobID + "/cancel",
		},
		{
			name:     "jobs skip",
			call:     func(ctx context.Context, c *Client) error { _, err := c.Jobs.Skip(ctx, jobID); return err },
			response: jobResponse,
			method:   http.MethodPost,
			uri:      "/v1/jobs/" + jobID + "/skip",
		},
		{
			name:     "jobs retry",
			call:     func(ctx context.Context, c *Client) error { _, err := c.Jobs.Retry(ctx, jobID); return err },
			response: jobResponse,
			method:   http.MethodPost,
			uri:      "/v1/jobs/" + jobID + "/retry",
		},
		{
			name: "executions list with a schedule id",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.Executions.List(ctx, ExecutionListParams{ScheduleID: NewNullable(scheduleID), Status: ExecutionStatusTimeout})
				return err
			},
			response: executionPageResponse,
			method:   http.MethodGet,
			uri:      "/v1/executions?schedule_id=" + scheduleID + "&status=timeout",
		},
		{
			name: "executions list without a schedule",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.Executions.List(ctx, ExecutionListParams{JobID: jobID, ScheduleID: Null[string]()})
				return err
			},
			response: executionPageResponse,
			method:   http.MethodGet,
			uri:      "/v1/executions?job_id=" + jobID + "&schedule_id=null",
		},
		{
			name:     "executions get",
			call:     func(ctx context.Context, c *Client) error { _, err := c.Executions.Get(ctx, execID); return err },
			response: executionResponse,
			method:   http.MethodGet,
			uri:      "/v1/executions/" + execID,
		},
		{
			name:     "account get",
			call:     func(ctx context.Context, c *Client) error { _, err := c.Account.Get(ctx); return err },
			response: func(t *testing.T) fakeResponse { return okResponse(fixture(t, "account.json")) },
			method:   http.MethodGet,
			uri:      "/v1/account",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, api := newTestClient(t, tt.response(t))
			if err := tt.call(t.Context(), c); err != nil {
				t.Fatalf("call failed: %v", err)
			}

			req := api.lastRequest(t)
			if req.method != tt.method {
				t.Errorf("method = %s, want %s", req.method, tt.method)
			}
			if req.requestURI != tt.uri {
				t.Errorf("uri = %s, want %s", req.requestURI, tt.uri)
			}
			assertJSONEqual(t, req.body, tt.body)
			if got := req.header.Get("Authorization"); got != "Bearer "+testAPIKey {
				t.Errorf("Authorization = %q", got)
			}
			if got := req.header.Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q", got)
			}
			if got := req.header.Get("User-Agent"); got != "chronos-go/"+Version {
				t.Errorf("User-Agent = %q", got)
			}
		})
	}
}

func TestInvalidIDsFailBeforeAnyRequest(t *testing.T) {
	for _, id := range []string{"", "   ", ".", ".."} {
		t.Run(fmt.Sprintf("%q", id), func(t *testing.T) {
			c, api := newTestClient(t, scheduleResponse(t))
			if _, err := c.Schedules.Get(t.Context(), id); !errors.Is(err, ErrInvalidID) {
				t.Errorf("error = %v, want ErrInvalidID", err)
			}
			if api.requestCount() != 0 {
				t.Error("a request was sent")
			}
		})
	}
}

func TestIDsAreEscapedAsOnePathSegment(t *testing.T) {
	c, api := newTestClient(t, scheduleResponse(t))
	if _, err := c.Schedules.Get(t.Context(), "a/../b?c#d"); err != nil {
		t.Fatal(err)
	}
	if got, want := api.lastRequest(t).requestURI, "/v1/schedules/a%2F..%2Fb%3Fc%23d"; got != want {
		t.Errorf("uri = %s, want %s", got, want)
	}
}

func TestResponsesDecodeIntoTypes(t *testing.T) {
	c, _ := newTestClient(t, scheduleResponse(t))
	s, err := c.Schedules.Get(t.Context(), scheduleID)
	if err != nil {
		t.Fatal(err)
	}
	if s.Delivery.HTTP == nil || s.Delivery.HTTP.URL != "https://example.com/hooks/report" {
		t.Errorf("delivery = %+v", s.Delivery)
	}
	if got := s.Payload["account_number"]; got != json.Number("9007199254740992") {
		t.Errorf("payload integer = %#v, want json.Number(\"9007199254740992\")", got)
	}
	if s.Description != nil || s.Interval != nil || s.Runs != nil || s.LastRunAt != nil {
		t.Errorf("null fields decoded as non-nil: %+v", s)
	}
	if want := time.Date(2026, 10, 24, 22, 48, 14, 693_000_000, time.UTC); !s.CreatedAt.Equal(want) {
		t.Errorf("created_at = %s, want %s", s.CreatedAt, want)
	}
}

func scheduleResponse(t *testing.T) fakeResponse  { return okResponse(fixture(t, "schedule.json")) }
func jobResponse(t *testing.T) fakeResponse       { return okResponse(fixture(t, "job.json")) }
func executionResponse(t *testing.T) fakeResponse { return okResponse(fixture(t, "execution.json")) }

func schedulePageResponse(t *testing.T) fakeResponse {
	return okPage("["+fixture(t, "schedule.json")+"]", 1, 1)
}

func jobPageResponse(t *testing.T) fakeResponse {
	return okPage("["+fixture(t, "job.json")+"]", 1, 1)
}

func executionPageResponse(t *testing.T) fakeResponse {
	return okPage("["+fixture(t, "execution.json")+"]", 1, 1)
}

func assertJSONEqual(t *testing.T, got, want string) {
	t.Helper()
	if want == "" {
		if got != "" {
			t.Errorf("body = %s, want none", got)
		}
		return
	}
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("body %q is not JSON: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want %q is not JSON: %v", want, err)
	}
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	if string(gb) != string(wb) {
		t.Errorf("body = %s, want %s", gb, wb)
	}
}
