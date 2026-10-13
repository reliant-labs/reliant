// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/getsentry/sentry-go"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
	"google.golang.org/protobuf/types/known/emptypb"
)

// One failed RPC is one Sentry issue, grouped by the procedure that failed.
//
// These drive a failing handler through the real interceptor chain
// (newInterceptors: forge's Recovery → RequestID → Logging → … and Reliant's
// extras) over HTTP, with the process logger installed the way every server
// installs it (logging.Setup: forge's error-class policy, the metrics and
// Sentry bridges), a live SentryReporter, and a Sentry transport the test
// reads. So they pin what Sentry receives for a failed RPC, not what one layer
// hands the next.
//
// Before, one failed GetFileTree (prod, 2026-10-10T20:52:28Z) was three
// issues: forge's "rpc failed", forwarded by the bridge and grouped with every
// other procedure's failure (ELECTRON-CH); the ErrorReporterInterceptor's
// duplicate "[gRPC] Request error" line (ELECTRON-CJ); and its exception
// capture (ELECTRON-AW).

const (
	getFileTreeProcedure = "/reliant.v1.FileSystemService/GetFileTree"
	sendMessageProcedure = "/reliant.v1.ChatService/SendMessage"
	watchFilesProcedure  = "/reliant.v1.FileSystemService/WatchFiles"

	// reportSettle is how long a test waits, after the reports it expects,
	// for one it does not: the bridge reports from its own goroutine.
	reportSettle = 200 * time.Millisecond
)

// sentryRecorder is a Sentry transport that keeps what the client would send.
type sentryRecorder struct {
	mu     sync.Mutex
	events []*sentry.Event
	sent   chan struct{}
}

func (r *sentryRecorder) Flush(time.Duration) bool              { return true }
func (r *sentryRecorder) FlushWithContext(context.Context) bool { return true }
func (r *sentryRecorder) Configure(sentry.ClientOptions)        {}
func (r *sentryRecorder) Close()                                {}
func (r *sentryRecorder) SendEvent(event *sentry.Event) {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
	r.sent <- struct{}{}
}

// awaitExactly waits for n more events, then for reportSettle, and fails if
// any further event arrives. It returns every event recorded so far.
func (r *sentryRecorder) awaitExactly(t *testing.T, n int) []*sentry.Event {
	t.Helper()
	for i := range n {
		select {
		case <-r.sent:
		case <-time.After(5 * time.Second):
			t.Fatalf("expected %d Sentry event(s), got %d", n, i)
		}
	}
	select {
	case <-r.sent:
		r.mu.Lock()
		defer r.mu.Unlock()
		last := r.events[len(r.events)-1]
		t.Fatalf("expected %d Sentry event(s), got another: %q (exception=%v, fingerprint=%v)",
			n, last.Message, exceptionTypes(last), last.Fingerprint)
	case <-time.After(reportSettle):
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*sentry.Event(nil), r.events...)
}

func exceptionTypes(event *sentry.Event) []string {
	var types []string
	for _, exception := range event.Exception {
		types = append(types, exception.Type)
	}
	return types
}

// syncBuffer is the process log output: the bridge's goroutines and the
// servers' write to it concurrently.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// records returns every JSON log line with the given message.
func (b *syncBuffer) records(t *testing.T, message string) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(b.buf.Bytes()))
	for scanner.Scan() {
		var record map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			continue
		}
		if record["msg"] == message {
			out = append(out, record)
		}
	}
	return out
}

// installProcessReporting installs the process logger as the servers do,
// writing JSON lines to the returned buffer, and makes the process reporter a
// live SentryReporter whose events land on the returned recorder. Everything
// is restored when the test ends.
func installProcessReporting(t *testing.T) (*sentryRecorder, *syncBuffer) {
	t.Helper()

	previousLogger := slog.Default()
	previousOutput := logging.DefaultOutput
	previousTrace := logging.TraceEnabled
	hub := sentry.CurrentHub()
	previousClient := hub.Client()
	previousReporter := telemetry.GetReporter()
	t.Cleanup(func() {
		telemetry.SetReporter(previousReporter)
		hub.BindClient(previousClient)
		logging.DefaultOutput = previousOutput
		logging.TraceEnabled = previousTrace
		slog.SetDefault(previousLogger)
	})

	lines := &syncBuffer{}
	logging.DefaultOutput = lines
	logging.Setup(logging.Settings{Format: "json"}, slog.LevelInfo)

	reporter, err := telemetry.NewSentryReporter(telemetry.SentryConfig{
		Enabled: true,
		DSN:     "https://public@example.ingest.sentry.io/1",
	})
	require.NoError(t, err)
	require.NotNil(t, reporter)
	recorder := &sentryRecorder{sent: make(chan struct{}, 64)}
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn:       "https://public@example.ingest.sentry.io/1",
		Transport: recorder,
		// Without a release the SDK derives one by running git, a child
		// process that costs most of a second.
		Release: "reliant@test",
	})
	require.NoError(t, err)
	hub.BindClient(client)
	telemetry.SetReporter(reporter)
	return recorder, lines
}

