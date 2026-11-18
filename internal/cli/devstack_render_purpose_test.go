package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/devstack"
	"github.com/reliant-labs/forge/internal/kclplugin"
)

// A render of an environment that does not run on this machine must not claim
// a port block on this machine.
//
// fp.allocate_port exists for parallel LOCAL dev stacks: base + block(key)*100,
// with the block memoized in the primary checkout's .forge/blocks.json and
// bounded by dev_stack.max_stacks. A port for a cloud env is only meaningful to
// `forge env up <env>` actually launching one of that env's host processes (a
// dev server for prod's SPA). Neither `forge env render` nor `forge env deploy`
// of a GKE-hosted env launches anything here.
//
// Incident: control-plane's prod KCL keys reliant-web's dev-server port on
// "prod-" + option("worktree"). Rendering prod from a linked worktree during the
// v1.6.0 rollout allocated a NEW block for "prod-rollout-160". The registry was
// already at the 8-block ceiling, so the read-only render FAILED with
// "refusing to allocate a NEW port block". Every earlier render from a
// throwaway worktree had leaked one more permanent block the same way, and
// `--fail-on-write` never noticed: the registry lives at the PRIMARY checkout,
// outside the linked worktree the write scan walks.

const cloudProdMainK = `import forge
import forge.workloads as fw
import kcl_plugin.forge as fp

# control-plane's prod pattern, verbatim: "prod" on the primary checkout,
# "prod-<worktree>" in a linked one.
_worktree = option("worktree") or ""
_web_port_key = "prod-" + _worktree if _worktree else "prod"
_web_port = fp.allocate_port(3000, _web_port_key)

_bundle = forge.Bundle {
    project = "portblock"
    cluster_target = forge.ClusterTarget {
        cluster = "gke_example_us-central1_prod"
        namespace = "portblock-prod"
    }
    workloads = [fw.Workload {
        name = "api"
        image = "reg.example.com/portblock"
        env = {WEB_PORT = "${_web_port}"}
        runtime = forge.OnCluster {target = forge.ClusterTarget {cluster = "gke_example_us-central1_prod", namespace = "portblock-prod"}}
    }]
}

output = forge.render(_bundle)
`

// localDevMainK is the same shape on a LOCAL cluster (a k3d context), keyed the
// way a dev env keys its stack: on the bare worktree name.
const localDevMainK = `import forge
import forge.workloads as fw
import kcl_plugin.forge as fp

_key = option("worktree") or ""
_web_port = fp.allocate_port(3000, _key)

_bundle = forge.Bundle {
    project = "portblock"
    cluster_target = forge.ClusterTarget {
        cluster = "k3d-portblock"
        namespace = "portblock-dev"
    }
    workloads = [fw.Workload {
        name = "api"
        image = "reg.example.com/portblock"
        env = {WEB_PORT = "${_web_port}"}
        runtime = forge.OnCluster {target = forge.ClusterTarget {cluster = "k3d-portblock", namespace = "portblock-dev"}}
    }]
}

output = forge.render(_bundle)
`

// fullCeilingRegistry is a registry at the default 8-block ceiling: block 0
// plus blocks 1..7, so the next NEW key would need block 8 and is refused.
const fullCeilingRegistry = `{
  "": {"block": 0},
  "prod": {"block": 1},
  "newtool-5709b18d": {"block": 2},
  "prod-forge-deploy-882e308d": {"block": 3},
  "forge-deploy-882e308d": {"block": 4, "stack": true},
  "prod-cp-obs": {"block": 5},
  "my-new-feature-50f77334": {"block": 6, "stack": true},
  "prod-newtool-5709b18d": {"block": 7}
}
`

func portblockGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

// portblockRepo makes a primary checkout plus a linked worktree named
// "rollout-160" and returns both paths. The forge project lives in whichever
// checkout the caller writes it into; the block registry always lives at the
// PRIMARY, because every worktree of a repo shares one (devstack.RepoAnchor).
func portblockRepo(t *testing.T) (primary, worktree string) {
	t.Helper()
	primary = t.TempDir()
	portblockGit(t, primary, "init", "-q")
	// Local identity only: this machine's git config is shared by concurrent agents.
	portblockGit(t, primary, "config", "user.email", "forge-portblock-test@example.com")
	portblockGit(t, primary, "config", "user.name", "forge portblock test")
	portblockGit(t, primary, "commit", "-q", "--allow-empty", "-m", "init")
	worktree = filepath.Join(t.TempDir(), "rollout-160")
	portblockGit(t, primary, "worktree", "add", "-q", "-b", "rollout-160", worktree)
	return primary, worktree
}

