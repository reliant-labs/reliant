package pgtest

import (
	"errors"
	"io"
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

// withFetchPolicy points the relay at upstream alone (no mirror) with no real
// waiting, for the duration of the test. It also empties the pinned checksum
// table, so a fake archive is verified against what the fake repository
// publishes; a test of the pin sets its own.
func withFetchPolicy(t *testing.T, upstream string, attempts int) {
	t.Helper()
	saved := fetchPolicy
	savedPins := pinnedArchiveSHA256
	fetchPolicy.repositories = []string{upstream}
	fetchPolicy.attempts = attempts
	fetchPolicy.firstDelay = time.Millisecond
	fetchPolicy.maxDelay = time.Millisecond
	pinnedArchiveSHA256 = map[string]string{}
	t.Cleanup(func() {
		fetchPolicy = saved
		pinnedArchiveSHA256 = savedPins
	})
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

	ff := relay.lastFailure()
	if ff == nil {
		t.Fatal("a repository that never responds is not recorded as a FetchError")
	}
	fe := ff.lead
	if fe.Status != 0 || fe.Err == nil || fe.Attempts != 2 {
		t.Errorf("FetchError = %+v, want Status 0, a transport error, 2 attempts", fe)
	}
	for _, want := range []string{repo.URL + artifact, "transient"} {
		if !strings.Contains(fe.Error(), want) {
			t.Errorf("error does not contain %q:\n%v", want, fe)
		}
	}
}

// relayArtifact is an archive path as the library asks for it.
const relayArtifact = "/io/zonky/test/postgres/embedded-postgres-binaries-linux-amd64/18.3.0/embedded-postgres-binaries-linux-amd64-18.3.0.jar"

// scriptedRepository answers every archive GET with status (and, on 200,
// body), counting those requests, and publishes the body's .sha256 beside
// it when it serves the body (a 404 otherwise).
func scriptedRepository(t *testing.T, status int, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			if status != http.StatusOK {
				http.NotFound(w, r)
				return
			}
			_, _ = io.WriteString(w, sha256Hex([]byte(body)))
			return
		}
		hits.Add(1)
		if status == http.StatusOK {
			_, _ = io.WriteString(w, body)
			return
		}
		http.Error(w, http.StatusText(status), status)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// relayGet fetches relayArtifact through a relay over repositories.
func relayGet(t *testing.T, repositories ...string) (*fetchRelay, int, string) {
	t.Helper()
	relay, err := startFetchRelay(repositories...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(relay.close)
	resp, err := http.Get(relay.baseURL() + relayArtifact)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return relay, resp.StatusCode, string(body)
}

// TestFetchRelay_A404FallsBackToTheMirror is the defect from forge's main
// (run 44491105411): Maven Central's CDN answered 404 for the published
// 18.3.0 archive, and every pgtest-backed test failed. A 404 from the first
// repository must not be final: the mirror is asked, and serves it.
func TestFetchRelay_A404FallsBackToTheMirror(t *testing.T) {
	primary, primaryHits := scriptedRepository(t, http.StatusNotFound, "")
	mirror, mirrorHits := scriptedRepository(t, http.StatusOK, "archive bytes")
	withFetchPolicy(t, primary.URL, 3)

	relay, status, body := relayGet(t, primary.URL, mirror.URL)
	if status != http.StatusOK || body != "archive bytes" {
		t.Fatalf("relay answered %d %q, want the mirror's 200 archive", status, body)
	}
	if got := primaryHits.Load(); got != 1 {
		t.Errorf("primary asked %d time(s); a 404 is an answer, not a blip to retry", got)
	}
	if got := mirrorHits.Load(); got != 1 {
		t.Errorf("mirror asked %d time(s), want 1", got)
	}
	if fe := relay.lastFailure(); fe != nil {
		t.Errorf("a fetch the mirror served is recorded as a failure: %v", fe)
	}
}

// TestFetchRelay_ThePrimaryServingSkipsTheMirror: the mirror is a fallback,
// never a second request on the happy path.
func TestFetchRelay_ThePrimaryServingSkipsTheMirror(t *testing.T) {
	primary, _ := scriptedRepository(t, http.StatusOK, "archive bytes")
	mirror, mirrorHits := scriptedRepository(t, http.StatusOK, "mirror bytes")
	withFetchPolicy(t, primary.URL, 3)

	_, status, body := relayGet(t, primary.URL, mirror.URL)
	if status != http.StatusOK || body != "archive bytes" {
		t.Fatalf("relay answered %d %q, want the primary's archive", status, body)
	}
	if got := mirrorHits.Load(); got != 0 {
		t.Errorf("mirror asked %d time(s) although the primary served the archive", got)
	}
}

// TestFetchRelay_A404EverywhereIsNoPublishedBinary: only when every
// repository says 404 is it reported as a version with no binary — naming
// each repository asked.
func TestFetchRelay_A404EverywhereIsNoPublishedBinary(t *testing.T) {
	primary, _ := scriptedRepository(t, http.StatusNotFound, "")
	mirror, _ := scriptedRepository(t, http.StatusNotFound, "")
	withFetchPolicy(t, primary.URL, 3)

	relay, status, _ := relayGet(t, primary.URL, mirror.URL)
	if status != http.StatusNotFound {
		t.Errorf("relay answered %d, want 404", status)
	}
	fe := relay.lastFailure()
	if fe == nil || fe.lead.Status != http.StatusNotFound || len(fe.also) != 1 {
		t.Fatalf("fetch failure = %+v, want a 404 with the mirror's 404 beside it", fe)
	}
	// The public view: errors.As finds the lead, and its Also still names
	// the mirror's answer, as it did before fetchFailures existed.
	var lead *FetchError
	if !errors.As(fe, &lead) {
		t.Fatalf("errors.As(%v, *FetchError) = false", fe)
	}
	if also := lead.Also(); len(also) != 1 || also[0].URL != mirror.URL+relayArtifact || also[0].Status != http.StatusNotFound {
		t.Errorf("Also() = %+v, want the mirror's 404", also)
	}
	if also := (&FetchError{URL: "x"}).Also(); also != nil {
		t.Errorf("Also() on a FetchError built by hand = %+v, want nil", also)
	}
	for _, want := range []string{primary.URL + relayArtifact, mirror.URL + relayArtifact, "no published binary"} {
		if !strings.Contains(fe.Error(), want) {
			t.Errorf("error does not contain %q:\n%v", want, fe)
		}
	}
}

// TestFetchRelay_ATransientMirrorFailureIsNotAMissingVersion: the primary's
// 404 and the mirror's 503 together say "the registry is unwell", not "no
// such version" — the transient failure leads, and the 404 is still named.
func TestFetchRelay_ATransientMirrorFailureIsNotAMissingVersion(t *testing.T) {
	primary, _ := scriptedRepository(t, http.StatusNotFound, "")
	mirror, mirrorHits := scriptedRepository(t, http.StatusServiceUnavailable, "")
	withFetchPolicy(t, primary.URL, 2)

	relay, status, _ := relayGet(t, primary.URL, mirror.URL)
	if status != http.StatusServiceUnavailable {
		t.Errorf("relay answered %d, want the mirror's 503", status)
	}
	if got := mirrorHits.Load(); got != 2 {
		t.Errorf("mirror asked %d time(s), want the 2 the policy allows", got)
	}
	fe := relay.lastFailure()
	if fe == nil || fe.lead.Status != http.StatusServiceUnavailable {
		t.Fatalf("fetch failure = %+v, want the mirror's 503 to lead", fe)
	}
	msg := fe.Error()
	if strings.Contains(msg, "no published binary") {
		t.Errorf("a 404 beside a 503 is reported as a missing version:\n%s", msg)
	}
	for _, want := range []string{"transient", mirror.URL + relayArtifact, "Also tried " + primary.URL + relayArtifact + ": HTTP 404"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not contain %q:\n%s", want, msg)
		}
	}
}

// TestDefaultRepositoriesFallBackToAMirror pins production's list: Maven
// Central first (the library's own default), then a mirror on separate
// infrastructure.
func TestDefaultRepositoriesFallBackToAMirror(t *testing.T) {
	got := fetchPolicy.repositories
	if len(got) != 2 || got[0] != defaultBinaryRepositoryURL || got[1] != mavenCentralMirrorURL {
		t.Errorf("fetchPolicy.repositories = %v, want [%s %s]", got, defaultBinaryRepositoryURL, mavenCentralMirrorURL)
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
