package pgtest

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
)

// fakeRepository is a Maven repository whose artifact GETs answer with the
// statuses in script, in order, then 404 for ever after. It serves no real
// archive: every test here is about what happens BEFORE a successful
// download, and a green download is exercised by every real boot in this
// package's other tests.
func fakeRepository(t *testing.T, script ...int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			http.NotFound(w, r)
			return
		}
		n := int(hits.Add(1))
		status := http.StatusNotFound
		if n <= len(script) {
			status = script[n-1]
		}
		http.Error(w, http.StatusText(status), status)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// withFetchPolicy points the relay at upstream with no real waiting, for the
// duration of the test.
func withFetchPolicy(t *testing.T, upstream string, attempts int) {
	t.Helper()
	saved := fetchPolicy
	fetchPolicy.upstreamURL = upstream
	fetchPolicy.attempts = attempts
	fetchPolicy.firstDelay = time.Millisecond
	fetchPolicy.maxDelay = time.Millisecond
	t.Cleanup(func() { fetchPolicy = saved })
}

// coldConfig is an embedded-postgres config whose binary cache is an empty
// temp dir, so Start MUST download — which is the only path under test.
func coldConfig(t *testing.T) embeddedpostgres.Config {
	t.Helper()
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	return embeddedpostgres.DefaultConfig().
		Port(port).
		CachePath(dir + "/cache").
		RuntimePath(dir + "/runtime").
		Logger(nil)
}

// TestStartEmbedded_TransientFetchFailureNamesTheFetch is the reported
// defect. The library, handed a repository answering 503, says "no version
// found matching 18.3.0" — which is false, and sent a CI investigation
// looking for a version-pinning bug. The error must say the FETCH failed,
// name the URL and the status, and say it may be transient.
func TestStartEmbedded_TransientFetchFailureNamesTheFetch(t *testing.T) {
	repo, hits := fakeRepository(t, 503, 503, 503, 503, 503, 503)
	withFetchPolicy(t, repo.URL, 3)

	_, err := StartEmbedded(coldConfig(t))
	if err == nil {
		t.Fatal("Start succeeded against a repository that only returns 503")
	}
	msg := err.Error()
	if strings.Contains(msg, "no version found") {
		t.Errorf("a 503 is reported as a missing postgres version:\n%s", msg)
	}
	var fe *FetchError
	if !errors.As(err, &fe) {
		t.Fatalf("error is not a *FetchError, so callers cannot tell a download failure apart: %v", err)
	}
	if fe.Status != http.StatusServiceUnavailable {
		t.Errorf("FetchError.Status = %d, want 503", fe.Status)
	}
	for _, want := range []string{repo.URL + "/io/zonky/test/postgres/", ".jar", "HTTP 503", "transient"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not contain %q:\n%s", want, msg)
		}
	}
	if got := hits.Load(); got != 3 {
		t.Errorf("archive fetched %d time(s), want exactly the 3 the policy allows", got)
	}
}

// TestStartEmbedded_RetriesATransientFailure pins the retry itself: a
// repository that 503s twice and then answers must be retried past the
// blips. (It answers 404 on the third try because the fake serves no real
// archive; what is under test is that the THIRD request happened.)
func TestStartEmbedded_RetriesATransientFailure(t *testing.T) {
	repo, hits := fakeRepository(t, 503, 429)
	withFetchPolicy(t, repo.URL, 4)

	_, err := StartEmbedded(coldConfig(t))
	if got := hits.Load(); got != 3 {
		t.Fatalf("archive fetched %d time(s); two transient failures must be retried and the third answer taken (err: %v)", got, err)
	}
	var fe *FetchError
	if !errors.As(err, &fe) || fe.Status != http.StatusNotFound {
		t.Fatalf("the final answer (404) is not what was reported: %v", err)
	}
}