// writePortblockProject lays a minimal forge project into dir with one env.
func writePortblockProject(t *testing.T, dir, env, mainK string) {
	t.Helper()
	files := map[string]string{
		"forge.yaml":                    "name: portblock\nmodule_path: github.com/example/portblock\n",
		"deploy/kcl/kcl.mod":            "[package]\nname = \"portblock-deploy\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n\n[dependencies]\n",
		"deploy/kcl/" + env + "/main.k": mainK,
	}
	for rel, body := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// resetDevStackGlobals returns every process-global the render context arms
// to its unarmed default, so one test's arming cannot leak into the next.
func resetDevStackGlobals(t *testing.T) {
	t.Helper()
	reset := func() {
		kclplugin.UseBlockAllocator(nil)
		kclplugin.UseDevStacks(nil)
		kclplugin.UseFileWriter("")
		_ = kclplugin.SuppressedWrites()
		kclplugin.ResetDefaultResolverForTest()
		devstack.SetActive(devstack.Options{})
		devstack.SetMaxStacks(0)
	}
	reset()
	t.Cleanup(reset)
}

// TestEnvRender_CloudEnvClaimsNoPortBlock is the regression test for the prod
// render that failed at the port-block ceiling.
//
// Mutation that fails it: arm the persistent block allocator unconditionally
// in activateDevStack (the pre-fix behavior) — the full-ceiling case fails the
// render, and the other two create or rewrite blocks.json.
func TestEnvRender_CloudEnvClaimsNoPortBlock(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL; runs in task test")
	}
	kclplugin.Register()

	cases := []struct {
		name string
		// fromWorktree renders from the linked worktree rather than the
		// primary checkout.
		fromWorktree bool
		// registry is the primary's blocks.json before the render; "" means
		// no registry exists yet.
		registry string
	}{
		{name: "linked worktree, ceiling full", fromWorktree: true, registry: fullCeilingRegistry},
		{name: "linked worktree, no registry yet", fromWorktree: true},
		{name: "primary checkout, no registry yet", fromWorktree: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetDevStackGlobals(t)
			primary, worktree := portblockRepo(t)
			projectDir := primary
			if tc.fromWorktree {
				projectDir = worktree
			}
			writePortblockProject(t, projectDir, "prod", cloudProdMainK)

			registryPath := filepath.Join(primary, ".forge", "blocks.json")
			if tc.registry != "" {
				if err := os.MkdirAll(filepath.Dir(registryPath), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(registryPath, []byte(tc.registry), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			before, beforeErr := os.ReadFile(registryPath)

			stdout, stderr, err := runRenderCapturingProcessStdout(t, projectDir, "prod")
			if err != nil {
				t.Fatalf("forge env render prod must succeed for a cloud env, got: %v\nstderr:\n%s", err, stderr)
			}

			after, afterErr := os.ReadFile(registryPath)
			switch {
			case os.IsNotExist(beforeErr) && !os.IsNotExist(afterErr):
				t.Errorf("a cloud render CREATED the block registry %s:\n%s", registryPath, after)
			case beforeErr == nil && !bytes.Equal(before, after):
				t.Errorf("a cloud render REWROTE the block registry %s\nbefore:\n%s\nafter:\n%s", registryPath, before, after)
			}

			// Deterministic: the base port, on every checkout, whatever the
			// registry holds. Neither 3100 ("prod"'s recorded block) nor
			// a newly-offset block.
			if !strings.Contains(stdout, "WEB_PORT") {
				t.Fatalf("the rendered Deployment is missing its WEB_PORT env var; stdout:\n%s", stdout)
			}
			if !strings.Contains(stdout, `value: "3000"`) {
				t.Errorf("a cloud render must resolve allocate_port to its base 3000; stdout:\n%s", stdout)
			}
		})
	}
}

// renderWebPort renders env from projectDir after activating the dev-stack
// context for purpose — the exact sequence `forge env up` / `forge env deploy`
// run — and returns the WEB_PORT the KCL resolved.
func renderWebPort(t *testing.T, projectDir, env string, purpose renderPurpose, claim blockClaim) int {
	t.Helper()
	t.Chdir(projectDir)
	activateDevStack(t.Context(), projectDir, env, purpose, claim)
	entities, err := RenderKCL(t.Context(), projectDir, env)
	if err != nil {
		t.Fatalf("render %s: %v", env, err)
	}
	for _, w := range entities.Workloads {
		for _, ev := range w.EnvVars() {
			if ev.Name != "WEB_PORT" {
				continue
			}
			port, err := strconv.Atoi(ev.Value)
			if err != nil {
				t.Fatalf("WEB_PORT %q is not a port: %v", ev.Value, err)
			}
			return port
		}
	}
	t.Fatalf("no WEB_PORT env var in the rendered %s entities", env)
	return 0
}

// readRegistryKeys returns the keys in the primary checkout's blocks.json.
func readRegistryKeys(t *testing.T, primary string) map[string]bool {
	t.Helper()
	blocks, err := devstack.List(primary)
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	keys := map[string]bool{}
	for _, b := range blocks {
		keys[b.Key] = true
	}
	return keys
}

// TestLocalEnvStillClaimsItsPortBlock is the other half: the fix must not
// quietly disarm the allocator for the env it exists to serve. A linked
// worktree's `forge env up dev` — and `forge env deploy dev`, which must agree
// with it — still claims a stable block keyed on the worktree.
//
// Mutation that fails it: return false from entitiesTargetThisMachine (or
// disarm the allocator for renderDeclaration unconditionally) — deploy's port
// drops back to the base and stops agreeing with up's.
func TestLocalEnvStillClaimsItsPortBlock(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL; runs in task test")
	}
	kclplugin.Register()

	for _, purpose := range []struct {
		name    string
		purpose renderPurpose
	}{
		{"env up (launch)", renderToLaunch},
		{"env deploy (declaration)", renderDeclaration},
	} {
		t.Run(purpose.name, func(t *testing.T) {
			resetDevStackGlobals(t)
			primary, worktree := portblockRepo(t)
			writePortblockProject(t, worktree, "dev", localDevMainK)

			got := renderWebPort(t, worktree, "dev", purpose.purpose, claimNewBlocks)
			if got != 3100 {
				t.Errorf("a linked worktree's dev render must claim block 1 (port 3100), got %d", got)
			}
			if !readRegistryKeys(t, primary)["rollout-160"] {
				t.Errorf("the dev stack's block was not recorded in the primary's blocks.json")
			}
		})
	}
}

// TestLaunchingCloudEnvLocallyStillClaimsABlock pins the one case where a cloud
// env legitimately needs a local port: `forge env up prod --target reliant-web`
// runs prod's SPA on a local dev server, and it must not land on dev's :3000.
func TestLaunchingCloudEnvLocallyStillClaimsABlock(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL; runs in task test")
	}
	kclplugin.Register()
	resetDevStackGlobals(t)
	primary, worktree := portblockRepo(t)
	writePortblockProject(t, worktree, "prod", cloudProdMainK)

	if got := renderWebPort(t, worktree, "prod", renderToLaunch, claimNewBlocks); got != 3100 {
		t.Errorf("`forge env up prod` must claim a block for its local dev server (port 3100), got %d", got)
	}
	if !readRegistryKeys(t, primary)["prod-rollout-160"] {
		t.Errorf("`forge env up prod` from a worktree must record its prod-<worktree> block")
	}
}

