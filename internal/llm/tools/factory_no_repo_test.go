// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// heldRepos walks v and reports every *db.Repo it holds in an interface field:
// how many there are, and how many of them are a nil pointer. A nil *db.Repo in
// an interface is a non-nil interface, so a tool holding one passes its own
// "requires a database connection" check and dereferences nil instead.
func heldRepos(v reflect.Value, seen map[uintptr]bool) (held, typedNil int) {
	repoType := reflect.TypeOf((*db.Repo)(nil))
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return 0, 0
		}
		inner := v.Elem()
		if inner.Type() == repoType {
			if inner.IsNil() {
				return 1, 1
			}
			return 1, 0
		}
		return heldRepos(inner, seen)
	case reflect.Pointer:
		if v.IsNil() || v.Type() == repoType {
			return 0, 0
		}
		if seen[v.Pointer()] {
			return 0, 0
		}
		seen[v.Pointer()] = true
		return heldRepos(v.Elem(), seen)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			h, n := heldRepos(v.Field(i), seen)
			held, typedNil = held+h, typedNil+n
		}
	}
	return held, typedNil
}

// toolsHoldingRepos builds every registered tool from f and counts the
// *db.Repo values they hold.
func toolsHoldingRepos(t *testing.T, f *ToolsFactory) (tools, typedNil int) {
	t.Helper()
	for _, name := range f.ListAvailableTools() {
		tool := f.GetToolByName(name, nil)
		if tool == nil {
			continue
		}
		held, nilHeld := heldRepos(reflect.ValueOf(&tool).Elem(), map[uintptr]bool{})
		if held > 0 {
			tools++
		}
		if nilHeld > 0 {
			t.Errorf("tool %s holds a nil *db.Repo as a non-nil role", name)
			typedNil++
		}
	}
	return tools, typedNil
}

// A factory with no database — the daemon runtime's, docgen's, a catalog
// listing's — hands every tool a nil role, whether Repo was left unset or set
// to a nil *db.Repo, so each database-backed tool reports that it is
// unavailable rather than dereferencing nil.
func TestToolsFactoryWithoutRepoHandsToolsANilRole(t *testing.T) {
	// The walk must reach the field a tool keeps its role in, or the
	// assertions below are vacuous: a non-nil repo is found in many tools.
	withRepo, _ := toolsHoldingRepos(t, NewToolsFactory(&ToolsOptions{Repo: new(db.Repo)}))
	require.Greater(t, withRepo, 20, "the walk finds the role each database-backed tool holds")

	for name, opts := range map[string]*ToolsOptions{
		"unset":       {},
		"nil pointer": {Repo: (*db.Repo)(nil)},
	} {
		t.Run(name, func(t *testing.T) {
			f := NewToolsFactory(opts)
			held, typedNil := toolsHoldingRepos(t, f)
			require.Zero(t, typedNil)
			require.Zero(t, held)

			// And the behaviour that nil role buys.
			rc := rctx.NewToolContext(context.Background(), "chat-1", "thread-1", nil, nil)
			resp, err := f.GetToolByName(SpawnStopToolName, nil).Run(rc, ToolCall{ID: "call-1", Name: SpawnStopToolName, Input: `{"agent_id":"a1"}`})
			require.NoError(t, err)
			require.True(t, resp.IsError)
			require.Contains(t, resp.Content, "requires a database connection")
		})
	}
}
