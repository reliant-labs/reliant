package observe

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	logglobal "go.opentelemetry.io/otel/log/global"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
)

func init() {
	otelEnvVars = append(otelEnvVars, EnvOTLPHeadersFile, "OTEL_EXPORTER_OTLP_TRACES_HEADERS_FILE")
}

// recheckEveryRequest makes every request re-read the file, and restores the
// production intervals afterwards.
func recheckEveryRequest(t *testing.T) {
	t.Helper()
	r, w := headersFileRecheck, headersFileWarnEvery
	headersFileRecheck = 0
	t.Cleanup(func() { headersFileRecheck, headersFileWarnEvery = r, w })
}

// writeHeadersFile atomically replaces path, the way a kubelet Secret refresh
// swaps its symlink.
func writeHeadersFile(t *testing.T, path, content string) {
	t.Helper()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func tracesWith(t *testing.T, cfg Config) {
	t.Helper()
	shutdown, _, err := Setup(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	emitSpan(context.Background())
	flush(t, shutdown)
}

func TestParseOTLPHeaders(t *testing.T) {
	got := parseOTLPHeaders("authorization=Bearer%20abc, x-team=forge\n=novalue\nbare\nx-eq=a=b\r\n")
	want := []headerKV{{"authorization", "Bearer abc"}, {"x-team", "forge"}, {"x-eq", "a=b"}}
	if len(got) != len(want) {
		t.Fatalf("parsed %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("header %d = %v, want %v", i, got[i], want[i])
		}
	}
	if len(parseOTLPHeaders("  \n")) != 0 {
		t.Error("blank content must parse to no headers")
	}
}

func TestHeadersFileIsReReadOnlyWhenItChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "headers")
	writeHeadersFile(t, path, "authorization=one")
	now := time.Unix(1_000_000, 0)
	f := &headerFile{path: path, now: func() time.Time { return now }, log: slog.Default()}

	if got := f.current(); len(got) != 1 || got[0].value != "one" {
		t.Fatalf("first read = %v", got)
	}
	// Inside the recheck window nothing is even read, so the cache answers.
	writeHeadersFile(t, path, "authorization=two")
	if got := f.current(); got[0].value != "one" {
		t.Fatalf("within the recheck window the cached value must be served, got %v", got)
	}
	now = now.Add(headersFileRecheck + time.Second)
	if got := f.current(); got[0].value != "two" {
		t.Fatalf("after the window the new content must be read, got %v", got)
	}
}

