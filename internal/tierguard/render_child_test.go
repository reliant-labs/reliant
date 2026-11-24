package tierguard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/reliant-labs/forge/internal/checksums"
)

// Rendering a fixture costs ~30s of real codegen (project new, scaffold,
// buf, go mod tidy) and the guard needs four of them. Run serially
// that is the single slowest package in the repo; run concurrently it is
// roughly the cost of the slowest fixture.
//
// They cannot be made concurrent in-process. render() drives forge
// through forgeIn, which sets the working directory with os.Chdir and
// the command line with os.Args, and it reads the Tier-1 inventory from
// checksums.Tier1TargetSet. All three are process-global: two renders
// sharing them would chdir out from under each other and cross-attribute
// targeted files. That is not a lock we can take — the globals are the
// pipeline's own interface, and serializing on them just restores the
// original runtime.
//
// So each render gets its own PROCESS, where the globals are private
// again. The child is this same test binary re-executed with a marker
// argv, which is the mechanism the protoc-gen-forge dispatch in TestMain
// already established; the child renders exactly one fixture and writes
// the result back as JSON on stdout.

// renderChildArgv is the marker argv that turns this test binary into a
// single-fixture renderer. It is not a flag: TestMain inspects os.Args[1]
// before the testing package parses anything, exactly as the
// protoc-gen-forge dispatch does.
const renderChildArgv = "tierguard-render-child"

// renderChildResult is the wire form of a renderResult.
//
// renderResult carries map[string][]byte for the file bodies, which
// encoding/json renders as base64 strings and decodes back to bytes
// without loss, so the struct crosses the process boundary as-is. The
// bodies are the whole point of the guard, so they travel with it rather
// than being re-read by the parent — the parent would then be reading a
// tree the child had already post-processed (marker-stripped), which is
// the kind of skew that makes a guard quietly compare the wrong bytes.
type renderChildResult struct {
	Result *renderResult `json:"result"`
	Err    string        `json:"err,omitempty"`
	// ChokepointTargets is len(checksums.Tier1Targets()) as observed in
	// the child immediately after the render, i.e. IN the process that
	// actually ran the pipeline.
	//
	// It exists because the property it feeds —
	// TestTier1InventoryIsProducerDerived's "the set was populated
	// through the checksums chokepoint" — is a statement about the
	// rendering process's memory, and the parent no longer renders. The
	// observation therefore has to travel with the result rather than
	// being re-read from the parent's own (correctly empty) globals.
	// Dropping the assertion instead would retire the check that catches
	// a Tier-1 writer bypassing checksums.WriteGeneratedFile.
	ChokepointTargets int `json:"chokepoint_targets"`
}

// fixtureByKey maps the stable key passed on the child's command line to
// the fixture constructor.
//
// The KEY crosses the process boundary, never the fixture struct. A
// serialized fixture would be a second, silently divergent definition of
// the same shape: add a field to fixture, forget it in the wire form,
// and the child renders a subtly different project while every test
// still passes. Constructing from the same function in both processes
// makes that class of drift impossible.
func fixtureByKey(key string) (fixture, bool) {
	switch key {
	case "a":
		return projectA(), true
	case "b":
		return projectB(), true
	case "c":
		return projectC(), true
	case "d":
		return projectD(), true
	}
	return fixture{}, false
}

// runRenderChild is the child half: render one fixture, report it as
// JSON on stdout, exit. Called from TestMain before m.Run, so the
// testing framework never starts here.
func runRenderChild() {
	if len(os.Args) < 4 {
		fmt.Fprintf(os.Stderr, "%s: want <key> <parent-dir>\n", renderChildArgv)
		os.Exit(2)
	}
	key, parent := os.Args[2], os.Args[3]

	fx, ok := fixtureByKey(key)
	if !ok {
		fmt.Fprintf(os.Stderr, "%s: unknown fixture key %q\n", renderChildArgv, key)
		os.Exit(2)
	}

	// The pipeline writes progress to stdout, which is the channel this
	// child reports on. Point the process's own stdout at stderr for the
	// duration of the render so that output is preserved for debugging
	// (the parent forwards it on failure) without corrupting the JSON.
	realStdout := os.Stdout
	os.Stdout = os.Stderr

	res, err := render(parent, fx)

	os.Stdout = realStdout

	payload := renderChildResult{
		Result:            res,
		ChokepointTargets: len(checksums.Tier1Targets()),
	}
	if err != nil {
		payload.Err = err.Error()
	}
	if encErr := json.NewEncoder(os.Stdout).Encode(payload); encErr != nil {
		fmt.Fprintf(os.Stderr, "%s: encoding result: %v\n", renderChildArgv, encErr)
		os.Exit(1)
	}
	os.Exit(0)
}

