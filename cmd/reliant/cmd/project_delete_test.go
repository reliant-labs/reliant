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
type fakeDeleteClient struct {
	projects  []*reliantv1.Project
	deleted   []string
	deleteErr error
}

func (f *fakeDeleteClient) ListProjects(context.Context, *connect.Request[reliantv1.ListProjectsRequest]) (*connect.Response[reliantv1.ListProjectsResponse], error) {
	return connect.NewResponse(&reliantv1.ListProjectsResponse{Projects: f.projects}), nil
}

func (f *fakeDeleteClient) DeleteProject(_ context.Context, req *connect.Request[reliantv1.DeleteProjectRequest]) (*connect.Response[reliantv1.DeleteProjectResponse], error) {
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	f.deleted = append(f.deleted, req.Msg.GetProjectId())
	return connect.NewResponse(&reliantv1.DeleteProjectResponse{Success: true}), nil
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
