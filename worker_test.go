package chronos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	testJobID       = "0192f3a2-0000-7000-8000-000000000003"
	testExecutionID = "0192f3a3-0000-7000-8000-000000000004"
	testResultURI   = "/v1/workers/executions/" + testExecutionID + "/result"
)

var (
	emptyClaim     = okResponse("null")
	resultRecorded = okResponse("null")
	keyRejected    = apiFailure(http.StatusUnauthorized, "unauthorized", "Unauthorized")
)

func claimedJob(handler string, timeout int) fakeResponse {
	return okResponse(fmt.Sprintf(
		`{"job_id":%q,"execution_id":%q,"handler":%q,"scheduled_for":"2026-10-24T22:48:14.977Z",`+
			`"attempt":2,"timeout":%d,"payload":{"count":9007199254740993},"schedule":{"id":"s1","name":"Nightly"}}`,
		testJobID, testExecutionID, handler, timeout,
	))
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newTestWorker(c *Client, opts ...WorkerOption) (*Worker, *lockedBuffer) {
	logs := &lockedBuffer{}
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	return NewWorker(c, append([]WorkerOption{WithWorkerLogger(logger)}, opts...)...), logs
}

const testWait = 10 * time.Second

func testContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(t.Context(), testWait)
}

func newRelease(t *testing.T) (<-chan struct{}, func()) {
	t.Helper()
	ch := make(chan struct{})
	release := sync.OnceFunc(func() { close(ch) })
	t.Cleanup(release)
	return ch, release
}

func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(testWait):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func runUntilKeyRejected(t *testing.T, w *Worker) {
	t.Helper()
	ctx, cancel := testContext(t)
	defer cancel()
	if err := w.Run(ctx); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Run: got %v, want ErrUnauthorized", err)
	}
}

func awaitRun(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(testWait):
		t.Fatal("Run did not return")
		return nil
	}
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(testWait)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func workerGoroutines() int {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	count := 0
	for stack := range strings.SplitSeq(string(buf), "\n\n") {
		if strings.Contains(stack, "chronos.sh/go.(*Worker)") {
			count++
		}
	}
	return count
}

func assertNoWorkerGoroutines(t *testing.T) {
	t.Helper()
	waitFor(t, "worker goroutines to exit", func() bool { return workerGoroutines() == 0 })
}

func noopHandler(context.Context, *ClaimedJob) (map[string]any, error) { return nil, nil }

func TestClaimSendsTheDocumentedRequest(t *testing.T) {
	c, api := newTestClient(t, keyRejected)
	w, _ := newTestWorker(c, WithWorkerID(" collector-1 "), WithPollWait(5*time.Second))
	w.Handle("send-email", noopHandler)
	w.Handle(" cleanup ", noopHandler)

	runUntilKeyRejected(t, w)

	req := api.lastRequest(t)
	if req.method != http.MethodPost || req.requestURI != "/v1/workers/jobs/claim" {
		t.Errorf("got %s %s", req.method, req.requestURI)
	}
	var body struct {
		WaitTimeSeconds int      `json:"wait_time_seconds"`
		Handlers        []string `json:"handlers"`
		Worker          struct {
			ID         string `json:"id"`
			SDKVersion string `json:"sdk_version"`
			Runtime    string `json:"runtime"`
			UptimeMS   *int64 `json:"uptime_ms"`
		} `json:"worker"`
	}
	if err := json.Unmarshal([]byte(req.body), &body); err != nil {
		t.Fatal(err)
	}
	if body.WaitTimeSeconds != 5 {
		t.Errorf("wait_time_seconds: got %d", body.WaitTimeSeconds)
	}
	if strings.Join(body.Handlers, ",") != "send-email,cleanup" {
		t.Errorf("handlers: got %v", body.Handlers)
	}
	if body.Worker.ID != "collector-1" || body.Worker.SDKVersion != Version {
		t.Errorf("worker: got %+v", body.Worker)
	}
	if !strings.HasPrefix(body.Worker.Runtime, "go/") || body.Worker.UptimeMS == nil {
		t.Errorf("worker: got %+v", body.Worker)
	}
}

func TestEmptyClaimClaimsAgainWithoutWaiting(t *testing.T) {
	c, api := newTestClient(t, emptyClaim, emptyClaim, keyRejected)
	w, _ := newTestWorker(c)
	w.Handle("h", noopHandler)

	runUntilKeyRejected(t, w)

	if api.requestCount() != 3 {
		t.Fatalf("got %d requests, want 3", api.requestCount())
	}
	if delays := api.recordedDelays(); len(delays) != 0 {
		t.Errorf("got delays %v", delays)
	}
}

