package tools_daemon_test

import (
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	tools_daemon "github.com/reliant-labs/reliant/internal/handlers/tools_daemon"
)

// ConnectGateway is served by daemons, not by the gateway, which dials it as
// a client (RegisterOutboundConnection). The gateway answers it exactly as
// the embedded Unimplemented handler always did, not with forge's scaffold
// stub.
func TestConnectGateway_AnswersUnimplemented(t *testing.T) {
	t.Parallel()

	svc := tools_daemon.NewTestToolsDaemon(t)
	err := svc.ConnectGateway(tools_daemon.AuthedContext(t), reliantv1connect.ToolsDaemonServiceConnectGatewayServerStream{})
	require.Equal(t, connect.CodeUnimplemented, svcerr.CodeV2(err))
	require.ErrorContains(t, err, "reliant.v1.ToolsDaemonService.ConnectGateway is not implemented")
}