// TestHeadersFileChangeIsDecidedByContent rewrites the file in place with
// content of the same size and puts its mtime back: the same file, the same
// size, the same mtime. That is what two writes inside one coarse filesystem
// clock tick look like, and on Windows os.SameFile cannot tell even a replaced
// file from its predecessor. Only the bytes say the credential changed.
func TestHeadersFileChangeIsDecidedByContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "headers")
	if err := os.WriteFile(path, []byte("authorization=one"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_000_000, 0)
	f := &headerFile{path: path, now: func() time.Time { return now }, log: slog.Default()}
	if got := f.current(); len(got) != 1 || got[0].value != "one" {
		t.Fatalf("first read = %v", got)
	}

	if err := os.WriteFile(path, []byte("authorization=two"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("premise broken: size %d→%d, mtime %v→%v must not move", before.Size(), after.Size(), before.ModTime(), after.ModTime())
	}

	now = now.Add(headersFileRecheck + time.Second)
	if got := f.current(); len(got) != 1 || got[0].value != "two" {
		t.Fatalf("a rewrite that keeps size and mtime must still be read, got %v", got)
	}
}

func TestRotatingTheFileMidExportChangesTheNextRequest(t *testing.T) {
	clearOTelEnv(t)
	snapshotGlobals(t)
	recheckEveryRequest(t)
	rcv := newOTLPReceiver(t)
	path := filepath.Join(t.TempDir(), "otlp-headers")
	writeHeadersFile(t, path, "authorization=Bearer old-token")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", rcv.URL)
	t.Setenv("OTEL_METRICS_EXPORTER", "none")
	t.Setenv(EnvOTLPHeadersFile, path)

	// One long-lived exporter: the same Setup exports twice, and the file is
	// rotated between the two exports.
	shutdown, _, err := Setup(context.Background(), Config{ServiceName: "svc"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })
	tp := otel.GetTracerProvider().(interface {
		ForceFlush(context.Context) error
	})

	emitSpan(context.Background())
	if err := tp.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := rcv.requests("/v1/traces")
	if len(first) != 1 || first[0].Header.Get("Authorization") != "Bearer old-token" {
		t.Fatalf("first export = %+v", first)
	}

	writeHeadersFile(t, path, "authorization=Bearer new-token, x-added=yes")
	emitSpan(context.Background())
	if err := tp.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	all := rcv.requests("/v1/traces")
	if len(all) != 2 {
		t.Fatalf("want 2 trace exports, saw %d", len(all))
	}
	if got := all[1].Header.Get("Authorization"); got != "Bearer new-token" {
		t.Errorf("Authorization after rotation = %q, want the new token", got)
	}
	if all[1].Header.Get("X-Added") != "yes" {
		t.Errorf("a header added to the file must reach the next request: %v", all[1].Header)
	}
}

func TestEveryHTTPSignalReadsTheFile(t *testing.T) {
	clearOTelEnv(t)
	snapshotGlobals(t)
	recheckEveryRequest(t)
	rcv := newOTLPReceiver(t)
	path := filepath.Join(t.TempDir(), "h")
	writeHeadersFile(t, path, "authorization=file-token")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", rcv.URL)
	t.Setenv("OTEL_LOGS_EXPORTER", "otlp")
	t.Setenv(EnvOTLPHeadersFile, path)

	shutdown, _, err := Setup(context.Background(), Config{ServiceName: "svc"})
	if err != nil {
		t.Fatal(err)
	}
	emitSpan(context.Background())
	var rec otellog.Record
	rec.SetBody(attribute.StringValue("hello"))
	rec.SetTimestamp(time.Now())
	logglobal.GetLoggerProvider().Logger("headers-test").Emit(context.Background(), rec)
	flush(t, shutdown)

	for _, p := range []string{"/v1/traces", "/v1/metrics", "/v1/logs"} {
		got := rcv.requests(p)
		if len(got) == 0 {
			t.Errorf("%s: no export reached the receiver", p)
			continue
		}
		if got[0].Header.Get("Authorization") != "file-token" {
			t.Errorf("%s: headers %v lack the file's authorization", p, got[0].Header)
		}
	}
}

func TestExplicitEnvHeadersBeatTheFile(t *testing.T) {
	clearOTelEnv(t)
	snapshotGlobals(t)
	recheckEveryRequest(t)
	rcv := newOTLPReceiver(t)
	path := filepath.Join(t.TempDir(), "h")
	writeHeadersFile(t, path, "authorization=from-file,x-file-only=1,X-Team=file-team")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", rcv.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "authorization=from-env,x-team=env-team")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_HEADERS", "x-team=metrics-team")
	t.Setenv(EnvOTLPHeadersFile, path)

	tracesWith(t, Config{ServiceName: "svc"})
	got := rcv.requests("/v1/traces")
	if len(got) == 0 {
		t.Fatal("no export")
	}
	h := got[0].Header
	if h.Get("Authorization") != "from-env" || h.Get("X-Team") != "env-team" {
		t.Errorf("explicit OTEL_EXPORTER_OTLP_HEADERS must win, got %v", h)
	}
	if h.Get("X-File-Only") != "1" {
		t.Errorf("a header only the file carries must still be sent, got %v", h)
	}

	// The per-signal variable replaces the general one for its signal (SDK
	// semantics), so it wins for the names it carries and the file fills in
	// the rest, authorization included.
	m := rcv.requests("/v1/metrics")
	if len(m) == 0 {
		t.Fatal("no metrics export")
	}
	if m[0].Header.Get("X-Team") != "metrics-team" || m[0].Header.Get("Authorization") != "from-file" {
		t.Errorf("metrics headers = %v", m[0].Header)
	}
}

