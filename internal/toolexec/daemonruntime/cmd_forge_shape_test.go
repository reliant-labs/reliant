// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"encoding/json"
	"strings"
	"testing"

	forgecli "github.com/reliant-labs/forge/cli"
)

// forge.env_shape is the bootstrap path for an environment the control plane
// has never seen (briefing §6). Preview reads it, and the BROWSER then calls
// EnsureEnvironment with the kind and shape it carries — so these tests pin
// the two things a caller downstream depends on: the argv forge is handed,
// and that forge's document arrives upstream unaltered.

func TestForgeEnvShapeIsRegistered(t *testing.T) {
	// A handler that is written but not registered is invisible to the RPC
	// layer above it, which is the failure this catches.
	stubForge(t, forgeCommandResult{Stdout: []byte(`{"ok":true}`)}, nil)
	if _, err := handle(t, "forge.env_shape", map[string]string{
		"project_path": t.TempDir(),
		"env":          "prod",
	}); err != nil {
		t.Fatalf("forge.env_shape not dispatched: %v", err)
	}
}

func TestForgeEnvShapeArgv(t *testing.T) {
	// Pinned exactly. The env is a POSITIONAL, as `forge env build <env>`
	// and `forge env render <env>` take it — not a --env flag.
	dir := forgeProject(t)
	call := stubForge(t, forgeCommandResult{Stdout: []byte(`{"kind":"self_managed"}`)}, nil)

	if _, err := handle(t, "forge.env_shape", forgeEnvShapeArgs{ProjectPath: dir, Env: "prod"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if call.Count != 1 {
		t.Fatalf("expected one forge invocation, got %d", call.Count)
	}
	want := []string{"env", "shape", "prod", "--json"}
	if strings.Join(call.Args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args:\n got %q\nwant %q", call.Args, want)
	}
	if call.ProjectDir != dir {
		t.Fatalf("project dir: got %q want %q", call.ProjectDir, dir)
	}
}

func TestForgeEnvShapeRequiresEnv(t *testing.T) {
	// Refused rather than sent. `forge env shape` with no positional would
	// resolve to a different command surface, and a Register built from
	// whatever that printed would write the wrong environment's shape.
	stubForge(t, forgeCommandResult{Stdout: []byte(`{}`)}, nil)
	if _, err := handle(t, "forge.env_shape", forgeEnvShapeArgs{ProjectPath: forgeProject(t)}); err == nil {
		t.Fatal("expected an error for a missing env")
	}
}

// TestForgeEnvShapePassesForgeDocumentThrough is the contract Register is
// built on. The browser reads `kind`, `shape` and `provenance` out of this
// document and sends them to EnsureEnvironment, so a daemon that re-derived,
// filtered or re-keyed any of it would let a Register write a declaration
// that disagrees with what `forge env build` records for the same env — and
// the env's kind is immutable, so whichever got there first would win.
func TestForgeEnvShapePassesForgeDocumentThrough(t *testing.T) {
	// The JSON contract from briefing §6: forge's own projection of the
	// env's render, which `forge env build` records verbatim.
	document := `{
		"project": "hounders",
		"env": "prod",
		"kind": "self_managed",
		"shape": {
			"kind": "self_managed",
			"workloads": [{"name": "api", "runtime": "cluster", "cluster": "prod-gke"}],
			"secrets": [{"name": "STRIPE_WEBHOOK_SECRET", "provider": "hosted", "declared_by": ["api"]}],
			"domains": ["app.example.com"],
			"clusters": ["prod-gke"],
			"objects": []
		},
		"provenance": {
			"repo": "github.com/reliant-labs/hounders",
			"commit": "def56781234",
			"branch": "feat-x",
			"dirty": true,
			"tree": "a1b2c3",
			"forge_version": "v0.1.44"
		}
	}`

	dir := forgeProject(t)
	stubForge(t, forgeCommandResult{Stdout: []byte(document)}, nil)

	raw, err := handle(t, "forge.env_shape", forgeEnvShapeArgs{ProjectPath: dir, Env: "prod"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := decodeForgeResponse(t, raw)
	if !got.IsForgeProject {
		t.Fatal("is_forge_project should be true for a dir holding forge.yaml")
	}

	// Compared as decoded JSON rather than as bytes: the envelope is free
	// to re-encode, and what must survive is the document's CONTENT.
	var want, have any
	if err := json.Unmarshal([]byte(document), &want); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	if err := json.Unmarshal(got.Report, &have); err != nil {
		t.Fatalf("unmarshal report: %v (%s)", err, got.Report)
	}
	wantJSON, _ := json.Marshal(want)
	haveJSON, _ := json.Marshal(have)
	if string(wantJSON) != string(haveJSON) {
		t.Fatalf("forge's document was altered in transit:\n got %s\nwant %s", haveJSON, wantJSON)
	}
}

// TestForgeEnvShapeArgvParsesAgainstEmbeddedForge is the same guard
// cmd_forge_argv_test.go applies to every other forge argv: parse it against
// the forge command tree THIS BINARY EMBEDS, so a forge CLI change that
// breaks the daemon fails in `go test` rather than in a user's daemon.
//
// It is SKIPPED, not omitted, while the pinned forge predates the verb. `forge
// env shape` ships in F-DECL; reliant's forge pin is bumped by the
// orchestrator once that merges, and this test starts enforcing itself on that
// commit with no further edit. Asserting against a forge that does not have
// the command yet would fail for the one reason that is not a defect, and
// deleting the test instead would mean the pin bump lands unguarded — which is
// exactly how `forge secret list <env> --json` survived forge moving the env
// to a required flag.
func TestForgeEnvShapeArgvParsesAgainstEmbeddedForge(t *testing.T) {
	args := forgeEnvShapeArgs{ProjectPath: "/p", Env: "prod"}.shapeArgs()

	// The probe checks the RESOLVED COMMAND PATH, not whether Find errored.
	// cobra resolves an unknown verb to its PARENT with the verb swallowed as
	// a positional and no error at all — `env shape` comes back as
	// `forge env` on a forge that lacks the subcommand. Trusting the error
	// alone would make the skip never fire and the test fail on the one
	// condition that is not a defect.
	root := forgecli.NewRootCmd()
	resolved, _, findErr := root.Find([]string{"env", "shape"})
	if findErr != nil || resolved.CommandPath() != "forge env shape" {
		t.Skip("the pinned forge has no `env shape` yet (F-DECL); this test enforces itself once reliant's forge pin carries it")
	}

	path, err := parseAgainstEmbeddedForge(args)
	if err != nil {
		t.Fatalf("embedded forge rejects daemon argv %q (resolved %q): %v", args, path, err)
	}
	if want := "forge env shape"; path != want {
		t.Errorf("argv %q resolved to %q, want %q", args, path, want)
	}
}
