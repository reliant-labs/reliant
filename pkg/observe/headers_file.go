// File: headers_file.go — OTLP request headers that can change while the
// process runs.
//
// OTEL_EXPORTER_OTLP_HEADERS is read once, when an exporter is built, so a
// credential that rotates (a short-lived ingest token a control plane renews in
// a mounted Secret) would need a pod restart. OTEL_EXPORTER_OTLP_HEADERS_FILE
// names a file in the same format instead, and every signal reads it per
// request:
//
//   - HTTP exporters get an http.Client whose RoundTripper adds the headers;
//   - gRPC exporters and the profiles sender get them as per-RPC metadata.
//
// There is no watcher goroutine. A request re-reads the file when the last
// check is older than headersFileRecheck and re-parses it only when its bytes
// changed, so a steady state costs one small read per interval per process,
// shared by all four signals. Change is decided by content, not by the file's
// identity, size and mtime: on Windows os.SameFile resolves a stat'ed path when
// it is called, so it compares the current file with itself, and an mtime
// cannot tell apart two same-size writes inside one clock tick on any OS.
//
// Precedence: a header named in OTEL_EXPORTER_OTLP_HEADERS or a per-signal
// OTEL_EXPORTER_OTLP_<SIGNAL>_HEADERS is never overridden by the file. The file
// supplies the rest.
//
// A missing, unreadable or empty file means "no file headers": export carries
// on without them (the collector decides whether that is acceptable), and one
// WARN per headersFileWarnEvery says why. It never fails an export call and
// never blocks the app.

package observe

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// EnvOTLPHeadersFile names a file holding OTLP request headers in the
// OTEL_EXPORTER_OTLP_HEADERS format, re-read when it changes. The per-signal
// OTEL_EXPORTER_OTLP_<SIGNAL>_HEADERS_FILE overrides it for that signal.
const EnvOTLPHeadersFile = "OTEL_EXPORTER_OTLP_HEADERS_FILE"

// How often a request may re-read the file, and how often a persistent failure
// is logged. Variables so tests can shrink them.
var (
	headersFileRecheck   = 5 * time.Second
	headersFileWarnEvery = time.Minute
)

type headerKV struct{ key, value string }

// parseOTLPHeaders parses the OTEL_EXPORTER_OTLP_HEADERS format: comma
// separated key=value pairs, values percent-decoded. A newline also separates
// pairs, because a file written by a Secret or an editor often ends in one.
// Pairs without a key are skipped.
func parseOTLPHeaders(raw string) []headerKV {
	var out []headerKV
	for _, kv := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' }) {
		k, v, ok := strings.Cut(kv, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			continue
		}
		v = strings.TrimSpace(v)
		if dv, err := url.PathUnescape(v); err == nil {
			v = dv
		}
		out = append(out, headerKV{k, v})
	}
	return out
}

// headerFile is one watched headers file. Safe for concurrent use.
type headerFile struct {
	path string
	now  func() time.Time
	log  *slog.Logger

	mu       sync.Mutex
	checked  time.Time
	raw      []byte // the file content the cached headers were parsed from
	headers  []headerKV
	lastWarn time.Time
	failing  bool
}

var (
	headerFilesMu sync.Mutex
	headerFiles   = map[string]*headerFile{}
)

// sharedHeaderFile returns the process-wide watcher for path, so the four
// signals share one stat and one cached read.
func sharedHeaderFile(path string) *headerFile {
	headerFilesMu.Lock()
	defer headerFilesMu.Unlock()
	f, ok := headerFiles[path]
	if !ok {
		f = &headerFile{path: path, now: time.Now, log: slog.Default().With("component", "otlp-headers")}
		headerFiles[path] = f
	}
	return f
}

// current returns the file's headers as of the last check, refreshing it first
// when the check is older than headersFileRecheck.
func (f *headerFile) current() []headerKV {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	if !f.checked.IsZero() && now.Sub(f.checked) < headersFileRecheck {
		return f.headers
	}
	f.checked = now
	f.refreshLocked(now)
	return f.headers
}

