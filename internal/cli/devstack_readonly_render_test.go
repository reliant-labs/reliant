package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/devstack"
	"github.com/reliant-labs/forge/internal/kclplugin"
)

// A read-only command must never register a port block — not even for an env
// that runs on this machine.
//
// Incident: `forge env render dev` and `forge env config dev` from a fresh git
// worktree of control-plane. dev IS local (k3d + host processes), so the
// renderDeclaration gate (devstack_render_purpose_test.go) correctly decided
// its ports mattered — and then armed the CLAIMING allocator, which tried to
// register a block for the new worktree. The registry was at the 8-block
// dev_stack.max_stacks ceiling, so a command that only prints manifests
// failed with "refusing to allocate a NEW port block". Below the ceiling it
// would have leaked a block per throwaway worktree instead.
//
// The fix separates "may claim" (blockClaim) from "which ports" (renderPurpose).
// These tests pin both halves of the read-only contract: nothing is claimed,
// and a key that DOES already hold a block still resolves to it — a preview
// that reverted every worktree to base ports would describe the wrong stack.

// TestEnvRender_LocalEnvFromNewWorktreeClaimsNothing is the regression test.
//
// Mutation that fails it: pass claimNewBlocks from runEnvRender (the pre-fix
// behaviour) — the full-ceiling render fails, and the empty-registry render
// creates blocks.json.
func TestEnvRender_LocalEnvFromNewWorktreeClaimsNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL; runs in task test")
	}
	kclplugin.Register()

	for _, tc := range []struct {
		name     string
		registry string
	}{
		{name: "ceiling full", registry: fullCeilingRegistry},
		{name: "no registry yet"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetDevStackGlobals(t)
			primary, worktree := portblockRepo(t)
			writePortblockProject(t, worktree, "dev", localDevMainK)
			// The production ceiling: the repo's forge.yaml default.
			devstack.SetMaxStacks(8)

			registryPath := filepath.Join(primary, ".forge", "blocks.json")
			if tc.registry != "" {
				writeRegistryFile(t, registryPath, tc.registry)
			}
			before, beforeErr := os.ReadFile(registryPath)

			stdout, stderr, err := runRenderCapturingProcessStdout(t, worktree, "dev")
			if err != nil {
				t.Fatalf("forge env render dev from a new worktree must succeed, got: %v\nstderr:\n%s", err, stderr)
			}
			assertRegistryUntouched(t, registryPath, before, beforeErr)

			if !strings.Contains(stdout, `value: "3000"`) {
				t.Errorf("an unclaimed key must preview at its base port 3000; stdout:\n%s", stdout)
			}
			if !strings.Contains(stderr, "PREVIEW") || !strings.Contains(stderr, `"rollout-160"`) {
				t.Errorf("an unclaimed key's base-port render must be announced as a PREVIEW on stderr; stderr:\n%s", stderr)
			}
			if strings.Contains(stdout, "PREVIEW") {
				t.Errorf("the PREVIEW notice leaked into stdout, which must be manifests only; stdout:\n%s", stdout)
			}
		})
	}
}

// TestEnvRender_ExistingBlockStillResolves: inspecting is not "base ports for
// everyone". A worktree whose stack is already up renders the ports it was
// given — otherwise `env render` would print manifests for a stack that does
// not exist.
//
// Mutation that fails it: make inspectBlockAllocator return base
// unconditionally.
func TestEnvRender_ExistingBlockStillResolves(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL; runs in task test")
	}
	kclplugin.Register()
	resetDevStackGlobals(t)
	primary, worktree := portblockRepo(t)
	writePortblockProject(t, worktree, "dev", localDevMainK)

	registryPath := filepath.Join(primary, ".forge", "blocks.json")
	writeRegistryFile(t, registryPath, `{"": {"block": 0}, "rollout-160": {"block": 3, "stack": true}}`)
	before, beforeErr := os.ReadFile(registryPath)

	stdout, stderr, err := runRenderCapturingProcessStdout(t, worktree, "dev")
	if err != nil {
		t.Fatalf("render: %v\nstderr:\n%s", err, stderr)
	}
	assertRegistryUntouched(t, registryPath, before, beforeErr)
	if !strings.Contains(stdout, `value: "3300"`) {
		t.Errorf("a key holding block 3 must render base+300 = 3300; stdout:\n%s", stdout)
	}
	if strings.Contains(stderr, "PREVIEW") {
		t.Errorf("a key that holds a block is not a preview; stderr:\n%s", stderr)
	}
}

