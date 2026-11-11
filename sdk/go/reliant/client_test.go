// Copyright (c) 2025 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

package reliant

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1"
)

func TestNewRequiresAToken(t *testing.T) {
	if _, err := New(); err == nil || !strings.Contains(err.Error(), "no token") {
		t.Fatalf("New() with no token = %v, want a 'no token' error", err)
	}
}

func TestBaseURLMustBeHTTPSExceptLoopbackOptIn(t *testing.T) {
	cases := []struct {
		url      string
		loopback bool
		ok       bool
	}{
		{"https://api.reliantapi.com", false, true},
		{"https://api.reliantapi.com/", false, true},
		{"http://api.reliantapi.com", false, false},
		{"http://api.reliantapi.com", true, false}, // the opt-in is for the local machine only
		{"http://localhost:8080", false, false},    // loopback must be asked for
		{"http://localhost:8080", true, true},
		{"http://127.0.0.1:8080", true, true},
		{"http://[::1]:8080", true, true},
		{"http://dev.localhost:8080", true, true},
		{"http://evil.com/localhost", true, false},
		{"ftp://x", false, false},
		{"", false, false},
		{"https://user:pw@api.reliantapi.com", false, false},
		{"https://api.reliantapi.com?x=1", false, false},
	}
	for _, tc := range cases {
		opts := []Option{WithToken("rlat_x"), WithBaseURL(tc.url)}
		if tc.loopback {
			opts = append(opts, WithInsecureLoopback())
		}
		_, err := New(opts...)
		if (err == nil) != tc.ok {
			t.Errorf("New(base=%q, loopback=%v) err = %v, want ok=%v", tc.url, tc.loopback, err, tc.ok)
		}
	}
}

func TestEveryRequestCarriesTheBearerToken(t *testing.T) {
	f := &fake{getRun: func(*reliantv1.GetRunRequest) (*reliantv1.GetRunResponse, error) {
		return &reliantv1.GetRunResponse{Run: &reliantv1.Run{Id: "r1"}}, nil
	}}
	c, _ := f.start(t)
	if _, err := c.Runs.Get(context.Background(), "r1"); err != nil {
		t.Fatal(err)
	}
	if len(f.authHeaders) != 1 || f.authHeaders[0] != "Bearer "+testToken {
		t.Fatalf("Authorization = %v, want [Bearer %s]", f.authHeaders, testToken)
	}
}

