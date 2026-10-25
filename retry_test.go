package chronos

import (
	"context"
	"errors"
	"math"
	"net/http"
	"slices"
	"testing"
	"time"
)

func unavailable() fakeResponse {
	return apiFailure(http.StatusServiceUnavailable, "error", "Unavailable")
}

func rateLimited(retryAfter string) fakeResponse {
	res := apiFailure(http.StatusTooManyRequests, "rate_limit_exceeded", "Too many requests")
	if retryAfter != "" {
		res.header = map[string]string{"Retry-After": retryAfter}
	}
	return res
}

func TestGetRetriesOn5xxUpToTheLimit(t *testing.T) {
	c, api := newTestClient(t, unavailable())
	if _, err := c.Schedules.Get(t.Context(), scheduleID); err == nil {
		t.Fatal("want an error")
	}
	if api.requestCount() != 1+DefaultMaxRequestRetries {
		t.Errorf("requests = %d, want %d", api.requestCount(), 1+DefaultMaxRequestRetries)
	}
	if want := []time.Duration{500 * time.Millisecond, time.Second}; !slices.Equal(api.recordedDelays(), want) {
		t.Errorf("delays = %v, want %v", api.recordedDelays(), want)
	}
}

func TestGetRecoversAfterARetry(t *testing.T) {
	c, api := newTestClient(t, unavailable(), scheduleResponse(t))
	if _, err := c.Schedules.Get(t.Context(), scheduleID); err != nil {
		t.Fatal(err)
	}
	if api.requestCount() != 2 {
		t.Errorf("requests = %d, want 2", api.requestCount())
	}
}

func TestWritesDoNotRetryOn5xx(t *testing.T) {
	calls := map[string]func(c *Client) error{
		"schedule create": func(c *Client) error {
			_, err := c.Schedules.Create(t.Context(), ScheduleCreateParams{Name: "x"})
			return err
		},
		"job create with an idempotency key": func(c *Client) error {
			_, err := c.Jobs.Create(t.Context(), JobCreateParams{Name: "x", IdempotencyKey: new("k")})
			return err
		},
		"schedule trigger": func(c *Client) error {
			_, err := c.Schedules.Trigger(t.Context(), scheduleID)
			return err
		},
		"schedule update": func(c *Client) error {
			_, err := c.Schedules.Update(t.Context(), scheduleID, ScheduleUpdateParams{Name: new("x")})
			return err
		},
		"schedule delete": func(c *Client) error { return c.Schedules.Delete(t.Context(), scheduleID) },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			c, api := newTestClient(t, unavailable())
			if err := call(c); err == nil {
				t.Fatal("want an error")
			}
			if api.requestCount() != 1 {
				t.Errorf("requests = %d, want 1", api.requestCount())
			}
		})
	}
}

func TestWritesRetryOn429(t *testing.T) {
	c, api := newTestClient(t, rateLimited("2"), scheduleResponse(t))
	if _, err := c.Schedules.Create(t.Context(), ScheduleCreateParams{Name: "x"}); err != nil {
		t.Fatal(err)
	}
	if want := []time.Duration{2 * time.Second}; !slices.Equal(api.recordedDelays(), want) {
		t.Errorf("delays = %v, want %v", api.recordedDelays(), want)
	}
}

func TestRetryAfterAboveTheCapFailsAtOnce(t *testing.T) {
	c, api := newTestClient(t, rateLimited("61"))
	_, err := c.Schedules.Get(t.Context(), scheduleID)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("error = %v", err)
	}
	if api.requestCount() != 1 {
		t.Errorf("requests = %d, want 1", api.requestCount())
	}
}

func TestRetryAfterZeroFallsBackToBackoff(t *testing.T) {
	c, api := newTestClient(t, rateLimited("0"), scheduleResponse(t))
	if _, err := c.Schedules.Get(t.Context(), scheduleID); err != nil {
		t.Fatal(err)
	}
	if want := []time.Duration{500 * time.Millisecond}; !slices.Equal(api.recordedDelays(), want) {
		t.Errorf("delays = %v, want %v", api.recordedDelays(), want)
	}
}

func TestRetryAfterPastTheDeadlineReturnsTheRateLimit(t *testing.T) {
	c, api := newTestClient(t, rateLimited("30"))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := c.Schedules.Get(ctx, scheduleID); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("error = %v, want ErrRateLimited", err)
	}
	if len(api.recordedDelays()) != 0 {
		t.Errorf("slept %v before giving up", api.recordedDelays())
	}
}

func TestRetriesDisabled(t *testing.T) {
	c, api := newTestClient(t, unavailable())
	c.maxRequestRetries = 0
	if _, err := c.Schedules.Get(t.Context(), scheduleID); err == nil {
		t.Fatal("want an error")
	}
	if api.requestCount() != 1 {
		t.Errorf("requests = %d, want 1", api.requestCount())
	}
}

func TestCancelDuringBackoffStopsRetrying(t *testing.T) {
	c, api := newTestClient(t, unavailable())
	ctx, cancel := context.WithCancel(t.Context())
	c.sleep = func(ctx context.Context, d time.Duration) error {
		cancel()
		return sleep(ctx, d)
	}
	_, err := c.Schedules.Get(ctx, scheduleID)
	if err != context.Canceled {
		t.Fatalf("error = %v", err)
	}
	if api.requestCount() != 1 {
		t.Errorf("requests = %d, want 1", api.requestCount())
	}
}

func TestBackoffDelays(t *testing.T) {
	c := &Client{jitter: func() float64 { return 0 }}
	for retry, want := range []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second} {
		if got, _ := c.retryDelay(errors.New("x"), retry); got != want {
			t.Errorf("retry %d: delay %s, want %s", retry, got, want)
		}
	}
	if got, _ := c.retryDelay(errors.New("x"), 100); got != retryMaxDelay {
		t.Errorf("retry 100: delay %s, want %s", got, retryMaxDelay)
	}

	c.jitter = func() float64 { return 1 }
	if got, _ := c.retryDelay(errors.New("x"), 0); got != 375*time.Millisecond {
		t.Errorf("full jitter: delay %s, want 375ms", got)
	}
}

func TestParseRetryAfter(t *testing.T) {
	tests := map[string]time.Duration{
		"":                              0,
		"5":                             5 * time.Second,
		"-5":                            0,
		"1.5":                           0,
		"Wed, 21 Oct 2026 07:28:00 GMT": 0,
		"99999999999999999999999":       time.Duration(math.MaxInt64),
	}
	for value, want := range tests {
		if got := parseRetryAfter(value); got != want {
			t.Errorf("parseRetryAfter(%q) = %s, want %s", value, got, want)
		}
	}
}