// renderInChild renders fx into parent by re-executing this test binary,
// returning the same *renderResult an in-process render() would.
func renderInChild(parent string, fx fixture) (*renderResult, error) {
	key, ok := fixtureKey(fx)
	if !ok {
		return nil, fmt.Errorf("fixture %q has no registered key; add it to fixtureByKey", fx.Label)
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, err
	}

	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locating the test binary: %w", err)
	}

	cmd := exec.Command(self, renderChildArgv, key, parent)
	// Inherit the environment: the pipeline shells out to buf and
	// go, all of which need PATH, HOME and the Go env as configured for
	// the parent run.
	//
	// EXCEPT GOWORK. Each fixture's `project new` bridges the fixture to this
	// forge checkout through the fixture's OWN go.work (this test binary is
	// an unreleased forge, which no module proxy serves). An inherited
	// GOWORK=off — the usual setting for a gate run from inside another
	// project — disables that bridge for every `go` command the pipeline
	// runs, and generate's version-compatibility check then (correctly)
	// refuses the unbridged fixture. The guard must not depend on how the
	// caller's shell is configured, so the child decides its own workspace.
	//
	// Two more the child must own rather than inherit. The renderer IS the
	// forge under test, so it must not hand a command to a built twin of the
	// bridged checkout: that exec replaces the child's process and the JSON
	// below is never written (FORGE_ALLOW_VERSION_SKEW is the "run THIS
	// forge" switch). And the fixture's initial commit needs a git identity,
	// which a clean machine or CI container does not have globally.
	cmd.Env = append(withoutEnv(os.Environ(), "GOWORK"),
		"FORGE_ALLOW_VERSION_SKEW=1",
		"GIT_AUTHOR_NAME=forge-tierguard", "GIT_AUTHOR_EMAIL=tierguard@example.com",
		"GIT_COMMITTER_NAME=forge-tierguard", "GIT_COMMITTER_EMAIL=tierguard@example.com",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if runErr := cmd.Run(); runErr != nil {
		return nil, fmt.Errorf("render child for %s: %w\n--- child output ---\n%s",
			fx.Label, runErr, tail(stderr.String()))
	}

	var payload renderChildResult
	if decErr := json.Unmarshal(stdout.Bytes(), &payload); decErr != nil {
		return nil, fmt.Errorf("decoding render child result for %s: %w\n--- child output ---\n%s",
			fx.Label, decErr, tail(stderr.String()))
	}
	if payload.Err != "" {
		return nil, fmt.Errorf("%s\n--- child output ---\n%s", payload.Err, tail(stderr.String()))
	}
	if payload.Result == nil {
		return nil, fmt.Errorf("render child for %s reported neither a result nor an error", fx.Label)
	}
	payload.Result.ChokepointTargets = payload.ChokepointTargets
	return payload.Result, nil
}

// fixtureKey is the inverse of fixtureByKey, matched on the fixture's
// Label so the two stay in step through the constructors rather than
// through a hand-maintained second table.
func fixtureKey(fx fixture) (string, bool) {
	for _, key := range []string{"a", "b", "c", "d"} {
		if candidate, ok := fixtureByKey(key); ok && candidate.Label == fx.Label {
			return key, true
		}
	}
	return "", false
}

// tail bounds child output in a failure message. A failing render can
// emit a lot of pipeline logging, and the useful part is the end.
func tail(s string) string {
	const max = 4000
	if len(s) <= max {
		return s
	}
	return "...(truncated)...\n" + s[len(s)-max:]
}

// withoutEnv returns env with every `key=...` entry removed.
func withoutEnv(env []string, key string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if !strings.HasPrefix(kv, prefix) {
			out = append(out, kv)
		}
	}
	return out
}
