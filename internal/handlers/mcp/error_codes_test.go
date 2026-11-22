package mcp_test

import (
	"context"
	"errors"
	"testing"

	"github.com/reliant-labs/reliant/internal/projectaccess/projectaccesstest"

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
				projectaccesstest.Bind(repo, func(context.Context, string, string) (*db.Project, error) { return nil, tc.projectErr })
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
	_, client := mcp.NewTestMCPServer(t, mcp.WithMCPDeps(mcp.Deps{Repo: repo, Projects: projectaccesstest.For(repo)}))
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

// prod a629f936: listing the tools of a user's MCP server that closed its
// connection answered Internal and was an issue. The daemon's report of the
// user's own server failing is FailedPrecondition, in the daemon's words; a
// failure of ours stays Internal.
func TestGetServerToolsOfABrokenUserServerIsFailedPrecondition(t *testing.T) {
	t.Parallel()

	stored := `{"project":"{\"mcpServers\":{\"tickets\":{\"command\":\"go\",\"args\":[\"version\"],\"enabled\":true}}}"}`
	cases := map[string]struct {
		list func() ([]byte, error)
		code connect.Code
		msg  string
	}{
		"the user's server failed": {
			list: func() ([]byte, error) {
				return []byte(`{"error":"failed to connect to MCP server: connection closed: calling \"initialize\": client is closing: EOF"}`), nil
			},
			code: connect.CodeFailedPrecondition,
			msg:  `failed to list tools: failed to connect to MCP server: connection closed: calling "initialize": client is closing: EOF`,
		},
		"an unreadable answer": {
			list: func() ([]byte, error) { return []byte(`not json`), nil },
			code: connect.CodeInternal,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			router := daemonFunc(func(commandType string, _ []byte) ([]byte, error) {
				switch commandType {
				case "mcp.list_tools":
					return tc.list()
				case "mcp.server_status":
					return []byte(`{"servers":[{"name":"tickets","connected":false}]}`), nil
				}
				return []byte(`{}`), nil
			})
			svc := newService(t, ownedProjectRepo(t, stored), router)
			_, err := svc.GetServerTools(mcp.AuthedContext(t), connect.NewRequest(&reliantv1.GetServerToolsRequest{
				ProjectId: "p-1", Name: "tickets",
			}))
			require.Error(t, err)
			assert.Equal(t, tc.code, svcerr.Code(err), "%v", err)
			if tc.msg != "" {
				assert.Equal(t, tc.msg, svcerr.ToConnect(err).Message())
			}
		})
	}
}
