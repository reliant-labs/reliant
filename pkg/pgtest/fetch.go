package pgtest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"

	"github.com/reliant-labs/forge/pkg/observe"
)

// Fetching the postgres binary, with retry and an honest error.
//
// embedded-postgres downloads its binary archive from Maven Central the first
// time a cache is cold, and it answers ANY non-200 from that fetch with
//
//	no version found matching 18.3.0
//
// (fergusstrange/embedded-postgres v1.34.0, remote_fetch.go). A 503 from the
// registry, a 429, a CDN hiccup — every one of them reads as "this postgres
// version does not exist", which sends whoever is reading it looking for a
// version-pinning bug that is not there. control-plane's CI hit exactly that
// on "Verify Generated Code" (`forge generate` → "start embedded postgres: no
// version found matching 18.3.0") and passed on a plain rerun.
//
// The library offers no hook into its fetch: the strategy is unexported, and
// it runs only when the cached archive is missing. What it DOES expose is the
// repository base URL. So forge points that at a loopback relay for the one
// Start call that may fetch, and the relay is where retry and diagnosis live:
//
//   - it forwards each GET to the real repository, retrying a transient
//     failure (a transport error, 408, 425, 429, 5xx) with bounded backoff;
//   - it records the last upstream failure — URL, status, attempts — so a
//     Start that still fails is reported as the fetch failure it was;
//   - everything else stays the library's: it still computes the archive
//     name for this OS/arch/version, verifies the published sha256, and
//     writes its own cache. forge re-derives none of that, so nothing here
//     drifts when the library changes its naming.
//
// # Pre-warming the cache in CI
//
// A warm cache never fetches at all, which removes the registry from the
// critical path entirely. The archive lands at
//
//	<os.UserCacheDir()>/forge/embedded-postgres/embedded-postgres-binaries-<os>-<arch>-<version>.txz
//
// which is ~/.cache/forge/embedded-postgres on a Linux runner. Cache that
// directory, and key the saved entry on what is IN it — its file names carry
// every version — not on go.sum alone. A go.sum key holds whichever archives
// the first job to save it happened to fetch, and a hit is never saved again:
// forge's own cache held only the default 18.3.0, so every main run of a
// test that boots postgres 16 fetched 16.9.0 from Maven Central until a 429
// failed it. forge's CI does this in .github/actions/embedded-postgres-cache
// (restore) and .github/actions/embedded-postgres-cache-save (save on main).

// defaultBinaryRepositoryURL is the library's own default (DefaultConfig's
// binaryRepositoryURL). Restated here because the library exposes a setter
// for it but no getter, and the relay has to know where to forward.
const defaultBinaryRepositoryURL = "https://repo1.maven.org/maven2"

// fetchPolicy bounds the retries. Package-level so a test can shrink the
// waits; production never changes it.
//
// Sized for Maven Central's rate limit, the failure CI actually meets: a
// 429 answered four tries spread over 7s (1+2+4s) on forge's main, all
// inside the same throttling window. Six tries backing off 2s to 30s wait up
// to ~60s in all, which outlasts a burst of parallel jobs; each wait is
// jittered so processes that were throttled together do not come back
// together, and a Retry-After the registry sends is honored up to
// maxRetryAfter.
var fetchPolicy = struct {
	attempts      int
	firstDelay    time.Duration
	maxDelay      time.Duration
	maxRetryAfter time.Duration
	perAttempt    time.Duration
	upstreamURL   string
}{
	attempts:      6,
	firstDelay:    2 * time.Second,
	maxDelay:      30 * time.Second,
	maxRetryAfter: 60 * time.Second,
	perAttempt:    2 * time.Minute,
	upstreamURL:   defaultBinaryRepositoryURL,
}

// FetchError is a postgres binary download that failed. It replaces the
// library's "no version found matching X", which it returns for any non-200.
type FetchError struct {
	// URL is the upstream artifact the library asked for.
	URL string
	// Status is the last HTTP status the repository returned, 0 when the
	// request never got a response (DNS, connection refused, TLS, timeout).
	Status int
	// Attempts is how many times forge tried.
	Attempts int
	// Err is the last transport error, when Status is 0.
	Err error
}

func (e *FetchError) Error() string {
	var last string
	switch {
	case e.Status == http.StatusNotFound:
		// The one status that plausibly DOES mean "no such version".
		return fmt.Sprintf("download postgres binary: %s returned 404 Not Found — this postgres "+
			"version/platform has no published binary (not a transient failure)", e.URL)
	case e.Status != 0:
		last = fmt.Sprintf("HTTP %d %s", e.Status, http.StatusText(e.Status))
	case e.Err != nil:
		last = e.Err.Error()
	default:
		last = "no response"
	}
	return fmt.Sprintf("download postgres binary: fetch of %s failed after %d attempt(s), last: %s. "+
		"This is a registry/network failure, possibly transient — the version exists; re-run. "+
		"To take the registry off the critical path, cache the embedded-postgres archive directory "+
		"(see pkg/pgtest/fetch.go)", e.URL, e.Attempts, last)
}

func (e *FetchError) Unwrap() error { return e.Err }

// retryableStatus reports whether an HTTP status is worth another attempt.
// Everything 4xx other than the throttling/timeout family is a real answer.
func retryableStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests:
		return true
	}
	return code >= 500
}

// fetchRelay is the loopback repository the library fetches through.
type fetchRelay struct {
	upstream string
	client   *http.Client
	srv      *http.Server
	ln       net.Listener

	mu      sync.Mutex
	failure *FetchError // the last artifact GET that never succeeded
}

