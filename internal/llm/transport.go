// Copyright (c) 2025 Reliant Labs
package llm

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// DNS cache: stores resolved host → IP address with TTL.
// Avoids repeated DNS lookups for the same API host across LLM iterations.
var (
	dnsCache   = make(map[string]dnsCacheEntry)
	dnsCacheMu sync.RWMutex
)

const (
	// dnsCacheTTL is how long a successful DNS resolution is cached.
	// 60s is conservative — API hosts rarely change IPs, and this prevents
	// repeated lookups within a single agent loop.
	dnsCacheTTL = 60 * time.Second

	// dnsRetryAttempts is how many times to retry the system resolver
	// before falling back to public DNS.
	dnsRetryAttempts = 3

	// dnsRetryBaseDelay is the base delay between DNS retry attempts.
	// Actual delay is (attempt+1) * base, so 200ms, 400ms, 600ms.
	dnsRetryBaseDelay = 200 * time.Millisecond

	// dnsFallbackTimeout is the timeout for connecting to fallback DNS servers.
	dnsFallbackTimeout = 5 * time.Second
)

// Fallback DNS servers used when the system resolver fails.
var fallbackDNSServers = []string{"8.8.8.8:53", "1.1.1.1:53"}

type dnsCacheEntry struct {
	addr    string // resolved "host:port" or just "ip"
	expires time.Time
}

// sharedResilientTransport is the ONE transport — and therefore the one
// connection pool — every LLM client in the process uses.
//
// It is a process-wide singleton because an *http.Transport IS the connection
// pool: a per-call transport pools nothing, since it is garbage two seconds
// after the call ends. That made MaxIdleConnsPerHost below dead config and
// forced a fresh TCP + TLS handshake for every single LLM request (1,271 of
// them in one afternoon's logs), which is both wasteful and the reason a
// single transient network event could kill every in-flight stream at once —
// observed as idle-timeout/EOF failures arriving in the same SECOND across
// unrelated chats and workflows.
//
// One pool is correct even with several providers configured. Go's Transport
// keys its idle connections by (scheme, host, proxy), so Anthropic, OpenAI and
// Gemini traffic never share a connection with each other — they share only
// the pool's bookkeeping and the DNS cache. MaxIdleConnsPerHost is per-host,
// so providers cannot starve one another either.
//
// Safe to share: http.Transport is explicitly documented as safe for
// concurrent use by multiple goroutines, and no caller mutates the returned
// value (they wrap it in otelhttp/idle-timeout decorators, which is additive).
var sharedResilientTransport = newResilientTransport()

// ResilientTransport returns the shared *http.Transport hardened against DNS
// failures.
//
// Three layers of defense:
//  1. Retry: DNS failures are retried 3 times with the system resolver (handles transient packet loss).
//  2. Fallback: If the system resolver consistently fails, tries Google (8.8.8.8) and Cloudflare (1.1.1.1).
//  3. Cache: Successful DNS resolutions are cached for 60s, so subsequent requests skip DNS entirely.
//
// Also increases MaxIdleConnsPerHost from Go's default of 2 to 10, promoting
// HTTP/2 connection reuse and reducing the frequency of DNS lookups.
//
// Callers MUST NOT mutate the returned transport — it is shared process-wide.
// A caller needing different transport settings should build its own with
// newResilientTransport.
func ResilientTransport() *http.Transport {
	return sharedResilientTransport
}

