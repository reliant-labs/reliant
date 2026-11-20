// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"fmt"
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

// fakeLifecycle is DaemonService's create, delete, disk and port-access rpcs.
type fakeLifecycle struct {
	fakeDaemons
	calls []string
}

func (f *fakeLifecycle) CreateDaemon(_ context.Context, req *connect.Request[daemonv1.CreateDaemonRequest]) (*connect.Response[daemonv1.CreateDaemonResponse], error) {
	m := req.Msg
	f.calls = append(f.calls, fmt.Sprintf("create %s %s %s %s", m.GetName(), machineTypeName(m.GetDaemonType()), machineSizeName(m.GetSize()), m.GetIdleTimeout()))
	return connect.NewResponse(&daemonv1.CreateDaemonResponse{Daemon: &controlplanev1.Daemon{Id: "d-new", Name: m.GetName(), Size: m.GetSize()}}), nil
}

func (f *fakeLifecycle) DeleteDaemon(_ context.Context, req *connect.Request[daemonv1.DeleteDaemonRequest]) (*connect.Response[daemonv1.DeleteDaemonResponse], error) {
	f.calls = append(f.calls, fmt.Sprintf("delete %s snapshot=%t", req.Msg.GetDaemonId(), req.Msg.GetSnapshotBeforeDelete()))
	return connect.NewResponse(&daemonv1.DeleteDaemonResponse{}), nil
}

func (f *fakeLifecycle) GrowDaemonDisk(_ context.Context, req *connect.Request[daemonv1.GrowDaemonDiskRequest]) (*connect.Response[daemonv1.GrowDaemonDiskResponse], error) {
	f.calls = append(f.calls, fmt.Sprintf("grow %s %d", req.Msg.GetDaemonId(), req.Msg.GetStorageGib()))
	return connect.NewResponse(&daemonv1.GrowDaemonDiskResponse{}), nil
}

func (f *fakeLifecycle) SetPortAccess(_ context.Context, req *connect.Request[daemonv1.SetPortAccessRequest]) (*connect.Response[daemonv1.SetPortAccessResponse], error) {
	f.calls = append(f.calls, fmt.Sprintf("set %s %d %s", req.Msg.GetDaemonId(), req.Msg.GetPort(), portAccessModeName(req.Msg.GetAccessMode())))
	return connect.NewResponse(&daemonv1.SetPortAccessResponse{}), nil
}

func (f *fakeLifecycle) RemovePortAccess(_ context.Context, req *connect.Request[daemonv1.RemovePortAccessRequest]) (*connect.Response[daemonv1.RemovePortAccessResponse], error) {
	f.calls = append(f.calls, fmt.Sprintf("remove %s %d", req.Msg.GetDaemonId(), req.Msg.GetPort()))
	return connect.NewResponse(&daemonv1.RemovePortAccessResponse{}), nil
}

func (f *fakeLifecycle) ListPortAccessRules(_ context.Context, req *connect.Request[daemonv1.ListPortAccessRulesRequest]) (*connect.Response[daemonv1.ListPortAccessRulesResponse], error) {
	return connect.NewResponse(&daemonv1.ListPortAccessRulesResponse{Rules: []*controlplanev1.PortAccessRule{
		{DaemonId: req.Msg.GetDaemonId(), Port: 5173, AccessMode: controlplanev1.PortAccessMode_PORT_ACCESS_MODE_PUBLIC, Url: "https://5173-d-cicd.example.test"},
	}}), nil
}

func (f *fakeLifecycle) SetDefaultPortAccess(_ context.Context, req *connect.Request[daemonv1.SetDefaultPortAccessRequest]) (*connect.Response[daemonv1.SetDefaultPortAccessResponse], error) {
	f.calls = append(f.calls, fmt.Sprintf("default %s %s", req.Msg.GetDaemonId(), portAccessModeName(req.Msg.GetDefaultAccessMode())))
	return connect.NewResponse(&daemonv1.SetDefaultPortAccessResponse{}), nil
}

