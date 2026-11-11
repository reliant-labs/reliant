// Copyright (c) 2025 Reliant Labs
package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A finally handler runs on a failure too, so a triggered workflow with one
// has a failure path.
func TestNoFailurePath_FinallyCounts(t *testing.T) {
	good, err := os.ReadFile(filepath.Join("testdata", RuleNoFailurePath, "good.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	src := strings.Replace(string(good), "on_failure:", "finally:", 1)
	src = strings.Replace(src, "failed at {{ failure.node_id }}", "ended {{ run.status }}", 1)
	path := filepath.Join(t.TempDir(), "finally.yaml")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := findingsOf(Check(loadFixture(t, path)), RuleNoFailurePath); len(got) > 0 {
		t.Fatalf("a workflow with finally still reported: %v", got)
	}
}
