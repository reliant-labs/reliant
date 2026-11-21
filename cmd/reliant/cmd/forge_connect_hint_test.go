// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/credentials"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/cliauth"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/toolexec/bootstrap"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonstate"
)

const hintDefaultServer = "https://app.example.com"

func hintDeps(env map[string]string, loggedIn bool, state sandboxState) (connectHintDeps, *string) {
	var askedAccount string
	return connectHintDeps{
		getenv: func(k string) string { return env[k] },
		lookup: func(string) (credentials.Credential, string, error) {
			if !loggedIn {
				return credentials.Credential{}, "", cliauth.ErrNotLoggedIn
			}
			return credentials.Credential{Token: "t", AccountID: "user-1"}, "", nil
		},
		state: func(_, account string) sandboxState {
			askedAccount = account
			return state
		},
		defaultServer: hintDefaultServer,
	}, &askedAccount
}

func TestForgeConnectHint(t *testing.T) {
	deploy := []string{"env", "deploy", "prod"}
	for name, tc := range map[string]struct {
		args     []string
		env      map[string]string
		loggedIn bool
		state    sandboxState
		want     string
	}{
		"paired, not connected":   {deploy, nil, true, sandboxPaired, connectHintCommand},
		"never paired":            {deploy, nil, true, sandboxNone, connectHintCommand},
		"connected":               {deploy, nil, true, sandboxConnected, ""},
		"agent shell":             {deploy, map[string]string{daemonpolicy.AgentShellEnv: "1"}, true, sandboxNone, ""},
		"no login":                {deploy, nil, false, sandboxNone, ""},
		"already set":             {deploy, map[string]string{forgeConnectHintEnv: "mine"}, true, sandboxNone, ""},
		"not env deploy":          {[]string{"env", "up", "dev"}, nil, true, sandboxNone, ""},
		"generate":                {[]string{"generate"}, nil, true, sandboxNone, ""},
		"flags around env deploy": {[]string{"-v", "env", "--json", "deploy", "prod"}, nil, true, sandboxNone, connectHintCommand},
		"non-default server": {deploy, map[string]string{envServerURL: "http://localhost:8090"}, true, sandboxPaired,
			connectHintCommand + " --server http://localhost:8090"},
	} {
		t.Run(name, func(t *testing.T) {
			d, account := hintDeps(tc.env, tc.loggedIn, tc.state)
			assert.Equal(t, tc.want, forgeConnectHint(tc.args, d))
			if tc.want != "" {
				assert.Equal(t, "user-1", *account, "the state is asked for the login's account")
			}
		})
	}
}

func TestExportForgeConnectHint_PreservesExistingValue(t *testing.T) {
	t.Setenv(forgeConnectHintEnv, "keep me")
	exportForgeConnectHint([]string{"env", "deploy", "prod"})
	assert.Equal(t, "keep me", os.Getenv(forgeConnectHintEnv))
}

// A hint is for the `env deploy` it was computed for: any other forge command
// runs without one, whether this process exported it for an earlier command
// or inherited it.
func TestExportForgeConnectHint_ClearsItForOtherCommands(t *testing.T) {
	prev := exportedConnectHint
	t.Cleanup(func() { exportedConnectHint = prev })

	t.Setenv(forgeConnectHintEnv, "inherited")
	exportForgeConnectHint([]string{"generate"})
	_, set := os.LookupEnv(forgeConnectHintEnv)
	assert.False(t, set, "an inherited hint is removed for a command that is not env deploy")

	// One this process exported for an earlier deploy is its own, not the
	// caller's: it is neither kept for another command nor taken as the
	// caller's choice for the next deploy.
	exportedConnectHint = "stale"
	t.Setenv(forgeConnectHintEnv, "stale")
	exportForgeConnectHint([]string{"env", "up", "dev"})
	_, set = os.LookupEnv(forgeConnectHintEnv)
	assert.False(t, set, "this process's own hint does not outlive its deploy")
}

type fakeProjectCreator struct {
	calls []*reliantv1.CreateProjectRequest
	err   error
}

func (f *fakeProjectCreator) CreateProject(_ context.Context, r *connect.Request[reliantv1.CreateProjectRequest]) (*connect.Response[reliantv1.CreateProjectResponse], error) {
	f.calls = append(f.calls, r.Msg)
	if f.err != nil {
		return nil, f.err
	}
	return connect.NewResponse(&reliantv1.CreateProjectResponse{Project: &reliantv1.Project{Id: "p1", Name: "app"}, Attached: true}), nil
}

// connectedDataDir is a data dir whose runtime record is this (live) process
// with an established stream, and an assigned daemon id.
func connectedDataDir(t *testing.T) string {
	dir := t.TempDir()
	require.NoError(t, daemonstate.Init(dir, daemonstate.Startup{}))
	require.NoError(t, daemonstate.SetStream(dir, daemonstate.StreamConnected, ""))
	require.NoError(t, bootstrap.WriteDaemonID(dir, "daemon-7"))
	return dir
}

func TestAttachCheckoutWhenConnected_CreatesOnce(t *testing.T) {
	dataDir := connectedDataDir(t)
	checkout := t.TempDir()
	fake := &fakeProjectCreator{}
	var out bytes.Buffer
	attachCheckoutWhenConnected(context.Background(), &out, dataDir, checkout,
		func() (projectCreator, error) { return fake, nil }, time.Millisecond)

	require.Len(t, fake.calls, 1)
	assert.Equal(t, checkout, fake.calls[0].GetPath())
	assert.Equal(t, filepath.Base(checkout), fake.calls[0].GetName())
	assert.Equal(t, "daemon-7", fake.calls[0].GetDaemonId())
	assert.Contains(t, out.String(), `Linked `+checkout+` to project "app" (p1)`)
}

func TestAttachCheckoutWhenConnected_FailureIsAWarning(t *testing.T) {
	dataDir := connectedDataDir(t)
	fake := &fakeProjectCreator{err: connect.NewError(connect.CodeUnavailable, errors.New("down"))}
	var out bytes.Buffer
	attachCheckoutWhenConnected(context.Background(), &out, dataDir, t.TempDir(),
		func() (projectCreator, error) { return fake, nil }, time.Millisecond)
	assert.Contains(t, out.String(), "Warning: could not link")
	assert.Contains(t, out.String(), "the daemon keeps running")
}

func TestAttachCheckoutWhenConnected_WaitsForTheStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	fake := &fakeProjectCreator{}
	attachCheckoutWhenConnected(ctx, &bytes.Buffer{}, t.TempDir(), t.TempDir(),
		func() (projectCreator, error) { return fake, nil }, time.Millisecond)
	assert.Empty(t, fake.calls, "nothing is linked before the daemon connects")
}
