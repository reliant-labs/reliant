// Copyright (c) 2025 Reliant Labs
package commands

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// fakeProjectClient is a minimal projectResolverClient for exercising
// resolveProjectID without a live server.
type fakeProjectClient struct {
	// listPages is returned by successive ListProjects calls; the last page is
	// reused once exhausted. Lets a test model "empty, then populated after a
	// concurrent create".
	listPages [][]*reliantv1.Project
	listCalls int

	createErr    error
	createdID    string
	createCalls  int
	lastCreateOf string // path passed to the last CreateProject call
}

func (f *fakeProjectClient) ListProjects(_ context.Context, _ *connect.Request[reliantv1.ListProjectsRequest]) (*connect.Response[reliantv1.ListProjectsResponse], error) {
	idx := f.listCalls
	if idx >= len(f.listPages) {
		idx = len(f.listPages) - 1
	}
	f.listCalls++
	var projects []*reliantv1.Project
	if idx >= 0 {
		projects = f.listPages[idx]
	}
	return connect.NewResponse(&reliantv1.ListProjectsResponse{Projects: projects}), nil
}

func (f *fakeProjectClient) CreateProject(_ context.Context, req *connect.Request[reliantv1.CreateProjectRequest]) (*connect.Response[reliantv1.CreateProjectResponse], error) {
	f.createCalls++
	f.lastCreateOf = req.Msg.GetPath()
	if f.createErr != nil {
		return nil, f.createErr
	}
	return connect.NewResponse(&reliantv1.CreateProjectResponse{
		Project: &reliantv1.Project{Id: f.createdID, Path: req.Msg.GetPath(), Name: req.Msg.GetName()},
	}), nil
}

func proj(id, path string) *reliantv1.Project {
	return &reliantv1.Project{Id: id, Path: path}
}

func TestResolveProjectID(t *testing.T) {
	const path = "/abs/work/myproj"

	// The ordinary repeat run. Resolution goes to the server FIRST even though a
	// matching row would be visible in a local listing: only the server can
	// check that the row's directory is still on the daemon. AlreadyExists is
	// its "found" answer.
	t.Run("returns existing project id when path matches", func(t *testing.T) {
		client := &fakeProjectClient{
			listPages: [][]*reliantv1.Project{{
				proj("other-id", "/abs/work/somethingelse"),
				proj("match-id", path),
			}},
			createErr: connect.NewError(connect.CodeAlreadyExists, errors.New("a project already exists at this path")),
		}
		id, err := resolveProjectID(context.Background(), client, path)
		if err != nil {
			t.Fatalf("resolveProjectID: %v", err)
		}
		if id != "match-id" {
			t.Errorf("id = %q, want match-id", id)
		}
		if client.createCalls != 1 {
			t.Errorf("CreateProject called %d times, want 1 — the server must get a chance to verify the directory", client.createCalls)
		}
	})

	t.Run("creates project when none exists at path", func(t *testing.T) {
		client := &fakeProjectClient{
			listPages: [][]*reliantv1.Project{{proj("other-id", "/abs/other")}},
			createdID: "new-id",
		}
		id, err := resolveProjectID(context.Background(), client, path)
		if err != nil {
			t.Fatalf("resolveProjectID: %v", err)
		}
		if id != "new-id" {
			t.Errorf("id = %q, want new-id", id)
		}
		if client.createCalls != 1 {
			t.Errorf("CreateProject called %d times, want 1", client.createCalls)
		}
		if client.lastCreateOf != path {
			t.Errorf("created path = %q, want %q", client.lastCreateOf, path)
		}
	})

	// AlreadyExists that the caller cannot then see in its own listing — the row
	// belongs to another user, or the read lags the write. There is no id to
	// bind a run to, so this must be an error; returning an empty id would send
	// an unusable project into 'workflow run'.
	t.Run("errors when AlreadyExists cannot be resolved to an id", func(t *testing.T) {
		client := &fakeProjectClient{
			listPages: [][]*reliantv1.Project{{}},
			createErr: connect.NewError(connect.CodeAlreadyExists, errors.New("a project already exists at this path")),
		}
		id, err := resolveProjectID(context.Background(), client, path)
		if err == nil {
			t.Fatalf("resolveProjectID returned id %q and no error, want an error", id)
		}
		if id != "" {
			t.Errorf("id = %q, want empty", id)
		}
		if !strings.Contains(err.Error(), path) {
			t.Errorf("error = %q, want it to name the path %q", err, path)
		}
	})

	// The failure this guards: a projects row can outlive its directory, and
	// resolution used to return that row's id without ever asking whether the
	// directory was still there. The run then bound to a phantom path,
	// executed nothing, and reported success. The server refuses with
	// FailedPrecondition; resolution must surface that refusal rather than
	// hand back an id, and the message must name the path.
	t.Run("surfaces the server's refusal for a stale project row", func(t *testing.T) {
		client := &fakeProjectClient{
			// The row is present — a path-only lookup would happily match it.
			listPages: [][]*reliantv1.Project{{proj("stale-id", path)}},
			createErr: connect.NewError(connect.CodeFailedPrecondition, errors.New(
				"project stale-id is registered at "+path+" but that directory does not exist; "+
					"restore the directory or delete the project, then retry")),
		}
		id, err := resolveProjectID(context.Background(), client, path)
		if err == nil {
			t.Fatalf("resolveProjectID returned id %q and no error, want a refusal naming the missing directory", id)
		}
		if id != "" {
			t.Errorf("id = %q, want empty on refusal", id)
		}
		if !strings.Contains(err.Error(), path) {
			t.Errorf("error = %q, want it to name the path %q", err, path)
		}
	})

	t.Run("propagates non-AlreadyExists create errors", func(t *testing.T) {
		client := &fakeProjectClient{
			listPages: [][]*reliantv1.Project{{}},
			createErr: connect.NewError(connect.CodePermissionDenied, errors.New("nope")),
		}
		if _, err := resolveProjectID(context.Background(), client, path); err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}