func TestTokenSourceIsAskedPerRequestAndWinsOverToken(t *testing.T) {
	var n atomic.Int32
	f := &fake{getRun: func(*reliantv1.GetRunRequest) (*reliantv1.GetRunResponse, error) {
		return &reliantv1.GetRunResponse{Run: &reliantv1.Run{Id: "r1"}}, nil
	}}
	c, _ := f.start(t, WithTokenSource(func(context.Context) (string, error) {
		return "rlat_rot" + string(rune('0'+n.Add(1))), nil
	}))
	for i := 0; i < 2; i++ {
		if _, err := c.Runs.Get(context.Background(), "r1"); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.authHeaders; len(got) != 2 || got[0] != "Bearer rlat_rot1" || got[1] != "Bearer rlat_rot2" {
		t.Fatalf("Authorization = %v, want a fresh token per request", got)
	}
}

func TestTokenSourceFailureFailsTheCallWithoutSendingIt(t *testing.T) {
	f := &fake{}
	c, _ := f.start(t, WithTokenSource(func(context.Context) (string, error) { return "", errors.New("vault down") }))
	_, err := c.Runs.Get(context.Background(), "r1")
	if err == nil || !strings.Contains(err.Error(), "vault down") {
		t.Fatalf("err = %v, want it to carry the token source's failure", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("a request was sent without a token: %v", f.calls)
	}
}

// The token belongs to the server it was issued for. A redirect to anywhere
// else must not carry it.
func TestTokenIsNotSentToAnotherHost(t *testing.T) {
	var leaked atomic.Value
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked.Store(r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	defer other.Close()
	home := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	defer home.Close()

	c, err := New(WithBaseURL(home.URL), WithToken(testToken), WithInsecureLoopback())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Runs.Get(context.Background(), "r1"); err == nil {
		t.Fatal("a redirect off the base origin was followed")
	}
	if v, ok := leaked.Load().(string); ok {
		t.Fatalf("the redirect target was reached (Authorization %q): a redirect off the origin must not be sent", v)
	}
}

// net/http re-sends a request's headers on a redirect to the same host even
// from https to http. An API call must not follow one: the token would cross
// the network in the clear.
func TestTokenIsNotSentOnAnHTTPSDowngrade(t *testing.T) {
	var conns atomic.Int32
	home := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+r.Host+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	home.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		if st == http.StateNew {
			conns.Add(1)
		}
	}
	home.StartTLS()
	defer home.Close()

	c, err := New(WithBaseURL(home.URL), WithToken(testToken), WithHTTPClient(home.Client()))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Runs.Get(context.Background(), "r1")
	if err == nil {
		t.Fatal("an https-to-http redirect was followed")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("the error carries the token: %v", err)
	}
	if n := conns.Load(); n != 1 {
		t.Fatalf("the client opened %d connections to the server, want 1: the plain-http redirect was sent", n)
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv(EnvToken, "")
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), EnvToken) {
		t.Fatalf("FromEnv with no token = %v, want an error naming %s", err, EnvToken)
	}

	t.Setenv(EnvToken, "rlat_env")
	t.Setenv(EnvServerURL, "")
	c, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL() != DefaultBaseURL {
		t.Fatalf("BaseURL = %q, want the default", c.BaseURL())
	}

	t.Setenv(EnvServerURL, "https://reliant.example.com/")
	c, err = FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL() != "https://reliant.example.com" {
		t.Fatalf("BaseURL = %q", c.BaseURL())
	}

	// Options override the environment.
	c, err = FromEnv(WithBaseURL("https://other.example.com"))
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL() != "https://other.example.com" {
		t.Fatalf("an option did not override the environment: %q", c.BaseURL())
	}
}

func TestErrorsMapToSentinelsAndKeepTheServerMessage(t *testing.T) {
	cases := []struct {
		code connect.Code
		want error
	}{
		{connect.CodeNotFound, ErrNotFound},
		{connect.CodeUnauthenticated, ErrUnauthenticated},
		{connect.CodePermissionDenied, ErrPermissionDenied},
		{connect.CodeUnavailable, ErrUnavailable},
		{connect.CodeInvalidArgument, ErrInvalidArgument},
		{connect.CodeFailedPrecondition, ErrFailedPrecondition},
	}
	for _, tc := range cases {
		f := &fake{getRun: func(*reliantv1.GetRunRequest) (*reliantv1.GetRunResponse, error) {
			return nil, connect.NewError(tc.code, errors.New("because reasons"))
		}}
		c, srv := f.start(t)
		_, err := c.Runs.Get(context.Background(), "r1")
		if !errors.Is(err, tc.want) {
			t.Errorf("code %v: errors.Is(%v, %v) = false", tc.code, err, tc.want)
		}
		var e *Error
		if !errors.As(err, &e) {
			t.Fatalf("code %v: not an *Error: %T", tc.code, err)
		}
		if e.Op != "runs.get" || e.Server != srv.URL || e.Code != tc.code || !strings.Contains(e.Message, "because reasons") {
			t.Errorf("code %v: %+v", tc.code, e)
		}
		if connect.CodeOf(err) != tc.code {
			t.Errorf("code %v: connect.CodeOf(err) = %v", tc.code, connect.CodeOf(err))
		}
		// Not another sentinel.
		if tc.want != ErrNotFound && errors.Is(err, ErrNotFound) {
			t.Errorf("code %v matches ErrNotFound", tc.code)
		}
	}
}

func TestUnreachableServerIsUnavailableAndNamesTheServer(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing is listening any more

	c, err := New(WithBaseURL(url), WithToken(testToken), WithInsecureLoopback())
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Runs.Get(context.Background(), "r1")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if !strings.Contains(err.Error(), url) {
		t.Fatalf("err %q does not name the server %s", err, url)
	}
}

// A caller's own cancellation is theirs, not an API failure.
func TestContextCancellationIsNotWrapped(t *testing.T) {
	f := &fake{getRun: func(*reliantv1.GetRunRequest) (*reliantv1.GetRunResponse, error) {
		return &reliantv1.GetRunResponse{Run: &reliantv1.Run{Id: "r1"}}, nil
	}}
	c, _ := f.start(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Runs.Get(ctx, "r1")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	var e *Error
	if errors.As(err, &e) {
		t.Fatalf("a cancelled context was reported as an API error: %+v", e)
	}
}

func TestSuccessFalseIsAnErrorNotAQuietNo(t *testing.T) {
	f := &fake{cancelRun: func(*reliantv1.CancelRunRequest) (*reliantv1.CancelRunResponse, error) {
		return &reliantv1.CancelRunResponse{Success: false, Message: "run already finished"}, nil
	}}
	c, _ := f.start(t)
	err := c.Runs.Cancel(context.Background(), "r1")
	if !errors.Is(err, ErrNotApplied) || !strings.Contains(err.Error(), "run already finished") {
		t.Fatalf("Cancel with success=false = %v, want ErrNotApplied carrying the server's message", err)
	}
}

func TestInsecureHTTPAllowsPlainHTTPToAnyHostAndOnlyThat(t *testing.T) {
	cases := []struct {
		url string
		ok  bool
	}{
		{"http://reliant.internal:8080", true},
		{"http://10.1.2.3:8080/", true},
		{"http://localhost:8080", true},
		{"https://api.reliantapi.com", true},
		{"ftp://reliant.internal", false},
		{"http://user:pw@reliant.internal", false},
		{"http://reliant.internal?x=1", false},
		{"", false},
	}
	for _, tc := range cases {
		_, err := New(WithToken("rlat_x"), WithBaseURL(tc.url), WithInsecureHTTP())
		if (err == nil) != tc.ok {
			t.Errorf("New(base=%q, WithInsecureHTTP) err = %v, want ok=%v", tc.url, err, tc.ok)
		}
	}
	// Without the option the refusal stands, and names the way out.
	_, err := New(WithToken("rlat_x"), WithBaseURL("http://reliant.internal:8080"))
	if err == nil || !strings.Contains(err.Error(), "WithInsecureHTTP") {
		t.Fatalf("plain http without the opt-in = %v, want a refusal naming WithInsecureHTTP", err)
	}
}

func TestInsecureHTTPStillRefusesARedirectOffTheOrigin(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("a redirect off the origin was followed")
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	c, err := New(WithToken("rlat_x"), WithBaseURL(srv.URL), WithInsecureHTTP())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Runs.Get(context.Background(), "r1"); err == nil {
		t.Fatal("a redirect to another origin must fail")
	}
}
