package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	if p, err := hostRunnerLookPath("air"); err == nil {
		preferPinnedAirOverOld(ctx, p)
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

// airVersionFn reports the version of the air at path ("" when it cannot
// tell). Swappable so a test needs no air binary.
var airVersionFn = func(ctx context.Context, path string) string {
	v, _ := captureVersion(ctx, airToolCheck(), func(ctx context.Context, _ string, args []string) (string, error) {
		return realVersionRunner(ctx, path, args)
	})
	return v
}

// preferPinnedAirOverOld checks the air on PATH against the floor `forge
// doctor` enforces (airToolCheck). An older air cannot run a config that
// names its binary with `entrypoint`, so the run would get as far as the
// readiness wait and fail there. Below the floor, forge provisions its
// pinned air and launches that for this run, saying so; when it cannot, it
// warns and keeps the air on PATH. An air whose version cannot be read is
// used as is, the same disposition doctor gives it.
func preferPinnedAirOverOld(ctx context.Context, pathAir string) {
	floor := airToolCheck().forOS(runtime.GOOS).MinVersion
	have := airVersionFn(ctx, pathAir)
	if have == "" {
		return
	}
	if cmp, ok := compareVersions(have, floor); !ok || cmp >= 0 {
		return
	}
	upgrade := installHintForOS(airToolCheck(), runtime.GOOS)
	dir, err := installAirFn(ctx)
	if err != nil {
		fmt.Printf("[up] warning: air %s on PATH (%s) is below %s, the oldest release forge's air configs are known to work with, and forge could not provision its pinned %s (%v): upgrade it with `%s`\n",
			have, pathAir, floor, airPinnedVersion, firstLine(err.Error()), upgrade)
		return
	}
	hostToolOverrides["air"] = filepath.Join(dir, "air"+exeSuffix())
	fmt.Printf("[up] air %s on PATH (%s) is below %s, the oldest release forge's air configs are known to work with: running forge's pinned %s for this run instead (upgrade yours with `%s`)\n",
		have, pathAir, floor, airPinnedVersion, upgrade)
}

// prepareAirDirs creates the directories an air-run workload's config
// writes into before air starts (hostlaunch.RunnerSpec.PrepareAirDirs):
// an air older than v1.63.7 cannot create a nested tmp_dir in a fresh
// checkout. A config forge cannot read is left for air to report.
func prepareAirDirs(w WorkloadEntity) {
	spec := hostRunnerSpec(w)
	if strings.TrimSpace(spec.Runner) != "air" {
		return
	}
	if err := spec.PrepareAirDirs(); err != nil && !errors.Is(err, fs.ErrNotExist) {
		fmt.Printf("[up] host %s: could not prepare the directories its air config writes into: %v\n", w.Name, err)
	}
}

// hostToolDirs are forge-managed tool directories host launches resolve
// binaries from after PATH.
var hostToolDirs []string

// hostToolOverrides pins a host runner's executable ahead of PATH, for a
// tool on PATH forge found too old to run this project (see
// preferPinnedAirOverOld).
var hostToolOverrides = map[string]string{}

// hostToolPath resolves a host runner's executable: an override first, then
// PATH, then the directories forge provisioned this run.
func hostToolPath(bin string) (string, bool) {
	if p, ok := hostToolOverrides[bin]; ok {
		return p, true
	}
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
// executable is not on PATH, or when the one on PATH is too old
// (hostToolOverrides).
func pinHostToolPath(cmd *exec.Cmd, runner string) {
	if strings.TrimSpace(runner) != "air" || len(cmd.Args) == 0 || cmd.Args[0] != "air" {
		return
	}
	if p, ok := hostToolPath("air"); ok {
		cmd.Path = p
		cmd.Err = nil
	}
}
