package chronos

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestErrorResponsesMapToAPIError(t *testing.T) {
	tests := []struct {
		name     string
		response fakeResponse
		status   int
		code     ErrorCode
		message  string
		sentinel error
	}{
		{
			name:     "401 unauthorized",
			response: apiFailure(401, "unauthorized", "Invalid API key"),
			status:   401, code: ErrorCodeUnauthorized, message: "Invalid API key", sentinel: ErrUnauthorized,
		},
		{
			name:     "404 not found",
			response: apiFailure(404, "schedule_not_found", "Schedule not found"),
			status:   404, code: ErrorCodeScheduleNotFound, message: "Schedule not found", sentinel: ErrNotFound,
		},
		{
			name:     "409 already paused",
			response: apiFailure(409, "schedule_already_paused", "Schedule is already paused"),
			status:   409, code: ErrorCodeScheduleAlreadyPaused, message: "Schedule is already paused",
		},
		{
			name:     "409 idempotency conflict",
			response: apiFailure(409, "idempotency_key_conflict", "Idempotency key already used"),
			status:   409, code: ErrorCodeIdempotencyKeyConflict, message: "Idempotency key already used",
		},
		{
			name:     "403 plan limit",
			response: apiFailure(403, "plan_limit_exceeded", "Plan limit reached"),
			status:   403, code: ErrorCodePlanLimitExceeded, message: "Plan limit reached",
		},
		{
			name:     "429 rate limited",
			response: apiFailure(429, "rate_limit_exceeded", "Too many requests"),
			status:   429, code: ErrorCodeRateLimitExceeded, message: "Too many requests", sentinel: ErrRateLimited,
		},
		{
			name:     "500",
			response: apiFailure(500, "internal_error", "An unknown error occurred"),
			status:   500, code: ErrorCodeInternalError, message: "An unknown error occurred",
		},
		{
			name:     "non-JSON 502 from a proxy",
			response: fakeResponse{status: 502, body: "<html>Bad Gateway</html>"},
			status:   502, message: "request failed",
		},
		{
			name:     "401 without an envelope",
			response: fakeResponse{status: 401, body: "Unauthorized"},
			status:   401, message: "request failed", sentinel: ErrUnauthorized,
		},
		{
			name:     "200 with success false",
			response: fakeResponse{status: 200, body: `{"success":false,"message":"Not processed","code":"error"}`},
			status:   200, code: ErrorCodeError, message: "Not processed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, tt.response)
			c.maxRequestRetries = 0
			_, err := c.Schedules.Get(t.Context(), scheduleID)

			apiErr, ok := errors.AsType[*APIError](err)
			if !ok {
				t.Fatalf("error = %v, want *APIError", err)
			}
			if apiErr.StatusCode != tt.status || apiErr.Code != tt.code || apiErr.Message != tt.message {
				t.Errorf("got status %d code %q message %q", apiErr.StatusCode, apiErr.Code, apiErr.Message)
			}
			if apiErr.Body == nil {
				t.Error("Body is empty")
			}
			if tt.sentinel != nil && !errors.Is(err, tt.sentinel) {
				t.Errorf("errors.Is(err, %v) = false", tt.sentinel)
			}
		})
	}
}

func TestSentinelsMatchOnlyTheirStatus(t *testing.T) {
	err := &APIError{StatusCode: 404}
	if errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrRateLimited) {
		t.Error("a 404 matched another status's sentinel")
	}
}

func TestValidationErrorCarriesFieldErrors(t *testing.T) {
	c, _ := newTestClient(t, fakeResponse{status: 422, body: `{
		"success": false,
		"message": "request body failed validation",
		"code": "validation_error",
		"errors": [
			{"message": "\"delivery.http.url\" must be a valid uri", "field": "delivery.http.url", "value": "nope"},
			{"message": "provide either cron or interval, not both", "field": "cron, interval"},
			{"message": "at least one field must be provided", "field": null},
			"not-an-object"
		]
	}`})
	_, err := c.Schedules.Create(t.Context(), ScheduleCreateParams{Name: "x"})

	apiErr, ok := errors.AsType[*APIError](err)
	if !ok {
		t.Fatalf("error = %v", err)
	}
	want := []FieldError{
		{Message: `"delivery.http.url" must be a valid uri`, Field: "delivery.http.url", Value: "nope"},
		{Message: "provide either cron or interval, not both", Field: "cron, interval"},
		{Message: "at least one field must be provided"},
	}
	if !reflect.DeepEqual(apiErr.FieldErrors, want) {
		t.Errorf("FieldErrors = %+v, want %+v", apiErr.FieldErrors, want)
	}
}