func startFetchRelay(upstream string) (*fetchRelay, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("pgtest: start postgres download relay: %w", err)
	}
	r := &fetchRelay{
		upstream: strings.TrimRight(upstream, "/"),
		client:   observe.NewHTTPClient(observe.HTTPClientOptions{Timeout: fetchPolicy.perAttempt}),
		ln:       ln,
	}
	r.srv = &http.Server{Handler: r, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = r.srv.Serve(ln) }()
	return r, nil
}

// baseURL is what the library is configured with in place of the real
// repository.
func (r *fetchRelay) baseURL() string { return "http://" + r.ln.Addr().String() }

func (r *fetchRelay) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = r.srv.Shutdown(ctx)
}

// lastFailure is the most recent artifact fetch that never succeeded, or nil.
func (r *fetchRelay) lastFailure() *FetchError {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failure
}

func (r *fetchRelay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	target := r.upstream + req.URL.Path
	resp, attempts, err := r.getWithRetry(req.Context(), target)
	if err != nil || resp.StatusCode != http.StatusOK {
		fe := &FetchError{URL: target, Attempts: attempts, Err: err}
		if resp != nil {
			fe.Status = resp.StatusCode
			_ = resp.Body.Close()
		}
		// The .sha256 sidecar is optional to the library (it skips the check
		// on a non-200), so only the archive itself is worth reporting.
		if !strings.HasSuffix(req.URL.Path, ".sha256") {
			r.mu.Lock()
			r.failure = fe
			r.mu.Unlock()
		}
		status := fe.Status
		if status == 0 {
			status = http.StatusBadGateway
		}
		http.Error(w, fe.Error(), status)
		return
	}
	defer resp.Body.Close()
	if resp.ContentLength >= 0 {
		w.Header().Set("Content-Length", fmt.Sprint(resp.ContentLength))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, resp.Body)
}

// getWithRetry GETs target, retrying transient failures with jittered
// exponential backoff, waiting at least as long as a Retry-After asks. It
// returns the final response (whatever its status) or the final transport
// error, and how many attempts were made.
func (r *fetchRelay) getWithRetry(ctx context.Context, target string) (*http.Response, int, error) {
	backoff := fetchPolicy.firstDelay
	var lastErr error
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, attempt, err
		}
		resp, err := r.client.Do(req)
		switch {
		case err == nil && !retryableStatus(resp.StatusCode):
			return resp, attempt, nil
		case attempt >= fetchPolicy.attempts:
			return resp, attempt, err
		}
		wait := jittered(backoff)
		if err == nil {
			if after, ok := retryAfter(resp.Header, time.Now()); ok {
				wait = max(wait, min(after, fetchPolicy.maxRetryAfter))
			}
			_ = resp.Body.Close()
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return nil, attempt, errors.Join(ctx.Err(), lastErr)
		case <-time.After(wait):
		}
		backoff = min(backoff*2, fetchPolicy.maxDelay)
	}
}

// jittered spreads a backoff step over [d/2, d), so processes throttled by
// the same burst retry at different moments instead of in a second burst.
func jittered(d time.Duration) time.Duration {
	if d <= 1 {
		return d
	}
	half := d / 2
	return half + rand.N(d-half)
}

// retryAfter reads a Retry-After header (RFC 9110 §10.2.3): delay-seconds or
// an HTTP-date. ok is false when there is none or it does not parse; a date
// already past is a zero wait.
func retryAfter(h http.Header, now time.Time) (wait time.Duration, ok bool) {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.ParseUint(v, 10, 32); err == nil {
		return time.Duration(secs) * time.Second, true
	}
	if at, err := http.ParseTime(v); err == nil {
		return max(at.Sub(now), 0), true
	}
	return 0, false
}

// StartEmbedded builds and starts an embedded postgres from cfg, fetching
// its binary (on a cold cache) through a retrying relay. A download failure
// comes back as a *FetchError naming the URL and the upstream status, never
// as the library's "no version found".
//
// cfg must not set BinaryRepositoryURL: the relay takes that slot and
// forwards to Maven Central, the library's default.
//
// The returned Embedded owns the temp log file the library created for this
// Start (see embeddedlog.go): Stop removes it after the library's final
// flush, and a caller whose server outlives the handle calls Detach. A failed
// Start removes its own file here. No other process's log file is ever
// touched.
func StartEmbedded(cfg embeddedpostgres.Config) (*Embedded, error) {
	relay, err := startFetchRelay(fetchPolicy.upstreamURL)
	if err != nil {
		return nil, err
	}
	defer relay.close()

	// Serialized so that "the log files this process newly holds open after
	// Start" is exactly this Start's. See embeddedlog.go.
	startMu.Lock()
	defer startMu.Unlock()

	tempRoot := os.TempDir()
	before, beforeErr := ownEmbeddedLogs(tempRoot, ownOpenPaths)

	ep := embeddedpostgres.NewDatabase(cfg.BinaryRepositoryURL(relay.baseURL()))
	startErr := ep.Start()

	// Whatever the outcome, the library has created its file by now.
	var mine []string
	if beforeErr == nil {
		if after, err := ownEmbeddedLogs(tempRoot, ownOpenPaths); err == nil {
			mine = claimStartedLogs(before, after)
		}
	}
	// Reclaim leftovers nobody holds; best-effort and never blocking a start.
	defer sweepStaleEmbeddedLogs(tempRoot, time.Now(), staleEmbeddedLogAge, machineOpenFiles)

	if startErr != nil {
		removeFiles(mine)
		if fe := relay.lastFailure(); fe != nil {
			// The library's own wording ("no version found matching …") is
			// wrong for everything except a 404, so it is dropped rather
			// than wrapped: the FetchError says what actually happened.
			return nil, fe
		}
		return nil, startErr
	}
	return &Embedded{ep: ep, logs: mine}, nil
}