func TestDefaultWorkerIdentityAndPollWait(t *testing.T) {
	c, api := newTestClient(t, emptyClaim, keyRejected)
	w, _ := newTestWorker(c)
	w.Handle("h", noopHandler)

	runUntilKeyRejected(t, w)

	generated := regexp.MustCompile(`"id":"(w_[0-9a-f]{16})"`)
	first := generated.FindStringSubmatch(api.requestBody(t, 0))
	if first == nil {
		t.Fatalf("no generated worker id in %s", api.requestBody(t, 0))
	}
	if !strings.Contains(api.requestBody(t, 1), first[0]) {
		t.Errorf("worker id changed between claims: %s", api.requestBody(t, 1))
	}
	if !strings.Contains(api.requestBody(t, 0), `"wait_time_seconds":20`) {
		t.Errorf("default poll wait: %s", api.requestBody(t, 0))
	}
}

func TestLongPollWaitIsNotCountedAgainstTheRequestTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	c, err := NewClient(testAPIKey, WithBaseURL(server.URL), WithRequestTimeout(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	w, _ := newTestWorker(c, WithPollWait(time.Second))
	w.Handle("h", noopHandler)

	runUntilKeyRejected(t, w)
}

type nilReceiverError struct{ message string }

func (e *nilReceiverError) Error() string { return e.message }

type selfPanickingError struct{}

func (selfPanickingError) Error() string { panic(selfPanickingError{}) }

type panickingMarshaler struct{}

func (panickingMarshaler) MarshalJSON() ([]byte, error) { panic("cannot encode") }

func TestHandlerOutcomeIsReported(t *testing.T) {
	tests := []struct {
		name    string
		handler Handler
		want    string
	}{
		{
			name: "result",
			handler: func(context.Context, *ClaimedJob) (map[string]any, error) {
				return map[string]any{"sent": true}, nil
			},
			want: `{"status":"completed","result":{"sent":true}}`,
		},
		{
			name:    "no result",
			handler: noopHandler,
			want:    `{"status":"completed"}`,
		},
		{
			name: "empty result",
			handler: func(context.Context, *ClaimedJob) (map[string]any, error) {
				return map[string]any{}, nil
			},
			want: `{"status":"completed","result":{}}`,
		},
		{
			name: "error",
			handler: func(context.Context, *ClaimedJob) (map[string]any, error) {
				return nil, errors.New("smtp refused")
			},
			want: `{"status":"failed","error":"smtp refused"}`,
		},
		{
			name: "blank error",
			handler: func(context.Context, *ClaimedJob) (map[string]any, error) {
				return nil, errors.New("  ")
			},
			want: `{"status":"failed","error":"chronos: handler failed"}`,
		},
		{
			name: "panic",
			handler: func(context.Context, *ClaimedJob) (map[string]any, error) {
				panic("nil map")
			},
			want: `{"status":"failed","error":"chronos: handler panicked: nil map"}`,
		},
		{
			name: "error whose Error method panics",
			handler: func(context.Context, *ClaimedJob) (map[string]any, error) {
				var err *nilReceiverError
				return nil, err
			},
			want: `{"status":"failed","error":"chronos: handler panicked: runtime error: invalid memory address or nil pointer dereference"}`,
		},
		{
			name: "error that panics when printed",
			handler: func(context.Context, *ClaimedJob) (map[string]any, error) {
				return nil, selfPanickingError{}
			},
			want: `{"status":"failed","error":"chronos: handler panicked"}`,
		},
		{
			name: "result whose MarshalJSON panics",
			handler: func(context.Context, *ClaimedJob) (map[string]any, error) {
				return map[string]any{"bad": panickingMarshaler{}}, nil
			},
			want: `{"status":"failed","error":"chronos: handler panicked: cannot encode"}`,
		},
		{
			name: "goroutine exit",
			handler: func(context.Context, *ClaimedJob) (map[string]any, error) {
				runtime.Goexit()
				return nil, nil
			},
			want: `{"status":"failed","error":"chronos: handler exited without returning"}`,
		},
		{
			name: "result that cannot be encoded",
			handler: func(context.Context, *ClaimedJob) (map[string]any, error) {
				return map[string]any{"bad": make(chan int)}, nil
			},
			want: `{"status":"failed","error":"chronos: handler result cannot be encoded to JSON: json: unsupported type: chan int"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, api := newTestClient(t, claimedJob("h", 30), resultRecorded, keyRejected)
			w, _ := newTestWorker(c)
			w.Handle("h", tt.handler)

			runUntilKeyRejected(t, w)

			if api.requestCount() != 3 {
				t.Fatalf("got %d requests, want 3", api.requestCount())
			}
			if got := api.requestURI(t, 1); got != testResultURI {
				t.Errorf("URI: got %s", got)
			}
			if got := api.requestBody(t, 1); got != tt.want {
				t.Errorf("body:\n got %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestHandlerReceivesTheClaimedJob(t *testing.T) {
	c, _ := newTestClient(t, okResponse(fixture(t, "claimed_job.json")), resultRecorded, keyRejected)
	w, _ := newTestWorker(c)
	var got *ClaimedJob
	var deadline time.Time
	w.Handle("send-email", func(ctx context.Context, job *ClaimedJob) (map[string]any, error) {
		got = job
		deadline, _ = ctx.Deadline()
		return nil, nil
	})

	runUntilKeyRejected(t, w)

	if got.JobID != testJobID || got.ExecutionID != testExecutionID || got.Handler != "send-email" {
		t.Errorf("job: %+v", got)
	}
	if got.Attempt != 1 || got.Timeout != 30 || got.ScheduledFor.IsZero() {
		t.Errorf("job: %+v", got)
	}
	if got.Schedule == nil || got.Schedule.Name != "Nightly report" {
		t.Errorf("schedule: %+v", got.Schedule)
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > 30*time.Second {
		t.Errorf("deadline is %s away", remaining)
	}
}

func TestPayloadKeepsLargeIntegers(t *testing.T) {
	c, _ := newTestClient(t, claimedJob("h", 30), resultRecorded, keyRejected)
	w, _ := newTestWorker(c)
	var payload string
	w.Handle("h", func(_ context.Context, job *ClaimedJob) (map[string]any, error) {
		payload = string(job.Payload)
		return nil, nil
	})

	runUntilKeyRejected(t, w)

	if payload != `{"count":9007199254740993}` {
		t.Errorf("payload: %s", payload)
	}
}

func TestHandlerChangingItsJobDoesNotAffectTheReportOrLogs(t *testing.T) {
	c, api := newTestClient(t, claimedJob("h", 30), resultRecorded, keyRejected)
	w, logs := newTestWorker(c)
	w.Handle("h", func(_ context.Context, job *ClaimedJob) (map[string]any, error) {
		job.ExecutionID = "other"
		return nil, errors.New("failed")
	})

	runUntilKeyRejected(t, w)

	if got := api.requestURI(t, 1); got != testResultURI {
		t.Errorf("URI: got %s", got)
	}
	if strings.Contains(logs.String(), "other") {
		t.Errorf("logs: %s", logs)
	}
}

type panickingLogHandler struct{ slog.Handler }

func (panickingLogHandler) Handle(context.Context, slog.Record) error { panic("logger broke") }

func TestPanickingLoggerDoesNotCrashTheHandlerGoroutine(t *testing.T) {
	c, api := newTestClient(t, claimedJob("h", 30), resultRecorded)
	logger := slog.New(panickingLogHandler{slog.DiscardHandler})
	w := NewWorker(c, WithWorkerLogger(logger))
	ctx, cancel := testContext(t)
	defer cancel()
	w.Handle("h", func(context.Context, *ClaimedJob) (map[string]any, error) {
		cancel()
		panic("boom")
	})

	if err := w.Run(ctx); err != nil {
		t.Errorf("Run: got %v", err)
	}
	if got := api.requestBody(t, 1); got != `{"status":"failed","error":"chronos: handler panicked: boom"}` {
		t.Errorf("got %s", got)
	}
}

func TestClaimWithoutAnExecutionIDKeepsClaiming(t *testing.T) {
	c, api := newTestClient(t, okResponse(`{"job_id":"j","execution_id":"","handler":"h","timeout":30}`), keyRejected)
	w, logs := newTestWorker(c)
	w.Handle("h", noopHandler)

	runUntilKeyRejected(t, w)

	if got := api.requestCount(); got != 2 {
		t.Errorf("got %d requests, want 2 claims", got)
	}
	if !strings.Contains(logs.String(), "result not reported") {
		t.Errorf("logs: %s", logs)
	}
}

func TestUnregisteredHandlerFailsTheJob(t *testing.T) {
	c, api := newTestClient(t, claimedJob("gone", 30), resultRecorded, keyRejected)
	w, _ := newTestWorker(c)
	w.Handle("h", noopHandler)

	runUntilKeyRejected(t, w)

	want := `{"status":"failed","error":"chronos: received a job for unregistered handler \"gone\""}`
	if got := api.requestBody(t, 1); got != want {
		t.Errorf("got %s", got)
	}
}

func TestReportedErrorFitsTheAPILimit(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    int
	}{
		{"ascii", strings.Repeat("a", 5000), 4096},
		{"surrogate pairs", strings.Repeat("😀", 5000), 4096},
		{"under the limit", "short", 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := failedResult(tt.message).Error
			if units := len(utf16.Encode([]rune(got))); units != tt.want {
				t.Errorf("got %d UTF-16 units, want %d", units, tt.want)
			}
			if !utf8.ValidString(got) {
				t.Error("truncated string is not valid UTF-8")
			}
		})
	}
}

func TestHandlerHonouringItsTimeoutIsReportedAsTimeout(t *testing.T) {
	c, api := newTestClient(t, claimedJob("h", 1), resultRecorded, keyRejected)
	w, _ := newTestWorker(c)
	w.jobTimeoutUnit = 20 * time.Millisecond
	var handlerErr error
	w.Handle("h", func(ctx context.Context, _ *ClaimedJob) (map[string]any, error) {
		<-ctx.Done()
		handlerErr = ctx.Err()
		return nil, handlerErr
	})

	runUntilKeyRejected(t, w)

	if !errors.Is(handlerErr, context.DeadlineExceeded) {
		t.Errorf("handler context: got %v", handlerErr)
	}
	if got := api.requestBody(t, 1); got != `{"status":"timeout"}` {
		t.Errorf("got %s", got)
	}
}

func TestHandlerReturnAfterItsDeadlineIsClassified(t *testing.T) {
	expired, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()

	if got := resultOf(expired, slog.Default(), handlerReturn{err: expired.Err()}); got.Status != ExecutionStatusTimeout {
		t.Errorf("error after the deadline: got %+v", got)
	}
	if got := resultOf(expired, slog.Default(), handlerReturn{}); got.Status != ExecutionStatusCompleted {
		t.Errorf("success after the deadline: got %+v", got)
	}
}

func TestTimedOutHandlerHoldsTheNextClaim(t *testing.T) {
	c, api := newTestClient(t, claimedJob("h", 1), resultRecorded, keyRejected)
	w, _ := newTestWorker(c)
	w.jobTimeoutUnit = 10 * time.Millisecond
	blocked, release := newRelease(t)
	w.Handle("h", func(context.Context, *ClaimedJob) (map[string]any, error) {
		<-blocked
		return nil, nil
	})

	ctx, cancel := testContext(t)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	waitFor(t, "the timeout report", func() bool { return api.requestCount() == 2 })
	if got := api.requestBody(t, 1); got != `{"status":"timeout"}` {
		t.Errorf("got %s", got)
	}
	time.Sleep(50 * time.Millisecond)
	if got := api.requestCount(); got != 2 {
		t.Fatalf("claimed again while the handler was running: %d requests", got)
	}

	release()
	if err := awaitRun(t, done); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("Run: got %v", err)
	}
}

func TestHandlerIgnoringItsTimeoutIsAbandonedAfterTheGraceAndCanStillExit(t *testing.T) {
	c, api := newTestClient(t, claimedJob("h", 1), resultRecorded, keyRejected)
	w, logs := newTestWorker(c)
	w.jobTimeoutUnit = 10 * time.Millisecond
	w.timedOutHandlerGrace = 10 * time.Millisecond
	blocked, release := newRelease(t)
	w.Handle("h", func(context.Context, *ClaimedJob) (map[string]any, error) {
		<-blocked
		return map[string]any{"late": true}, nil
	})

	runUntilKeyRejected(t, w)

	if !strings.Contains(logs.String(), "ignored its timeout") {
		t.Errorf("logs: %s", logs)
	}
	if got := w.stuckHandlers.Load(); got != 1 {
		t.Errorf("got %d stuck handlers before release, want 1", got)
	}
	release()
	assertNoWorkerGoroutines(t)
	if got := api.requestCount(); got != 3 {
		t.Errorf("late return sent a report: %d requests", got)
	}
}

func TestTooManyStuckHandlersStopsTheWorkerUntilTheyReturn(t *testing.T) {
	var responses []fakeResponse
	for range maxStuckHandlers {
		responses = append(responses, claimedJob("h", 1), resultRecorded)
	}
	c, api := newTestClient(t, append(responses, keyRejected)...)
	w, _ := newTestWorker(c)
	w.jobTimeoutUnit = time.Millisecond
	w.timedOutHandlerGrace = time.Millisecond
	blocked, release := newRelease(t)
	w.Handle("h", func(context.Context, *ClaimedJob) (map[string]any, error) {
		<-blocked
		return nil, nil
	})

	ctx, cancel := testContext(t)
	defer cancel()
	if err := w.Run(ctx); !errors.Is(err, ErrTooManyStuckHandlers) {
		t.Fatalf("Run: got %v", err)
	}
	if got, want := api.requestCount(), 2*maxStuckHandlers; got != want {
		t.Errorf("got %d requests, want %d", got, want)
	}
	if err := w.Run(ctx); !errors.Is(err, ErrTooManyStuckHandlers) {
		t.Errorf("second Run while still stuck: got %v", err)
	}

	release()
	waitFor(t, "stuck handlers to return", func() bool { return w.stuckHandlers.Load() == 0 })
	assertNoWorkerGoroutines(t)
	runUntilKeyRejected(t, w)
}

func TestPanicAfterTheGraceDoesNotCrashTheProcess(t *testing.T) {
	c, _ := newTestClient(t, claimedJob("h", 1), resultRecorded, keyRejected)
	w, logs := newTestWorker(c)
	w.jobTimeoutUnit = 10 * time.Millisecond
	w.timedOutHandlerGrace = 10 * time.Millisecond
	blocked, release := newRelease(t)
	w.Handle("h", func(context.Context, *ClaimedJob) (map[string]any, error) {
		<-blocked
		panic("late")
	})

	runUntilKeyRejected(t, w)
	release()

	assertNoWorkerGoroutines(t)
	if !strings.Contains(logs.String(), "handler panicked") {
		t.Errorf("logs: %s", logs)
	}
}

func TestRejectedKeyStopsTheWorker(t *testing.T) {
	tests := []struct {
		name     string
		response fakeResponse
		match    func(error) bool
	}{
		{
			name:     "401",
			response: keyRejected,
			match:    func(err error) bool { return errors.Is(err, ErrUnauthorized) },
		},
		{
			name:     "401 without an envelope",
			response: fakeResponse{status: http.StatusUnauthorized, body: "<html>nope</html>"},
			match:    func(err error) bool { return errors.Is(err, ErrUnauthorized) },
		},
		{
			name:     "missing scope",
			response: apiFailure(http.StatusForbidden, "insufficient_scope", "API key is missing the workers:run scope"),
			match: func(err error) bool {
				return isForbiddenWithCode(err, ErrorCodeInsufficientScope) && strings.Contains(err.Error(), "workers:run")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, api := newTestClient(t, tt.response)
			w, logs := newTestWorker(c)
			w.Handle("h", noopHandler)

			ctx, cancel := testContext(t)
			defer cancel()
			err := w.Run(ctx)

			if !tt.match(err) {
				t.Errorf("Run: got %v", err)
			}
			if got := api.requestCount(); got != 1 {
				t.Errorf("got %d requests, want 1", got)
			}
			if got := strings.Count(logs.String(), "\n"); got != 1 {
				t.Errorf("got %d log lines, want 1: %s", got, logs)
			}
		})
	}
}

func TestRejectedKeyOnReportStopsWithoutAnotherClaim(t *testing.T) {
	tests := []struct {
		name     string
		response fakeResponse
	}{
		{"401", keyRejected},
		{"missing scope", apiFailure(http.StatusForbidden, "insufficient_scope", "missing scope")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, api := newTestClient(t, claimedJob("h", 30), tt.response)
			w, logs := newTestWorker(c)
			w.Handle("h", noopHandler)

			ctx, cancel := testContext(t)
			defer cancel()
			if err := w.Run(ctx); !stopsWorker(err) {
				t.Errorf("Run: got %v", err)
			}
			if got := api.requestCount(); got != 2 {
				t.Errorf("got %d requests, want 2", got)
			}
			for _, want := range []string{testExecutionID, `"result_recorded":false`} {
				if !strings.Contains(logs.String(), want) {
					t.Errorf("logs missing %s: %s", want, logs)
				}
			}
		})
	}
}

func TestRejectedKeyAfterCancelIsLoggedAndRunReturnsNil(t *testing.T) {
	c, api := newTestClient(t, claimedJob("h", 30), keyRejected)
	w, logs := newTestWorker(c)
	ctx, cancel := testContext(t)
	defer cancel()
	w.Handle("h", func(context.Context, *ClaimedJob) (map[string]any, error) {
		cancel()
		return nil, nil
	})

	if err := w.Run(ctx); err != nil {
		t.Errorf("Run: got %v", err)
	}
	if got := api.requestCount(); got != 2 {
		t.Errorf("got %d requests, want 2", got)
	}
	if !strings.Contains(logs.String(), `"result_recorded":false`) {
		t.Errorf("logs: %s", logs)
	}
}

func TestClaimFailureWaitsThenClaimsAgain(t *testing.T) {
	tests := []struct {
		name     string
		response fakeResponse
		want     time.Duration
		log      string
	}{
		{"quota used up", apiFailure(http.StatusForbidden, "plan_limit_exceeded", "limit"), time.Minute, "execution quota"},
		{"other 403", apiFailure(http.StatusForbidden, "forbidden", "no"), time.Second, "claim failed"},
		{"500", apiFailure(http.StatusInternalServerError, "internal_error", "oops"), time.Second, "claim failed"},
		{"Retry-After is honoured", rateLimited("3"), 3 * time.Second, "claim failed"},
		{"Retry-After is capped", rateLimited("3600"), time.Minute, "claim failed"},
		{"Retry-After has a floor", rateLimited("0"), time.Second, "claim failed"},
		{"invalid body", okResponse(`"not a job"`), time.Second, "claim failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, api := newTestClient(t, tt.response, keyRejected)
			w, logs := newTestWorker(c)
			w.Handle("h", noopHandler)

			runUntilKeyRejected(t, w)

			delays := api.recordedDelays()
			if len(delays) != 1 || delays[0] != tt.want {
				t.Errorf("delays: got %v, want [%s]", delays, tt.want)
			}
			if !strings.Contains(logs.String(), tt.log) {
				t.Errorf("logs missing %q: %s", tt.log, logs)
			}
		})
	}
}

func TestNetworkFailureOnClaimWaitsThenClaimsAgain(t *testing.T) {
	c, api := newTestClient(t, keyRejected)
	requests := 0
	transport := http.DefaultTransport
	c.httpClient = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			return nil, errors.New("connection reset")
		}
		return transport.RoundTrip(r)
	})}
	w, _ := newTestWorker(c)
	w.Handle("h", noopHandler)

	runUntilKeyRejected(t, w)

	if delays := api.recordedDelays(); len(delays) != 1 || delays[0] != time.Second {
		t.Errorf("delays: got %v", delays)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestReportRetries(t *testing.T) {
	serverError := apiFailure(http.StatusInternalServerError, "internal_error", "oops")
	tests := []struct {
		name     string
		failures []fakeResponse
		log      string
	}{
		{"500 until attempts run out", []fakeResponse{serverError, serverError, serverError}, "failed after retries"},
		{"429 until attempts run out", []fakeResponse{rateLimited(""), rateLimited(""), rateLimited("")}, "failed after retries"},
		{
			"408 until attempts run out",
			[]fakeResponse{{status: 408}, {status: 408}, {status: 408}},
			"failed after retries",
		},
		{"invalid body until attempts run out", []fakeResponse{{status: 200}, {status: 200}, {status: 200}}, "failed after retries"},
		{"400 is final", []fakeResponse{apiFailure(400, "bad_request", "no")}, "rejected by the API"},
		{"403 is final", []fakeResponse{apiFailure(403, "forbidden", "no")}, "rejected by the API"},
		{"404 is final", []fakeResponse{apiFailure(404, "execution_not_found", "no")}, "rejected by the API"},
		{"422 is final", []fakeResponse{apiFailure(422, "validation_error", "no")}, "rejected by the API"},
		{"failure envelope with 200 is final", []fakeResponse{apiFailure(200, "error", "no")}, "rejected by the API"},
		{"409 is discarded", []fakeResponse{apiFailure(409, "execution_not_running", "no")}, "already finished"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			responses := append([]fakeResponse{claimedJob("h", 30)}, tt.failures...)
			c, api := newTestClient(t, append(responses, keyRejected)...)
			w, logs := newTestWorker(c)
			w.Handle("h", noopHandler)

			runUntilKeyRejected(t, w)

			if got, want := api.requestCount(), len(tt.failures)+2; got != want {
				t.Errorf("got %d requests, want %d", got, want)
			}
			if !strings.Contains(logs.String(), tt.log) {
				t.Errorf("logs missing %q: %s", tt.log, logs)
			}
		})
	}
}

func TestReportRecoversOnRetry(t *testing.T) {
	c, api := newTestClient(t, claimedJob("h", 30), rateLimited("5"), resultRecorded, keyRejected)
	w, logs := newTestWorker(c)
	w.Handle("h", noopHandler)

	runUntilKeyRejected(t, w)

	if delays := api.recordedDelays(); len(delays) != 1 || delays[0] != 5*time.Second {
		t.Errorf("delays: got %v", delays)
	}
	if got := api.requestBody(t, 1); got != api.requestBody(t, 2) {
		t.Errorf("retry changed the body: %s then %s", got, api.requestBody(t, 2))
	}
	if strings.Contains(logs.String(), `"level":"ERROR","msg":"chronos: result`) {
		t.Errorf("logs: %s", logs)
	}
}

func TestReportAfterCancelStillSendsEveryAttemptWithoutWaiting(t *testing.T) {
	serverError := apiFailure(http.StatusInternalServerError, "internal_error", "oops")
	c, api := newTestClient(t, claimedJob("h", 30), serverError)
	c.sleep = func(ctx context.Context, _ time.Duration) error {
		if ctx.Err() == nil {
			t.Error("report waited on a live context")
		}
		return ctx.Err()
	}
	w, _ := newTestWorker(c)
	ctx, cancel := testContext(t)
	defer cancel()
	w.Handle("h", func(context.Context, *ClaimedJob) (map[string]any, error) {
		cancel()
		return nil, nil
	})

	if err := w.Run(ctx); err != nil {
		t.Errorf("Run: got %v", err)
	}
	if got := api.requestCount(); got != 4 {
		t.Errorf("got %d requests, want a claim and 3 reports", got)
	}
}

func TestCancelDuringLongPollReturnsNil(t *testing.T) {
	reached := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(reached)
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	c, err := NewClient(testAPIKey, WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	w, logs := newTestWorker(c)
	w.Handle("h", noopHandler)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	waitClosed(t, reached, "the claim")
	cancel()
	if err := awaitRun(t, done); err != nil {
		t.Errorf("Run: got %v", err)
	}
	if got := logs.String(); got != "" {
		t.Errorf("logged a clean shutdown: %s", got)
	}
	assertNoWorkerGoroutines(t)
}

func TestCancelDuringClaimFailureWaitReturnsNil(t *testing.T) {
	c, _ := newTestClient(t, apiFailure(http.StatusForbidden, "plan_limit_exceeded", "limit"))
	c.sleep = sleep
	w, logs := newTestWorker(c)
	w.Handle("h", noopHandler)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	waitFor(t, "the pause", func() bool { return strings.Contains(logs.String(), "execution quota") })
	cancel()
	if err := awaitRun(t, done); err != nil {
		t.Errorf("Run: got %v", err)
	}
}

func TestCancelMidJobLetsTheHandlerFinishAndReports(t *testing.T) {
	type ctxKey struct{}
	c, api := newTestClient(t, claimedJob("h", 30), resultRecorded)
	w, _ := newTestWorker(c)

	started := make(chan struct{})
	blocked, release := newRelease(t)
	var handlerCtxErr error
	var handlerCtxValue any
	w.Handle("h", func(ctx context.Context, _ *ClaimedJob) (map[string]any, error) {
		close(started)
		<-blocked
		handlerCtxErr = ctx.Err()
		handlerCtxValue = ctx.Value(ctxKey{})
		return map[string]any{"ok": true}, nil
	})

	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), ctxKey{}, "kept"))
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	waitClosed(t, started, "the handler")
	cancel()
	select {
	case <-done:
		t.Fatal("Run returned while the handler was running")
	case <-time.After(50 * time.Millisecond):
	}
	release()

	if err := awaitRun(t, done); err != nil {
		t.Errorf("Run: got %v", err)
	}
	if handlerCtxErr != nil {
		t.Errorf("handler context ended on shutdown: %v", handlerCtxErr)
	}
	if handlerCtxValue != "kept" {
		t.Errorf("handler context value: got %v", handlerCtxValue)
	}
	if got := api.requestCount(); got != 2 {
		t.Fatalf("got %d requests, want a claim and a report", got)
	}
	if got := api.requestBody(t, 1); got != `{"status":"completed","result":{"ok":true}}` {
		t.Errorf("got %s", got)
	}
	assertNoWorkerGoroutines(t)
}

