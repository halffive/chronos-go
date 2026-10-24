package chronos

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Sentinel errors for use with [errors.Is].
var (
	// ErrUnauthorized matches an [*APIError] with status 401: the API key is
	// missing, malformed, unknown or revoked.
	ErrUnauthorized = errors.New("chronos: unauthorized")
	// ErrNotFound matches an [*APIError] with status 404.
	ErrNotFound = errors.New("chronos: not found")
	// ErrRateLimited matches an [*APIError] with status 429.
	ErrRateLimited = errors.New("chronos: rate limited")
	// ErrTimeout matches a [*NetworkError] for an attempt that timed out.
	ErrTimeout = errors.New("chronos: request timed out")
	// ErrInvalidResponse is wrapped when a successful response does not have
	// the shape the API documents. The message carries the status and request
	// id.
	ErrInvalidResponse = errors.New("chronos: invalid response from the API")
	// ErrInvalidID is returned, before any request, for an id that is empty
	// or would change the request path.
	ErrInvalidID = errors.New("chronos: invalid id")
	// ErrInvalidWebhook is wrapped by every [Webhook.Verify] failure.
	ErrInvalidWebhook = errors.New("chronos: invalid webhook")
)

// ErrorCode is the code field of an API error. The API can add codes, so
// keep a default branch when switching on one.
type ErrorCode string

// Error codes the schedule, job and execution routes return.
const (
	ErrorCodeError                  ErrorCode = "error"
	ErrorCodeBadRequest             ErrorCode = "bad_request"
	ErrorCodeUnauthorized           ErrorCode = "unauthorized"
	ErrorCodeForbidden              ErrorCode = "forbidden"
	ErrorCodeNotFound               ErrorCode = "not_found"
	ErrorCodeConflict               ErrorCode = "conflict"
	ErrorCodeValidationError        ErrorCode = "validation_error"
	ErrorCodeRateLimitExceeded      ErrorCode = "rate_limit_exceeded"
	ErrorCodePlanLimitExceeded      ErrorCode = "plan_limit_exceeded"
	ErrorCodeInternalError          ErrorCode = "internal_error"
	ErrorCodeScheduleNotFound       ErrorCode = "schedule_not_found"
	ErrorCodeScheduleArchived       ErrorCode = "schedule_archived"
	ErrorCodeScheduleCompleted      ErrorCode = "schedule_completed"
	ErrorCodeScheduleAlreadyPaused  ErrorCode = "schedule_already_paused"
	ErrorCodeScheduleAlreadyActive  ErrorCode = "schedule_already_active"
	ErrorCodeScheduleStatusChanged  ErrorCode = "schedule_status_changed"
	ErrorCodeScheduleCadenceInvalid ErrorCode = "schedule_cadence_invalid"
	ErrorCodeHandlerRequiredForPull ErrorCode = "handler_required_for_pull"
	ErrorCodeJobNotFound            ErrorCode = "job_not_found"
	ErrorCodeJobNotCancellable      ErrorCode = "job_not_cancellable"
	ErrorCodeJobNotScheduled        ErrorCode = "job_not_scheduled"
	ErrorCodeJobNotSkippable        ErrorCode = "job_not_skippable"
	ErrorCodeJobNotFailed           ErrorCode = "job_not_failed"
	ErrorCodeJobStatusChanged       ErrorCode = "job_status_changed"
	ErrorCodeIdempotencyKeyConflict ErrorCode = "idempotency_key_conflict"
	ErrorCodeExecutionNotFound      ErrorCode = "execution_not_found"
	ErrorCodeExecutionNotRunning    ErrorCode = "execution_not_running"
)

// APIError is returned when the API answers with a non-2xx status or
// "success": false. Read it with [errors.AsType], and test the status with
// [errors.Is] and [ErrUnauthorized], [ErrNotFound] or [ErrRateLimited].
type APIError struct {
	StatusCode int
	// Code is empty when the body was not an API error envelope.
	Code    ErrorCode
	Message string
	// RequestID is the X-Request-Id response header. Quote it when asking
	// for support.
	RequestID string
	// RetryAfter is the Retry-After header on a 429 or 5xx, or 0.
	RetryAfter time.Duration
	// RateLimit is the X-RateLimit-* budget on responses other than 429.
	RateLimit   *RateLimit
	FieldErrors []FieldError
	Body        []byte
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "chronos: %s (status %d", e.Message, e.StatusCode)
	if e.Code != "" {
		fmt.Fprintf(&b, ", code %s", e.Code)
	}
	if e.RequestID != "" {
		fmt.Fprintf(&b, ", request id %s", e.RequestID)
	}
	b.WriteString(")")
	return b.String()
}

// Is reports whether target is the sentinel for e's status.
func (e *APIError) Is(target error) bool {
	switch target {
	case ErrUnauthorized:
		return e.StatusCode == http.StatusUnauthorized
	case ErrNotFound:
		return e.StatusCode == http.StatusNotFound
	case ErrRateLimited:
		return e.StatusCode == http.StatusTooManyRequests
	}
	return false
}

// FieldError is one invalid field of a rejected request, usually a 422
// validation_error.
type FieldError struct {
	Message string `json:"message"`
	// Field is a dotted path such as "delivery.http.url". A rule across
	// several fields gives their names, comma-separated ("cron, interval").
	// Empty when the rule names no field.
	Field string `json:"field"`
	// Value is the rejected value. The API omits objects and secrets.
	Value any `json:"value"`
}

// RateLimit is the request budget from the X-RateLimit-* headers.
type RateLimit struct {
	Limit     int
	Remaining int
	// Reset is the time until the window resets.
	Reset time.Duration
}

// NetworkError is returned when a request gets no usable response: a DNS
// failure, a refused or reset connection, or a timeout.
//
// A timeout also matches [context.DeadlineExceeded]. Cancelling your own
// context, or reaching its deadline, returns the context's error instead, so
// check ctx.Err() to tell the two apart.
type NetworkError struct {
	Err      error
	timedOut bool
}

func (e *NetworkError) Error() string {
	prefix := "chronos: request failed"
	if e.timedOut {
		prefix = "chronos: request timed out"
	}
	if e.Err == nil {
		return prefix
	}
	return prefix + ": " + e.Err.Error()
}

func (e *NetworkError) Unwrap() error { return e.Err }

// Timeout reports whether the attempt timed out.
func (e *NetworkError) Timeout() bool { return e.timedOut }

// Is reports whether e is a timeout and target is [ErrTimeout] or
// [context.DeadlineExceeded].
func (e *NetworkError) Is(target error) bool {
	if !e.timedOut {
		return false
	}
	return target == ErrTimeout || target == context.DeadlineExceeded
}

// WaitTimeoutError is returned by [JobsService.Wait] when the job is still
// not finished at the deadline. It matches [context.DeadlineExceeded].
type WaitTimeoutError struct {
	// Job is the last state seen, or nil if no request finished in time.
	Job *Job
}

func (e *WaitTimeoutError) Error() string {
	if e.Job == nil {
		return "chronos: timed out waiting for the job"
	}
	return fmt.Sprintf("chronos: timed out waiting for job %s (status %s)", e.Job.ID, e.Job.Status)
}

func (e *WaitTimeoutError) Unwrap() error { return context.DeadlineExceeded }
