package mcp_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	mcp "github.com/reliant-labs/reliant/internal/handlers/mcp"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// What a store failure tells the caller: a classified error keeps its code,
// and an unclassified one is the handler's Internal with its text withheld.
func TestMCPStoreErrorCodes(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		projectErr error
		configErr  error
		code       connect.Code
		msg        string
	}{
		"project not found": {projectErr: core.ErrProjectNotFound, code: connect.CodeNotFound, msg: "project not found"},
		"project deadline":  {projectErr: context.DeadlineExceeded, code: connect.CodeDeadlineExceeded},
		"project canceled":  {projectErr: context.Canceled, code: connect.CodeCanceled},
		"project opaque":    {projectErr: errors.New("dsn=postgres://u:secret@h"), code: connect.CodeInternal, msg: "failed to get project"},
		"config deadline":   {configErr: context.DeadlineExceeded, code: connect.CodeDeadlineExceeded},
		"config opaque":     {configErr: errors.New("dsn=postgres://u:secret@h"), code: connect.CodeInternal, msg: "failed to read MCP configuration"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			repo := ownedProjectRepo(t, `{}`)
			if tc.projectErr != nil {
				repo.GetProjectWithUserCheckFunc = func(context.Context, string, string) (*db.Project, error) { return nil, tc.projectErr }
			}
			if tc.configErr != nil {
				repo.GetProjectMCPConfigsJSONFunc = func(context.Context, string) (*string, error) { return nil, tc.configErr }
			}
			svc := newService(t, repo, nil)
			_, err := svc.ListServers(mcp.AuthedContext(t), connect.NewRequest(&reliantv1.ListServersRequest{ProjectId: "p-1"}))
			require.Error(t, err)
			assert.Equal(t, tc.code, svcerr.Code(err))
			msg := svcerr.ToConnect(err).Message()
			assert.NotContains(t, msg, "secret")
			if tc.msg != "" {
				assert.Equal(t, tc.msg, msg)
			}
		})
	}
}

// A config write the daemon refuses keeps the daemon's own words (they come
// from the user's machine, about it); a machine that is not connected keeps
// the router's marker and code, which the web's machine-wait keys on.
func TestMCPConfigWriteFailuresKeepTheDaemonsWords(t *testing.T) {
	t.Parallel()

	stored := `{"project":"{\"mcpServers\":{\"toggle-server\":{\"command\":\"go\",\"args\":[\"version\"],\"enabled\":true}}}"}`
	cases := map[string]struct {
		write func() ([]byte, error)
		code  connect.Code
		msg   string
	}{
		"daemon refused": {
			write: func() ([]byte, error) {
				return []byte(`{"success":false,"error":"open /work/.reliant/mcp.json: permission denied"}`), nil
			},
			code: connect.CodeInternal,
			msg:  "failed to save MCP configuration: open /work/.reliant/mcp.json: permission denied",
		},
		"no daemon connected": {
			write: func() ([]byte, error) { return nil, toolexec.ErrDaemonPending },
			code:  connect.CodeUnavailable,
			msg:   "daemon config write failed: no daemon connected: daemon record exists but has not registered yet (still starting)",
		},
		"transport": {
			write: func() ([]byte, error) { return nil, errors.New("nats: timeout on _INBOX.secret") },
			code:  connect.CodeUnavailable,
			msg:   "daemon config write failed",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			router := daemonFunc(func(commandType string, _ []byte) ([]byte, error) {
				if commandType == "mcp.write_config" {
					return tc.write()
				}
				return []byte(`{"servers":[]}`), nil
			})
			svc := newService(t, ownedProjectRepo(t, stored), router)
			_, err := svc.SetServerEnabled(mcp.AuthedContext(t), connect.NewRequest(&reliantv1.SetServerEnabledRequest{
				ProjectId: "p-1", Name: "toggle-server", Enabled: false,
			}))
			require.Error(t, err)
			assert.Equal(t, tc.code, svcerr.Code(err), "%v", err)
			msg := svcerr.ToConnect(err).Message()
			assert.NotContains(t, msg, "secret", "transport text stays server-side")
			if tc.msg != "" {
				assert.Equal(t, tc.msg, msg)
			}
		})
	}
}

// Over a real Connect client, through the svcerr interceptor: a project the
// caller does not own is NotFound, an opaque store failure Internal.
func TestMCPErrorsOnTheWire(t *testing.T) {
	t.Parallel()

	repo := ownedProjectRepo(t, `{}`)
	_, client := mcp.NewTestMCPServer(t, mcp.WithMCPDeps(mcp.Deps{Repo: repo}))
	_, err := client.ListServers(context.Background(), connect.NewRequest(&reliantv1.ListServersRequest{ProjectId: "p-2"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))
	assert.Equal(t, "project not found", svcerr.ToConnect(err).Message())

	repo.GetProjectMCPConfigsJSONFunc = func(context.Context, string) (*string, error) {
		return nil, errors.New("dsn=postgres://u:secret@h")
	}
	_, err = client.ListServers(context.Background(), connect.NewRequest(&reliantv1.ListServersRequest{ProjectId: "p-1"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, svcerr.Code(err))
	assert.Equal(t, "failed to read MCP configuration", svcerr.ToConnect(err).Message())
}
