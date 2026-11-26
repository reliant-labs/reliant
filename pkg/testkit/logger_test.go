package testkit_test

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"

	"connectrpc.com/connect"
	connectv2 "connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// The children below are run by TestLogger_ShowsTheServerSideOfAFailedTest,
// in a re-exec of this test binary: a test cannot observe its own output, and
// the behaviour under test is what `go test` prints for a failing test.
var loggerChild = flag.Bool("testkit-logger-child", false, "run the logger subprocess fixture")

// driverText is what a repository's driver error says, and exactly what the
// client must not read.
const driverText = `select jobs: ERROR: relation "jobs" does not exist (SQLSTATE 42P01)`

const failProcedure = "/testkit.loggertest.v1.JobService/ListJobs"

// failingService registers a handler that logs through Logger(t), as a
// generated harness's Deps.Logger does, then fails with an error nothing
// classified.
func failingService(t *testing.T) func(*http.ServeMux, ...connect.HandlerOption) {
	logger := testkit.Logger(t).With("service", "jobs")
	return func(mux *http.ServeMux, opts ...connect.HandlerOption) {
		mux.Handle(failProcedure, connect.NewUnaryHandler(failProcedure,
			func(ctx context.Context, _ *connect.Request[structpb.Struct]) (*connect.Response[structpb.Struct], error) {
				logger.InfoContext(ctx, "listing jobs")
				return nil, errors.New(driverText)
			}, opts...))
	}
}

func failingClient(hc connect.HTTPClient, url string, opts ...connect.ClientOption) *connect.Client[structpb.Struct, structpb.Struct] {
	return connect.NewClient[structpb.Struct, structpb.Struct](hc, url+failProcedure, opts...)
}

// callRedacted makes the call and checks what the client reads is redacted.
func callRedacted(t *testing.T) {
	t.Helper()
	_, client := testkit.NewConnectClient(t, failingService(t), failingClient)
	_, err := client.CallUnary(context.Background(), connect.NewRequest(&structpb.Struct{}))
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != connect.CodeInternal || ce.Message() != svcerr.InternalMessage {
		t.Fatalf("client read %v, want internal %q", err, svcerr.InternalMessage)
	}
	if strings.Contains(err.Error(), "SQLSTATE") {
		t.Fatalf("the driver text reached the client: %v", err)
	}
}

func TestLoggerChild_FailingV1(t *testing.T) {
	if !*loggerChild {
		t.Skip("run by TestLogger_ShowsTheServerSideOfAFailedTest")
	}
	callRedacted(t)
	t.Error("the test author's assertion failed")
}

func TestLoggerChild_PassingV1(t *testing.T) {
	if !*loggerChild {
		t.Skip("run by TestLogger_ShowsTheServerSideOfAFailedTest")
	}
	callRedacted(t)
}

var failSpecV2 = connectv2.Spec{Procedure: failProcedure, StreamType: connectv2.StreamTypeUnary}

func TestLoggerChild_FailingV2(t *testing.T) {
	if !*loggerChild {
		t.Skip("run by TestLogger_ShowsTheServerSideOfAFailedTest")
	}
	_, client := testkit.NewConnectClientV2(t, func(server *connectv2.Server) {
		server.Register(connectv2.Method{Spec: failSpecV2, Handler: func(_ context.Context, _ connectv2.Spec, stream connectv2.ServerStream) error {
			var req structpb.Struct
			if err := stream.Receive(&req); err != nil {
				return err
			}
			return errors.New(driverText)
		}})
	}, clientV2)
	err := client.CallUnary(t.Context(), failSpecV2, &structpb.Struct{}, &structpb.Struct{})
	var ce *connectv2.Error
	if !errors.As(err, &ce) || ce.Code() != connectv2.CodeInternal || ce.Message() != svcerr.InternalMessage {
		t.Fatalf("client read %v, want internal %q", err, svcerr.InternalMessage)
	}
	t.Error("the test author's assertion failed")
}

// A handler test whose server fails with an unclassified error reads only
// "internal server error" on the client. The test's output must carry the
// server's side — the cause svcerr withheld, and the handler's own records —
// when the test fails, and stay silent when it passes, even under -v.
func TestLogger_ShowsTheServerSideOfAFailedTest(t *testing.T) {
	if *loggerChild {
		t.Skip("a child run")
	}
	run := func(name string, wantExit int) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run", "^"+name+"$", "-test.v", "-test.count=1", "-testkit-logger-child")
		out, err := cmd.CombinedOutput()
		if wantExit == 0 {
			if err != nil {
				t.Fatalf("%s: unexpected child failure: %v\n%s", name, err, out)
			}
		} else {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != wantExit {
				t.Fatalf("%s: child error %v, want exit %d\n%s", name, err, wantExit, out)
			}
		}
		return string(out)
	}

	for _, name := range []string{"TestLoggerChild_FailingV1", "TestLoggerChild_FailingV2"} {
		out := run(name, 1)
		if !strings.Contains(out, "--- FAIL: "+name) || !strings.Contains(out, "the test author's assertion failed") {
			t.Fatalf("%s did not fail as planned:\n%s", name, out)
		}
		for _, want := range []string{"server log (testkit.Logger)", "rpc failed", failProcedure, "code=internal", "SQLSTATE 42P01"} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: a failing test's output does not show %q:\n%s", name, want, out)
			}
		}
		if name == "TestLoggerChild_FailingV1" && !strings.Contains(out, `msg="listing jobs" service=jobs`) {
			t.Errorf("%s: the handler's own Logger(t) record is missing:\n%s", name, out)
		}
	}

	out := run("TestLoggerChild_PassingV1", 0)
	if !strings.Contains(out, "--- PASS: TestLoggerChild_PassingV1") {
		t.Fatalf("TestLoggerChild_PassingV1 did not pass:\n%s", out)
	}
	if strings.Contains(out, "server log") || strings.Contains(out, "SQLSTATE") {
		t.Errorf("a passing test printed the server log under -v:\n%s", out)
	}
}
