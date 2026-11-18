// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// fakeDeleteClient lists fixed projects and records the delete asked for.
// With deleteAfter unset it answers as a server from before the restore
// window: DeleteProject deletes, and PurgeProjectNow is unimplemented.
type fakeDeleteClient struct {
	projects    []*reliantv1.Project
	deleted     []string
	deleteErr   error
	deleteAfter string
	removals    []*reliantv1.ProjectMachineRemoval
	fromMachine []bool
	purged      []string
	// scheduled lists the projects ListDeletedProjects answers, as a server
	// with the restore window does once they are deleted (and gone from
	// ListProjects).
	scheduled []*reliantv1.Project
}

func (f *fakeDeleteClient) ListDeletedProjects(_ context.Context, req *connect.Request[reliantv1.ListDeletedProjectsRequest]) (*connect.Response[reliantv1.ListDeletedProjectsResponse], error) {
	if f.deleteAfter == "" {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("unimplemented"))
	}
	var out []*reliantv1.Project
	for _, p := range f.scheduled {
		if p.PurgeStartedAt == nil || req.Msg.GetIncludePurging() {
			out = append(out, p)
		}
	}
	return connect.NewResponse(&reliantv1.ListDeletedProjectsResponse{Projects: out}), nil
}

func (f *fakeDeleteClient) ListProjects(context.Context, *connect.Request[reliantv1.ListProjectsRequest]) (*connect.Response[reliantv1.ListProjectsResponse], error) {
	return connect.NewResponse(&reliantv1.ListProjectsResponse{Projects: f.projects}), nil
}

func (f *fakeDeleteClient) DeleteProject(_ context.Context, req *connect.Request[reliantv1.DeleteProjectRequest]) (*connect.Response[reliantv1.DeleteProjectResponse], error) {
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	f.deleted = append(f.deleted, req.Msg.GetProjectId())
	f.fromMachine = append(f.fromMachine, req.Msg.GetRemoveFromMachines())
	resp := &reliantv1.DeleteProjectResponse{Success: true}
	if f.deleteAfter != "" {
		resp.DeleteAfter = &f.deleteAfter
		resp.MachineRemovals = f.removals
	}
	return connect.NewResponse(resp), nil
}

func (f *fakeDeleteClient) PurgeProjectNow(_ context.Context, req *connect.Request[reliantv1.PurgeProjectNowRequest]) (*connect.Response[reliantv1.PurgeProjectNowResponse], error) {
	if f.deleteAfter == "" {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("unimplemented"))
	}
	f.purged = append(f.purged, req.Msg.GetProjectId()+" as "+req.Msg.GetConfirmName())
	return connect.NewResponse(&reliantv1.PurgeProjectNowResponse{MachineRemovals: f.removals}), nil
}

