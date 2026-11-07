package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// airPinnedVersion is the air release forge provisions when `forge env up`
// needs the hot-reload runner and none is on PATH. It is above the floor
// `forge doctor` enforces (airToolCheck).
const airPinnedVersion = "v1.67.4"

var airTool = protoTool{
	Binary:        "air",
	Module:        "github.com/air-verse/air",
	VersionModule: "github.com/air-verse/air",
}

// installAirFn provisions the pinned air into forge's tool cache and returns
// the directory holding the binary. Swappable so a test needs no network.
var installAirFn = installPinnedAir

func installPinnedAir(ctx context.Context) (string, error) {
	dir, err := toolCacheDir(airTool, airPinnedVersion)
	if err != nil {
		return "", err
	}
	if _, err := installPinnedTool(ctx, airTool, airPinnedVersion, dir, "not found on PATH", func() {
		fmt.Printf("[up] air (hot reload) is not on PATH: installing %s into forge's tool cache (first run only)...\n", airPinnedVersion)
	}); err != nil {
		return "", err
	}
	return dir, nil
}

// provisionAir makes every air-runner host workload in the run launchable
// without the user having installed air: an air on PATH wins; otherwise the
// pinned air is installed into forge's tool cache; and if that fails the
// workloads run under go-run, with one line saying why. Hot reload is a
// convenience, so it never fails the dev loop.
func provisionAir(ctx context.Context, e *KCLEntities, targets []string) {
	if e == nil {
		return
	}
	var air []*HostRuntime
	for _, w := range e.WorkloadsOn(RuntimeHost) {
		if !w.LongRunning() || !inTargetSet(targets, w.Name) || w.Runtime.Host == nil {
			continue
		}
		if strings.TrimSpace(w.Runtime.Host.Runner) == "air" {
			air = append(air, w.Runtime.Host)
		}
	}
	if len(air) == 0 {
		return
	}
	if _, err := hostRunnerLookPath("air"); err == nil {
		return
	}
	dir, err := installAirFn(ctx)
	if err != nil {
		for _, h := range air {
			h.Runner = "go-run"
		}
		fmt.Printf("[up] air is not on PATH and forge could not install it (%v): running the API under go-run — no hot reload (install air to get it back)\n", firstLine(err.Error()))
		return
	}
	hostToolDirs = append(hostToolDirs, dir)
}

// hostToolDirs are forge-managed tool directories host launches resolve
// binaries from after PATH.
var hostToolDirs []string

// hostToolPath resolves a host runner's executable: PATH first, then the
// directories forge provisioned this run.
func hostToolPath(bin string) (string, bool) {
	if p, err := hostRunnerLookPath(bin); err == nil {
		return p, true
	}
	for _, d := range hostToolDirs {
		p := filepath.Join(d, bin+exeSuffix())
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, true
		}
	}
	return "", false
}

// pinHostToolPath points cmd at the provisioned binary when the runner's
// executable is not on PATH.
func pinHostToolPath(cmd *exec.Cmd, runner string) {
	if strings.TrimSpace(runner) != "air" || len(cmd.Args) == 0 || cmd.Args[0] != "air" {
		return
	}
	if p, ok := hostToolPath("air"); ok {
		cmd.Path = p
		cmd.Err = nil
	}
}
