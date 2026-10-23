package ciguard

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

var workflowsDir = filepath.Join("..", "..", ".github", "workflows")

// needsList reads `needs:`, which a workflow may spell as one job or a list.
type needsList []string

func (n *needsList) UnmarshalYAML(v *yaml.Node) error {
	if v.Kind == yaml.ScalarNode {
		*n = []string{v.Value}
		return nil
	}
	var list []string
	if err := v.Decode(&list); err != nil {
		return err
	}
	*n = list
	return nil
}

type skipJob struct {
	If    string    `yaml:"if"`
	Needs needsList `yaml:"needs"`
}

// TestNoJobInheritsAnUpstreamSkip proves no job silently skips because a job
// two or more hops upstream was skipped on purpose.
//
// A job with no `if:` runs on `success()`, and success() is false when ANY
// job in its needs chain was skipped — transitively, not only its direct
// needs. So when job A is conditional (it has an `if:`), B needs A and gets
// past the skip with always(), and C needs only B, C still skips whenever A
// does, though B succeeded. e2e-suite.yml shipped exactly that: `changes`
// applies only to pull requests, `prepare` needed it under always(), and the
// shards (no `if:`) skipped on every push to main and every nightly call.
// The suite ran nowhere but on pull requests while the workflow's only
// complaint was its ledger's "found 0 shard results".
//
// The rule: a job that is downstream (at any distance) of a conditional job
// it does not directly need must say when it runs. Spelling `if:` out —
// usually `!cancelled() && needs.<parent>.result == 'success'` — is what
// makes the skip propagation a decision rather than an accident.
func TestNoJobInheritsAnUpstreamSkip(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(workflowsDir, "*.yml"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no workflows under %s (err %v) — this guard would inspect nothing", workflowsDir, err)
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var wf struct {
			Jobs map[string]skipJob `yaml:"jobs"`
		}
		if err := yaml.Unmarshal(raw, &wf); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		names := make([]string, 0, len(wf.Jobs))
		for name := range wf.Jobs {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			j := wf.Jobs[name]
			if strings.TrimSpace(j.If) != "" {
				continue
			}
			if far := conditionalBeyondDirectNeeds(wf.Jobs, name); len(far) > 0 {
				t.Errorf("%s: job %q has no `if:`, so it skips whenever %v is skipped, even when every job it needs succeeded. "+
					"Say when it runs, e.g. `if: ${{ !cancelled() && needs.%s.result == 'success' }}`",
					filepath.Base(path), name, far, j.Needs[0])
			}
		}
	}
}

// conditionalBeyondDirectNeeds returns the conditional jobs reachable from
// name's needs chain that name does not need directly.
func conditionalBeyondDirectNeeds(jobs map[string]skipJob, name string) []string {
	direct := map[string]bool{}
	for _, n := range jobs[name].Needs {
		direct[n] = true
	}
	seen := map[string]bool{}
	var far []string
	var walk func(string)
	walk = func(job string) {
		for _, up := range jobs[job].Needs {
			if seen[up] {
				continue
			}
			seen[up] = true
			if !direct[up] && strings.TrimSpace(jobs[up].If) != "" {
				far = append(far, up)
			}
			walk(up)
		}
	}
	walk(name)
	sort.Strings(far)
	return far
}