func (f *headerFile) refreshLocked(now time.Time) {
	raw, err := os.ReadFile(f.path)
	if err != nil {
		f.failLocked(now, err)
		return
	}
	if !f.failing && f.raw != nil && bytes.Equal(raw, f.raw) {
		return
	}
	hs := parseOTLPHeaders(string(raw))
	if len(hs) == 0 {
		f.failLocked(now, fmt.Errorf("file contains no key=value headers"))
		return
	}
	if f.failing {
		f.log.Info("OTLP headers file is readable again", "path", f.path, "headers", len(hs))
	}
	f.failing = false
	f.raw, f.headers = raw, hs
}

// failLocked drops the cached headers (the file no longer says what they were)
// and warns, at most once per headersFileWarnEvery.
func (f *headerFile) failLocked(now time.Time, err error) {
	f.headers, f.raw = nil, nil
	f.failing = true
	if f.lastWarn.IsZero() || now.Sub(f.lastWarn) >= headersFileWarnEvery {
		f.lastWarn = now
		f.log.Warn("OTLP headers file unusable; exporting without its headers",
			"path", f.path, "error", err, "recheck", headersFileRecheck.String())
	}
}

// headerSource is what an exporter consults on every request. A nil source
// means no file is configured for the signal.
type headerSource struct {
	file *headerFile
	// explicit holds the lowercased names set through the environment
	// variables, which the file must not override.
	explicit map[string]struct{}
}

// headerSourceFor resolves the headers file of signal s: the per-signal
// variable, then OTEL_EXPORTER_OTLP_HEADERS_FILE.
func headerSourceFor(s signal) *headerSource {
	path := s.env("HEADERS_FILE")
	if path == "" {
		path = envValue(EnvOTLPHeadersFile)
	}
	if path == "" {
		return nil
	}
	return &headerSource{file: sharedHeaderFile(path), explicit: explicitHeaderNames(s)}
}

// explicitHeaderNames lists the header names the environment sets for s. The
// SDK exporters treat OTEL_EXPORTER_OTLP_<SIGNAL>_HEADERS as a replacement for
// OTEL_EXPORTER_OTLP_HEADERS, not a merge, so the same rule decides which names
// are explicit here: a name the exporter would not send must not mask the file.
func explicitHeaderNames(s signal) map[string]struct{} {
	raw := s.env("HEADERS")
	if raw == "" {
		raw = envValue(EnvOTLPHeaders)
	}
	out := map[string]struct{}{}
	for _, kv := range parseOTLPHeaders(raw) {
		out[strings.ToLower(kv.key)] = struct{}{}
	}
	return out
}

// each calls fn for every file header the environment did not set explicitly.
func (h *headerSource) each(fn func(key, value string)) {
	for _, kv := range h.file.current() {
		if _, set := h.explicit[strings.ToLower(kv.key)]; set {
			continue
		}
		fn(kv.key, kv.value)
	}
}

// GetRequestMetadata implements credentials.PerRPCCredentials. gRPC metadata
// keys are lowercase.
func (h *headerSource) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	md := map[string]string{}
	h.each(func(k, v string) { md[strings.ToLower(k)] = v })
	return md, nil
}

// RequireTransportSecurity implements credentials.PerRPCCredentials. The
// exporter's own endpoint scheme decides TLS; refusing a plaintext collector
// here would silently drop telemetry that the env-var form would have sent.
func (h *headerSource) RequireTransportSecurity() bool { return false }

// headerTransport adds the file's headers to every request.
type headerTransport struct {
	base http.RoundTripper
	src  *headerSource
}

func (t headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// A RoundTripper must not modify the request it is given.
	req = req.Clone(req.Context())
	t.src.each(func(k, v string) { req.Header.Set(k, v) })
	return t.base.RoundTrip(req)
}

