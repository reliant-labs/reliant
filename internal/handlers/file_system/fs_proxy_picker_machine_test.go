package file_system

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec/toolexectest"
)

// pickerRouter records which machine each command reached: "" for default
// resolution, else the machine it was pinned to.
type pickerRouter struct {
	toolexectest.Stub
	reached []string
	offline map[string]bool
}

func (r *pickerRouter) SendDaemonCommand(_ context.Context, _, commandType string, _ []byte, _ int32) ([]byte, error) {
	r.reached = append(r.reached, "")
	return pickerAnswer(commandType)
}

func (r *pickerRouter) SendDaemonCommandToDaemon(_ context.Context, _, daemonID, commandType string, _ []byte, _ int32) ([]byte, error) {
	r.reached = append(r.reached, daemonID)
	if r.offline[daemonID] {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("no daemon connected"))
	}
	return pickerAnswer(commandType)
}

func pickerAnswer(commandType string) ([]byte, error) {
	if commandType == "fs.list_dir" {
		return json.Marshal(map[string]any{"path": "/home/me", "entries": []map[string]any{{"name": "src", "is_dir": true}}})
	}
	return json.Marshal(map[string]any{})
}

// pickerMachines owns "mine" and "asleep"; "theirs" is another user's.
func pickerMachines() *MockRepository {
	return &MockRepository{GetDaemonFunc: func(_ context.Context, id string) (*db.Daemon, error) {
		switch id {
		case "mine", "asleep":
			return &db.Daemon{ID: id, UserID: fsProxyTestUser}, nil
		case "theirs":
			return &db.Daemon{ID: id, UserID: "someone-else"}, nil
		}
		return nil, errors.New("not found")
	}}
}

func TestListDirectory_BrowsesTheNamedMachineElseTheDefault(t *testing.T) {
	router := &pickerRouter{}
	svc := newProxy(t, router, pickerMachines())

	resp, err := svc.ListDirectory(fsProxyContext(), connect.NewRequest(&reliantv1.ListDirectoryRequest{}))
	require.NoError(t, err)
	assert.Equal(t, "/home/me/src", resp.Msg.Entries[0].Path)
	assert.Nil(t, resp.Msg.DaemonId, "the default machine is echoed as no machine")

	machine := "mine"
	resp, err = svc.ListDirectory(fsProxyContext(), connect.NewRequest(&reliantv1.ListDirectoryRequest{DaemonId: &machine}))
	require.NoError(t, err)
	assert.Equal(t, "mine", resp.Msg.GetDaemonId(), "the listed machine is echoed, so a client can tell an older server ignored it")

	assert.Equal(t, []string{"", "mine"}, router.reached, "no daemon_id is the default machine, as before; a named one is pinned")
}

func TestListDirectory_RefusesAnotherUsersMachineAndReportsAnOfflineOne(t *testing.T) {
	router := &pickerRouter{offline: map[string]bool{"asleep": true}}
	svc := newProxy(t, router, pickerMachines())

	theirs := "theirs"
	_, err := svc.ListDirectory(fsProxyContext(), connect.NewRequest(&reliantv1.ListDirectoryRequest{DaemonId: &theirs}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, svcerr.ToConnect(err).Code())
	assert.Empty(t, router.reached, "another user's machine is never sent a command")

	asleep := "asleep"
	_, err = svc.ListDirectory(fsProxyContext(), connect.NewRequest(&reliantv1.ListDirectoryRequest{DaemonId: &asleep}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, svcerr.ToConnect(err).Code())
	assert.Contains(t, err.Error(), "no daemon connected")
}

func TestCreateDirectory_MakesTheFolderOnTheNamedMachine(t *testing.T) {
	router := &pickerRouter{}
	svc := newProxy(t, router, pickerMachines())

	machine := "mine"
	created, err := svc.CreateDirectory(fsProxyContext(), connect.NewRequest(&reliantv1.CreateDirectoryRequest{
		Path: "/home/me/new", DaemonId: &machine,
	}))
	require.NoError(t, err)
	assert.Equal(t, "mine", created.Msg.GetDaemonId())
	assert.Equal(t, []string{"mine"}, router.reached)

	theirs := "theirs"
	_, err = svc.CreateDirectory(fsProxyContext(), connect.NewRequest(&reliantv1.CreateDirectoryRequest{
		Path: "/home/me/new", DaemonId: &theirs,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, svcerr.ToConnect(err).Code())
}