// TestEnvConfig_RendersThisWorktreesStackWithoutClaiming: `forge env config`
// is the readback of what `forge env up` passes, so from a linked worktree it
// must describe THAT worktree's stack (its block), not the primary's — and,
// like render, claim nothing when the worktree has no block yet.
//
// Mutation that fails it: drop the activateDevStack call from env config — the
// worktree's block is ignored and WEB_PORT reads 3000.
func TestEnvConfig_RendersThisWorktreesStackWithoutClaiming(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL; runs in task test")
	}
	kclplugin.Register()

	for _, tc := range []struct {
		name     string
		registry string
		wantPort string
	}{
		{name: "worktree holds a block", registry: `{"rollout-160": {"block": 2, "stack": true}}`, wantPort: "3200"},
		{name: "worktree has no block, ceiling full", registry: fullCeilingRegistry, wantPort: "3000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetDevStackGlobals(t)
			primary, worktree := portblockRepo(t)
			writePortblockProject(t, worktree, "dev", localDevMainK)
			devstack.SetMaxStacks(8)
			registryPath := filepath.Join(primary, ".forge", "blocks.json")
			writeRegistryFile(t, registryPath, tc.registry)
			before, beforeErr := os.ReadFile(registryPath)

			t.Chdir(worktree)
			cmd := newEnvConfigCmd()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs([]string{"dev", "--workload", "api"})
			cmd.SetContext(context.Background())
			if err := cmd.Execute(); err != nil {
				t.Fatalf("forge env config dev: %v\n%s", err, out.String())
			}
			assertRegistryUntouched(t, registryPath, before, beforeErr)
			if !strings.Contains(out.String(), tc.wantPort) {
				t.Errorf("WEB_PORT should be %s for this worktree's stack; got:\n%s", tc.wantPort, out.String())
			}
		})
	}
}

// TestLaunchStillClaimsPastTheInspectPath pins the other side: the launch
// path keeps claiming. Otherwise `forge env up` from a new worktree would run
// on base ports and collide with the primary's stack.
func TestLaunchStillClaimsPastTheInspectPath(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL; runs in task test")
	}
	kclplugin.Register()
	resetDevStackGlobals(t)
	primary, worktree := portblockRepo(t)
	writePortblockProject(t, worktree, "dev", localDevMainK)

	if got := renderWebPort(t, worktree, "dev", renderToLaunch, claimNewBlocks); got != 3100 {
		t.Errorf("env up from a new worktree must claim block 1 (3100), got %d", got)
	}
	if !readRegistryKeys(t, primary)["rollout-160"] {
		t.Errorf("env up must record the worktree's block")
	}
}

func writeRegistryFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertRegistryUntouched(t *testing.T, path string, before []byte, beforeErr error) {
	t.Helper()
	after, afterErr := os.ReadFile(path)
	switch {
	case os.IsNotExist(beforeErr) && !os.IsNotExist(afterErr):
		t.Errorf("a read-only command CREATED the block registry %s:\n%s", path, after)
	case beforeErr == nil && !bytes.Equal(before, after):
		t.Errorf("a read-only command REWROTE the block registry %s\nbefore:\n%s\nafter:\n%s", path, before, after)
	}
	lock := filepath.Join(filepath.Dir(path), "blocks.lock")
	if os.IsNotExist(beforeErr) {
		if _, err := os.Stat(lock); err == nil {
			t.Errorf("a read-only command created the block lock %s", lock)
		}
	}
}
