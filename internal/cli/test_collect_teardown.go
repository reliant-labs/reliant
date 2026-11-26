package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/testcapture"
	"github.com/reliant-labs/forge/internal/testcollect"
)

// Collect before teardown: `forge env down` and `forge env preview delete`
// pull the env's coverage before they stop it, because a process that is
// SIGKILLed (a container `docker rm -f` removes, a pod whose grace ran out)
// takes its counters with it. It runs only when a test run is active
// (FORGE_TEST_RUN, which `forge test begin` and every captured lane export)
// or --collect asks, and only for an env with instrumented workloads. It is
// best effort: a failure is a warning, and never blocks or fails the
// teardown.

// teardownCollectTimeout bounds the whole collect, so an unreachable
// cluster cannot hold a teardown.
const teardownCollectTimeout = 2 * time.Minute

type teardownCollectKey struct{}

// withTeardownCollect carries a verb's --collect to the teardown it runs.
func withTeardownCollect(ctx context.Context, on bool) context.Context {
	return context.WithValue(ctx, teardownCollectKey{}, on)
}

func teardownCollectRequested(ctx context.Context) bool {
	on, _ := ctx.Value(teardownCollectKey{}).(bool)
	return on
}

// collectBeforeTeardown collects env's coverage into the active run (else,
// with --collect, a new one). e is the env's render when the caller has it;
// nil renders it.
func collectBeforeTeardown(ctx context.Context, projectDir, env string, requested bool, e *KCLEntities, out io.Writer) {
	if !requested && strings.TrimSpace(os.Getenv(testcapture.EnvRun)) == "" {
		return
	}
	if testcapture.Off(os.Getenv) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, teardownCollectTimeout)
	defer cancel()
	warn := func(format string, a ...any) {
		fmt.Fprintf(out, "warning: coverage not collected before teardown: "+format+"\n", a...)
	}
	if e == nil {
		var err error
		if e, err = RenderKCL(ctx, projectDir, env); err != nil {
			warn("render env %s: %v", env, err)
			return
		}
	}
	targets, err := collectTargets(e, projectDir, env, config.ReadTestsCoverage(projectDir))
	if err != nil {
		warn("%v", err)
		return
	}
	instrumented := false
	for _, t := range targets {
		instrumented = instrumented || t.Coverage == ""
	}
	if !instrumented {
		return
	}
	lane := strings.TrimSpace(os.Getenv(testcapture.EnvLane))
	if lane == "" {
		lane = testcollect.DefaultLane
	}
	doc, err := runTestCollect(ctx, e, testCollectRequest{
		projectDir: projectDir, env: env, lane: lane,
		command: []string{"forge", "test", "collect", env, "--lane=" + lane},
	})
	if doc != nil {
		testcollect.WriteText(out, doc)
	}
	if err != nil {
		warn("%v", err)
	}
}