func TestPerSignalHeadersFileOverridesTheGeneralOne(t *testing.T) {
	clearOTelEnv(t)
	dir := t.TempDir()
	general, perSignal := filepath.Join(dir, "g"), filepath.Join(dir, "t")
	writeHeadersFile(t, general, "authorization=general")
	writeHeadersFile(t, perSignal, "authorization=traces")
	t.Setenv(EnvOTLPHeadersFile, general)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_HEADERS_FILE", perSignal)
	if src := headerSourceFor(signalTraces); src == nil || src.file.path != perSignal {
		t.Errorf("traces must use the per-signal file, got %+v", src)
	}
	if src := headerSourceFor(signalMetrics); src == nil || src.file.path != general {
		t.Errorf("metrics must use the general file, got %+v", src)
	}
}

func TestNoHeadersFileMeansNoSource(t *testing.T) {
	clearOTelEnv(t)
	for _, s := range []signal{signalTraces, signalMetrics, signalLogs, signalProfiles} {
		if headerSourceFor(s) != nil {
			t.Errorf("%s: a source exists without the variable", s)
		}
	}
}

// warnCapture installs a slog default that records every record, and restores it.
func warnCapture(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	var mu sync.Mutex
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&lockedWriter{&mu, &buf}, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

type lockedWriter struct {
	mu *sync.Mutex
	b  *bytes.Buffer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func TestMissingFileExportsWithoutTheHeaderAndWarnsOncePerInterval(t *testing.T) {
	clearOTelEnv(t)
	snapshotGlobals(t)
	recheckEveryRequest(t)
	headersFileWarnEvery = time.Hour
	logs := warnCapture(t)
	rcv := newOTLPReceiver(t)
	path := filepath.Join(t.TempDir(), "absent")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", rcv.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "x-env=kept")
	t.Setenv(EnvOTLPHeadersFile, path)

	for i := 0; i < 3; i++ {
		tracesWith(t, Config{ServiceName: "svc"})
	}
	got := rcv.requests("/v1/traces")
	if len(got) == 0 {
		t.Fatal("a missing headers file must not stop the export")
	}
	if got[0].Header.Get("Authorization") != "" || got[0].Header.Get("X-Env") != "kept" {
		t.Errorf("headers = %v, want only the env ones", got[0].Header)
	}
	// Three exporters x several requests each, one warning.
	if n := strings.Count(logs.String(), "OTLP headers file unusable"); n != 1 {
		t.Errorf("want exactly 1 WARN for the interval, got %d:\n%s", n, logs)
	}
}

func TestFileThatAppearsLaterIsPickedUp(t *testing.T) {
	clearOTelEnv(t)
	recheckEveryRequest(t)
	logs := warnCapture(t)
	path := filepath.Join(t.TempDir(), "late")
	f := sharedHeaderFile(path)
	if got := f.current(); len(got) != 0 {
		t.Fatalf("absent file must yield no headers, got %v", got)
	}
	writeHeadersFile(t, path, "authorization=arrived")
	if got := f.current(); len(got) != 1 || got[0].value != "arrived" {
		t.Fatalf("the file appearing must be noticed, got %v", got)
	}
	if !strings.Contains(logs.String(), "readable again") {
		t.Errorf("recovery should be logged once:\n%s", logs)
	}
}

func TestFileRemovedAfterwardsDropsTheStaleToken(t *testing.T) {
	recheckEveryRequest(t)
	warnCapture(t)
	path := filepath.Join(t.TempDir(), "gone")
	writeHeadersFile(t, path, "authorization=stale")
	f := sharedHeaderFile(path)
	if len(f.current()) != 1 {
		t.Fatal("setup: file should be read")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := f.current(); len(got) != 0 {
		t.Errorf("a removed file must not keep serving its last token, got %v", got)
	}
}

func TestEmptyFileWarnsAndSendsNothing(t *testing.T) {
	recheckEveryRequest(t)
	logs := warnCapture(t)
	path := filepath.Join(t.TempDir(), "empty")
	writeHeadersFile(t, path, "\n")
	if got := sharedHeaderFile(path).current(); len(got) != 0 {
		t.Errorf("got %v", got)
	}
	if !strings.Contains(logs.String(), "no key=value headers") {
		t.Errorf("an empty file should say so:\n%s", logs)
	}
}

func TestGRPCSignalsReadTheFilePerRPC(t *testing.T) {
	clearOTelEnv(t)
	snapshotGlobals(t)
	recheckEveryRequest(t)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	sink := &traceSink{}
	srv := grpc.NewServer()
	collectortrace.RegisterTraceServiceServer(srv, sink)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	path := filepath.Join(t.TempDir(), "h")
	writeHeadersFile(t, path, "authorization=grpc-old, x-env=file")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://"+lis.Addr().String())
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")
	t.Setenv("OTEL_METRICS_EXPORTER", "none")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "x-env=explicit")
	t.Setenv(EnvOTLPHeadersFile, path)

	shutdown, _, err := Setup(context.Background(), Config{ServiceName: "svc"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })
	tp := otel.GetTracerProvider().(interface{ ForceFlush(context.Context) error })

	emitSpan(context.Background())
	if err := tp.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	writeHeadersFile(t, path, "authorization=grpc-new, x-env=file")
	emitSpan(context.Background())
	if err := tp.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.md) != 2 {
		t.Fatalf("want 2 gRPC exports, saw %d", len(sink.md))
	}
	if v := sink.md[0].Get("authorization"); len(v) != 1 || v[0] != "grpc-old" {
		t.Errorf("first authorization = %v", v)
	}
	if v := sink.md[1].Get("authorization"); len(v) != 1 || v[0] != "grpc-new" {
		t.Errorf("authorization after rotation = %v, want grpc-new", v)
	}
	if v := sink.md[1].Get("x-env"); len(v) != 1 || v[0] != "explicit" {
		t.Errorf("explicit env header must win over the file on gRPC too, got %v", v)
	}
}

func TestProfilesSenderReadsTheFilePerSend(t *testing.T) {
	clearProfilesEnv(t)
	recheckEveryRequest(t)
	var mu sync.Mutex
	var auths []string
	srv := newProfilesTestServer(t, func(r *http.Request) {
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization")+"|"+r.Header.Get("X-Env"))
		mu.Unlock()
	})
	path := filepath.Join(t.TempDir(), "h")
	writeHeadersFile(t, path, "authorization=p-old,x-env=file")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", srv.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "x-env=explicit")
	t.Setenv(EnvOTLPHeadersFile, path)

	tgt, err := resolveProfilesTarget(Config{})
	if err != nil {
		t.Fatal(err)
	}
	send, closeFn, err := newProfilesSender(tgt)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	pd, err := pprofToOTLP(encode(t, goCPUProfile()), testResource())
	if err != nil {
		t.Fatal(err)
	}
	if err := send(context.Background(), pd); err != nil {
		t.Fatal(err)
	}
	writeHeadersFile(t, path, "authorization=p-new,x-env=file")
	if err := send(context.Background(), pd); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"p-old|explicit", "p-new|explicit"}
	if len(auths) != 2 || auths[0] != want[0] || auths[1] != want[1] {
		t.Errorf("profile requests carried %v, want %v", auths, want)
	}
}

