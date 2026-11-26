// Copyright (c) 2025 Reliant Labs
package file_system

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/errreason"
	"github.com/reliant-labs/reliant/internal/machinewake"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/toolexec/toolexectest"
)

// machineDirRouter answers fs.list_machine_dir like a current daemon: it
// applies the viewer's credential guard to the path, lists it from the real
// disk, and marks the reply guarded. reply, when set, replaces the answer.
type machineDirRouter struct {
	toolexectest.Stub
	commands []string
	payloads []map[string]string
	reply    func(commandType string) ([]byte, error)
}

func (r *machineDirRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, _ string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

func (r *machineDirRouter) SendDaemonCommand(_ context.Context, _ string, commandType string, payload []byte, _ int32) ([]byte, error) {
	r.commands = append(r.commands, commandType)
	var req map[string]string
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, err
	}
	r.payloads = append(r.payloads, req)
	if r.reply != nil {
		return r.reply(commandType)
	}
	if strings.HasPrefix(req["path"], "~") {
		// Only the machine expands "~"; this fake has no home to expand it in.
		return []byte(`{"path":"/home/me","entries":[],"viewer_guarded":true}`), nil
	}
	if err := daemonpolicy.DenyViewerRead(req["path"], req["viewer_root"]); err != nil {
		return nil, toolexec.DaemonReportedFailure(commandType, err.Error())
	}
	des, err := os.ReadDir(req["path"])
	if err != nil {
		return nil, toolexec.DaemonReportedFailure(commandType, err.Error())
	}
	entries := make([]map[string]any, 0, len(des))
	for _, de := range des {
		entries = append(entries, map[string]any{"name": de.Name(), "path": filepath.Join(req["path"], de.Name()), "is_dir": de.IsDir()})
	}
	return json.Marshal(map[string]any{"path": req["path"], "home_dir": "/home/me", "entries": entries, "viewer_guarded": true})
}

func listMachine(t *testing.T, svc *Service, projectID, path string) (*reliantv1.ListMachineDirectoryResponse, error) {
	t.Helper()
	resp, err := svc.ListMachineDirectory(fsProxyContext(), connect.NewRequest(&reliantv1.ListMachineDirectoryRequest{ProjectId: projectID, Path: &path}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// The owner browses outside the project: an absolute path anywhere on the
// workspace's machine, and ".." (the folder the project is in), listed by
// the read-only fs.list_machine_dir with the workspace as the guard's root.
func TestListMachineDirectory_TheOwnerBrowsesOutsideTheProject(t *testing.T) {
	repo, projectID, base := seedFSProxyProject(t)
	scratch := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(scratch, "report.csv"), []byte("a,b\n"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(scratch, "sub"), 0o755))

	router := &machineDirRouter{}
	got, err := listMachine(t, newProxy(t, router, repo), projectID, scratch)
	require.NoError(t, err)
	assert.Equal(t, scratch, got.Path)
	assert.Equal(t, base, got.WorkspaceRoot, "the client maps absolute paths back into the project tree by it")
	assert.Equal(t, "/home/me", got.HomeDir)
	names := map[string]bool{}
	for _, e := range got.Entries {
		names[e.Name] = e.IsDirectory
		assert.Equal(t, filepath.Join(scratch, e.Name), e.Path, "entries carry absolute paths")
	}
	assert.Equal(t, map[string]bool{"report.csv": false, "sub": true}, names)
	assert.Equal(t, []string{listMachineDirCommand}, router.commands)
	assert.Equal(t, base, router.payloads[0]["viewer_root"], "the listing is a viewer read: the daemon guards it")

	_, err = listMachine(t, newProxy(t, router, repo), projectID, "..")
	require.NoError(t, err)
	assert.Equal(t, filepath.Dir(base), router.payloads[1]["path"], `".." climbs above the project root`)

	_, err = listMachine(t, newProxy(t, router, repo), projectID, "~/notes")
	require.NoError(t, err)
	assert.Equal(t, "~/notes", router.payloads[2]["path"], `"~" is left for the machine, which alone knows its home`)
}

// A credential store is never listed, by path or through a symlink in the
// project: PermissionDenied, as the viewer refuses to open one.
func TestListMachineDirectory_RefusesCredentialStores(t *testing.T) {
	repo, projectID, base := seedFSProxyProject(t)
	home, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Setenv("HOME", home)
	ssh := filepath.Join(home, ".ssh")
	require.NoError(t, os.MkdirAll(ssh, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(ssh, "id_rsa"), []byte("fixture: not a key\n"), 0o600))
	require.NoError(t, os.Symlink(ssh, filepath.Join(base, "keys")))

	for _, p := range []string{ssh, filepath.Join(base, "keys")} {
		_, err := listMachine(t, newProxy(t, &machineDirRouter{}, repo), projectID, p)
		assert.Equal(t, connect.CodePermissionDenied, svcerr.Code(err), "list %s: %v", p, err)
	}
	got, err := listMachine(t, newProxy(t, &machineDirRouter{}, repo), projectID, home)
	require.NoError(t, err, "the home directory itself lists; only the stores in it are refused")
	assert.NotEmpty(t, got.Entries)
}

// A daemon from before fs.list_machine_dir answers with the registry's
// "older version" refusal: FailedPrecondition with the reason the Files tab
// keys on to say "update Reliant on this machine", never an Internal error.
// A reply without the guard's mark is never shown either.
func TestListMachineDirectory_OlderDaemons(t *testing.T) {
	repo, projectID, _ := seedFSProxyProject(t)

	outdated := &machineDirRouter{reply: func(commandType string) ([]byte, error) {
		return nil, toolexec.DaemonReportedFailure(commandType, fmt.Sprintf(
			"this machine is running an older version of Reliant (v1.20.0) that doesn't support %q — update it to continue", commandType))
	}}
	_, err := listMachine(t, newProxy(t, outdated, repo), projectID, "/tmp")
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err), "%v", err)
	reason, _ := svcerr.ReasonOf(err)
	assert.Equal(t, errreason.MachineBrowseUnsupported, reason)

	unguarded := &machineDirRouter{reply: func(string) ([]byte, error) {
		return []byte(`{"path":"/home/me/.ssh","entries":[{"name":"id_rsa"}]}`), nil
	}}
	_, err = listMachine(t, newProxy(t, unguarded, repo), projectID, "/home/me/.ssh")
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err), "an unguarded reply is not shown: %v", err)
}

