package tools_daemon_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	tools_daemon "github.com/reliant-labs/reliant/internal/handlers/tools_daemon"
)

// ReportToolResult is retired: daemons report results on the ConnectDaemon
// stream. It answers Unimplemented, with a pointer to the stream.
func TestReportToolResult_Generated(t *testing.T) {
	t.Parallel()

	svc := tools_daemon.NewTestToolsDaemon(t)
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ReportToolResultRequest, pb.ReportToolResultResponse]{
		{
			Name:    "retired in favour of the bidi stream",
			Req:     connect.NewRequest(&pb.ReportToolResultRequest{}),
			Ctx:     tools_daemon.AuthedContext(t),
			WantErr: connect.CodeUnimplemented,
		},
	}, svc.ReportToolResult)
}
