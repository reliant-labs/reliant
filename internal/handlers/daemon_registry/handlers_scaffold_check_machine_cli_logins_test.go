package daemon_registry_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/db"
	daemon_registry "github.com/reliant-labs/reliant/internal/handlers/daemon_registry"
	"github.com/reliant-labs/reliant/internal/vendorcli"
)

// The checks before the machine is asked.
func TestCheckMachineCLILogins_Generated(t *testing.T) {
	t.Parallel()

	svc := daemon_registry.NewTestDaemonRegistry(t, daemon_registry.WithDaemonRegistryDeps(registryWithDaemons()))
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.CheckMachineCLILoginsRequest, pb.CheckMachineCLILoginsResponse]{
		{
			Name:    "unauthenticated caller is rejected",
			Req:     connect.NewRequest(&pb.CheckMachineCLILoginsRequest{DaemonId: "offline"}),
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "an unknown vendor is refused before anything runs",
			Req:     connect.NewRequest(&pb.CheckMachineCLILoginsRequest{DaemonId: "offline", Vendors: []string{"claude; rm -rf ~"}}),
			Ctx:     daemon_registry.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "daemon_id is required",
			Req:     connect.NewRequest(&pb.CheckMachineCLILoginsRequest{}),
			Ctx:     daemon_registry.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "another user's machine is not found",
			Req:     connect.NewRequest(&pb.CheckMachineCLILoginsRequest{DaemonId: "theirs"}),
			Ctx:     daemon_registry.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "an offline machine is unavailable",
			Req:     connect.NewRequest(&pb.CheckMachineCLILoginsRequest{DaemonId: "offline"}),
			Ctx:     daemon_registry.AuthedContext(t),
			WantErr: connect.CodeUnavailable,
		},
	}, svc.CheckMachineCLILogins)
}

// An online machine of the caller's is sent the vendor table's own probe
// through exec.run, and only the verdicts come back.
func TestCheckMachineCLILogins_ReturnsTheMachinesVerdicts(t *testing.T) {
	t.Parallel()

	repo := knownDaemonsRepo(map[string]*db.Daemon{"box": {ID: "box", UserID: "test-user"}})
	repo.ListFreshDaemonAttachmentsForUserFunc = func(context.Context, string, time.Duration) ([]*db.DaemonAttachment, error) {
		return []*db.DaemonAttachment{{DaemonID: "box"}}, nil
	}
	var sentCommand, sentScript, sentTo string
	router := &daemon_registry.MockRouter{
		SendDaemonCommandToDaemonFunc: func(_ context.Context, _, daemonID, commandType string, payload []byte, _ int32) ([]byte, error) {
			sentTo, sentCommand = daemonID, commandType
			var req daemon.RunCommandRequest
			require.NoError(t, json.Unmarshal(payload, &req))
			sentScript = req.Command
			return json.Marshal(daemon.CommandResult{Stdout: "RELIANT_CLI\tclaude\tsigned_in\nRELIANT_CLI\tcodex\tmissing\nnoise\n"})
		},
	}
	svc := daemon_registry.NewTestDaemonRegistry(t, daemon_registry.WithDaemonRegistryDeps(daemon_registry.Deps{Repo: repo, Router: router}))

	resp, err := svc.CheckMachineCLILogins(daemon_registry.AuthedContext(t), connect.NewRequest(&pb.CheckMachineCLILoginsRequest{
		DaemonId: "box", Vendors: []string{"gemini", "codex", "claude"},
	}))
	require.NoError(t, err)

	require.Equal(t, "box", sentTo)
	require.Equal(t, "exec.run", sentCommand)
	require.Equal(t, vendorcli.StatusScript([]string{"claude", "codex", "gemini"}), sentScript,
		"the probe is the vendor table's, in its order, for the vendors asked")
	got := map[string]pb.CLILoginState{}
	var order []string
	for _, l := range resp.Msg.GetLogins() {
		got[l.GetVendor()] = l.GetState()
		order = append(order, l.GetVendor())
		require.NotEmpty(t, l.GetName())
	}
	require.Equal(t, "claude,codex,gemini", strings.Join(order, ","))
	require.Equal(t, pb.CLILoginState_CLI_LOGIN_STATE_SIGNED_IN, got["claude"])
	require.Equal(t, pb.CLILoginState_CLI_LOGIN_STATE_NOT_INSTALLED, got["codex"])
	require.Equal(t, pb.CLILoginState_CLI_LOGIN_STATE_UNKNOWN, got["gemini"], "no verdict is unknown, never signed in")
}

func TestCLILoginStateProto(t *testing.T) {
	for state, want := range map[vendorcli.State]pb.CLILoginState{
		vendorcli.StateSignedIn:  pb.CLILoginState_CLI_LOGIN_STATE_SIGNED_IN,
		vendorcli.StateSignedOut: pb.CLILoginState_CLI_LOGIN_STATE_SIGNED_OUT,
		vendorcli.StateExpired:   pb.CLILoginState_CLI_LOGIN_STATE_EXPIRED,
		vendorcli.StateMissing:   pb.CLILoginState_CLI_LOGIN_STATE_NOT_INSTALLED,
		vendorcli.StateUnknown:   pb.CLILoginState_CLI_LOGIN_STATE_UNKNOWN,
		"":                       pb.CLILoginState_CLI_LOGIN_STATE_UNKNOWN,
	} {
		require.Equal(t, want, daemon_registry.CLILoginStateProto(state), string(state))
	}
}
