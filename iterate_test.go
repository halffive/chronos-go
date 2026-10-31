package chronos

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
)

func scheduleList(ids ...string) string {
	items := make([]string, len(ids))
	for i, id := range ids {
		items[i] = `{"id":"` + id + `","delivery":{"type":"pull"},"created_at":"2026-10-24T00:00:00.000Z","updated_at":"2026-10-24T00:00:00.000Z"}`
	}
	return "[" + strings.Join(items, ",") + "]"
}

func collectIDs(t *testing.T, c *Client, params ScheduleListParams) ([]string, error) {
	t.Helper()
	var ids []string
	for s, err := range c.Schedules.Iterate(t.Context(), params) {
		if err != nil {
			return ids, err
		}
		ids = append(ids, s.ID)
	}
	return ids, nil
}

func TestIterateWalksToTheLastPage(t *testing.T) {
	c, api := newTestClient(t,
		okPage(scheduleList("a", "b"), 1, 2),
		okPage(scheduleList("c"), 2, 2),
	)
	ids, err := collectIDs(t, c, ScheduleListParams{Status: ScheduleStatusActive})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a", "b", "c"}; !slices.Equal(ids, want) {
		t.Errorf("ids = %v, want %v", ids, want)
	}
	if api.requestCount() != 2 {
		t.Errorf("requests = %d, want 2", api.requestCount())
	}
	if got := api.requestURI(t, 0); got != "/v1/schedules?page=1&size=100&status=active" {
		t.Errorf("first page uri = %s", got)
	}
}

func TestEveryIterateWalksPagesWithItsFilters(t *testing.T) {
	tests := []struct {
		name string
		call func(c *Client) ([]string, error)
		uris []string
	}{
		{
			name: "schedules",
			call: func(c *Client) ([]string, error) {
				var ids []string
				for s, err := range c.Schedules.Iterate(t.Context(), ScheduleListParams{Status: ScheduleStatusPaused}) {
					if err != nil {
						return ids, err
					}
					ids = append(ids, s.ID)
				}
				return ids, nil
			},
			uris: []string{"/v1/schedules?page=1&size=100&status=paused", "/v1/schedules?page=2&size=100&status=paused"},
		},
		{
			name: "jobs",
			call: func(c *Client) ([]string, error) {
				var ids []string
				for j, err := range c.Jobs.Iterate(t.Context(), JobListParams{Status: JobStatusFailed}) {
					if err != nil {
						return ids, err
					}
					ids = append(ids, j.ID)
				}
				return ids, nil
			},
			uris: []string{"/v1/jobs?page=1&size=100&status=failed", "/v1/jobs?page=2&size=100&status=failed"},
		},
		{
			name: "executions",
			call: func(c *Client) ([]string, error) {
				var ids []string
				for e, err := range c.Executions.Iterate(t.Context(), ExecutionListParams{ScheduleID: Null[string]()}) {
					if err != nil {
						return ids, err
					}
					ids = append(ids, e.ID)
				}
				return ids, nil
			},
			uris: []string{"/v1/executions?page=1&schedule_id=null&size=100", "/v1/executions?page=2&schedule_id=null&size=100"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, api := newTestClient(t, okPage(`[{"id":"a"}]`, 1, 2), okPage(`[{"id":"b"}]`, 2, 2))
			ids, err := tt.call(c)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(ids, []string{"a", "b"}) {
				t.Errorf("ids = %v, want [a b]", ids)
			}
			for i, uri := range tt.uris {
				if got := api.requestURI(t, i); got != uri {
					t.Errorf("request %d = %s, want %s", i, got, uri)
				}
			}
		})
	}
}

func TestIterateKeepsFiltersAndRestartsOnEachRange(t *testing.T) {
	c, api := newTestClient(t,
		okPage(scheduleList("a"), 1, 2),
		okPage(scheduleList("b"), 2, 2),
		okPage(scheduleList("a"), 1, 2),
		okPage(scheduleList("b"), 2, 2),
	)
	seq := c.Schedules.Iterate(t.Context(), ScheduleListParams{Search: "nightly"})
	for range 2 {
		for _, err := range seq {
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	want := []string{
		"/v1/schedules?page=1&search=nightly&size=100",
		"/v1/schedules?page=2&search=nightly&size=100",
		"/v1/schedules?page=1&search=nightly&size=100",
		"/v1/schedules?page=2&search=nightly&size=100",
	}
	for i, uri := range want {
		if got := api.requestURI(t, i); got != uri {
			t.Errorf("request %d = %s, want %s", i, got, uri)
		}
	}
}

func TestIterateKeepsTheCallersPageSize(t *testing.T) {
	c, api := newTestClient(t, okPage(scheduleList("a"), 1, 1))
	if _, err := collectIDs(t, c, ScheduleListParams{Size: 5}); err != nil {
		t.Fatal(err)
	}
	if got := api.requestURI(t, 0); got != "/v1/schedules?page=1&size=5" {
		t.Errorf("uri = %s", got)
	}
}

func TestIterateSkipsRowsShiftedOntoTheNextPage(t *testing.T) {
	c, _ := newTestClient(t,
		okPage(scheduleList("b", "c"), 1, 2),
		okPage(scheduleList("c", "d"), 2, 2),
	)
	ids, err := collectIDs(t, c, ScheduleListParams{})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"b", "c", "d"}; !slices.Equal(ids, want) {
		t.Errorf("ids = %v, want %v", ids, want)
	}
}

func TestIterateStopsOnAnEmptyPage(t *testing.T) {
	c, api := newTestClient(t, okPage(scheduleList(), 1, 3))
	ids, err := collectIDs(t, c, ScheduleListParams{})
	if err != nil || len(ids) != 0 || api.requestCount() != 1 {
		t.Errorf("ids %v err %v requests %d", ids, err, api.requestCount())
	}
}

func TestIterateYieldsTheErrorFromALaterPage(t *testing.T) {
	c, _ := newTestClient(t,
		okPage(scheduleList("a"), 1, 2),
		apiFailure(400, "bad_request", "Bad request"),
	)
	ids, err := collectIDs(t, c, ScheduleListParams{})
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.StatusCode != 400 {
		t.Fatalf("error = %v, want the page 2 failure", err)
	}
	if !slices.Equal(ids, []string{"a"}) {
		t.Errorf("ids before the error = %v", ids)
	}
}

func TestOneSequenceCanBeRangedConcurrently(t *testing.T) {
	c, _ := newTestClient(t, okPage(scheduleList("a"), 1, 2), okPage(scheduleList("b"), 2, 2))
	seq := c.Schedules.Iterate(t.Context(), ScheduleListParams{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range seq {
			}
		})
	}
	wg.Wait()
}

func TestIterateStopsWhenTheCallerBreaks(t *testing.T) {
	c, api := newTestClient(t,
		okPage(scheduleList("a", "b"), 1, 2),
		okPage(scheduleList("c"), 2, 2),
	)
	for range c.Schedules.Iterate(t.Context(), ScheduleListParams{}) {
		break
	}
	if api.requestCount() != 1 {
		t.Errorf("requests = %d, want 1", api.requestCount())
	}
}