func TestInvalidSetupReturnsAnErrorBeforeAnyRequest(t *testing.T) {
	tests := []struct {
		name  string
		opts  []WorkerOption
		setup func(*Worker)
		want  string
	}{
		{"no handlers", nil, func(*Worker) {}, "at least one handler"},
		{"negative poll wait", []WorkerOption{WithPollWait(-time.Second)}, nil, "poll wait"},
		{"poll wait above the maximum", []WorkerOption{WithPollWait(21 * time.Second)}, nil, "poll wait"},
		{"partial second poll wait", []WorkerOption{WithPollWait(1500 * time.Millisecond)}, nil, "poll wait"},
		{"blank worker id", []WorkerOption{WithWorkerID("  ")}, nil, "worker id"},
		{"long worker id", []WorkerOption{WithWorkerID(strings.Repeat("a", 256))}, nil, "worker id"},
		{"nil logger", []WorkerOption{WithWorkerLogger(nil)}, nil, "logger"},
		{"blank handler name", nil, func(w *Worker) { w.Handle(" ", noopHandler) }, "handler name"},
		{"long handler name", nil, func(w *Worker) { w.Handle(strings.Repeat("😀", 128), noopHandler) }, "handler name"},
		{"nil handler", nil, func(w *Worker) { w.Handle("h", nil) }, "must not be nil"},
		{
			"duplicate handler",
			nil,
			func(w *Worker) {
				w.Handle("h", noopHandler)
				w.Handle(" h ", noopHandler)
			},
			"already registered",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, api := newTestClient(t, keyRejected)
			w := NewWorker(c, tt.opts...)
			setup := tt.setup
			if setup == nil {
				setup = func(w *Worker) { w.Handle("valid", noopHandler) }
			}
			setup(w)

			err := w.Run(t.Context())

			if !errors.Is(err, ErrInvalidWorker) || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Run: got %v, want ErrInvalidWorker mentioning %q", err, tt.want)
			}
			if got := api.requestCount(); got != 0 {
				t.Errorf("sent %d requests", got)
			}
		})
	}
}

