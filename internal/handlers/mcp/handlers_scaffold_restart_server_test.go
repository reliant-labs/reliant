package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/db"
	mcp "github.com/reliant-labs/reliant/internal/handlers/mcp"
)

func TestRestartServer_Generated(t *testing.T) {
	t.Parallel()

	// docs is an enabled HTTP server (its preflight needs no machine); off
	// is disabled.
	const stored = `{"project":"{\"mcpServers\":{` +
		`\"docs\":{\"type\":\"http\",\"url\":\"https://docs.example.com/mcp\",\"enabled\":true},` +
		`\"off\":{\"command\":\"npx\",\"enabled\":false}}}"}`

	unrouted := newService(t, ownedProjectRepo(t, stored), nil)
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.RestartServerRequest, pb.RestartServerResponse]{
		{
			Name:    "a project the caller does not own is not found",
			Req:     connect.NewRequest(&pb.RestartServerRequest{ProjectId: "p-2", Name: "docs"}),
			Ctx:     mcp.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "a server name is required",
			Req:     connect.NewRequest(&pb.RestartServerRequest{ProjectId: "p-1"}),
			Ctx:     mcp.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "without a daemon there is nothing to restart",
			Req:     connect.NewRequest(&pb.RestartServerRequest{ProjectId: "p-1", Name: "docs"}),
			Ctx:     mcp.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, unrouted.RestartServer)

	store := ownedProjectRepo(t, stored)
	var refetched []string
	store.EmitUserRefetchFunc = func(_ context.Context, userID string, kind db.RefetchType, opts db.RefetchOpts) error {
		refetched = append(refetched, fmt.Sprintf("%s %v %s", userID, kind, *opts.ProjectID))
		return nil
	}
	var actions []string
	routed := newService(t, store, daemonFunc(func(commandType string, payload []byte) ([]byte, error) {
		if commandType != "mcp.manage_server" {
			return nil, fmt.Errorf("unexpected daemon command %s", commandType)
		}
		var req struct {
			Action      string `json:"action"`
			ProjectPath string `json:"project_path"`
			ServerName  string `json:"server_name"`
		}
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, err
		}
		actions = append(actions, req.Action+" "+req.ServerName+" at "+req.ProjectPath)
		return []byte(`{"success":true}`), nil
	}))
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.RestartServerRequest, pb.RestartServerResponse]{
		{
			Name:    "a server the project does not configure is not found",
			Req:     connect.NewRequest(&pb.RestartServerRequest{ProjectId: "p-1", Name: "nope"}),
			Ctx:     mcp.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "a disabled server is stopped and not started again",
			Req:  connect.NewRequest(&pb.RestartServerRequest{ProjectId: "p-1", Name: "off"}),
			Ctx:  mcp.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.RestartServerResponse]) {
				if !resp.Msg.GetSuccess() || len(actions) != 1 || actions[0] != "remove off at /work" {
					t.Fatalf("success = %v, actions = %v, want only off removed", resp.Msg.GetSuccess(), actions)
				}
				actions = nil
			},
		},
		{
			Name: "an enabled server is stopped, started again, and the config health refetched",
			Req:  connect.NewRequest(&pb.RestartServerRequest{ProjectId: "p-1", Name: "docs"}),
			Ctx:  mcp.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.RestartServerResponse]) {
				if !resp.Msg.GetSuccess() || resp.Msg.GetName() != "docs" {
					t.Fatalf("response = %v, want docs restarted", resp.Msg)
				}
				if len(actions) != 2 || actions[0] != "remove docs at /work" || actions[1] != "add docs at /work" {
					t.Fatalf("actions = %v, want docs removed then added", actions)
				}
				if want := fmt.Sprintf("test-user %v p-1", db.RefetchConfigHealth); len(refetched) != 1 || refetched[0] != want {
					t.Fatalf("refetched = %v, want %q", refetched, want)
				}
			},
		},
	}, routed.RestartServer)
}
