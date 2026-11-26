// Copyright (c) 2025 Reliant Labs
package gaterun

import (
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/pipeline/config"
)

// skipDocker is what a container test's helper does in a gate: one line in
// the stage's skip log, then a skip.
func skipDocker(n int) string {
	return strings.Repeat(`echo pkg.TestContainer >>"$`+EnvDockerSkips+`"; `, n) + `echo "--- SKIP: ` + DockerSkipMessage + `"`
}

func TestSkippedContainerTestsAreCountedNeverPassedSilently(t *testing.T) {
	f := newFake(t, IsolationRunc)
	r := newRunner(t, f, nil)
	g, _ := runGate(t, f, r, startReq(
		cmdStage(config.StageLint, "true"),
		cmdStage(config.StageTest, skipDocker(3)),
		cmdStage(config.StageBuild, skipDocker(1)),
		offStage(config.StageSBOMScan),
	))
	if g.Class != ClassPass {
		t.Fatalf("class %s (%s)", g.Class, g.Reason)
	}
	if g.SkippedDockerTests != 4 {
		t.Fatalf("gate counted %d skipped container tests, want 4", g.SkippedDockerTests)
	}
	test := g.Stages[config.StageTest]
	if test.SkippedDockerTests != 3 || !strings.Contains(test.Summary, "3 container test(s) skipped: docker unavailable (gate)") {
		t.Fatalf("test stage %+v", test)
	}
	if lint := g.Stages[config.StageLint]; lint.SkippedDockerTests != 0 || lint.Summary != "" {
		t.Fatalf("lint stage %+v, want no skips", lint)
	}
}

func TestEachStageHasItsOwnSkipLogNamedForTheRun(t *testing.T) {
	f := newFake(t, IsolationRunc)
	r := newRunner(t, f, nil)
	g, _ := runGate(t, f, r, startReq(
		cmdStage(config.StageLint, `case "$`+EnvDockerSkips+`" in */skips/lint.gate-b1-1) ;; *) exit 1 ;; esac`),
		cmdStage(config.StageTest, `case "$`+EnvDockerSkips+`" in */skips/test.gate-b1-1) ;; *) exit 1 ;; esac`),
		cmdStage(config.StageBuild, "true"),
		offStage(config.StageSBOMScan),
	))
	if g.Class != ClassPass {
		t.Fatalf("a stage saw the wrong skip log: %s", g.Reason)
	}
}

func TestGateEnvMayNotSetTheSkipLog(t *testing.T) {
	if _, err := GateEnv("gate-b1-1", map[string]string{EnvDockerSkips: "/dev/null"}); err == nil {
		t.Fatalf("gate.env set %s", EnvDockerSkips)
	}
}

func TestTheWarmCacheIsOnlyForAnOptedInNotIsolatedGate(t *testing.T) {
	runc, sysbox := newFake(t, IsolationRunc), newFake(t, IsolationSysbox)
	for _, tc := range []struct {
		name        string
		b           Backend
		warm, allow bool
		want        string
	}{
		{"off by default", runc, false, true, ""},
		{"asked without the unisolated opt-in", runc, true, false, ""},
		{"opted in, on runc", runc, true, true, "reliant-gate-cache-acme-widgets-d782c8744023"},
		{"opted in, but isolated", sysbox, true, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := startReq()
			req.WarmCache, req.AllowNotIsolated = tc.warm, tc.allow
			if got := cacheVolume(tc.b, req); got != tc.want {
				t.Fatalf("cache volume %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTheRunnerPassesTheCacheVolumeToTheBackend(t *testing.T) {
	f := newFake(t, IsolationRunc)
	r := newRunner(t, f, nil)
	req := startReq(cmdStage(config.StageLint, "true"), cmdStage(config.StageTest, "true"),
		cmdStage(config.StageBuild, "true"), offStage(config.StageSBOMScan))
	req.WarmCache, req.AllowNotIsolated = true, true
	runGate(t, f, r, req)
	if len(f.specs) != 1 || f.specs[0].CacheVolume != "reliant-gate-cache-acme-widgets-d782c8744023" {
		t.Fatalf("specs %+v", f.specs)
	}
}

func TestCacheVolumeName(t *testing.T) {
	for repo, want := range map[string]string{
		"reliant-labs/reliant":       "reliant-gate-cache-reliant-labs-reliant-7bf4989553b3",
		"Reliant-Labs/Control_Plane": "reliant-gate-cache-reliant-labs-control_plane-cca132ff50d9",
		"a/b.c":                      "reliant-gate-cache-a-b.c-c06205148f78",
	} {
		if got := CacheVolumeName(repo); got != want {
			t.Errorf("CacheVolumeName(%q) = %q, want %q", repo, got, want)
		}
	}
	// The readable part is ambiguous; the hash keeps the names apart.
	if a, b := CacheVolumeName("a/b-c"), CacheVolumeName("a-b/c"); a == b {
		t.Fatalf("a/b-c and a-b/c share the cache volume %s", a)
	}
	// GitHub names are case-insensitive: one repository, one cache.
	if a, b := CacheVolumeName("Acme/Widgets"), CacheVolumeName("acme/widgets"); a != b {
		t.Fatalf("one repository, two caches: %s and %s", a, b)
	}
}
