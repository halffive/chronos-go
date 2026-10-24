package chronos

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestNewClientRejectsBadConfig(t *testing.T) {
	tests := map[string]struct {
		key  string
		opts []Option
	}{
		"empty key":                       {key: "  "},
		"relative base URL":               {key: testAPIKey, opts: []Option{WithBaseURL("api.chronos.sh")}},
		"non-HTTP base URL":               {key: testAPIKey, opts: []Option{WithBaseURL("ftp://api.chronos.sh")}},
		"nil HTTP client":                 {key: testAPIKey, opts: []Option{WithHTTPClient(nil)}},
		"zero timeout":                    {key: testAPIKey, opts: []Option{WithRequestTimeout(0)}},
		"negative retries":                {key: testAPIKey, opts: []Option{WithMaxRequestRetries(-1)}},
		"empty user agent":                {key: testAPIKey, opts: []Option{WithUserAgent(" ")}},
		"base URL with a query":           {key: testAPIKey, opts: []Option{WithBaseURL("https://api.chronos.sh?x=1")}},
		"base URL with a fragment":        {key: testAPIKey, opts: []Option{WithBaseURL("https://api.chronos.sh#x")}},
		"base URL with an empty query":    {key: testAPIKey, opts: []Option{WithBaseURL("https://api.chronos.sh?")}},
		"base URL with an empty fragment": {key: testAPIKey, opts: []Option{WithBaseURL("https://api.chronos.sh#")}},
		"base URL with user info":         {key: testAPIKey, opts: []Option{WithBaseURL("https://user:pass@api.chronos.sh")}},
		"control character in key":        {key: "chrns_a\x01b"},
		"control character in user agent": {key: testAPIKey, opts: []Option{WithUserAgent("app\x01/1")}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := NewClient(tt.key, tt.opts...); err == nil {
				t.Error("want an error")
			}
		})
	}
}

func TestBaseURLKeepsItsPathAndDropsTrailingSlashes(t *testing.T) {
	c, err := NewClient(testAPIKey, WithBaseURL(" https://example.com/chronos// "))
	if err != nil {
		t.Fatal(err)
	}
	req, err := c.prepare(apiRequest{path: "/v1/jobs"})
	if err != nil {
		t.Fatal(err)
	}
	if req.url != "https://example.com/chronos/v1/jobs" {
		t.Errorf("url = %s", req.url)
	}
}

func TestUserAgentPutsTheCallerFirst(t *testing.T) {
	c, err := NewClient(testAPIKey, WithUserAgent("chronos-cli/0.1.0"))
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	c.setHeaders(h)
	if got, want := h.Get("User-Agent"), "chronos-cli/0.1.0 chronos-go/"+Version; got != want {
		t.Errorf("User-Agent = %q, want %q", got, want)
	}
}

func TestAPIKeyNeverPrints(t *testing.T) {
	c, err := NewClient(testAPIKey, WithRequestTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
		if out := fmt.Sprintf(format, c); strings.Contains(out, testAPIKey) {
			t.Errorf("%s of *Client contains the API key", format)
		}
		if out := fmt.Sprintf(format, *c); strings.Contains(out, testAPIKey) {
			t.Errorf("%s of Client contains the API key", format)
		}
	}
}