// failingAPI serves each procedure in fail with a handler that returns its
// error, behind the interceptor chain the API server mounts every service with.
type failingAPI struct {
	url string
}

func newFailingAPI(t *testing.T, fail map[string]error) failingAPI {
	t.Helper()
	opts := connect.WithInterceptors(newInterceptors(true, &testNamedInterceptor{})...)
	mux := http.NewServeMux()
	for procedure, err := range fail {
		if procedure == watchFilesProcedure {
			mux.Handle(procedure, connect.NewServerStreamHandler(procedure,
				func(context.Context, *connect.Request[emptypb.Empty], *connect.ServerStream[emptypb.Empty]) error {
					return err
				}, opts))
			continue
		}
		mux.Handle(procedure, connect.NewUnaryHandler(procedure,
			func(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
				return nil, err
			}, opts))
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return failingAPI{url: server.URL}
}

// call makes one unary call, with requestID as its correlation header, and
// returns the code the client received.
func (a failingAPI) call(t *testing.T, procedure, requestID string) connect.Code {
	t.Helper()
	client := connect.NewClient[emptypb.Empty, emptypb.Empty](http.DefaultClient, a.url+procedure)
	req := connect.NewRequest(&emptypb.Empty{})
	req.Header().Set("X-Request-Id", requestID)
	_, err := client.CallUnary(context.Background(), req)
	require.Error(t, err)
	return connect.CodeOf(err)
}

// daemonFault is a daemon command that reached the machine and failed there:
// a real fault, which the fs proxy answers Internal. id varies per request,
// as the subject and paths in real daemon errors do.
func daemonFault(id string) error {
	return connect.NewError(connect.CodeInternal, fmt.Errorf(
		"daemon command fs.get_tree (subject reliant.daemon.%s.command) via NATS failed: %w",
		id, errors.New("decode tree: unexpected EOF")))
}

func TestFailedRPCIsOneSentryEventGroupedByProcedure(t *testing.T) {
	recorder, _ := installProcessReporting(t)

	const firstID, secondID = "2aab1465-0d3c-4b8e-9b1e-5f0c1d2e3f40", "66a045ce-5872-4efe-a510-910f63fcb8a6"
	api := newFailingAPI(t, map[string]error{getFileTreeProcedure: daemonFault(firstID)})
	again := newFailingAPI(t, map[string]error{
		getFileTreeProcedure: daemonFault(secondID),
		sendMessageProcedure: daemonFault(firstID),
	})

	require.Equal(t, connect.CodeInternal, api.call(t, getFileTreeProcedure, "34699621-e7ef-4eb9-985c-911e84222c8c"))
	events := recorder.awaitExactly(t, 1)
	first := events[0]
	assert.Equal(t, "rpc failed", first.Message, "the report is forge's record of the failed call")
	assert.Empty(t, first.Exception, "a failed RPC is not also captured as an exception (ELECTRON-AW)")
	require.Len(t, first.Fingerprint, 5)
	assert.Equal(t, []string{getFileTreeProcedure, "internal"}, first.Fingerprint[3:],
		"grouped by the procedure that failed and its code, not only by the interceptor that logged it")
	assert.Equal(t, "FileSystemService/GetFileTree", first.Transaction)
	assert.Equal(t, getFileTreeProcedure, first.Tags["procedure"])

	// The same procedure failing the same way for another daemon and
	// request: other ids, the same issue.
	require.Equal(t, connect.CodeInternal, again.call(t, getFileTreeProcedure, "0b1c2d3e-4f50-6172-8394-a5b6c7d8e9f0"))
	events = recorder.awaitExactly(t, 1)
	assert.Equal(t, first.Fingerprint, events[1].Fingerprint, "ids and error text do not split a procedure's issue")

	// Another procedure failing with the same error: another issue.
	require.Equal(t, connect.CodeInternal, again.call(t, sendMessageProcedure, "34699621-e7ef-4eb9-985c-911e84222c8c"))
	events = recorder.awaitExactly(t, 1)
	assert.NotEqual(t, first.Fingerprint, events[2].Fingerprint, "another procedure failing is another issue")
	assert.Equal(t, []string{sendMessageProcedure, "internal"}, events[2].Fingerprint[3:])
}

func TestFailedStreamIsOneSentryEvent(t *testing.T) {
	recorder, _ := installProcessReporting(t)
	api := newFailingAPI(t, map[string]error{watchFilesProcedure: daemonFault("2aab1465-0d3c-4b8e-9b1e-5f0c1d2e3f40")})

	client := connect.NewClient[emptypb.Empty, emptypb.Empty](http.DefaultClient, api.url+watchFilesProcedure)
	stream, err := client.CallServerStream(context.Background(), connect.NewRequest(&emptypb.Empty{}))
	require.NoError(t, err)
	for stream.Receive() {
	}
	require.Equal(t, connect.CodeInternal, connect.CodeOf(stream.Err()))
	require.NoError(t, stream.Close())

	events := recorder.awaitExactly(t, 1)
	assert.Equal(t, "stream failed", events[0].Message)
	assert.Empty(t, events[0].Exception)
	assert.Equal(t, []string{watchFilesProcedure, "internal"}, events[0].Fingerprint[3:])
}

// The prod failure itself, as the fs proxy now answers it: a connected machine
// that did not answer before the caller's deadline is DeadlineExceeded, which
// forge writes at WARN and the bridge never forwards.
func TestDaemonTimeoutRPCIsNotASentryEvent(t *testing.T) {
	recorder, lines := installProcessReporting(t)
	timeout := connect.NewError(connect.CodeDeadlineExceeded, fmt.Errorf(
		"daemon command fs.get_tree (subject reliant.daemon.2aab1465.command) via NATS failed: %w: %w",
		toolexec.ErrDaemonTimeout, context.DeadlineExceeded))
	api := newFailingAPI(t, map[string]error{
		getFileTreeProcedure: timeout,
		sendMessageProcedure: daemonFault("2aab1465-0d3c-4b8e-9b1e-5f0c1d2e3f40"),
	})

	require.Equal(t, connect.CodeDeadlineExceeded, api.call(t, getFileTreeProcedure, "req-timeout"))
	// A failure that is reported, after the one that must not be: once its
	// event is in, the timeout's would have been too.
	api.call(t, sendMessageProcedure, "req-fault")
	events := recorder.awaitExactly(t, 1)
	assert.Equal(t, sendMessageProcedure, events[0].Tags["procedure"], "only the real fault reaches Sentry")

	record := rpcFailedRecord(t, lines, getFileTreeProcedure)
	assert.Equal(t, "WARN", record["level"], "visible and retryable, never a page")
	assert.Equal(t, "deadline_exceeded", record["code"])
}

// A failure nobody has to act on — a workflow error Temporal carries as
// Benign — is logged by the one record a failed RPC gets at the user-error
// level, as the process logger logs every other record carrying it
// (errclass.Library). The record already carries error_class when it reaches
// the process handler, which then trusts it, so the classifier has to be the
// logging interceptor's own.
func TestBenignWorkflowFailureRPCIsLoggedAtUserLevel(t *testing.T) {
	recorder, lines := installProcessReporting(t)
	benign := temporal.NewApplicationErrorWithOptions("AI provider usage limit reached", "ProviderUsageLimit",
		temporal.ApplicationErrorOptions{NonRetryable: true, Category: temporal.ApplicationErrorCategoryBenign})
	api := newFailingAPI(t, map[string]error{
		sendMessageProcedure: connect.NewError(connect.CodeInternal, fmt.Errorf("start chat: %w", benign)),
		getFileTreeProcedure: daemonFault("2aab1465-0d3c-4b8e-9b1e-5f0c1d2e3f40"),
	})

	require.Equal(t, connect.CodeInternal, api.call(t, sendMessageProcedure, "req-benign"), "the wire code is the handler's")
	api.call(t, getFileTreeProcedure, "req-fault")
	events := recorder.awaitExactly(t, 1)
	assert.Equal(t, getFileTreeProcedure, events[0].Tags["procedure"], "only the real fault reaches Sentry")

	record := rpcFailedRecord(t, lines, sendMessageProcedure)
	assert.Equal(t, "INFO", record["level"])
	assert.Equal(t, "user", record["error_class"])
}

// rpcFailedRecord is the one "rpc failed" line logged for procedure.
func rpcFailedRecord(t *testing.T, lines *syncBuffer, procedure string) map[string]any {
	t.Helper()
	var matched []map[string]any
	for _, record := range lines.records(t, "rpc failed") {
		if record["procedure"] == procedure {
			matched = append(matched, record)
		}
	}
	require.Len(t, matched, 1, "one log record per failed call; got lines for %s: %v", procedure,
		strings.TrimSpace(fmt.Sprint(matched)))
	return matched[0]
}