// newResilientTransport builds a fresh resilient transport with its own
// connection pool. Prefer ResilientTransport; this exists for the singleton
// above and for tests that need pool isolation.
func newResilientTransport() *http.Transport {
	base := http.DefaultTransport.(*http.Transport).Clone()

	// Promote connection reuse — Go's default of 2 causes frequent connection
	// churn, and each new connection requires a DNS lookup.
	base.MaxIdleConnsPerHost = 10

	// ResponseHeaderTimeout limits how long we wait for response headers after
	// sending a request. This catches hung connections where the server accepts
	// the request but never responds. Once headers arrive, the body can stream
	// indefinitely (this timeout does NOT apply to body reads) — that gap is
	// covered by the idle-timeout reader below.
	//
	// This stays deliberately loose at 2 minutes, and must not be tightened to
	// match the idle timeout. The same client also serves NON-streaming
	// completions (SendMessages, ValidateKey), and a non-streaming provider
	// withholds response headers until the whole generation is finished. For
	// those calls "time to headers" is "time to generate", so a tight header
	// timeout would kill legitimate long completions. For STREAMING calls the
	// headers arrive at request-acceptance time and this timeout is never the
	// binding constraint.
	base.ResponseHeaderTimeout = 2 * time.Minute

	systemDialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	fallbackResolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{Timeout: dnsFallbackTimeout}
			var lastErr error
			for _, dns := range fallbackDNSServers {
				conn, err := d.DialContext(ctx, "udp", dns)
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			// All fallback servers failed, try the original address as last resort
			if lastErr != nil {
				return d.DialContext(ctx, network, address)
			}
			return nil, lastErr
		},
	}

	fallbackDialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Resolver:  fallbackResolver,
	}

	base.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			// Can't parse host:port, fall through to default behavior
			return systemDialer.DialContext(ctx, network, addr)
		}

		// Skip resilience for IP addresses (no DNS needed)
		if net.ParseIP(host) != nil {
			return systemDialer.DialContext(ctx, network, addr)
		}

		// Layer 3: Check DNS cache first
		if cachedIP := getCachedDNS(host); cachedIP != "" {
			conn, connErr := systemDialer.DialContext(ctx, network, net.JoinHostPort(cachedIP, port))
			if connErr == nil {
				return conn, nil
			}
			// Cached IP might be stale (host migrated), evict and do fresh lookup
			evictDNS(host)
			logging.Debug("[Transport] Cached DNS entry failed, doing fresh lookup", "host", host, "cachedIP", cachedIP, "error", connErr)
		}

		// Layer 1: Try system resolver with retries
		var lastErr error
		for attempt := 0; attempt < dnsRetryAttempts; attempt++ {
			conn, dialErr := systemDialer.DialContext(ctx, network, addr)
			if dialErr == nil {
				// Success — resolve and cache the IP for future use
				cacheResolvedHost(host)
				return conn, nil
			}
			lastErr = dialErr

			// Only retry DNS errors; other errors (TLS, connection refused) won't benefit from retry
			if !isDNSError(dialErr) {
				return nil, dialErr
			}

			// Don't sleep on the last attempt
			if attempt < dnsRetryAttempts-1 {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(time.Duration(attempt+1) * dnsRetryBaseDelay):
				}
			}
		}

		// Layer 2: Fallback to public DNS servers
		logging.Warn("[Transport] System DNS failed, trying public DNS fallback",
			"host", host,
			"attempts", dnsRetryAttempts,
			"error", lastErr,
		)

		conn, dialErr := fallbackDialer.DialContext(ctx, network, addr)
		if dialErr == nil {
			cacheResolvedHost(host)
			logging.Info("[Transport] Public DNS fallback succeeded", "host", host)
			return conn, nil
		}

		logging.Error("[Transport] All DNS resolution attempts failed",
			"host", host,
			"systemError", lastErr,
			"fallbackError", dialErr,
		)
		return nil, lastErr
	}

	return base
}

// ResilientHTTPClient returns an *http.Client using ResilientTransport.
// Convenience for drivers that pass an http.Client to SDK options.
func ResilientHTTPClient() *http.Client {
	return &http.Client{
		Transport: otelhttp.NewTransport(ResilientTransport()),
	}
}

// DefaultStreamIdleTimeout is the maximum time a streaming response may go
// without receiving ANY data before the read is aborted. It catches the
// "silent hang": the server keeps the TCP connection open (so nothing below
// this layer notices) but stops sending SSE frames, and the request only ends
// minutes later when a middlebox resets the connection.
//
// 90 seconds, derived from 4,039 real completed LLM streams in this repo's
// worker logs:
//
//	p50 5.8s   p90 28.8s   p95 51.0s   p99 137.6s
//	97.8% of streams FINISH ENTIRELY in under 90s.
//
// Any byte resets this clock, including SSE keepalives, ping frames and
// reasoning-summary deltas — so a live-but-thinking stream has to be silent for
// 90 consecutive seconds to trip it, not merely slow. The failure this guards
// produced zero bytes for 8-17 minutes, and the automatic retry then succeeded
// in 12-17s, so the cost of a false positive is bounded and small.
//
// Override with RELIANT_LLM_STREAM_IDLE_TIMEOUT (a Go duration, e.g. "3m") if a
// provider is found to hold a stream silent for longer than this.
const DefaultStreamIdleTimeout = 90 * time.Second

