// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/oauth2"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	daemonv1 "github.com/reliant-labs/reliant/gen/controlplane/services/daemon/v1"
	"github.com/reliant-labs/reliant/gen/controlplane/services/daemon/v1/controlplanev1connect"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/cliauth"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/handlers/daemon_registry"
	"github.com/reliant-labs/reliant/pkg/config"
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

// fakeRegistry is the Reliant API's DaemonRegistryService: machine labels
// and pools.
type fakeRegistry struct {
	reliantv1connect.UnimplementedDaemonRegistryServiceHandler
	bearer   string
	labels   map[string]map[string]string
	pools    map[string]*reliantv1.MachinePoolSpec
	setCalls int
	puts     []*reliantv1.MachinePoolSpec
	deleted  []string
}

func (f *fakeRegistry) ListDaemons(_ context.Context, req *connect.Request[reliantv1.ListDaemonsRequest]) (*connect.Response[reliantv1.ListDaemonsResponse], error) {
	f.bearer = req.Header().Get("Authorization")
	return connect.NewResponse(&reliantv1.ListDaemonsResponse{Daemons: []*reliantv1.DaemonInfo{
		{DaemonId: "d-box", Name: "build-box", Hostname: "box.lan", Labels: f.labels["d-box"]},
		{DaemonId: "d-laptop", Hostname: "laptop", Labels: f.labels["d-laptop"]},
	}}), nil
}

func (f *fakeRegistry) SetMachineLabels(_ context.Context, req *connect.Request[reliantv1.SetMachineLabelsRequest]) (*connect.Response[reliantv1.SetMachineLabelsResponse], error) {
	f.setCalls++
	f.labels[req.Msg.GetDaemonId()] = req.Msg.GetLabels()
	return connect.NewResponse(&reliantv1.SetMachineLabelsResponse{Daemon: &reliantv1.DaemonInfo{
		DaemonId: req.Msg.GetDaemonId(), Labels: req.Msg.GetLabels(),
	}}), nil
}

func (f *fakeRegistry) ListMachinePools(context.Context, *connect.Request[reliantv1.ListMachinePoolsRequest]) (*connect.Response[reliantv1.ListMachinePoolsResponse], error) {
	var out []*reliantv1.MachinePool
	for _, name := range []string{"gate", "pipeline"} {
		spec, ok := f.pools[name]
		if !ok {
			continue
		}
		p := &reliantv1.MachinePool{Id: "mp-" + name, Spec: proto.Clone(spec).(*reliantv1.MachinePoolSpec)}
		if name == "pipeline" {
			p.Members = []*reliantv1.MachinePoolMember{{DaemonId: "d-box", Name: "build-box", State: "connected", LiveRuns: 1, Size: "2xl"}}
			p.Warnings = []string{"1 member, min_members is 2"}
		}
		out = append(out, p)
	}
	return connect.NewResponse(&reliantv1.ListMachinePoolsResponse{Pools: out}), nil
}

func (f *fakeRegistry) PutMachinePool(_ context.Context, req *connect.Request[reliantv1.PutMachinePoolRequest]) (*connect.Response[reliantv1.PutMachinePoolResponse], error) {
	spec := req.Msg.GetPool()
	f.puts = append(f.puts, proto.Clone(spec).(*reliantv1.MachinePoolSpec))
	f.pools[spec.GetName()] = spec
	return connect.NewResponse(&reliantv1.PutMachinePoolResponse{Pool: &reliantv1.MachinePool{Id: "mp-" + spec.GetName(), Spec: spec}}), nil
}

func (f *fakeRegistry) DeleteMachinePool(_ context.Context, req *connect.Request[reliantv1.DeleteMachinePoolRequest]) (*connect.Response[reliantv1.DeleteMachinePoolResponse], error) {
	if req.Msg.GetName() == "pipeline" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("a trigger still runs on machine pool pipeline"))
	}
	f.deleted = append(f.deleted, req.Msg.GetName())
	return connect.NewResponse(&reliantv1.DeleteMachinePoolResponse{}), nil
}

// personGatedRegistry is fakeRegistry for reads, and the real
// DaemonRegistryService for the three writes, so the refusal the CLI shows is
// the one the server sends.
type personGatedRegistry struct {
	*fakeRegistry
	real *daemon_registry.Service
}

func (r personGatedRegistry) SetMachineLabels(ctx context.Context, req *connect.Request[reliantv1.SetMachineLabelsRequest]) (*connect.Response[reliantv1.SetMachineLabelsResponse], error) {
	return r.real.SetMachineLabels(ctx, req)
}