// TestFetchRelay_NoResponseNamesTheURL: no HTTP response at all (here the
// repository drops every connection unanswered; DNS, TLS or a refused
// connection in the wild). The library would say "unable to connect to
// <host>"; forge must still say which artifact and that it may be transient.
//
// It drives the relay directly, not StartEmbedded: StartEmbedded first
// checks that a port reserved earlier is still free, and on a loaded host
// another socket takes it in between (forge #2103, #1921). Nothing here
// depends on a port staying free: the relay keeps its listener, and the
// repository answers every connection by closing it.
func TestFetchRelay_NoResponseNamesTheURL(t *testing.T) {
	repo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, _, err := http.NewResponseController(w).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	t.Cleanup(repo.Close)
	withFetchPolicy(t, repo.URL, 2)

	relay, err := startFetchRelay(repo.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(relay.close)
	const artifact = "/io/zonky/test/postgres/embedded-postgres-binaries-linux-amd64/18.3.0/embedded-postgres-binaries-linux-amd64-18.3.0.jar"
	resp, err := http.Get(relay.baseURL() + artifact)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("relay answered %d for a repository that never responds, want 502", resp.StatusCode)
	}

	fe := relay.lastFailure()
	if fe == nil {
		t.Fatal("a repository that never responds is not recorded as a FetchError")
	}
	if fe.Status != 0 || fe.Err == nil || fe.Attempts != 2 {
		t.Errorf("FetchError = %+v, want Status 0, a transport error, 2 attempts", fe)
	}
	for _, want := range []string{repo.URL + artifact, "transient"} {
		if !strings.Contains(fe.Error(), want) {
			t.Errorf("error does not contain %q:\n%v", want, fe)
		}
	}
}

// TestStartEmbedded_HonorsRetryAfter: Maven Central throttles with 429, and a
// retry inside the window it names is wasted — forge's main failed four tries
// spread over 7s. A Retry-After is waited out even when the backoff (1ms
// here) is shorter.
func TestStartEmbedded_HonorsRetryAfter(t *testing.T) {
	var hits atomic.Int32
	var gap atomic.Int64
	var last atomic.Int64
	repo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			http.NotFound(w, r)
			return
		}
		now := time.Now().UnixNano()
		if prev := last.Swap(now); prev != 0 {
			gap.Store(now - prev)
		}
		if hits.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(repo.Close)
	withFetchPolicy(t, repo.URL, 4)

	_, _ = StartEmbedded(coldConfig(t))
	if got := hits.Load(); got != 2 {
		t.Fatalf("archive fetched %d time(s), want the 429 retried once and the 404 taken", got)
	}
	if got := time.Duration(gap.Load()); got < time.Second {
		t.Errorf("retried %s after a 429 that said Retry-After: 1", got)
	}
}

// TestStartEmbedded_RetryAfterIsCapped: a Retry-After longer than the policy
// allows does not park the fetch for that long.
func TestStartEmbedded_RetryAfterIsCapped(t *testing.T) {
	repo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3600")
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	t.Cleanup(repo.Close)
	withFetchPolicy(t, repo.URL, 2)
	fetchPolicy.maxRetryAfter = 10 * time.Millisecond

	start := time.Now()
	_, err := StartEmbedded(coldConfig(t))
	var fe *FetchError
	if !errors.As(err, &fe) || fe.Status != http.StatusTooManyRequests || fe.Attempts != 2 {
		t.Fatalf("want a FetchError for 2 attempts ending in 429, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("a Retry-After of an hour held the fetch for %s; it is capped by maxRetryAfter", elapsed)
	}
}

// TestRetryAfter pins both Retry-After forms RFC 9110 allows.
func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 29, 9, 38, 0, 0, time.UTC)
	for _, tc := range []struct {
		header string
		want   time.Duration
		ok     bool
	}{
		{"", 0, false},
		{"5", 5 * time.Second, true},
		{" 120 ", 2 * time.Minute, true},
		{now.Add(30 * time.Second).Format(http.TimeFormat), 30 * time.Second, true},
		{now.Add(-time.Minute).Format(http.TimeFormat), 0, true},
		{"-1", 0, false},
		{"soon", 0, false},
	} {
		h := http.Header{}
		if tc.header != "" {
			h.Set("Retry-After", tc.header)
		}
		got, ok := retryAfter(h, now)
		if got != tc.want || ok != tc.ok {
			t.Errorf("retryAfter(%q) = %s, %v; want %s, %v", tc.header, got, ok, tc.want, tc.ok)
		}
	}
}

// TestJittered keeps every wait inside [d/2, d).
func TestJittered(t *testing.T) {
	const d = 8 * time.Second
	for range 1000 {
		if got := jittered(d); got < d/2 || got >= d {
			t.Fatalf("jittered(%s) = %s, outside [%s, %s)", d, got, d/2, d)
		}
	}
}

// TestRetryableStatus pins which answers are worth waiting for.
func TestRetryableStatus(t *testing.T) {
	for code, want := range map[int]bool{
		200: false, 403: false, 404: false, 410: false,
		408: true, 425: true, 429: true, 500: true, 502: true, 503: true, 504: true,
	} {
		if got := retryableStatus(code); got != want {
			t.Errorf("retryableStatus(%d) = %v, want %v", code, got, want)
		}
	}
}
