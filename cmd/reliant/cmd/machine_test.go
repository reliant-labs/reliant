// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/oauth2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	daemonv1 "github.com/reliant-labs/reliant/gen/controlplane/services/daemon/v1"
	"github.com/reliant-labs/reliant/gen/controlplane/services/daemon/v1/controlplanev1connect"
	"github.com/reliant-labs/reliant/internal/cliauth"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
)

// fakeDaemons is control-plane's DaemonService as Settings -> Machines uses it.
type fakeDaemons struct {
	controlplanev1connect.UnimplementedDaemonServiceHandler
	bearer  string
	resized []string
	resumed []string
	renamed []string
}

func (f *fakeDaemons) UpdateDaemon(_ context.Context, req *connect.Request[daemonv1.UpdateDaemonRequest]) (*connect.Response[daemonv1.UpdateDaemonResponse], error) {
	if req.Msg.GetNewName() == "default" {
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New(`a machine named "default" already exists; choose another name`))
	}
	f.renamed = append(f.renamed, req.Msg.GetDaemonId()+"="+req.Msg.GetNewName())
	return connect.NewResponse(&daemonv1.UpdateDaemonResponse{}), nil
}

func (f *fakeDaemons) ListDaemons(_ context.Context, req *connect.Request[daemonv1.ListDaemonsRequest]) (*connect.Response[daemonv1.ListDaemonsResponse], error) {
	f.bearer = req.Header().Get("Authorization")
	return connect.NewResponse(&daemonv1.ListDaemonsResponse{Daemons: []*controlplanev1.Daemon{
		{Id: "d-cicd", Name: "cicd", DaemonType: controlplanev1.DaemonType_DAEMON_TYPE_MANAGED,
			Status: controlplanev1.DaemonStatus_DAEMON_STATUS_SUSPENDED, Size: controlplanev1.DaemonSize_DAEMON_SIZE_XL},
		{Id: "d-default", Name: "default", DaemonType: controlplanev1.DaemonType_DAEMON_TYPE_MANAGED,
			Status: controlplanev1.DaemonStatus_DAEMON_STATUS_ACTIVE, Size: controlplanev1.DaemonSize_DAEMON_SIZE_L},
	}}), nil
}

func (f *fakeDaemons) ResizeDaemon(_ context.Context, req *connect.Request[daemonv1.ResizeDaemonRequest]) (*connect.Response[daemonv1.ResizeDaemonResponse], error) {
	f.resized = append(f.resized, req.Msg.GetDaemonId()+"="+machineSizeName(req.Msg.GetSize()))
	return connect.NewResponse(&daemonv1.ResizeDaemonResponse{}), nil
}

func (f *fakeDaemons) ResumeDaemon(_ context.Context, req *connect.Request[daemonv1.ResumeDaemonRequest]) (*connect.Response[daemonv1.ResumeDaemonResponse], error) {
	f.resumed = append(f.resumed, req.Msg.GetDaemonId())
	return connect.NewResponse(&daemonv1.ResumeDaemonResponse{}), nil
}

// The CLI finds the control plane the way login does (the API server's RFC
// 8414 metadata) and calls DaemonService there as the login.
func TestMachineCommandsCallTheControlPlaneAsTheLogin(t *testing.T) {
	isolateControlPlaneEnv(t)
	fake := &fakeDaemons{}
	mux := http.NewServeMux()
	path, handler := controlplanev1connect.NewDaemonServiceHandler(fake)
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	// One origin plays both the API server (metadata) and the control plane.
	mux.Handle(oauth2.AuthorizationServerMetadataPath, cliauth.MetadataHandler(srv.URL))
	t.Setenv(envServerURL, srv.URL)
	t.Setenv(envToken, "rlat_login")
	t.Setenv(daemonpolicy.AgentShellEnv, "")

	run := func(args ...string) (string, error) {
		return executeRoot(t, append([]string{"machine"}, args...)...)
	}

	out, err := run("list")
	require.NoError(t, err)
	assert.Equal(t, "Bearer rlat_login", fake.bearer)
	assert.Contains(t, out, "cicd")
	assert.Contains(t, out, "suspended")
	assert.Contains(t, out, "xl")

	_, err = run("resize", "cicd", "2xl")
	require.NoError(t, err)
	_, err = run("start", "d-cicd")
	require.NoError(t, err)
	assert.Equal(t, []string{"d-cicd=2xl"}, fake.resized)
	assert.Equal(t, []string{"d-cicd"}, fake.resumed)

	_, err = run("resize", "cicd", "huge")
	assert.ErrorContains(t, err, "unknown size")
	_, err = run("start", "nope")
	assert.ErrorContains(t, err, "no machine")

	// rename sends only the new name, by id, and reports a refusal verbatim.
	out, err = run("rename", "cicd", "build-box")
	require.NoError(t, err)
	assert.Contains(t, out, "Renamed cicd to build-box (d-cicd)")
	_, err = run("rename", "cicd", "default")
	assert.ErrorContains(t, err, "already exists")
	assert.Equal(t, []string{"d-cicd=build-box"}, fake.renamed)

	// An agent's shell is refused the money-spending and state-changing verbs.
	t.Setenv(daemonpolicy.AgentShellEnv, "1")
	for _, args := range [][]string{{"start", "cicd"}, {"stop", "cicd"}, {"resize", "cicd", "2xl"}} {
		_, err = run(args...)
		assert.ErrorIs(t, err, errAgentCannotManageMachines, args)
	}
	assert.Equal(t, []string{"d-cicd"}, fake.resumed, "a refused start reached the control plane")
}