// httpClientFor builds the http.Client every OTLP/HTTP exporter is handed: it
// runs on the shared transport (see otlpIdleConnTimeout) and, when src is not
// nil, carries src's headers. Passing a client makes the exporter ignore its
// own transport settings, so the ones the OTLP environment contract defines are
// rebuilt here: timeout, proxy from the environment, and the certificate
// variables.
func httpClientFor(s signal, src *headerSource) (*http.Client, error) {
	base, err := sharedBaseTransport(s)
	if err != nil {
		return nil, err
	}
	timeout := 10 * time.Second
	ms := s.env("TIMEOUT")
	if ms == "" {
		ms = envValue("OTEL_EXPORTER_OTLP_TIMEOUT")
	}
	if n, err := strconv.Atoi(ms); err == nil && n > 0 {
		timeout = time.Duration(n) * time.Millisecond
	}
	if src == nil {
		return &http.Client{Transport: base, Timeout: timeout}, nil
	}
	return &http.Client{Transport: headerTransport{base: base, src: src}, Timeout: timeout}, nil
}

// otlpIdleConnTimeout is how long an OTLP/HTTP exporter keeps an idle
// keep-alive connection. It must stay BELOW the collector's: an OTLP/HTTP
// receiver closes a connection idle for 60s by default, and the SDK's own
// transport (like http.DefaultTransport) keeps one for 90s. The periodic
// metric reader exports every 60s, so each export reused a connection just as
// the collector closed it, and the POST failed with `EOF` or `connection reset
// by peer`, losing that interval's metrics: the SDK does not retry them. The
// client must be the side that closes, so the next export dials a fresh
// connection instead.
var otlpIdleConnTimeout = 30 * time.Second

var (
	baseTransportsMu sync.Mutex
	baseTransports   = map[string]*http.Transport{}
)

// sharedBaseTransport returns the process-wide transport for s's TLS material.
// Exporters are rebuilt by Setup and nothing closes a supplied client's idle
// connections, so a transport per exporter would leak sockets and their
// goroutines; the SDK's own default transport is shared for the same reason.
func sharedBaseTransport(s signal) (*http.Transport, error) {
	tlsCfg, key, err := tlsFromEnv(s)
	if err != nil {
		return nil, err
	}
	baseTransportsMu.Lock()
	defer baseTransportsMu.Unlock()
	if t, ok := baseTransports[key]; ok {
		return t, nil
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSClientConfig = tlsCfg
	t.IdleConnTimeout = otlpIdleConnTimeout
	baseTransports[key] = t
	return t, nil
}

// closeIdleOTLPTransports drops the keep-alive connections the shared
// transports hold, so a shut-down Setup leaves no socket or goroutine behind.
func closeIdleOTLPTransports(context.Context) error {
	baseTransportsMu.Lock()
	defer baseTransportsMu.Unlock()
	for _, t := range baseTransports {
		t.CloseIdleConnections()
	}
	return nil
}

// tlsFromEnv reads OTEL_EXPORTER_OTLP_[<SIGNAL>_]CERTIFICATE (server CA) and
// CLIENT_CERTIFICATE / CLIENT_KEY (mTLS). The config is nil when none is set;
// key identifies the combination of files.
func tlsFromEnv(s signal) (cfg *tls.Config, key string, err error) {
	get := func(suffix string) string {
		if v := s.env(suffix); v != "" {
			return v
		}
		return envValue("OTEL_EXPORTER_OTLP_" + suffix)
	}
	ca, cert, keyFile := get("CERTIFICATE"), get("CLIENT_CERTIFICATE"), get("CLIENT_KEY")
	if ca == "" && cert == "" && keyFile == "" {
		return nil, "", nil
	}
	key = ca + "\x00" + cert + "\x00" + keyFile
	cfg = &tls.Config{MinVersion: tls.VersionTLS12}
	if ca != "" {
		pem, err := os.ReadFile(ca)
		if err != nil {
			return nil, "", fmt.Errorf("read OTLP CA certificate: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, "", fmt.Errorf("OTLP CA certificate %s holds no PEM certificate", ca)
		}
		cfg.RootCAs = pool
	}
	if cert != "" || keyFile != "" {
		pair, err := tls.LoadX509KeyPair(cert, keyFile)
		if err != nil {
			return nil, "", fmt.Errorf("load OTLP client certificate: %w", err)
		}
		cfg.Certificates = []tls.Certificate{pair}
	}
	return cfg, key, nil
}
