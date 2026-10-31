package chronos

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"
)

const testAPIKey = "chrns_testkey0123456789abcdefghijklmnop"

type recordedRequest struct {
	method     string
	requestURI string
	header     http.Header
	body       string
}

type fakeResponse struct {
	status int
	header map[string]string
	body   string
}

// fakeAPI replays responses in order and repeats the last one.
type fakeAPI struct {
	mu        sync.Mutex
	responses []fakeResponse
	requests  []recordedRequest
	delays    []time.Duration
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.requests = append(f.requests, recordedRequest{
		method:     r.Method,
		requestURI: r.RequestURI,
		header:     r.Header.Clone(),
		body:       string(body),
	})
	res := f.responses[min(len(f.requests), len(f.responses))-1]
	f.mu.Unlock()

	for k, v := range res.header {
		w.Header().Set(k, v)
	}
	w.WriteHeader(res.status)
	io.WriteString(w, res.body)
}

func (f *fakeAPI) lastRequest(t *testing.T) recordedRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		t.Fatal("no request reached the server")
	}
	return f.requests[len(f.requests)-1]
}

func (f *fakeAPI) recordedDelays() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.delays)
}

func (f *fakeAPI) request(t *testing.T, i int) recordedRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.requests) {
		t.Fatalf("got %d requests, want more than %d", len(f.requests), i)
	}
	return f.requests[i]
}

func (f *fakeAPI) requestURI(t *testing.T, i int) string {
	t.Helper()
	return f.request(t, i).requestURI
}

func (f *fakeAPI) requestBody(t *testing.T, i int) string {
	t.Helper()
	return f.request(t, i).body
}

func (f *fakeAPI) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// newTestClient returns a client wired to a fake API. Retry waits are
// recorded instead of slept, and jitter is off.
func newTestClient(t *testing.T, responses ...fakeResponse) (*Client, *fakeAPI) {
	t.Helper()
	api := &fakeAPI{responses: responses}
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)

	c, err := NewClient(testAPIKey, WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	c.sleep = func(ctx context.Context, d time.Duration) error {
		api.mu.Lock()
		api.delays = append(api.delays, d)
		api.mu.Unlock()
		return ctx.Err()
	}
	c.jitter = func() float64 { return 0 }
	return c, api
}

func okResponse(data string) fakeResponse {
	return fakeResponse{status: http.StatusOK, body: `{"success":true,"message":"ok","data":` + data + `}`}
}

func okPage(data string, page, pages int) fakeResponse {
	paging := `{"page":` + strconv.Itoa(page) + `,"pages":` + strconv.Itoa(pages) + `,"size":100,"total":0}`
	return fakeResponse{
		status: http.StatusOK,
		body:   `{"success":true,"message":"ok","data":` + data + `,"meta":{"paging":` + paging + `}}`,
	}
}

func apiFailure(status int, code, message string) fakeResponse {
	return fakeResponse{
		status: status,
		body:   `{"success":false,"message":"` + message + `","code":"` + code + `","errors":[]}`,
	}
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
