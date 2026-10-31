package chronos

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

type apiRequest struct {
	method        string
	path          string
	query         url.Values
	body          any
	list          bool
	allowNullData bool
	timeout       time.Duration
}

type preparedRequest struct {
	method        string
	url           string
	body          []byte
	list          bool
	allowNullData bool
	timeout       time.Duration
}

type envelope struct {
	Success *bool           `json:"success"`
	Message string          `json:"message"`
	Code    ErrorCode       `json:"code"`
	Data    json.RawMessage `json:"data"`
	Errors  json.RawMessage `json:"errors"`
	Meta    struct {
		Paging *Paging `json:"paging"`
	} `json:"meta"`
}

type response struct {
	status        int
	header        http.Header
	body          []byte
	envelope      *envelope
	list          bool
	allowNullData bool
}

type retrySkippedError struct {
	err error
}

func (e *retrySkippedError) Error() string { return e.err.Error() }
func (e *retrySkippedError) Unwrap() error { return e.err }

func (c *Client) do(ctx context.Context, r apiRequest, out any) (*Paging, error) {
	req, err := c.prepare(r)
	if err != nil {
		return nil, err
	}
	for retry := 0; ; retry++ {
		paging, err := c.send(ctx, req, out)
		if err == nil {
			return paging, nil
		}
		if retry == c.maxRequestRetries || !isRetryable(err, r.method) {
			return nil, err
		}
		delay, ok := c.retryDelay(err, retry)
		if !ok {
			return nil, err
		}
		if !fitsDeadline(ctx, delay) {
			return nil, &retrySkippedError{err: err}
		}
		if err := c.sleep(ctx, delay); err != nil {
			return nil, err
		}
	}
}

func (c *Client) sendOnce(ctx context.Context, r apiRequest, out any) error {
	req, err := c.prepare(r)
	if err != nil {
		return err
	}
	_, err = c.send(ctx, req, out)
	return err
}

func (c *Client) prepare(r apiRequest) (preparedRequest, error) {
	req := preparedRequest{
		method:        r.method,
		url:           c.baseURL + r.path,
		list:          r.list,
		allowNullData: r.allowNullData,
		timeout:       cmp.Or(r.timeout, c.timeout),
	}
	if len(r.query) > 0 {
		req.url += "?" + r.query.Encode()
	}
	if r.body == nil {
		return req, nil
	}
	body, err := json.Marshal(r.body)
	if err != nil {
		return req, fmt.Errorf("chronos: encode request body: %w", err)
	}
	req.body = body
	return req, nil
}

func fitsDeadline(ctx context.Context, delay time.Duration) bool {
	deadline, ok := ctx.Deadline()
	return !ok || time.Until(deadline) > delay
}

func (c *Client) send(ctx context.Context, r preparedRequest, out any) (*Paging, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	transportError := func(err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return &NetworkError{Err: err, timedOut: attemptCtx.Err() != nil || isTimeout(err)}
	}

	var body io.Reader
	if r.body != nil {
		body = bytes.NewReader(r.body)
	}
	req, err := http.NewRequestWithContext(attemptCtx, r.method, r.url, body)
	if err != nil {
		return nil, fmt.Errorf("chronos: build request: %w", err)
	}
	c.setHeaders(req.Header)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, transportError(err)
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusNoContent && out == nil {
		return nil, nil
	}
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, transportError(err)
	}

	return response{
		status:        res.StatusCode,
		header:        res.Header,
		body:          raw,
		envelope:      parseEnvelope(raw),
		list:          r.list,
		allowNullData: r.allowNullData,
	}.decode(out)
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	netErr, ok := errors.AsType[net.Error](err)
	return ok && netErr.Timeout()
}

func parseEnvelope(raw []byte) *envelope {
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil || env.Success == nil {
		return nil
	}
	return &env
}

func (r response) statusOK() bool {
	return r.status >= 200 && r.status < 300
}

func (r response) rejected() bool {
	if !r.statusOK() {
		return true
	}
	return r.envelope != nil && !*r.envelope.Success
}

