package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// `skill list --json --with-bodies` carries, for every skill, exactly what
// `skill load <path>` prints — the contract a host mirroring the catalog in
// one call relies on.
func TestSkillListWithBodies_MatchesSkillLoad(t *testing.T) {
	t.Chdir(t.TempDir())
	run := func(args ...string) string {
		t.Helper()
		root := newTestRootCmd(t)
		var out strings.Builder
		root.SetOut(&out)
		root.SetArgs(args)
		if err := root.Execute(); err != nil {
			t.Fatalf("forge %v: %v", args, err)
		}
		return out.String()
	}
	var listed []struct {
		Path string `json:"path"`
		Body string `json:"body"`
	}
	if err := json.Unmarshal([]byte(run("skill", "list", "--json", "--with-bodies")), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) < 10 {
		t.Fatalf("only %d skills listed", len(listed))
	}
	for _, s := range []int{0, len(listed) / 2, len(listed) - 1} {
		if got := run("skill", "load", listed[s].Path); got != listed[s].Body {
			t.Errorf("%s: the listed body differs from `skill load`", listed[s].Path)
		}
	}
}