func (r personGatedRegistry) PutMachinePool(ctx context.Context, req *connect.Request[reliantv1.PutMachinePoolRequest]) (*connect.Response[reliantv1.PutMachinePoolResponse], error) {
	return r.real.PutMachinePool(ctx, req)
}

func (r personGatedRegistry) DeleteMachinePool(ctx context.Context, req *connect.Request[reliantv1.DeleteMachinePoolRequest]) (*connect.Response[reliantv1.DeleteMachinePoolResponse], error) {
	return r.real.DeleteMachinePool(ctx, req)
}

// With a sign-in, the server takes label and pool writes only from a person,
// and the CLI login is an access token every agent shell inherits: label and
// pool put|rm are refused with PermissionDenied (reason person_required), and
// the message says how the change can be made instead. Nothing is written.
func TestMachineLabelAndPoolWritesWithTheCLILoginNeedAPerson(t *testing.T) {
	isolateControlPlaneEnv(t)
	fake := &fakeRegistry{
		labels: map[string]map[string]string{"d-box": {"team": "ci"}},
		pools:  map[string]*reliantv1.MachinePoolSpec{"pipeline": {Name: "pipeline", Selector: map[string]string{"pool": "pipeline"}, MemberIdleTimeout: "10m"}},
	}
	var writes int
	registry, err := daemon_registry.New(daemon_registry.Deps{
		Logger: slog.New(slog.DiscardHandler), Config: &config.Config{},
		Repo: &daemon_registry.MockRepository{
			SetDaemonLabelsFunc: func(context.Context, string, string, map[string]string) (bool, error) {
				writes++
				return true, nil
			},
			PutMachinePoolFunc: func(_ context.Context, p *core.MachinePool) (*core.MachinePool, error) {
				writes++
				return p, nil
			},
			DeleteMachinePoolByNameFunc: func(context.Context, string, string) error {
				writes++
				return nil
			},
		},
	})
	require.NoError(t, err)
	mux := http.NewServeMux()
	mux.Handle(reliantv1connect.NewDaemonRegistryServiceHandler(personGatedRegistry{fakeRegistry: fake, real: registry},
		connect.WithInterceptors(svcerr.Interceptor())))
	// What the API edge installs for an `rlat_` CLI login.
	edge := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), auth.UserIDContextKey, "u")
		mux.ServeHTTP(w, r.WithContext(auth.WithCredential(ctx, auth.CredentialAccessToken)))
	})
	srv := httptest.NewServer(edge)
	defer srv.Close()
	t.Setenv(envServerURL, srv.URL)
	t.Setenv(envToken, "rlat_login")
	t.Setenv(daemonpolicy.AgentShellEnv, "")

	for _, args := range [][]string{
		{"label", "build-box", "pool=pipeline"},
		{"pool", "put", "pipeline", "--max-runs-per-member", "3"},
		{"pool", "rm", "pipeline"},
	} {
		_, err := executeRoot(t, append([]string{"machine"}, args...)...)
		require.Error(t, err, args)
		assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err), args)
		var ce *connect.Error
		require.True(t, errors.As(err, &ce), args)
		assert.Equal(t, auth.ReasonPersonRequired, ce.Meta().Get(svcerr.ReasonHeader), args)
		assert.ErrorContains(t, err, "Only a person can", args)
		assert.ErrorContains(t, err, "approve its card", args)
		assert.ErrorContains(t, err, "reliant daemon start --label", args)
	}
	assert.Zero(t, writes, "a refused write reached the store")
	assert.Equal(t, map[string]string{"team": "ci"}, fake.labels["d-box"])
}

