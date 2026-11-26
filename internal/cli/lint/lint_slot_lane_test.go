package lint

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/hostslot"
)

// A lint narrowed to named paths waits in the express lane; a whole-project
// lint stays bulk. A scoped lint used to queue behind every hour-long bulk
// job, which on a busy machine turned a 15-second run into minutes (Ranger
// build, friction #10).
func TestLintSlotLane(t *testing.T) {
	cases := []struct {
		name  string
		scope []string
		paths []string
		want  string
	}{
		{"whole project", nil, []string{"./..."}, ""},
		{"--scope", []string{"internal/handlers/jobs"}, []string{"./..."}, hostslot.LaneExpress},
		{"package paths", nil, []string{"./internal/x/..."}, hostslot.LaneExpress},
		{"explicit ./... is the whole project", nil, []string{"./...", "."}, ""},
	}
	for _, tc := range cases {
		if got := lintSlotLane(tc.scope, tc.paths); got != tc.want {
			t.Errorf("%s: lane = %q, want %q", tc.name, got, tc.want)
		}
	}
}
