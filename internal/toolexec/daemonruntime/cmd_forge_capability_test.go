// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"encoding/json"
	"errors"
	"testing"
)

// errForgeUnrunnable stands in for "the forge process could not be started or
// was killed" — the case that yields no verdict at all.
var errForgeUnrunnable = errors.New("run forge: exec: no such file or directory")

// These tests pin the distinction that the whole command exists to draw: a
// daemon that is REACHABLE but whose forge cannot render is a capability
// answer, not a transport error. Before this command, that state reached the
// Forge tab as "Could not reach your daemon" — a message that sends the reader
// to the network for a build-flag problem.
//
// The canned documents below are the REAL `forge doctor --json` output,
// captured from two builds of the same reliant commit at forge v0.1.42: one
// CGO_ENABLED=0 (what the cloud workspace image shipped) and one
// CGO_ENABLED=1. That is what makes these fixtures evidence rather than a
// guess at forge's shape.

// doctorJSON builds a doctor report carrying the kcl-plugin check in the
// given state, plus an unrelated check, so a matcher that fuzzy-matches the
// wrong one is caught.
func doctorJSON(t *testing.T, status, message, evidence string) []byte {
	t.Helper()
	report := forgeDoctorReport{Checks: []forgeDoctorCheck{
		// `tool: kcl` is a DIFFERENT check: the kcl binary on PATH. It
		// passes on a binary that cannot render, so a substring match
		// on "kcl" would read this and report the opposite answer.
		{Name: "tool: kcl", Status: "pass", Message: "kcl present (0.11.2)"},
		{Name: forgeKCLPluginCheckName, Status: status, Message: message, Evidence: evidence},
	}}
	blob, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal doctor report: %v", err)
	}
	return blob
}

func capability(t *testing.T, res forgeCommandResult, runErr error) forgeRenderCapabilityResponse {
	t.Helper()
	stubForge(t, res, runErr)
	raw, err := handle(t, "forge.render_capability", forgeRenderCapabilityRequest{ProjectPath: forgeProject(t)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got forgeRenderCapabilityResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal response: %v (%s)", err, raw)
	}
	return got
}

// TestForgeRenderCapabilityReportsCGOFreeForgeAsIncapable is the fail-before
// case: the exact state of a managed cloud daemon, whose forge was built
// CGO-free and refused every render.
//
// The message and evidence are forge's own, verbatim from
// `reliant forge doctor --json` on a CGO_ENABLED=0 build at v0.1.42.
func TestForgeRenderCapabilityReportsCGOFreeForgeAsIncapable(t *testing.T) {
	const (
		message  = "kcl_plugin.forge unavailable — this forge was built without CGO; no environment can render"
		evidence = "expected: a forge built with CGO_ENABLED=1 (registers kcl_plugin.forge in-process)\n" +
			"found:    this binary (v0.1.42) registers nothing; internal/kclplugin.Register is the //go:build !cgo no-op"
	)

	// doctor exits NON-ZERO whenever any check fails, so a handler that
	// treated the exit code as the verdict would report "undetermined"
	// here and the UI would fall back to the unreachable state.
	got := capability(t, forgeCommandResult{
		Stdout:   doctorJSON(t, "fail", message, evidence),
		ExitCode: 1,
	}, nil)

	if got.CanRender {
		t.Fatal("a CGO-free forge was reported as able to render; the Forge tab would show environments it cannot deploy")
	}
	if !got.Determined {
		t.Error("the capability WAS determined (doctor answered); reporting it as unknown hides a definite answer")
	}
	if got.Reason != message {
		t.Errorf("reason must be forge's own wording, so the UI and `forge doctor` agree\n got %q\nwant %q", got.Reason, message)
	}
	if got.Detail != evidence {
		t.Errorf("evidence must survive to the UI — it carries the fix\n got %q", got.Detail)
	}
	if got.ForgeVersion == "" {
		t.Error("forge_version is required: 'cannot render' without a version gives the user nothing to act on")
	}
}

// TestForgeRenderCapabilityReportsCGOBuildAsCapable is the pass-after case.
func TestForgeRenderCapabilityReportsCGOBuildAsCapable(t *testing.T) {
	got := capability(t, forgeCommandResult{
		Stdout: doctorJSON(t, "pass", "kcl_plugin.forge registered in-process (CGO build)", ""),
	}, nil)

	if !got.CanRender {
		t.Fatal("a CGO-enabled forge must report that it can render")
	}
	if !got.Determined {
		t.Error("determined must be true on a pass")
	}
	if got.Reason != "" {
		t.Errorf("no reason belongs on a capable forge, got %q", got.Reason)
	}
}

// TestForgeRenderCapabilityLeavesUnknownUndetermined covers the three ways the
// probe can fail to produce an answer. None of them may be reported as
// "cannot render": refusing a project over an inconclusive probe is the same
// class of error as blaming the transport for a build flag.
func TestForgeRenderCapabilityLeavesUnknownUndetermined(t *testing.T) {
	for _, tc := range []struct {
		name   string
		res    forgeCommandResult
		runErr error
	}{
		// A forge too old to have the self-capability check at all.
		{"check absent", forgeCommandResult{Stdout: []byte(`{"checks":[{"name":"tool: kcl","status":"pass"}]}`)}, nil},
		{"unparseable output", forgeCommandResult{Stdout: []byte("not json")}, nil},
		{"forge could not be run", forgeCommandResult{}, errForgeUnrunnable},
		// SKIP means the check did not apply (deploy is off for this
		// project). That is not an assertion that rendering works.
		{"check skipped", forgeCommandResult{Stdout: []byte(`{"checks":[{"name":"forge: kcl-plugin","status":"skip"}]}`)}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := capability(t, tc.res, tc.runErr)
			if got.Determined {
				t.Error("an inconclusive probe must not claim a determined verdict")
			}
			if got.CanRender {
				t.Error("an inconclusive probe must not claim the forge can render")
			}
		})
	}
}

// TestForgeRenderCapabilityAsksDoctorNotRender pins the probe choice. A render
// is the wrong instrument: it needs a renderable environment and it WRITES
// files, so a capability question would depend on project state and have side
// effects. doctor's check is a pure in-process read.
func TestForgeRenderCapabilityAsksDoctorNotRender(t *testing.T) {
	call := stubForge(t, forgeCommandResult{
		Stdout: doctorJSON(t, "pass", "ok", ""),
	}, nil)
	if _, err := handle(t, "forge.render_capability", forgeRenderCapabilityRequest{ProjectPath: forgeProject(t)}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"doctor", "--json"}
	if len(call.Args) != len(want) || call.Args[0] != want[0] || call.Args[1] != want[1] {
		t.Fatalf("argv:\n got %q\nwant %q", call.Args, want)
	}
}