// A file is not a directory (the client opens it instead), a missing path is
// NotFound, and a directory the machine's user cannot read is a refusal by
// that disk, not a server failure.
func TestListMachineDirectory_ClassifiesTheDisksAnswers(t *testing.T) {
	repo, projectID, _ := seedFSProxyProject(t)
	for _, tc := range []struct {
		daemon string
		code   connect.Code
		reason string
	}{
		{"not a directory: /tmp/report.csv", connect.CodeFailedPrecondition, errreason.NotADirectory},
		{"open /nope: no such file or directory", connect.CodeNotFound, ""},
		{"open /root: permission denied", connect.CodePermissionDenied, ""},
	} {
		router := &machineDirRouter{reply: func(commandType string) ([]byte, error) {
			return nil, toolexec.DaemonReportedFailure(commandType, tc.daemon)
		}}
		_, err := listMachine(t, newProxy(t, router, repo), projectID, "/x")
		assert.Equal(t, tc.code, svcerr.Code(err), "%s: %v", tc.daemon, err)
		if tc.reason != "" {
			reason, _ := svcerr.ReasonOf(err)
			assert.Equal(t, tc.reason, reason, tc.daemon)
		}
	}
}

// The whole machine is browsed only by its owner. resolveScope already
// confines a request to the caller's own project and machines; this is the
// rule stated on its own, so a scope that ever resolves to another user's
// machine (an org project's checkout, say) still lists nothing there.
func TestListMachineDirectory_RequiresTheCallersOwnMachine(t *testing.T) {
	svc := newProxy(t, &machineDirRouter{}, &MockPlacement{GetDaemonFunc: func(_ context.Context, id string) (*db.Daemon, error) {
		switch id {
		case "mine":
			return &db.Daemon{ID: id, UserID: fsProxyTestUser}, nil
		case "theirs":
			return &db.Daemon{ID: id, UserID: "someone-else"}, nil
		}
		return nil, errors.New("not found")
	}})
	ctx := fsProxyContext()
	for id, want := range map[string]connect.Code{"theirs": connect.CodeNotFound, "gone": connect.CodeNotFound} {
		err := svc.requireOwnMachine(ctx, fsProxyTestUser, workspaceScope{target: machinewake.Target{DaemonID: id}})
		assert.Equal(t, want, svcerr.Code(err), id)
	}
	assert.NoError(t, svc.requireOwnMachine(ctx, fsProxyTestUser, workspaceScope{target: machinewake.Target{DaemonID: "mine"}}))
	assert.NoError(t, svc.requireOwnMachine(ctx, fsProxyTestUser, workspaceScope{}), "default resolution reaches the caller's own machine")
}

func TestMachinePath(t *testing.T) {
	for in, want := range map[string]string{
		"":              "",
		"~":             "~",
		"~/notes":       "~/notes",
		"/tmp/../etc":   "/etc",
		"..":            "/w",
		"../other/x":    "/w/other/x",
		"src":           "/w/proj/src",
		"  /tmp/a.csv ": "/tmp/a.csv",
	} {
		assert.Equal(t, want, machinePath("/w/proj", in), in)
	}
}
