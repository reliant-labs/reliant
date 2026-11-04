package guardedhttp

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestBoundedBodyAndRequest(t *testing.T) {
	target, _ := url.Parse("https://example.com/rpc")
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("request changed")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("12345"))}, nil
	})}
	if _, err := Do(context.Background(), client, "POST", target, http.Header{"Content-Type": []string{"application/json"}}, []byte("{}"), 4, nil); err == nil {
		t.Fatal("unbounded response accepted")
	}
	out, err := Do(context.Background(), client, "POST", target, http.Header{"Content-Type": []string{"application/json"}}, []byte("{}"), 5, nil)
	if err != nil || string(out.Body) != "12345" {
		t.Fatalf("exact bound failed: %v", err)
	}
}
