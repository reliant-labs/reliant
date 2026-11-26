package cli

import (
	"strings"
	"testing"
)

// Ranger friction #7: `env exec` — run a one-off command with the config a
// host process got, DATABASE_URL included — was the answer to "how do I run
// my CLI against this stack", and nobody found it. The summary box names it
// whenever a workload it serves (OnHost, OnCompose) is in the box, and not
// for a box with only frontends or a cluster, where it would refuse.
func TestUpSummaryPointsAtEnvExec(t *testing.T) {
	for name, tc := range map[string]struct {
		kind string
		want bool
	}{
		"host":     {"host", true},
		"compose":  {"compose", true},
		"frontend": {"frontend", false},
	} {
		t.Run(name, func(t *testing.T) {
			var b strings.Builder
			renderUpSummary(&b, "dev", []upServiceRow{{Name: "x", Kind: tc.kind, Log: ".forge/logs/dev/x.log"}}, "starting", false, nil, nil)
			got := strings.Contains(b.String(), Name()+" env exec dev -- <cmd>")
			if got != tc.want {
				t.Errorf("env exec hint shown = %v, want %v:\n%s", got, tc.want, b.String())
			}
		})
	}
}