func (r response) decode(out any) (*Paging, error) {
	if r.rejected() {
		return nil, r.apiError()
	}
	if r.envelope == nil {
		return nil, r.invalid(ErrInvalidResponse)
	}
	if r.list && !r.envelope.isList() {
		return nil, r.invalid(ErrInvalidResponse)
	}
	if r.allowNullData && bytes.Equal(r.envelope.Data, []byte("null")) {
		return r.envelope.Meta.Paging, nil
	}
	if err := decodeData(r.envelope.Data, out); err != nil {
		return nil, r.invalid(err)
	}
	return r.envelope.Meta.Paging, nil
}

func (e *envelope) isList() bool {
	var items []json.RawMessage
	if e.Meta.Paging == nil || json.Unmarshal(e.Data, &items) != nil {
		return false
	}
	return !slices.ContainsFunc(items, func(item json.RawMessage) bool {
		return bytes.Equal(item, []byte("null"))
	})
}

func (r response) invalid(err error) error {
	if id := r.header.Get("X-Request-Id"); id != "" {
		return fmt.Errorf("%w (status %d, request id %s)", err, r.status, id)
	}
	return fmt.Errorf("%w (status %d)", err, r.status)
}

func decodeData(data json.RawMessage, out any) error {
	if out == nil {
		return nil
	}
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return ErrInvalidResponse
	}
	if err := unmarshalNumbers(data, out); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidResponse, err)
	}
	return nil
}

func unmarshalNumbers(data []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("chronos: unexpected data after the JSON value")
	}
	return nil
}

func (r response) apiError() *APIError {
	e := &APIError{
		StatusCode: r.status,
		Message:    r.errorMessage(),
		RequestID:  r.header.Get("X-Request-Id"),
		Body:       r.body,
	}
	if r.envelope != nil {
		e.Code = r.envelope.Code
		e.FieldErrors = parseFieldErrors(r.envelope.Errors)
	}
	if r.status == http.StatusTooManyRequests || r.status >= 500 {
		e.RetryAfter = parseRetryAfter(r.header.Get("Retry-After"))
	}
	if r.status != http.StatusTooManyRequests {
		e.RateLimit = parseRateLimit(r.header)
	}
	return e
}

func (r response) errorMessage() string {
	if r.envelope != nil && strings.TrimSpace(r.envelope.Message) != "" {
		return r.envelope.Message
	}
	if r.statusOK() {
		return "invalid response from the API"
	}
	return "request failed"
}

func parseFieldErrors(raw json.RawMessage) []FieldError {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}
	var fieldErrors []FieldError
	for _, item := range items {
		var fe FieldError
		if unmarshalNumbers(item, &fe) == nil && fe.Message != "" {
			fieldErrors = append(fieldErrors, fe)
		}
	}
	return fieldErrors
}

func parseRateLimit(h http.Header) *RateLimit {
	limit, okLimit := parseDigits(h.Get("X-RateLimit-Limit"))
	remaining, okRemaining := parseDigits(h.Get("X-RateLimit-Remaining"))
	reset, okReset := parseDigits(h.Get("X-RateLimit-Reset"))
	if !okLimit || !okRemaining || !okReset {
		return nil
	}
	return &RateLimit{Limit: limit, Remaining: remaining, Reset: time.Duration(reset) * time.Second}
}

func parseDigits(s string) (int, bool) {
	if !isAllDigits(s) {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

func isAllDigits(s string) bool {
	return s != "" && strings.TrimLeft(s, "0123456789") == ""
}

func itemPath(collection, id string) (string, error) {
	if strings.TrimSpace(id) == "" || id == "." || id == ".." {
		return "", fmt.Errorf("%w for %s: %q", ErrInvalidID, collection, id)
	}
	return collection + "/" + url.PathEscape(id), nil
}

func fetch[T any](ctx context.Context, c *Client, r apiRequest) (*T, error) {
	var v T
	if _, err := c.do(ctx, r, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

func fetchPage[T any](ctx context.Context, c *Client, r apiRequest) (*Page[*T], error) {
	r.list = true
	var data []*T
	paging, err := c.do(ctx, r, &data)
	if err != nil {
		return nil, err
	}
	return &Page[*T]{Data: data, Paging: *paging}, nil
}
