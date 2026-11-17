package cli

import (
	"slices"
	"strings"
	"testing"
)

// TestGoBuildTargetsFromKCL_DelveRunnerBuildsForDebugging pins defect 7: a
// host workload run by the delve runner gets a debug build — symbols kept,
// optimizations off, source paths untrimmed — or dlv resolves no breakpoint
// in it. Other workloads keep the stripped release build, and a binary
// shared with a delve workload is built for the debugger.
func TestGoBuildTargetsFromKCL_DelveRunnerBuildsForDebugging(t *testing.T) {
	delve := func(w *WorkloadEntity) { w.Runtime.Host.Runner = "delve" }
	shared := func(w *WorkloadEntity) {
		w.Build = BuildConfigEntity{Type: "go", Go: &GoBuild{Cmd: "./cmd/proj", OutputName: "proj"}}
	}
	e := &KCLEntities{Workloads: []WorkloadEntity{
		hostWL("api", delve),
		hostWL("worker"),
		hostWL("a", shared),
		hostWL("b", shared, delve),
		clusterWL("c", "k3d-x", "ns", func(w *WorkloadEntity) {
			w.Build = BuildConfigEntity{Type: "go", Go: &GoBuild{Cmd: "./cmd/c", OutputName: "c"}}
		}),
	}}
	debug := map[string]bool{}
	for _, tt := range goBuildTargetsFromKCL(e) {
		debug[tt.outputName] = tt.debug
	}
	want := map[string]bool{"api": true, "worker": false, "proj": true, "c": false}
	for name, d := range want {
		if debug[name] != d {
			t.Errorf("target %s debug = %v, want %v (all: %v)", name, debug[name], d, debug)
		}
	}

	args := goBuildArgs(goBuildTarget{cmd: "./cmd/api", outputName: "api", debug: true, trimpath: true}, "bin/api", true, versionInfo{})
	joined := strings.Join(args, " ")
	if !slices.Contains(args, "-gcflags=all=-N -l") || !slices.Contains(args, "-trimpath=false") {
		t.Errorf("debug build args %v lack -N -l / -trimpath=false", args)
	}
	if strings.Contains(joined, "-s -w") || slices.Contains(args, "-trimpath") {
		t.Errorf("debug build args %v strip symbols or trim paths", args)
	}
	release := strings.Join(goBuildArgs(goBuildTarget{cmd: "./cmd/w", outputName: "w"}, "bin/w", false, versionInfo{}), " ")
	if !strings.Contains(release, "-s -w") {
		t.Errorf("release build lost its stripping: %s", release)
	}
}