// StreamIdleTimeoutEnv overrides DefaultStreamIdleTimeout at runtime.
const StreamIdleTimeoutEnv = "RELIANT_LLM_STREAM_IDLE_TIMEOUT"

// StreamIdleTimeout returns the configured stream idle timeout. It is read on
// every client construction (once per LLM call) so the override takes effect
// without a rebuild.
func StreamIdleTimeout() time.Duration {
	raw := strings.TrimSpace(os.Getenv(StreamIdleTimeoutEnv))
	if raw == "" {
		return DefaultStreamIdleTimeout
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		logging.Warn("[Transport] Ignoring unusable stream idle timeout override",
			"env", StreamIdleTimeoutEnv, "value", raw, "using", DefaultStreamIdleTimeout)
		return DefaultStreamIdleTimeout
	}
	return d
}

// ErrStreamIdleTimeout is returned by IdleTimeoutReader.Read when the stream
// went idle for longer than the configured timeout.
//
// The wording matters: activities.autoClassify scans the error text, and this
// string must miss every terminal pattern and hit a transient one ("timeout")
// so a silent stream is retried rather than failing the workflow.
var ErrStreamIdleTimeout = errors.New("llm stream idle timeout: provider sent no data before the idle deadline")

// ErrStreamContentStalled is returned when a stream kept its connection busy
// with keepalives but produced no actual content for DefaultStreamContentStallTimeout.
//
// Distinct from ErrStreamIdleTimeout because the two describe different
// faults and a reader of the logs needs to tell them apart: idle means the
// socket went silent, stalled means the provider kept pinging while doing
// nothing. Same classification requirements as above — it must read as
// transient so the turn is retried.
var ErrStreamContentStalled = errors.New("llm stream content stall timeout: provider sent only keepalives before the content deadline")

// DefaultStreamContentStallTimeout bounds how long a stream may deliver
// keepalives and nothing else.
//
// This is the guard for the credit-exhaustion hang: Anthropic answered 200,
// then emitted ping frames for 28-43 minutes while withholding all content,
// and because every ping is a byte, the byte-idle timer above never fired.
// Seven activities returned success with zero tokens after ~35 minutes each.
//
// It is deliberately MUCH looser than the byte-idle timeout, because the two
// guard different things and this one can fire against legitimately slow work:
// before the first token the provider may be queueing the request or
// processing a 400k-token prompt, and pings are all it sends. Sized against
// the same 4,039-stream sample as DefaultStreamIdleTimeout, whose p99 TOTAL
// duration is 137.6s — so at 5 minutes even a stream that spent more than
// twice its p99 entire lifetime without emitting content is left alone. The
// point is not to cut promptly; it is to make an unbounded hang bounded.
//
// Override with RELIANT_LLM_STREAM_CONTENT_STALL_TIMEOUT.
const DefaultStreamContentStallTimeout = 5 * time.Minute

// StreamContentStallTimeoutEnv overrides DefaultStreamContentStallTimeout.
const StreamContentStallTimeoutEnv = "RELIANT_LLM_STREAM_CONTENT_STALL_TIMEOUT"

// StreamContentStallTimeout returns the configured content-stall timeout,
// read per client construction like StreamIdleTimeout.
func StreamContentStallTimeout() time.Duration {
	raw := strings.TrimSpace(os.Getenv(StreamContentStallTimeoutEnv))
	if raw == "" {
		return DefaultStreamContentStallTimeout
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		logging.Warn("[Transport] Ignoring unusable stream content stall timeout override",
			"env", StreamContentStallTimeoutEnv, "value", raw, "using", DefaultStreamContentStallTimeout)
		return DefaultStreamContentStallTimeout
	}
	return d
}

