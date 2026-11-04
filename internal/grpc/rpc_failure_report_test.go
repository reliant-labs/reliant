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

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
	"google.golang.org/protobuf/types/known/emptypb"
)

// One failed RPC is one ERROR record, naming the procedure that failed.
//
// An ERROR record is the unit of a server error: the control plane's issues
// worker groups them by fingerprint (forge's sfp1: the logging function, the
// procedure, the message and the error type). These drive a failing handler
// through the real interceptor chain (NewUserChain: forge's Trace → Recovery →
// RequestID → Logging → … and Reliant's layers) over HTTP, with the process
// logger installed the way every server installs it (logging.Setup: forge's
// error-class policy and the metrics bridge), and read the lines it writes.
// So they pin what becomes an issue for a failed RPC, not what one layer hands
// the next.
//
// Before, one failed GetFileTree (prod, 2026-10-10T20:52:28Z) was three
// issues: forge's "rpc failed", the ErrorReporterInterceptor's duplicate
// "[gRPC] Request error" line, and an exception capture.

const (
	getFileTreeProcedure = "/reliant.v1.FileSystemService/GetFileTree"
	sendMessageProcedure = "/reliant.v1.ChatService/SendMessage"
	watchFilesProcedure  = "/reliant.v1.FileSystemService/WatchFiles"
)

// syncBuffer is the process log output: the servers write to it concurrently.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// matching returns every JSON log line keep accepts.
func (b *syncBuffer) matching(keep func(map[string]any) bool) []map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(b.buf.Bytes()))
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		var record map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			continue
		}
		if keep(record) {
			out = append(out, record)
		}
	}
	return out
}

// records returns every JSON log line with the given message.
func (b *syncBuffer) records(t *testing.T, message string) []map[string]any {
	t.Helper()
	return b.matching(func(r map[string]any) bool { return r["msg"] == message })
}

// errors returns every ERROR line: each one is an issue event.
func (b *syncBuffer) errors() []map[string]any {
	return b.matching(func(r map[string]any) bool { return r["level"] == "ERROR" })
}

// installProcessLogging installs the process logger as the servers do,
// writing JSON lines to the returned buffer. Everything is restored when the
// test ends.
func installProcessLogging(t *testing.T) *syncBuffer {
	t.Helper()

	previousLogger := slog.Default()
	previousOutput := logging.DefaultOutput
	previousTrace := logging.TraceEnabled
	t.Cleanup(func() {
		logging.DefaultOutput = previousOutput
		logging.TraceEnabled = previousTrace
		slog.SetDefault(previousLogger)
	})

	lines := &syncBuffer{}
	logging.DefaultOutput = lines
	logging.Setup(logging.Settings{Format: "json"}, slog.LevelInfo)
	return lines
}

// failingAPI serves each procedure in fail with a handler that returns its
// error, behind the interceptor chain the API server mounts every service with.
type failingAPI struct {
	url string
}

func newFailingAPI(t *testing.T, fail map[string]error) failingAPI {
	t.Helper()
	opts := connect.WithInterceptors(userChainInterceptors(t)...)
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
	return svcerr.Code(err)
}

// daemonFault is a daemon command that reached the machine and failed there:
// a real fault, which the fs proxy answers Internal. id varies per request,
// as the subject and paths in real daemon errors do.
func daemonFault(id string) error {
	return connect.NewError(connect.CodeInternal, fmt.Errorf(
		"daemon command fs.get_tree (subject reliant.daemon.%s.command) via NATS failed: %w",
		id, errors.New("decode tree: unexpected EOF")))
}

