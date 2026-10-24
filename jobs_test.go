package chronos

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func jobWithStatus(status JobStatus) fakeResponse {
	return okResponse(`{"id":"` + jobID + `","status":"` + string(status) + `","delivery":{"type":"pull"},"scheduled_for":"2026-10-24T00:00:00.000Z","run_at":"2026-10-24T00:00:00.000Z","created_at":"2026-10-24T00:00:00.000Z","updated_at":"2026-10-24T00:00:00.000Z"}`)
}

func TestWaitReturnsAFinishedJob(t *testing.T) {
	for _, status := range []JobStatus{JobStatusCompleted, JobStatusFailed, JobStatusCancelled, JobStatusSkipped} {
		t.Run(string(status), func(t *testing.T) {
			c, api := newTestClient(t, jobWithStatus(JobStatusQueued), jobWithStatus(JobStatusRunning), jobWithStatus(status))
			job, err := c.Jobs.Wait(t.Context(), jobID, JobWaitOptions{PollInterval: 5 * time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			if job.Status != status || api.requestCount() != 3 {
				t.Errorf("status %s after %d requests", job.Status, api.requestCount())
			}
			if api.recordedDelays()[0] != 5*time.Millisecond {
				t.Errorf("poll interval = %s", api.recordedDelays()[0])
			}
		})
	}
}

func TestWaitTimesOutWithTheLastJobSeen(t *testing.T) {
	c, _ := newTestClient(t, jobWithStatus(JobStatusRunning))
	c.sleep = sleep
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()

	_, err := c.Jobs.Wait(ctx, jobID, JobWaitOptions{PollInterval: 5 * time.Millisecond})
	timeoutErr, ok := errors.AsType[*WaitTimeoutError](err)
	if !ok {
		t.Fatalf("error = %v, want *WaitTimeoutError", err)
	}
	if timeoutErr.Job == nil || timeoutErr.Job.Status != JobStatusRunning {
		t.Errorf("last job = %+v", timeoutErr.Job)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Error("WaitTimeoutError does not match context.DeadlineExceeded")
	}
}

func TestWaitAppliesADefaultTimeout(t *testing.T) {
	c, _ := newTestClient(t, jobWithStatus(JobStatusRunning))
	c.sleep = sleep
	c.jobWaitTimeout = 300 * time.Millisecond

	_, err := c.Jobs.Wait(t.Context(), jobID, JobWaitOptions{PollInterval: 5 * time.Millisecond})
	_, timedOut := errors.AsType[*WaitTimeoutError](err)
	if !timedOut {
		t.Fatalf("error = %v, want *WaitTimeoutError", err)
	}
}

func TestWaitReturnsARequestTimeoutAsItself(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); server.Close() })

	c, err := NewClient(testAPIKey, WithBaseURL(server.URL), WithRequestTimeout(10*time.Millisecond), WithMaxRequestRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Jobs.Wait(t.Context(), jobID, JobWaitOptions{})
	_, timedOut := errors.AsType[*WaitTimeoutError](err)
	if timedOut || !errors.Is(err, ErrTimeout) {
		t.Errorf("error = %v, want the request's ErrTimeout", err)
	}
}

func TestWaitTimesOutWhenARetryWouldPassTheDeadline(t *testing.T) {
	c, _ := newTestClient(t, jobWithStatus(JobStatusRunning), unavailable())
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()

	_, err := c.Jobs.Wait(ctx, jobID, JobWaitOptions{})
	timeoutErr, ok := errors.AsType[*WaitTimeoutError](err)
	if !ok {
		t.Fatalf("error = %v, want *WaitTimeoutError", err)
	}
	if timeoutErr.Job == nil || timeoutErr.Job.Status != JobStatusRunning {
		t.Errorf("last job = %+v", timeoutErr.Job)
	}
}

func TestWaitReturnsOtherErrors(t *testing.T) {
	c, _ := newTestClient(t, apiFailure(404, "job_not_found", "Job not found"))
	if _, err := c.Jobs.Wait(t.Context(), jobID, JobWaitOptions{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func TestWaitStopsOnCancel(t *testing.T) {
	c, _ := newTestClient(t, jobWithStatus(JobStatusRunning))
	ctx, cancel := context.WithCancel(t.Context())
	c.sleep = func(ctx context.Context, d time.Duration) error {
		cancel()
		return sleep(ctx, d)
	}
	if _, err := c.Jobs.Wait(ctx, jobID, JobWaitOptions{}); err != context.Canceled {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

func TestWaitRejectsANegativeInterval(t *testing.T) {
	c, api := newTestClient(t, jobWithStatus(JobStatusRunning))
	if _, err := c.Jobs.Wait(t.Context(), jobID, JobWaitOptions{PollInterval: -time.Second}); err == nil {
		t.Error("want an error")
	}
	if api.requestCount() != 0 {
		t.Error("a request was sent")
	}
}