func TestProjectDeleteWithARestoreWindow(t *testing.T) {
	run := func(f *fakeDeleteClient, d projectDelete, ref string) (string, error) {
		var out bytes.Buffer
		d.client, d.out, d.in, d.yes = f, &out, strings.NewReader(""), true
		err := d.run(context.Background(), ref)
		return out.String(), err
	}
	t.Run("a delete says until when it can be restored, and how", func(t *testing.T) {
		f := deleteFixture()
		f.deleteAfter = "2026-11-25T10:00:00Z"
		out, err := run(f, projectDelete{}, "web")
		require.NoError(t, err)
		assert.Equal(t, []string{"p1"}, f.deleted)
		assert.Equal(t, []bool{false}, f.fromMachine)
		assert.Contains(t, out, "It can be restored until")
		assert.Contains(t, out, "reliant project restore p1")
		assert.Contains(t, out, "Nothing on disk was deleted")
	})
	t.Run("--remove-from-machines asks for it and reports each machine", func(t *testing.T) {
		f := deleteFixture()
		f.deleteAfter = "2026-11-25T10:00:00Z"
		f.removals = []*reliantv1.ProjectMachineRemoval{
			{DaemonId: "d1", MachineName: "laptop", Path: "/home/me/web", Status: reliantv1.ProjectMachineRemovalStatus_PROJECT_MACHINE_REMOVAL_STATUS_REMOVED},
			{DaemonId: "d2", MachineName: "cloud", Path: "/w/web", Status: reliantv1.ProjectMachineRemovalStatus_PROJECT_MACHINE_REMOVAL_STATUS_OFFLINE},
		}
		out, err := run(f, projectDelete{fromMachines: true}, "web")
		require.NoError(t, err)
		assert.Equal(t, []bool{true}, f.fromMachine)
		assert.Contains(t, out, "laptop: removed /home/me/web")
		assert.Contains(t, out, "cloud: offline, keeps its copy at /w/web")
	})
	t.Run("--now purges, naming the project", func(t *testing.T) {
		f := deleteFixture()
		f.deleteAfter = "2026-11-25T10:00:00Z"
		out, err := run(f, projectDelete{now: true}, "web")
		require.NoError(t, err)
		assert.Equal(t, []string{"p1 as web"}, f.purged)
		assert.Empty(t, f.deleted)
		assert.Contains(t, out, "for good")
	})
	t.Run("--now purges a project an ordinary delete already scheduled, by id", func(t *testing.T) {
		f := deleteFixture()
		f.deleteAfter = "2026-11-25T10:00:00Z"
		_, err := run(f, projectDelete{}, "p1")
		require.NoError(t, err)
		// Deleted: it leaves ListProjects and is listed as deleted.
		f.scheduled = []*reliantv1.Project{{Id: "p1", Name: "web", Path: "/home/me/web", DeleteAfter: &f.deleteAfter}}
		f.projects = f.projects[1:]
		_, err = run(f, projectDelete{now: true}, "p1")
		require.NoError(t, err)
		assert.Equal(t, []string{"p1 as web"}, f.purged)
	})
	t.Run("--now finishes a purge that stopped part way", func(t *testing.T) {
		f := deleteFixture()
		f.deleteAfter = "2026-11-25T10:00:00Z"
		started := "2026-11-18T10:00:00Z"
		f.scheduled = []*reliantv1.Project{{Id: "p1", Name: "web", DeleteAfter: &f.deleteAfter, PurgeStartedAt: &started}}
		f.projects = f.projects[1:]
		_, err := run(f, projectDelete{now: true}, "web")
		require.NoError(t, err)
		assert.Equal(t, []string{"p1 as web"}, f.purged)
	})
	t.Run("--now on a server from before the window is its immediate delete", func(t *testing.T) {
		f := deleteFixture()
		_, err := run(f, projectDelete{now: true}, "web")
		require.NoError(t, err)
		assert.Equal(t, []string{"p1"}, f.deleted)
	})
	t.Run("--json carries the restore date", func(t *testing.T) {
		f := deleteFixture()
		f.deleteAfter = "2026-11-25T10:00:00Z"
		out, err := run(f, projectDelete{json: true}, "p1")
		require.NoError(t, err)
		var got map[string]any
		require.NoError(t, json.Unmarshal([]byte(out), &got))
		assert.Equal(t, "2026-11-25T10:00:00Z", got["delete_after"])
	})
}

// fakeRestoreClient lists fixed deleted projects and records the restore.
type fakeRestoreClient struct {
	deleted  []*reliantv1.Project
	restored []string
}

func (f *fakeRestoreClient) ListDeletedProjects(context.Context, *connect.Request[reliantv1.ListDeletedProjectsRequest]) (*connect.Response[reliantv1.ListDeletedProjectsResponse], error) {
	return connect.NewResponse(&reliantv1.ListDeletedProjectsResponse{Projects: f.deleted}), nil
}

func (f *fakeRestoreClient) RestoreProject(_ context.Context, req *connect.Request[reliantv1.RestoreProjectRequest]) (*connect.Response[reliantv1.RestoreProjectResponse], error) {
	f.restored = append(f.restored, req.Msg.GetProjectId())
	return connect.NewResponse(&reliantv1.RestoreProjectResponse{TriggersResumed: 2}), nil
}

func TestProjectRestore(t *testing.T) {
	after := "2026-11-25T10:00:00Z"
	fixture := func() *fakeRestoreClient {
		return &fakeRestoreClient{deleted: []*reliantv1.Project{{Id: "p1", Name: "web", Path: "/home/me/web", DeleteAfter: &after}}}
	}
	run := func(f *fakeRestoreClient, ref string) (string, error) {
		var out bytes.Buffer
		err := projectRestore{client: f, out: &out}.run(context.Background(), ref)
		return out.String(), err
	}
	t.Run("a deleted project is restored by name", func(t *testing.T) {
		f := fixture()
		out, err := run(f, "web")
		require.NoError(t, err)
		assert.Equal(t, []string{"p1"}, f.restored)
		assert.Contains(t, out, "Restored web (p1). 2 automation(s)")
	})
	t.Run("no argument lists what can be restored", func(t *testing.T) {
		f := fixture()
		out, err := run(f, "")
		require.NoError(t, err)
		assert.Empty(t, f.restored)
		assert.Contains(t, out, "p1  web")
	})
	t.Run("a project that is not deleted is refused", func(t *testing.T) {
		f := fixture()
		_, err := run(f, "api")
		require.ErrorContains(t, err, "no deleted project of yours")
		assert.Empty(t, f.restored)
	})
}