// IdleTimeoutReader wraps an io.ReadCloser and enforces two independent
// deadlines on an SSE stream. Designed for streaming where the server may stop
// working while keeping the TCP connection alive.
//
//	byte-idle    no bytes at all for `timeout`        -> ErrStreamIdleTimeout
//	content-stall no CONTENT for `contentStallTimeout` -> ErrStreamContentStalled
//
// Two timers rather than one, because "the socket is alive" and "the request
// is progressing" are different questions and each has its own correct
// deadline. The byte-idle timer stays tight (90s) and keeps its original
// semantics: ANY byte, keepalives included, resets it, so a slow-but-live
// stream is never at risk. The content-stall timer is much looser (5m) and is
// reset only by real stream content, which is what makes an
// all-keepalives-forever stream terminate at all.
//
// Collapsing these into one timer is the tempting simplification and it is
// wrong in both directions: at 90s it cuts legitimate long prompt-processing
// that emits only pings, and at 5m it lets a genuinely silent socket sit for
// five minutes when 90s was demonstrably enough.
type IdleTimeoutReader struct {
	r                   io.ReadCloser
	timeout             time.Duration
	contentStallTimeout time.Duration
	timer               *time.Timer
	contentTimer        *time.Timer
	fired               atomic.Bool
	contentFired        atomic.Bool
	once                sync.Once
	// done releases both watcher goroutines when the stream ends normally, so
	// neither outlives the body it guards.
	done chan struct{}
	// progress is touched only from Read, which io.Reader forbids calling
	// concurrently, so it needs no lock of its own.
	progress sseProgressScanner
}

// NewIdleTimeoutReader wraps r with the byte-idle timeout and the default
// content-stall timeout.
func NewIdleTimeoutReader(r io.ReadCloser, timeout time.Duration) *IdleTimeoutReader {
	return newIdleTimeoutReader(r, timeout, StreamContentStallTimeout())
}

// newIdleTimeoutReader takes both deadlines explicitly so tests can exercise
// either guard without waiting the real durations.
func newIdleTimeoutReader(r io.ReadCloser, timeout, contentStall time.Duration) *IdleTimeoutReader {
	itr := &IdleTimeoutReader{
		r:                   r,
		timeout:             timeout,
		contentStallTimeout: contentStall,
		done:                make(chan struct{}),
	}

	// Both timers are created STOPPED and started only once both fields are
	// assigned. time.AfterFunc starts its clock immediately, so arming the
	// first timer inline let it fire — and call Close, which reads
	// contentTimer — before the second assignment had happened. With a short
	// timeout that is a real nil-deref/data race, not a theoretical one; the
	// race detector caught it on the 200ms unit test.
	itr.timer = time.NewTimer(timeout)
	itr.timer.Stop()
	itr.contentTimer = time.NewTimer(contentStall)
	itr.contentTimer.Stop()

	go itr.watch(itr.timer.C, &itr.fired, func() {
		logging.Warn("[IdleTimeoutReader] Stream idle timeout reached, closing connection",
			"timeout", timeout)
	})
	go itr.watch(itr.contentTimer.C, &itr.contentFired, func() {
		logging.Warn("[IdleTimeoutReader] Stream content stall timeout reached, closing connection",
			"timeout", contentStall,
			"detail", "connection stayed alive on keepalives but the provider sent no content")
	})

	itr.timer.Reset(timeout)
	itr.contentTimer.Reset(contentStall)
	return itr
}

// watch closes the stream when its timer fires, recording which deadline was
// breached so Read can report the right sentinel. Close is once-guarded, so
// whichever timer fires first wins and the other watcher returns via done.
func (itr *IdleTimeoutReader) watch(c <-chan time.Time, flag *atomic.Bool, logFire func()) {
	select {
	case <-c:
		flag.Store(true)
		logFire()
		_ = itr.Close()
	case <-itr.done:
	}
}

func (itr *IdleTimeoutReader) Read(p []byte) (int, error) {
	n, err := itr.r.Read(p)
	if n > 0 {
		// Any byte proves the connection is alive.
		itr.timer.Reset(itr.timeout)
		// Only real content proves the provider is working. sawContent must
		// see EVERY chunk — it carries partial-line and SSE frame state
		// between calls.
		if itr.progress.sawContent(p[:n]) {
			itr.contentTimer.Reset(itr.contentStallTimeout)
		}
	}
	if err != nil {
		// Report the real cause. Without this the caller sees whatever the
		// transport says about a body we closed underneath it ("read on closed
		// response body", "use of closed network connection"), which is neither
		// diagnosable in a log nor reliably classified as transient.
		//
		// Content-stall is checked first: when it fires, the byte-idle timer
		// is usually moments from firing too (the close stops both reads), and
		// the stall is the more specific, more actionable diagnosis.
		if itr.contentFired.Load() {
			return n, ErrStreamContentStalled
		}
		if itr.fired.Load() {
			return n, ErrStreamIdleTimeout
		}
		itr.timer.Stop()
		itr.contentTimer.Stop()
	}
	return n, err
}