func TestHeadersFileLeaksNoGoroutines(t *testing.T) {
	clearOTelEnv(t)
	snapshotGlobals(t)
	recheckEveryRequest(t)
	rcv := newOTLPReceiver(t)
	path := filepath.Join(t.TempDir(), "h")
	writeHeadersFile(t, path, "authorization=x")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", rcv.URL)
	t.Setenv("OTEL_PROFILES_EXPORTER", "none")
	t.Setenv(EnvOTLPHeadersFile, path)

	tracesWith(t, Config{ServiceName: "warm"}) // warm lazily started runtime goroutines
	rcv.CloseClientConnections()
	http.DefaultClient.CloseIdleConnections()
	time.Sleep(50 * time.Millisecond)
	before := runtime.NumGoroutine()
	for i := 0; i < 5; i++ {
		tracesWith(t, Config{ServiceName: "svc"})
		writeHeadersFile(t, path, "authorization=y"+strings.Repeat("z", i))
	}
	deadline := time.Now().Add(5 * time.Second)
	var after int
	for time.Now().Before(deadline) {
		after = runtime.NumGoroutine()
		if after <= before {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	buf := make([]byte, 1<<16)
	t.Errorf("goroutines grew from %d to %d across 5 setup/shutdown cycles\n%s", before, after, buf[:runtime.Stack(buf, true)])
}

func newProfilesTestServer(t *testing.T, onRequest func(*http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		onRequest(r)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}
