package config

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
)

// The three repositories' own pipeline.yaml files (MVP tasks C-f, C-c, C-r):
// reliant's is read from this checkout; forge's and control-plane's, and
// control-plane's infra patterns, are copies under testdata/repos of the files
// those repositories committed, since this module cannot read them. Each
// parses, validates against the editor schema, and resolves to the gate its
// repository meant.
func TestRepositoryFiles(t *testing.T) {
	rs := resolvedSchema(t)
	read := func(t *testing.T, path string) []byte {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	stage := func(r Resolved, n StageName) ResolvedStage { return r.Gate.Stage(n) }
	// sbomOff: the owner's decision (2026-11-26) holds in every repository
	// until forge vuln gate and the gate machines' grype databases exist: the
	// scan is off by the file's choice, never a default and never passed.
	sbomOff := func(t *testing.T, r Resolved) {
		t.Helper()
		if s := stage(r, StageSBOMScan); s.State != StageOff || s.Source != StageSourceOverride || s.Setting() != StageSettingOff {
			t.Errorf("sbom_scan = %+v, want off, written by the file", s)
		}
	}
	for _, tc := range []struct {
		name  string
		path  string
		check func(t *testing.T, r Resolved)
	}{
		{"reliant", filepath.Join("..", "..", "..", FilePath), func(t *testing.T, r Resolved) {
			if r.Check == nil || r.Check.Run != "task ci:merge" {
				t.Errorf("check = %+v, want task ci:merge", r.Check)
			}
			if s := stage(r, StageTest); s.State != StageOn || s.Command != "task --exit-code gate" {
				t.Errorf("test stage = %+v, want task gate", s)
			}
			if s := stage(r, StageLint); s.State != StageOff {
				t.Errorf("lint stage = %+v, want off (the test stage's lint lane runs it)", s)
			}
			if s := stage(r, StageBuild); s.Command != "go build ./..." {
				t.Errorf("build stage = %+v, want go build ./...", s)
			}
			if r.Regen != "task generate:forge" || !slices.Contains(r.Generated, "**/*_gen.go") {
				t.Errorf("regen %q, generated %q", r.Regen, r.Generated)
			}
			sbomOff(t, r)
		}},
		{"forge", filepath.Join("testdata", "repos", "forge.yaml"), func(t *testing.T, r Resolved) {
			if r.Check == nil || !slices.Equal(r.Check.Checks, []string{"CI gate"}) {
				t.Errorf("check = %+v, want the CI gate check", r.Check)
			}
			if r.Gate.Runner != RunnerMachine || !slices.Equal(r.Gate.Checks, []string{"Windows"}) {
				t.Errorf("gate = %+v, want a machine gate that also waits for Windows", r.Gate)
			}
			for n, cmd := range map[StageName]string{StageLint: "task --exit-code gate:lint", StageTest: "task --exit-code gate", StageBuild: "task --exit-code gate:build"} {
				if s := stage(r, n); s.State != StageOn || s.Command != cmd {
					t.Errorf("%s stage = %+v, want %s", n, s, cmd)
				}
			}
			sbomOff(t, r)
		}},
		{"control-plane", filepath.Join("testdata", "repos", "control-plane.yaml"), func(t *testing.T, r Resolved) {
			if r.Check == nil || r.Check.Run != "task ci" {
				t.Errorf("check = %+v, want task ci", r.Check)
			}
			if s := stage(r, StageTest); s.Command != "task --exit-code ci:full" {
				t.Errorf("test stage = %+v, want task ci:full", s)
			}
			if s := stage(r, StageLint); s.State != StageOff {
				t.Errorf("lint stage = %+v, want off (ci:full runs forge-lint)", s)
			}
			if s := stage(r, StageBuild); s.Command != "go build ./..." {
				t.Errorf("build stage = %+v, want go build ./...", s)
			}
			if r.Queue.BatchMax != 6 || r.Regen != "reliant forge generate" || r.Gate.InfraPatternsFile != ".reliant/infra.txt" {
				t.Errorf("queue %+v, regen %q, infra file %q", r.Queue, r.Regen, r.Gate.InfraPatternsFile)
			}
			sbomOff(t, r)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := read(t, tc.path)
			r, err := Load(data)
			if err != nil {
				t.Fatalf("Parse rejected %s:\n%v", tc.path, err)
			}
			if err := rs.Validate(asJSON(t, data)); err != nil {
				t.Errorf("the JSON schema rejected %s: %v", tc.path, err)
			}
			tc.check(t, r)
		})
	}

	t.Run("control-plane infra patterns", func(t *testing.T) {
		res, err := ParseInfraPatterns(read(t, filepath.Join("testdata", "repos", "control-plane-infra.txt")))
		if err != nil {
			t.Fatalf("ParseInfraPatterns: %v", err)
		}
		for _, line := range []string{
			"docker: Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?",
			"write /tmp/x: no space left on device",
			"heavy-lock: test-unit gave up waiting after 600s",
			"GATE HEALTH FAILURE: 1 infrastructure step(s); no PR blamed",
			"Error response from daemon: toomanyrequests: retry-after: 539.481µs",
		} {
			if !slices.ContainsFunc(res, func(p *regexp.Regexp) bool { return p.MatchString(line) }) {
				t.Errorf("no pattern matches the infra line %q", line)
			}
		}
		for _, line := range []string{
			"--- FAIL: TestCreateDaemon (0.02s)",
			"internal/billing/ledger_test.go:41: got 3, want 4",
		} {
			for _, p := range res {
				if p.MatchString(line) {
					t.Errorf("pattern %q matches the test failure %q: a red change would land as unproven", p, line)
				}
			}
		}
	})
}