func TestErrorHeadersAreRead(t *testing.T) {
	c, _ := newTestClient(t, fakeResponse{
		status: 503,
		header: map[string]string{
			"X-Request-Id":          "req_123",
			"Retry-After":           "7",
			"X-RateLimit-Limit":     "100",
			"X-RateLimit-Remaining": "4",
			"X-RateLimit-Reset":     "9",
		},
		body: `{"success":false,"message":"Unavailable","code":"error"}`,
	})
	c.maxRequestRetries = 0
	_, err := c.Schedules.Get(t.Context(), scheduleID)

	apiErr, ok := errors.AsType[*APIError](err)
	if !ok {
		t.Fatalf("error = %v", err)
	}
	if apiErr.RequestID != "req_123" || apiErr.RetryAfter != 7*time.Second {
		t.Errorf("RequestID %q RetryAfter %s", apiErr.RequestID, apiErr.RetryAfter)
	}
	if want := (RateLimit{Limit: 100, Remaining: 4, Reset: 9 * time.Second}); apiErr.RateLimit == nil || *apiErr.RateLimit != want {
		t.Errorf("RateLimit = %+v, want %+v", apiErr.RateLimit, want)
	}
	if !strings.Contains(err.Error(), "req_123") {
		t.Errorf("Error() = %q, want the request id", err.Error())
	}
}

func TestRetryAfterIgnoredOnOtherStatuses(t *testing.T) {
	c, _ := newTestClient(t, fakeResponse{status: 409, header: map[string]string{"Retry-After": "5"}, body: `{"success":false,"message":"x","code":"conflict"}`})
	_, err := c.Schedules.Pause(t.Context(), scheduleID)

	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.RetryAfter != 0 {
		t.Errorf("RetryAfter on a 409 = %v", err)
	}
}

func TestInvalidSuccessResponses(t *testing.T) {
	tests := map[string]fakeResponse{
		"not JSON":            {status: 200, body: "<html></html>"},
		"no envelope":         {status: 200, body: `{"id":"x"}`},
		"JSON array":          {status: 200, body: `[]`},
		"missing data":        {status: 200, body: `{"success":true}`},
		"null data":           {status: 200, body: `{"success":true,"data":null}`},
		"data of wrong shape": {status: 200, body: `{"success":true,"data":"nope"}`},
	}
	for name, res := range tests {
		t.Run(name, func(t *testing.T) {
			c, _ := newTestClient(t, res)
			if _, err := c.Schedules.Get(t.Context(), scheduleID); !errors.Is(err, ErrInvalidResponse) {
				t.Errorf("error = %v, want ErrInvalidResponse", err)
			}
		})
	}
}

func TestListWithoutPagingIsInvalid(t *testing.T) {
	c, _ := newTestClient(t, fakeResponse{status: 200, header: map[string]string{"X-Request-Id": "req_7"}, body: `{"success":true,"data":[]}`})
	_, err := c.Schedules.List(t.Context(), ScheduleListParams{})
	if !errors.Is(err, ErrInvalidResponse) || !strings.Contains(err.Error(), "request id req_7") {
		t.Errorf("error = %v, want ErrInvalidResponse with the request id", err)
	}
}

func TestRequestTimeoutIsATimeoutError(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); server.Close() })

	c, err := NewClient(testAPIKey, WithBaseURL(server.URL), WithRequestTimeout(20*time.Millisecond), WithMaxRequestRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Schedules.Get(t.Context(), scheduleID)

	netErr, ok := errors.AsType[*NetworkError](err)
	if !ok || !netErr.Timeout() || !errors.Is(err, ErrTimeout) {
		t.Fatalf("error = %v, want a timeout NetworkError", err)
	}
}

func TestTimeoutCoversTheBodyRead(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); server.Close() })

	c, err := NewClient(testAPIKey, WithBaseURL(server.URL), WithRequestTimeout(20*time.Millisecond), WithMaxRequestRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Schedules.Get(t.Context(), scheduleID); !errors.Is(err, ErrTimeout) {
		t.Fatalf("error = %v, want ErrTimeout", err)
	}
}