// label and pool talk to the Reliant API (not the control plane) as the
// login: label merges by default, put overlays only the flags passed onto
// the stored pool, and an agent's shell is refused the writes but may list.
func TestMachineLabelAndPoolCommands(t *testing.T) {
	isolateControlPlaneEnv(t)
	fake := &fakeRegistry{
		labels: map[string]map[string]string{"d-box": {"team": "ci", "gate": "yes"}},
		pools: map[string]*reliantv1.MachinePoolSpec{"pipeline": {
			Name: "pipeline", Selector: map[string]string{"pool": "pipeline"}, MaxRunsPerMember: 2,
			MinMembers: 2, MaxMembers: 4, MemberSize: "2xl", MemberIdleTimeout: "10m",
		}},
	}
	mux := http.NewServeMux()
	mux.Handle(reliantv1connect.NewDaemonRegistryServiceHandler(fake))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv(envServerURL, srv.URL)
	t.Setenv(envToken, "rlat_login")
	t.Setenv(daemonpolicy.AgentShellEnv, "")

	run := func(args ...string) (string, error) {
		return executeRoot(t, append([]string{"machine"}, args...)...)
	}

	// Merge by name, the login's bearer.
	out, err := run("label", "build-box", "pool=pipeline")
	require.NoError(t, err)
	assert.Equal(t, "Bearer rlat_login", fake.bearer)
	assert.Equal(t, map[string]string{"team": "ci", "gate": "yes", "pool": "pipeline"}, fake.labels["d-box"])
	assert.Contains(t, out, "build-box (d-box) labels: gate=yes,pool=pipeline,team=ci")

	// --remove, by id; a machine with no name is found by hostname.
	_, err = run("label", "d-box", "--remove", "gate")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"team": "ci", "pool": "pipeline"}, fake.labels["d-box"])
	_, err = run("label", "laptop", "pool=gate,gate=yes")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"pool": "gate", "gate": "yes"}, fake.labels["d-laptop"])

	// --replace makes the named labels the only ones.
	_, err = run("label", "build-box", "--replace", "pool=gate")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"pool": "gate"}, fake.labels["d-box"])

	// Refused before any call: a bad label, nothing to do, an unknown machine.
	calls := fake.setCalls
	_, err = run("label", "build-box", "Pool=x")
	assert.ErrorContains(t, err, "lowercase slugs")
	_, err = run("label", "build-box", "pool")
	assert.ErrorContains(t, err, "not key=value")
	_, err = run("label", "build-box")
	assert.ErrorContains(t, err, "name labels to set")
	_, err = run("label", "nope", "pool=x")
	assert.ErrorContains(t, err, "no machine")
	assert.Equal(t, calls, fake.setCalls)

	// put on an existing pool changes only what was passed.
	out, err = run("pool", "put", "pipeline", "--max-runs-per-member", "3")
	require.NoError(t, err)
	require.Len(t, fake.puts, 1)
	assert.True(t, proto.Equal(&reliantv1.MachinePoolSpec{
		Name: "pipeline", Selector: map[string]string{"pool": "pipeline"}, MaxRunsPerMember: 3,
		MinMembers: 2, MaxMembers: 4, MemberSize: "2xl", MemberIdleTimeout: "10m",
	}, fake.puts[0]), "got %v", fake.puts[0])
	assert.Contains(t, out, "pipeline")

	// put on a new pool sends what was passed and nothing else.
	_, err = run("pool", "put", "gate", "--selector", "pool=pipeline", "--selector", "gate=yes", "--member-idle-timeout", "15m")
	require.NoError(t, err)
	require.Len(t, fake.puts, 2)
	assert.True(t, proto.Equal(&reliantv1.MachinePoolSpec{
		Name: "gate", Selector: map[string]string{"pool": "pipeline", "gate": "yes"}, MemberIdleTimeout: "15m",
	}, fake.puts[1]), "got %v", fake.puts[1])
	_, err = run("pool", "put", "gate", "--selector", "gate")
	assert.ErrorContains(t, err, "not key=value")
	_, err = run("pool", "put", "Gate")
	assert.ErrorContains(t, err, "lowercase slug")
	assert.Len(t, fake.puts, 2)

	// ls: each pool, its selector and per-member limit, members and warnings.
	out, err = run("pool", "ls")
	require.NoError(t, err)
	assert.Contains(t, out, "gate=yes,pool=pipeline")
	assert.Regexp(t, `pipeline\s+pool=pipeline\s+3\s+1\s+2\s+4\s+2xl\s+10m`, out)
	assert.Regexp(t, `build-box\s+d-box\s+connected\s+1\s+2xl`, out)
	assert.Contains(t, out, "warning: 1 member, min_members is 2")
	assert.Contains(t, out, "no members: label a machine gate=yes,pool=pipeline")

	// rm reports a refusal verbatim.
	out, err = run("pool", "rm", "gate")
	require.NoError(t, err)
	assert.Contains(t, out, "Deleted machine pool gate.")
	_, err = run("pool", "rm", "pipeline")
	assert.ErrorContains(t, err, "a trigger still runs")
	assert.Equal(t, []string{"gate"}, fake.deleted)

	// An agent's shell may list, never write.
	t.Setenv(daemonpolicy.AgentShellEnv, "1")
	for _, args := range [][]string{{"label", "build-box", "pool=x"}, {"pool", "put", "pipeline"}, {"pool", "rm", "gate"}} {
		_, err = run(args...)
		assert.ErrorIs(t, err, errAgentCannotChangeLabels, args)
	}
	_, err = run("pool", "ls")
	require.NoError(t, err)
	assert.Len(t, fake.puts, 2)
	assert.Equal(t, []string{"gate"}, fake.deleted)
}
