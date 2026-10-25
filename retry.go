package chronos

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"
)

const (
	retryBaseDelay     = 500 * time.Millisecond
	retryMaxDelay      = 8 * time.Second
	maxRetryAfter      = 60 * time.Second
	retryJitterPortion = 0.25
)

func isRetryable(err error, method string) bool {
	apiErr, isAPIError := errors.AsType[*APIError](err)
	if isAPIError && apiErr.StatusCode == http.StatusTooManyRequests {
		return true
	}
	if method != http.MethodGet {
		return false
	}
	if isAPIError {
		return apiErr.StatusCode >= 500
	}
	_, isNetworkError := errors.AsType[*NetworkError](err)
	return isNetworkError
}

func (c *Client) retryDelay(err error, retry int) (time.Duration, bool) {
	apiErr, ok := errors.AsType[*APIError](err)
	if ok && apiErr.RetryAfter > 0 {
		return apiErr.RetryAfter, apiErr.RetryAfter <= maxRetryAfter
	}
	backoff := min(retryBaseDelay<<min(retry, 10), retryMaxDelay)
	return time.Duration(float64(backoff) * (1 - c.jitter()*retryJitterPortion)), true
}

func parseRetryAfter(value string) time.Duration {
	if !isAllDigits(value) {
		return 0
	}
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || seconds > int64(math.MaxInt64/time.Second) {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(seconds) * time.Second
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