func TestCallerCancellationReturnsTheContextError(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	c, err := NewClient(testAPIKey, WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Schedules.Get(ctx, scheduleID)
	if err != context.Canceled {
		t.Fatalf("error = %#v, want context.Canceled", err)
	}
}

func TestCancelledContextSendsNothing(t *testing.T) {
	c, api := newTestClient(t, scheduleResponse(t))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.Schedules.Get(ctx, scheduleID); err != context.Canceled {
		t.Errorf("error = %v", err)
	}
	if api.requestCount() != 0 {
		t.Error("a request was sent on a cancelled context")
	}
}

func TestNetworkFailureIsANetworkError(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()

	c, err := NewClient(testAPIKey, WithBaseURL(url), WithMaxRequestRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Schedules.Get(t.Context(), scheduleID)

	netErr, ok := errors.AsType[*NetworkError](err)
	if !ok || netErr.Timeout() {
		t.Fatalf("error = %v, want a non-timeout NetworkError", err)
	}
}

func TestNoContentWhereDataIsExpectedIsInvalid(t *testing.T) {
	c, _ := newTestClient(t, fakeResponse{status: http.StatusNoContent})
	if _, err := c.Jobs.Get(t.Context(), jobID); !errors.Is(err, ErrInvalidResponse) {
		t.Errorf("error = %v, want ErrInvalidResponse", err)
	}
}

func TestNullListItemIsInvalid(t *testing.T) {
	c, _ := newTestClient(t, okPage(`[null]`, 1, 1))
	_, err := c.Jobs.List(t.Context(), JobListParams{})
	if !errors.Is(err, ErrInvalidResponse) || !strings.Contains(err.Error(), "status 200") {
		t.Errorf("error = %v, want ErrInvalidResponse with the status", err)
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	c, api := newTestClient(t, fakeResponse{status: http.StatusMovedPermanently, header: map[string]string{"Location": "/elsewhere"}})
	_, err := c.Schedules.Update(t.Context(), scheduleID, ScheduleUpdateParams{Name: new("x")})
	if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.StatusCode != http.StatusMovedPermanently {
		t.Fatalf("error = %v, want a 301 APIError", err)
	}
	if api.requestCount() != 1 {
		t.Errorf("requests = %d, want 1", api.requestCount())
	}
}

type bareResponseTransport struct{ body string }

func (t bareResponseTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(t.body))}, nil
}

func TestTransportResponseWithoutRequestIsHandled(t *testing.T) {
	body := `{"success":true,"data":` + fixture(t, "schedule.json") + `}`
	c, err := NewClient(testAPIKey, WithHTTPClient(&http.Client{Transport: bareResponseTransport{body: body}}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Schedules.Get(t.Context(), scheduleID); err != nil {
		t.Fatal(err)
	}
}

func TestCustomHTTPClientDoesNotFollowRedirects(t *testing.T) {
	var mu sync.Mutex
	targetHits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		targetHits++
		mu.Unlock()
	}))
	t.Cleanup(target.Close)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(origin.Close)

	callerClient := &http.Client{}
	c, err := NewClient(testAPIKey, WithBaseURL(origin.URL), WithHTTPClient(callerClient))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Schedules.Create(t.Context(), ScheduleCreateParams{Name: "x"})
	if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("error = %v, want a 307 APIError", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if targetHits != 0 {
		t.Errorf("redirect target received %d requests", targetHits)
	}
	if callerClient.CheckRedirect != nil {
		t.Error("the caller's client was modified")
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

type netTimeoutTransport struct{}

func (netTimeoutTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, timeoutError{}
}

func TestNetTimeoutMatchesBothTimeoutSentinels(t *testing.T) {
	c, err := NewClient(testAPIKey, WithHTTPClient(&http.Client{Transport: netTimeoutTransport{}}), WithMaxRequestRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Schedules.Get(t.Context(), scheduleID)
	if !errors.Is(err, ErrTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want ErrTimeout and context.DeadlineExceeded", err)
	}
}

type wrappedDeadlineTransport struct{}

func (wrappedDeadlineTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("proxy: %w", context.DeadlineExceeded)
}

func TestWrappedDeadlineFromATransportIsATimeout(t *testing.T) {
	c, err := NewClient(testAPIKey, WithHTTPClient(&http.Client{Transport: wrappedDeadlineTransport{}}), WithMaxRequestRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Schedules.Get(t.Context(), scheduleID); !errors.Is(err, ErrTimeout) {
		t.Errorf("error = %v, want ErrTimeout", err)
	}
}

func TestInvalidResponseNamesTheRequest(t *testing.T) {
	c, _ := newTestClient(t, fakeResponse{status: 200, header: map[string]string{"X-Request-Id": "req_9"}, body: "<html></html>"})
	_, err := c.Schedules.Get(t.Context(), scheduleID)
	if !errors.Is(err, ErrInvalidResponse) || !strings.Contains(err.Error(), "status 200, request id req_9") {
		t.Errorf("error = %v", err)
	}
}

func TestHTTPClientTimeoutIsATimeout(t *testing.T) {
	c, err := NewClient(testAPIKey, WithBaseURL(hangingServer(t)), WithHTTPClient(&http.Client{Timeout: 20 * time.Millisecond}), WithMaxRequestRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Schedules.Get(t.Context(), scheduleID); !errors.Is(err, ErrTimeout) {
		t.Fatalf("error = %v, want ErrTimeout", err)
	}
}

func TestEachRetryGetsAFreshTimeout(t *testing.T) {
	var mu sync.Mutex
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		first := attempts == 1
		mu.Unlock()
		if first {
			<-r.Context().Done()
			return
		}
		io.WriteString(w, `{"success":true,"data":`+fixture(t, "schedule.json")+`}`)
	}))
	t.Cleanup(server.Close)

	c, err := NewClient(testAPIKey, WithBaseURL(server.URL), WithRequestTimeout(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	c.sleep = func(context.Context, time.Duration) error { return nil }
	if _, err := c.Schedules.Get(t.Context(), scheduleID); err != nil {
		t.Fatalf("second attempt failed: %v", err)
	}
}

func TestZeroNetworkErrorPrints(t *testing.T) {
	if got := (&NetworkError{}).Error(); got != "chronos: request failed" {
		t.Errorf("Error() = %q", got)
	}
}

func hangingServer(t *testing.T) string {
	t.Helper()
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); server.Close() })
	return server.URL
}
