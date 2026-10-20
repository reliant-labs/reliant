// Copyright (c) 2025 Reliant Labs
package chat

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Run now is a person pressing a button. An agent starts runs with start_run,
// which records agent.start_run and is bounded by the fork-bomb guard; if an
// agent could mint manual.run it would be an attended, unbounded start that
// slips past both. This pins that nothing an agent runs through can reach the
// RPC or the launch kind.
func TestRunNowIsNotReachableFromAnAgent(t *testing.T) {
	root := repoRoot(t)
	reach := regexp.MustCompile(`\bRunNow\b|TriggerEventKindManualRun|"manual\.run"`)

	agentSurface := []string{
		"internal/llm",       // the tool catalog and drivers
		"internal/agentruns", // start_run / pause / send_to_run adapter
		"internal/workflow",  // the workflow runtime and its activities
		"internal/mcp",       // tools exposed over MCP
		"internal/toolexec",  // the daemon's tool execution
	}
	for _, dir := range agentSurface {
		walkGo(t, filepath.Join(root, dir), func(path, src string) {
			// The status activity classifies manual.run as attended; that is a
			// reader of the kind, not a launcher of it.
			if strings.HasSuffix(path, "workflow_status.go") {
				return
			}
			if loc := reach.FindStringIndex(src); loc != nil {
				t.Errorf("%s reaches RunNow / manual.run (%q): an agent must not be able to start a manual run",
					strings.TrimPrefix(path, root+"/"), src[loc[0]:loc[1]])
			}
		})
	}
}

// Only RunNow launches manual.run. The launch kind is the server's decision, so
// no other handler may pass it to the launcher.
func TestOnlyRunNowLaunchesManualRun(t *testing.T) {
	root := repoRoot(t)
	launches := regexp.MustCompile(`TriggerEventKindManualRun`)
	walkGo(t, filepath.Join(root, "internal/handlers"), func(path, src string) {
		rel := strings.TrimPrefix(path, root+"/")
		if strings.HasSuffix(rel, "_test.go") || rel == "internal/handlers/chat/chat_run_now.go" {
			return
		}
		if launches.MatchString(src) {
			t.Errorf("%s names TriggerEventKindManualRun; only chat_run_now.go may launch it", rel)
		}
	})
	src, err := os.ReadFile(filepath.Join(root, "internal/handlers/chat/chat_run_now.go"))
	require.NoError(t, err)
	assert.Contains(t, string(src), "core.TriggerEventKindManualRun")
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "no go.mod above the test's directory")
		dir = parent
	}
}

func walkGo(t *testing.T, dir string, visit func(path, src string)) {
	t.Helper()
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return
	}
	require.NoError(t, filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		visit(path, string(src))
		return nil
	}))
}
