package ciguard

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

// mergeGroupOnCICD is the one `runs-on` that may reach the cicd self-hosted
// runners (ci.yml's SELF-HOSTED RUNNERS header): a merge group runs there and
// every other event runs on a GitHub-hosted runner. This repository is public,
// so a pull request, a fork's above all, must never reach a self-hosted runner.
const mergeGroupOnCICD = `${{ github.event_name == 'merge_group' && fromJSON('["self-hosted","cicd"]') || 'ubuntu-latest' }}`

// selfHostedMergeTier is the set of ci.yml jobs routed by mergeGroupOnCICD.
var selfHostedMergeTier = []string{"checks", "gate", "golangci", "test-short"}

// routingEvents is every event a job of this repository could see, plus one no
// workflow names, so an expression that keys on a short list of events is
// caught too.
var routingEvents = []string{
	"merge_group", "pull_request", "pull_request_target", "push", "workflow_dispatch",
	"workflow_run", "schedule", "issue_comment", "release", "repository_dispatch", "an_unknown_event",
}

// runnerRoute says where a job's runs-on sends it for one event: self-hosted or
// not. It understands a literal runner (a string or a list) and the one
// expression mergeGroupOnCICD; any other expression is refused (ok false), so
// routing nobody has reviewed cannot pass as hosted.
func runnerRoute(runsOn any, event string) (selfHosted, ok bool) {
	switch v := runsOn.(type) {
	case string:
		s := strings.TrimSpace(v)
		if s == mergeGroupOnCICD {
			return event == "merge_group", true
		}
		if strings.Contains(s, "${{") {
			return false, false
		}
		return s == "self-hosted" || s == "cicd", true
	case []any:
		for _, l := range v {
			s, isString := l.(string)
			if !isString || strings.Contains(s, "${{") {
				return false, false
			}
			if s == "self-hosted" || s == "cicd" {
				return true, true
			}
		}
		return false, true
	case nil:
		return false, true // a reusable-workflow call: its own jobs are checked where they are declared
	default:
		return false, false
	}
}

// TestSelfHostedRunnersServeOnlyTheMergeQueue walks every workflow and holds
// each job's `runs-on` to the routing ci.yml's header describes: only the merge
// tier's four jobs route to the cicd self-hosted runners, only through
// mergeGroupOnCICD, and so only for a merge group. Every other event, a pull
// request (with `ci:run` or not), a push, a dispatch or an event nobody named,
// stays on a GitHub-hosted runner, and windows-changed stays on Windows.
func TestSelfHostedRunnersServeOnlyTheMergeQueue(t *testing.T) {
	t.Parallel()
	paths, err := filepath.Glob(filepath.Join(workflowsDir, "*.y*ml"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no workflows under %s: %v", workflowsDir, err)
	}
	var routed []string
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var wf struct {
			Jobs map[string]struct {
				RunsOn any `yaml:"runs-on"`
			} `yaml:"jobs"`
		}
		if err := yaml.Unmarshal(raw, &wf); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		for name, j := range wf.Jobs {
			where := filepath.Base(p) + " job " + name
			for _, ev := range routingEvents {
				self, ok := runnerRoute(j.RunsOn, ev)
				if !ok {
					if strings.Contains(strings.ToLower(asText(j.RunsOn)), "self-hosted") || strings.Contains(asText(j.RunsOn), "cicd") {
						t.Errorf("%s: runs-on %q routes to the self-hosted runners in a way this guard cannot prove; use exactly %s", where, asText(j.RunsOn), mergeGroupOnCICD)
					}
					break // an expression with no self-hosted label in it cannot reach those runners
				}
				if self && ev != "merge_group" {
					t.Errorf("%s: a %s event runs on a self-hosted runner (runs-on %q); only merge groups may", where, ev, asText(j.RunsOn))
				}
				if self && ev == "merge_group" && filepath.Base(p) == "ci.yml" {
					routed = append(routed, name)
				}
				if self && filepath.Base(p) != "ci.yml" {
					t.Errorf("%s: runs on a self-hosted runner; only ci.yml's merge tier does", where)
				}
			}
		}
	}
	sort.Strings(routed)
	if !slices.Equal(routed, selfHostedMergeTier) {
		t.Errorf("ci.yml jobs on the cicd runners for a merge group: %v, want %v (ci.yml's SELF-HOSTED RUNNERS header; update both together)", routed, selfHostedMergeTier)
	}
}

// TestRunnerRouteRefusesUnprovenSelfHostedRouting is the guard's own negative
// case: each of these would send a pull request, or every event, to the
// self-hosted runners, and runnerRoute must either say so or refuse it.
func TestRunnerRouteRefusesUnprovenSelfHostedRouting(t *testing.T) {
	t.Parallel()
	for _, runsOn := range []any{
		"self-hosted",
		[]any{"self-hosted", "cicd"},
		`${{ fromJSON('["self-hosted","cicd"]') }}`,
		`${{ github.event_name != 'merge_group' && fromJSON('["self-hosted","cicd"]') || 'ubuntu-latest' }}`,
		`${{ github.event_name == 'pull_request' && fromJSON('["self-hosted","cicd"]') || 'ubuntu-latest' }}`,
		`${{ contains(github.event.pull_request.labels.*.name, 'ci:run') && 'self-hosted' || 'ubuntu-latest' }}`,
	} {
		self, ok := runnerRoute(runsOn, "pull_request")
		if ok && !self {
			t.Errorf("runnerRoute(%q, pull_request) = hosted; it must flag or refuse it", asText(runsOn))
		}
	}
	for _, ev := range routingEvents {
		self, ok := runnerRoute(mergeGroupOnCICD, ev)
		if !ok || self != (ev == "merge_group") {
			t.Errorf("runnerRoute(mergeGroupOnCICD, %s) = %v, %v; want self-hosted for merge_group alone", ev, self, ok)
		}
		if self, ok := runnerRoute("ubuntu-latest", ev); !ok || self {
			t.Errorf("runnerRoute(ubuntu-latest, %s) = %v, %v", ev, self, ok)
		}
	}
}

func asText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		var parts []string
		for _, p := range x {
			if s, ok := p.(string); ok {
				parts = append(parts, s)
			}
		}
		return "[" + strings.Join(parts, ",") + "]"
	default:
		return ""
	}
}