func TestProjectRestoreIsRegistered(t *testing.T) {
	found, _, err := newProjectCmd().Find([]string{"restore"})
	require.NoError(t, err)
	assert.Equal(t, "restore", found.Name())
}

func deleteFixture() *fakeDeleteClient {
	return &fakeDeleteClient{projects: []*reliantv1.Project{
		{Id: "p1", Name: "web", Path: "/home/me/web"},
		{Id: "p2", Name: "api", Path: "/home/me/api"},
		{Id: "p3", Name: "api", Path: "/home/me/api-2"},
	}}
}

func runDelete(f *fakeDeleteClient, stdin string, yes, asJSON bool, ref string) (string, error) {
	var out bytes.Buffer
	d := projectDelete{client: f, out: &out, in: strings.NewReader(stdin), yes: yes, json: asJSON}
	err := d.run(context.Background(), ref)
	return out.String(), err
}

func TestProjectDelete(t *testing.T) {
	t.Run("typing the name deletes the project", func(t *testing.T) {
		f := deleteFixture()
		out, err := runDelete(f, "web\n", false, false, "web")
		require.NoError(t, err)
		assert.Equal(t, []string{"p1"}, f.deleted)
		assert.Contains(t, out, "Nothing on disk is deleted")
		assert.Contains(t, out, "Deleted web (p1)")
	})

	t.Run("a wrong name deletes nothing", func(t *testing.T) {
		f := deleteFixture()
		_, err := runDelete(f, "wbe\n", false, false, "p1")
		require.ErrorContains(t, err, "nothing deleted")
		assert.Empty(t, f.deleted)
	})

	t.Run("no answer deletes nothing", func(t *testing.T) {
		f := deleteFixture()
		_, err := runDelete(f, "", false, false, "p1")
		require.ErrorContains(t, err, "pass --yes")
		assert.Empty(t, f.deleted)
	})

	t.Run("--yes deletes by id without asking", func(t *testing.T) {
		f := deleteFixture()
		out, err := runDelete(f, "", true, false, "p2")
		require.NoError(t, err)
		assert.Equal(t, []string{"p2"}, f.deleted)
		assert.NotContains(t, out, "Type ")
	})

	t.Run("an ambiguous name is refused", func(t *testing.T) {
		f := deleteFixture()
		_, err := runDelete(f, "", true, false, "api")
		require.ErrorContains(t, err, "2 projects are named")
		assert.Empty(t, f.deleted)
	})

	t.Run("an unknown project is refused", func(t *testing.T) {
		f := deleteFixture()
		_, err := runDelete(f, "", true, false, "nope")
		require.ErrorContains(t, err, "no project of yours")
		assert.Empty(t, f.deleted)
	})

	t.Run("a relative path names the project at its absolute path", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(dir, "web"), 0o755))
		t.Chdir(dir)
		abs, err := filepath.Abs("web")
		require.NoError(t, err)
		f := &fakeDeleteClient{projects: []*reliantv1.Project{{Id: "p9", Name: "site", Path: abs}}}
		_, err = runDelete(f, "", true, false, "web")
		require.NoError(t, err)
		assert.Equal(t, []string{"p9"}, f.deleted)
	})

	t.Run("a delete that stops part way says to run it again", func(t *testing.T) {
		f := deleteFixture()
		f.deleteErr = connect.NewError(connect.CodeDeadlineExceeded, errors.New("deadline exceeded"))
		_, err := runDelete(f, "", true, false, "p1")
		require.ErrorContains(t, err, "run the command again to resume")
	})

	t.Run("--json prints the deleted project", func(t *testing.T) {
		f := deleteFixture()
		out, err := runDelete(f, "", true, true, "p1")
		require.NoError(t, err)
		var got map[string]any
		require.NoError(t, json.Unmarshal([]byte(out), &got))
		assert.Equal(t, map[string]any{"id": "p1", "name": "web", "path": "/home/me/web", "deleted": true}, got)
	})
}

func TestProjectDeleteIsRegistered(t *testing.T) {
	cmd := newProjectCmd()
	found, _, err := cmd.Find([]string{"delete"})
	require.NoError(t, err)
	assert.Equal(t, "delete", found.Name())
	assert.False(t, found.Hidden)
}