// TestEntitiesTargetThisMachine pins the classification itself, which is read
// off the declaration and never off the env's name.
func TestEntitiesTargetThisMachine(t *testing.T) {
	cases := []struct {
		name     string
		contract string
		want     bool
	}{
		{"cloud cluster only", `{"workloads": [{"name": "api", "kind": "service", "runtime": {"type": "cluster", "cluster": "gke_p_us-central1_prod", "namespace": "p"}, "spec": {"kind": "service"}}]}`, false},
		{"firebase frontend only", `{"frontends": [{"name": "web", "path": "web", "deploy": {"type": "firebase"}}]}`, false},
		{"hosted workload", `{"workloads": [{"name": "api", "kind": "service", "runtime": {"type": "hosted"}, "spec": {"kind": "service"}}]}`, false},
		{"nothing declared", `{}`, false},
		{"k3d cluster declared", `{"clusters": [{"name": "cp", "context": "k3d-cp"}]}`, true},
		{"workload on a local context", `{"workloads": [{"name": "api", "kind": "service", "runtime": {"type": "cluster", "cluster": "k3d-demo", "namespace": "demo"}, "spec": {"kind": "service"}}]}`, true},
		{"host process", `{"workloads": [{"name": "api", "kind": "service", "runtime": {"type": "host", "runner": "go-run"}, "build": {"type": "go", "cmd": "./cmd/api"}, "spec": {"kind": "service"}}]}`, true},
		{"compose service", `{"workloads": [{"name": "db", "kind": "service", "runtime": {"type": "compose"}, "spec": {"kind": "service"}}]}`, true},
		{"helm chart into a local cluster", `{"helm_charts": [{"name": "cnpg", "version": "1", "namespace": "x", "cluster": "k3d-cp-daemon"}]}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entities, err := parseKCLEntities([]byte(tc.contract))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got := entitiesTargetThisMachine(entities); got != tc.want {
				t.Errorf("entitiesTargetThisMachine = %v, want %v", got, tc.want)
			}
		})
	}
}