func TestFailedRPCIsOneErrorRecordNamingItsProcedure(t *testing.T) {
	lines := installProcessLogging(t)

	const firstID, secondID = "2aab1465-0d3c-4b8e-9b1e-5f0c1d2e3f40", "66a045ce-5872-4efe-a510-910f63fcb8a6"
	api := newFailingAPI(t, map[string]error{getFileTreeProcedure: daemonFault(firstID)})
	again := newFailingAPI(t, map[string]error{
		getFileTreeProcedure: daemonFault(secondID),
		sendMessageProcedure: daemonFault(firstID),
	})

	require.Equal(t, connect.CodeInternal, api.call(t, getFileTreeProcedure, "34699621-e7ef-4eb9-985c-911e84222c8c"))
	errs := lines.errors()
	require.Len(t, errs, 1, "one failed call is one issue event, not one per layer: %v", errs)
	first := errs[0]
	assert.Equal(t, "rpc failed", first["msg"], "the report is forge's record of the failed call")
	assert.Equal(t, getFileTreeProcedure, first["procedure"], "grouped by the procedure that failed")
	assert.Equal(t, "internal", first["code"])

	// Another procedure failing with the same error: its own record, naming
	// its own procedure, so its own issue.
	require.Equal(t, connect.CodeInternal, again.call(t, getFileTreeProcedure, "0b1c2d3e-4f50-6172-8394-a5b6c7d8e9f0"))
	require.Equal(t, connect.CodeInternal, again.call(t, sendMessageProcedure, "34699621-e7ef-4eb9-985c-911e84222c8c"))
	errs = lines.errors()
	require.Len(t, errs, 3)
	assert.Equal(t, sendMessageProcedure, errs[2]["procedure"])
}

func TestFailedStreamIsOneErrorRecord(t *testing.T) {
	lines := installProcessLogging(t)
	api := newFailingAPI(t, map[string]error{watchFilesProcedure: daemonFault("2aab1465-0d3c-4b8e-9b1e-5f0c1d2e3f40")})

	client := connect.NewClient[emptypb.Empty, emptypb.Empty](http.DefaultClient, api.url+watchFilesProcedure)
	stream, err := client.CallServerStream(context.Background(), connect.NewRequest(&emptypb.Empty{}))
	require.NoError(t, err)
	for stream.Receive() {
	}
	require.Equal(t, connect.CodeInternal, svcerr.Code(stream.Err()))
	require.NoError(t, stream.Close())

	errs := lines.errors()
	require.Len(t, errs, 1, "%v", errs)
	assert.Equal(t, "stream failed", errs[0]["msg"])
	assert.Equal(t, watchFilesProcedure, errs[0]["procedure"])
}

// The prod failure itself, as the fs proxy now answers it: a connected machine
// that did not answer before the caller's deadline is DeadlineExceeded, which
// forge writes at WARN, so it is never an issue.
func TestDaemonTimeoutRPCIsNotAnErrorRecord(t *testing.T) {
	lines := installProcessLogging(t)
	timeout := connect.NewError(connect.CodeDeadlineExceeded, fmt.Errorf(
		"daemon command fs.get_tree (subject reliant.daemon.2aab1465.command) via NATS failed: %w: %w",
		toolexec.ErrDaemonTimeout, context.DeadlineExceeded))
	api := newFailingAPI(t, map[string]error{
		getFileTreeProcedure: timeout,
		sendMessageProcedure: daemonFault("2aab1465-0d3c-4b8e-9b1e-5f0c1d2e3f40"),
	})

	require.Equal(t, connect.CodeDeadlineExceeded, api.call(t, getFileTreeProcedure, "req-timeout"))
	api.call(t, sendMessageProcedure, "req-fault")
	errs := lines.errors()
	require.Len(t, errs, 1, "only the real fault is an ERROR: %v", errs)
	assert.Equal(t, sendMessageProcedure, errs[0]["procedure"])

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
	lines := installProcessLogging(t)
	benign := temporal.NewApplicationErrorWithOptions("AI provider usage limit reached", "ProviderUsageLimit",
		temporal.ApplicationErrorOptions{NonRetryable: true, Category: temporal.ApplicationErrorCategoryBenign})
	api := newFailingAPI(t, map[string]error{
		sendMessageProcedure: connect.NewError(connect.CodeInternal, fmt.Errorf("start chat: %w", benign)),
		getFileTreeProcedure: daemonFault("2aab1465-0d3c-4b8e-9b1e-5f0c1d2e3f40"),
	})

	require.Equal(t, connect.CodeInternal, api.call(t, sendMessageProcedure, "req-benign"), "the wire code is the handler's")
	api.call(t, getFileTreeProcedure, "req-fault")
	errs := lines.errors()
	require.Len(t, errs, 1, "only the real fault is an ERROR: %v", errs)
	assert.Equal(t, getFileTreeProcedure, errs[0]["procedure"])

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