func (itr *IdleTimeoutReader) Close() error {
	itr.once.Do(func() {
		itr.timer.Stop()
		itr.contentTimer.Stop()
		// Release whichever watcher did not fire.
		close(itr.done)
	})
	return itr.r.Close()
}

// idleTimeoutTransport wraps an http.RoundTripper and applies IdleTimeoutReader
// to all response bodies. This ensures SDK-managed streaming connections
// (where we don't have direct access to resp.Body) still get idle timeout
// protection.
type idleTimeoutTransport struct {
	base                http.RoundTripper
	timeout             time.Duration
	contentStallTimeout time.Duration
}

func (t *idleTimeoutTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if resp.Body != nil {
		stall := t.contentStallTimeout
		if stall <= 0 {
			stall = StreamContentStallTimeout()
		}
		resp.Body = newIdleTimeoutReader(resp.Body, t.timeout, stall)
	}
	return resp, nil
}

// WrapWithIdleTimeout wraps an existing http.RoundTripper with idle stream
// timeout protection. Use this when you have a custom transport chain (e.g.,
// with token refresh or header manipulation) and want to add idle timeout
// detection on top.
func WrapWithIdleTimeout(base http.RoundTripper) http.RoundTripper {
	return &idleTimeoutTransport{
		base:                base,
		timeout:             StreamIdleTimeout(),
		contentStallTimeout: StreamContentStallTimeout(),
	}
}

// StreamingHTTPClient returns an *http.Client configured for LLM streaming:
//   - DNS resilience (retry, fallback, caching) via ResilientTransport
//   - ResponseHeaderTimeout (2min) to detect hung connections before streaming starts
//   - Idle stream timeout to detect silent hangs during streaming
//
// Every LLM SDK client is built with this — see NewOpenAISDKClient,
// NewAnthropicSDKClient and NewGenAISDKClient in sdkclient.go, which are the
// only sanctioned way for a driver to construct one.
func StreamingHTTPClient() *http.Client {
	return newStreamingHTTPClient(StreamIdleTimeout())
}

// newStreamingHTTPClient builds the streaming client with an explicit idle
// timeout. Tests use it to exercise the guard without waiting the real timeout.
func newStreamingHTTPClient(idle time.Duration) *http.Client {
	return newStreamingHTTPClientWithStall(idle, StreamContentStallTimeout())
}

// newStreamingHTTPClientWithStall additionally pins the content-stall deadline,
// so a test can drive either guard independently of the other.
func newStreamingHTTPClientWithStall(idle, contentStall time.Duration) *http.Client {
	return &http.Client{
		Transport: &idleTimeoutTransport{
			base:                otelhttp.NewTransport(ResilientTransport()),
			timeout:             idle,
			contentStallTimeout: contentStall,
		},
	}
}

// isDNSError returns true if the error is a DNS resolution failure.
func isDNSError(err error) bool {
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr)
}

// cacheResolvedHost resolves a hostname and caches the first IP address.
func cacheResolvedHost(host string) {
	// Resolve the hostname to get the IP address for caching
	addrs, err := net.LookupHost(host)
	if err != nil || len(addrs) == 0 {
		return
	}

	dnsCacheMu.Lock()
	dnsCache[host] = dnsCacheEntry{
		addr:    addrs[0],
		expires: time.Now().Add(dnsCacheTTL),
	}
	dnsCacheMu.Unlock()
}

// getCachedDNS returns a cached IP for the host, or empty string if not cached or expired.
func getCachedDNS(host string) string {
	dnsCacheMu.RLock()
	entry, ok := dnsCache[host]
	dnsCacheMu.RUnlock()

	if !ok || time.Now().After(entry.expires) {
		if ok {
			// Expired — clean up
			evictDNS(host)
		}
		return ""
	}
	return entry.addr
}

// evictDNS removes a host from the DNS cache.
func evictDNS(host string) {
	dnsCacheMu.Lock()
	delete(dnsCache, host)
	dnsCacheMu.Unlock()
}