// A cloud machine's whole lifecycle and its preview access are scriptable,
// and an agent's shell is refused every write (north-star AW-25, AW-24).
func TestMachineLifecycleAndPreviewAccess(t *testing.T) {
	isolateControlPlaneEnv(t)
	fake := &fakeLifecycle{}
	mux := http.NewServeMux()
	path, handler := controlplanev1connect.NewDaemonServiceHandler(fake)
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.Handle(oauth2.AuthorizationServerMetadataPath, cliauth.MetadataHandler(srv.URL))
	t.Setenv(envServerURL, srv.URL)
	t.Setenv(envToken, "rlat_login")
	t.Setenv(daemonpolicy.AgentShellEnv, "")

	out, err := executeRoot(t, "machine", "create", "build-box", "--size", "m", "--idle-timeout", "30m")
	require.NoError(t, err)
	assert.Contains(t, out, "Created build-box (d-new), size m")

	_, err = executeRoot(t, "machine", "delete", "cicd")
	assert.ErrorContains(t, err, "--yes", "delete without --yes deletes nothing")
	_, err = executeRoot(t, "machine", "delete", "cicd", "--yes", "--snapshot")
	require.NoError(t, err)

	_, err = executeRoot(t, "machine", "grow-disk", "cicd", "205")
	assert.ErrorContains(t, err, "multiple of 10")
	// 21474836580 truncates to 100 in 32 bits: refused, never sent as 100.
	_, err = executeRoot(t, "machine", "grow-disk", "cicd", "21474836580")
	assert.ErrorContains(t, err, "invalid size")
	_, err = executeRoot(t, "machine", "grow-disk", "cicd", "200")
	require.NoError(t, err)

	out, err = executeRoot(t, "preview", "access", "list", "cicd")
	require.NoError(t, err)
	assert.Contains(t, out, "ports with no rule are authenticated")
	assert.Contains(t, out, "5173")
	assert.Contains(t, out, "public")

	_, err = executeRoot(t, "preview", "access", "set", "cicd", "5173", "token")
	assert.ErrorContains(t, err, "want one of public, authenticated", "token mode is the app's: the CLI could not show the token")
	_, err = executeRoot(t, "preview", "access", "set", "cicd", "5173", "public")
	require.NoError(t, err)
	_, err = executeRoot(t, "preview", "access", "remove", "cicd", "5173")
	require.NoError(t, err)
	_, err = executeRoot(t, "preview", "access", "default", "cicd", "token")
	assert.ErrorContains(t, err, "want one of authenticated, public", "a default has no token to carry")
	_, err = executeRoot(t, "preview", "access", "default", "cicd", "authenticated")
	require.NoError(t, err)

	assert.Equal(t, []string{
		"create build-box managed m 30m",
		"delete d-cicd snapshot=true",
		"grow d-cicd 200",
		"set d-cicd 5173 public",
		"remove d-cicd 5173",
		"default d-cicd authenticated",
	}, fake.calls)

	t.Setenv(daemonpolicy.AgentShellEnv, "1")
	for _, args := range [][]string{
		{"machine", "create", "x"}, {"machine", "delete", "cicd", "--yes"}, {"machine", "grow-disk", "cicd", "200"},
	} {
		_, err = executeRoot(t, args...)
		assert.ErrorIs(t, err, errAgentCannotManageMachines, args)
	}
	for _, args := range [][]string{
		{"preview", "access", "set", "cicd", "5173", "public"}, {"preview", "access", "remove", "cicd", "5173"},
		{"preview", "access", "default", "cicd", "public"},
	} {
		_, err = executeRoot(t, args...)
		assert.ErrorIs(t, err, errAgentCannotChangePortAccess, args)
	}
	_, err = executeRoot(t, "preview", "access", "list", "cicd")
	assert.NoError(t, err, "an agent may read who can open a preview")
	assert.Len(t, fake.calls, 6, "a refused write reached the control plane")
}