func TestNilClientReturnsAnError(t *testing.T) {
	w := NewWorker(nil)
	w.Handle("h", noopHandler)
	if err := w.Run(t.Context()); !errors.Is(err, ErrInvalidWorker) || !strings.Contains(err.Error(), "client") {
		t.Errorf("Run: got %v", err)
	}
}

func TestSecondRunAndLateHandleWhileRunning(t *testing.T) {
	c, api := newTestClient(t, claimedJob("h", 30), resultRecorded, keyRejected)
	w, logs := newTestWorker(c)
	started := make(chan struct{})
	blocked, release := newRelease(t)
	w.Handle("h", func(context.Context, *ClaimedJob) (map[string]any, error) {
		close(started)
		<-blocked
		return nil, nil
	})

	ctx, cancel := testContext(t)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	waitClosed(t, started, "the handler")

	if err := w.Run(ctx); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("second Run: got %v", err)
	}
	w.Handle("late", noopHandler)
	if !strings.Contains(logs.String(), "handler not registered") {
		t.Errorf("logs: %s", logs)
	}

	release()
	if err := awaitRun(t, done); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("Run: got %v", err)
	}
	if got := api.requestBody(t, 2); strings.Contains(got, "late") {
		t.Errorf("late handler was claimed for: %s", got)
	}
}

func TestRunCanBeCalledAgainAfterItReturns(t *testing.T) {
	c, api := newTestClient(t, keyRejected)
	w, _ := newTestWorker(c)
	w.Handle("h", noopHandler)

	runUntilKeyRejected(t, w)
	runUntilKeyRejected(t, w)

	if got := api.requestCount(); got != 2 {
		t.Errorf("got %d requests, want 2", got)
	}
}
