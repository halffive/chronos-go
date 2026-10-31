package chronos

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is the Chronos API base URL.
	DefaultBaseURL = "https://api.chronos.sh"
	// DefaultRequestTimeout bounds each request attempt.
	DefaultRequestTimeout = 30 * time.Second
	// DefaultMaxRequestRetries is how many times a request that is safe to
	// repeat is retried.
	DefaultMaxRequestRetries = 2
)

// Client calls the Chronos API. Create one with [NewClient] and reuse it; it
// is safe for concurrent use.
type Client struct {
	Schedules  *SchedulesService
	Jobs       *JobsService
	Executions *ExecutionsService
	Account    *AccountService

	baseURL           string
	httpClient        *http.Client
	timeout           time.Duration
	maxRequestRetries int
	jobWaitTimeout    time.Duration
	setHeaders        func(http.Header)
	sleep             func(ctx context.Context, d time.Duration) error
	jitter            func() float64

	common service
}

type service struct {
	client *Client
}

// Option configures a [Client].
type Option func(*options) error

type options struct {
	baseURL           string
	httpClient        *http.Client
	timeout           time.Duration
	maxRequestRetries int
	userAgent         string
}

// WithBaseURL points the client at another Chronos API, such as a local one.
func WithBaseURL(baseURL string) Option {
	return func(o *options) error {
		trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
		if !isBaseURL(trimmed) {
			return errors.New("chronos: base URL must be an absolute http or https URL with no query, fragment or user info")
		}
		o.baseURL = trimmed
		return nil
	}
}

func isBaseURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	if u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") || u.User != nil {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

// WithHTTPClient sets the HTTP client used for requests. The client is
// copied, and the copy never follows redirects, whatever its CheckRedirect.
// Its Timeout, if set, also bounds each attempt.
func WithHTTPClient(httpClient *http.Client) Option {
	return func(o *options) error {
		if httpClient == nil {
			return errors.New("chronos: HTTP client must not be nil")
		}
		copied := *httpClient
		copied.CheckRedirect = stopAtRedirect
		o.httpClient = &copied
		return nil
	}
}

// WithRequestTimeout bounds each request attempt, including reading the
// response. A context deadline bounds all attempts. A [Worker] claim adds its
// poll wait to this timeout. Defaults to [DefaultRequestTimeout].
func WithRequestTimeout(timeout time.Duration) Option {
	return func(o *options) error {
		if timeout <= 0 {
			return fmt.Errorf("chronos: timeout must be positive, got %s", timeout)
		}
		o.timeout = timeout
		return nil
	}
}

// WithMaxRequestRetries sets how many times a request that is safe to repeat
// is retried: any request rejected with 429, and GET requests that fail with
// a network error, a timeout or a 5xx. 0 disables retries. Defaults to
// [DefaultMaxRequestRetries].
func WithMaxRequestRetries(retries int) Option {
	return func(o *options) error {
		if retries < 0 {
			return fmt.Errorf("chronos: max request retries must not be negative, got %d", retries)
		}
		o.maxRequestRetries = retries
		return nil
	}
}

// WithUserAgent adds a product token, such as "my-app/1.2.0", ahead of the
// SDK's own in the User-Agent header.
func WithUserAgent(product string) Option {
	return func(o *options) error {
		trimmed := strings.TrimSpace(product)
		if trimmed == "" {
			return errors.New("chronos: user agent must not be empty")
		}
		if strings.ContainsFunc(trimmed, isHeaderControl) {
			return errors.New("chronos: user agent must not contain control characters")
		}
		o.userAgent = trimmed
		return nil
	}
}

// NewClient returns a client that authenticates with apiKey. The API checks
// the key on the first request; call [AccountService.Get] to check it up
// front.
func NewClient(apiKey string, opts ...Option) (*Client, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, errors.New("chronos: API key is required")
	}
	if strings.ContainsFunc(apiKey, isHeaderControl) {
		return nil, errors.New("chronos: API key must not contain control characters")
	}

	o := options{
		baseURL:           DefaultBaseURL,
		httpClient:        &http.Client{CheckRedirect: stopAtRedirect},
		timeout:           DefaultRequestTimeout,
		maxRequestRetries: DefaultMaxRequestRetries,
	}
	for _, opt := range opts {
		if err := opt(&o); err != nil {
			return nil, err
		}
	}

	authorization := "Bearer " + apiKey
	userAgent := "chronos-go/" + Version
	if o.userAgent != "" {
		userAgent = o.userAgent + " " + userAgent
	}

	c := &Client{
		baseURL:           o.baseURL,
		httpClient:        o.httpClient,
		timeout:           o.timeout,
		maxRequestRetries: o.maxRequestRetries,
		jobWaitTimeout:    defaultJobWaitTimeout,
		setHeaders: func(h http.Header) {
			h.Set("Authorization", authorization)
			h.Set("User-Agent", userAgent)
			h.Set("Content-Type", "application/json")
		},
		sleep:  sleep,
		jitter: rand.Float64,
	}
	c.common.client = c
	c.Schedules = (*SchedulesService)(&c.common)
	c.Jobs = (*JobsService)(&c.common)
	c.Executions = (*ExecutionsService)(&c.common)
	c.Account = (*AccountService)(&c.common)
	return c, nil
}

func stopAtRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

func isHeaderControl(r rune) bool {
	if r == '\t' {
		return false
	}
	return r < ' ' || r == 0x7f
}
