package tdd_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/reliant-labs/forge/pkg/tdd"
)

func TestE2EClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))

	type myClient struct{ baseURL string }
	c := tdd.E2EClient(t, srv, func(url string) *myClient { return &myClient{baseURL: url} })

	if c.baseURL != srv.URL {
		t.Fatalf("client baseURL = %q, want %q", c.baseURL, srv.URL)
	}

	// Confirm the cleanup is registered: the server should still be live
	// during the test (Cleanup runs on test exit, not now).
	resp, err := http.Get(c.baseURL)
	if err != nil {
		t.Fatalf("client GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Fatalf("body = %q, want %q", body, "ok")
	}
}
