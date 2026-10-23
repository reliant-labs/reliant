package deploytarget

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A SHARED compose stack (OnCompose.shared) is driven from ONE directory —
// the primary checkout — whichever worktree runs forge. These tests pin the
// argv that makes that true, and the ownership guard that refuses the
// takeover a non-shared stack used to perform silently.

func sharedSpec(projectDir string) *ComposeSpec {
	return &ComposeSpec{
		ComposeFile:      "docker-compose.yml",
		Service:          "dev-infra",
		EnvFile:          ".env.compose",
		ProjectDirectory: projectDir,
		Shared:           true,
	}
}

// Every compose subcommand selects the project the same way: by the pinned
// project directory, with the file and env_file resolved THERE. A relative
// `-f` would be resolved by compose against the process cwd — the worktree —
// and read that checkout's file while mounting the primary's.
func TestCompose_SharedStackRunsFromItsProjectDirectory(t *testing.T) {
	primary := t.TempDir()
	if err := os.WriteFile(filepath.Join(primary, ".env.compose"), []byte("FROM_PRIMARY=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{outputs: map[string]string{
		"docker compose --project-directory " + primary + " -f " + filepath.Join(primary, "docker-compose.yml") + " ps --status": "dev-infra-1  Up\n",
	}}
	group := ServiceGroup{Env: "dev", ProviderID: "compose", Services: []ResolvedService{
		{Name: "dev-infra", Compose: sharedSpec(primary)},
	}}
	if err := (ComposeProvider{ProjectDir: t.TempDir(), Runner: r}).Deploy(context.Background(), group); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	prefix := "docker compose --project-directory " + primary + " -f " + filepath.Join(primary, "docker-compose.yml")
	var sawUp bool
	for i, call := range r.calls {
		if call == "docker compose version --short" {
			continue
		}
		if !strings.HasPrefix(call, prefix) {
			t.Errorf("call %q does not select the shared project by %q", call, prefix)
		}
		if strings.Contains(call, " up -d") {
			sawUp = true
			if !strings.Contains(call, "--env-file "+filepath.Join(primary, ".env.compose")) {
				t.Errorf("up %q: env_file must resolve against the project directory", call)
			}
			if r.envCalls[i]["FROM_PRIMARY"] != "1" {
				t.Errorf("up ran without the primary's env_file overlay: %v", r.envCalls[i])
			}
		}
	}
	if !sawUp {
		t.Fatalf("no up call recorded: %v", r.calls)
	}
}

// Unpinned specs keep today's argv exactly.
func TestCompose_UnpinnedStackKeepsItsArgv(t *testing.T) {
	spec := &ComposeSpec{ComposeFile: "docker-compose.yml"}
	if got := strings.Join(composeArgs(spec), " "); got != "compose -f docker-compose.yml" {
		t.Errorf("composeArgs = %q, want the historical `compose -f docker-compose.yml`", got)
	}
	if got := composeEnvFile(&ComposeSpec{EnvFile: ".env.dev"}); got != ".env.dev" {
		t.Errorf("composeEnvFile = %q, want it untouched", got)
	}
}

// The incident: a linked worktree's non-shared deploy found the stack's
// containers owned by the primary checkout. It must be REFUSED before pull or
// up can recreate anything, and the refusal must name both fixes.
func TestCompose_RefusesToTakeOverAnotherCheckoutsContainers(t *testing.T) {
	owner := t.TempDir() // a live checkout
	here := t.TempDir()
	r := &fakeRunner{outputs: map[string]string{
		"docker compose -f " + filepath.Join(here, "docker-compose.yml") + " ps --all --format": "postgres\t" + owner + "\nnats\t" + owner + "\n",
	}}
	group := ServiceGroup{Env: "dev", ProviderID: "compose", Services: []ResolvedService{
		{Name: "dev-infra", Compose: &ComposeSpec{ComposeFile: filepath.Join(here, "docker-compose.yml"), Service: "dev-infra"}},
	}}
	err := (ComposeProvider{Runner: r}).Deploy(context.Background(), group)
	if err == nil {
		t.Fatalf("Deploy succeeded; it must refuse to recreate containers %s is running", owner)
	}
	for _, want := range []string{owner, "nats, postgres", "shared = True", "COMPOSE_PROJECT_NAME"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q:\n%v", want, err)
		}
	}
	for _, call := range r.calls {
		if strings.Contains(call, " pull ") || strings.Contains(call, " up ") {
			t.Errorf("ran %q after detecting a foreign owner — the guard must act BEFORE compose can recreate", call)
		}
	}
}

// Containers whose owner checkout has been deleted are adopted: nothing runs
// from a directory that no longer exists, and refusing would leave the stack
// unrecoverable without hand surgery.
func TestCompose_AdoptsContainersWhoseOwnerIsGone(t *testing.T) {
	here := t.TempDir()
	gone := filepath.Join(t.TempDir(), "deleted-worktree")
	file := filepath.Join(here, "docker-compose.yml")
	r := &fakeRunner{outputs: map[string]string{
		"docker compose -f " + file + " ps --all --format": "nats\t" + gone + "\n",
		"docker compose -f " + file + " ps --status":       "nats-1  Up\n",
	}}
	group := ServiceGroup{Env: "dev", ProviderID: "compose", Services: []ResolvedService{
		{Name: "nats", Compose: &ComposeSpec{ComposeFile: file}},
	}}
	if err := (ComposeProvider{ProjectDir: t.TempDir(), Runner: r}).Deploy(context.Background(), group); err != nil {
		t.Fatalf("Deploy refused containers whose owner no longer exists: %v", err)
	}
}

// Our own containers, and a daemon we cannot read, never block the deploy.
func TestCompose_OwnContainersAndUnreadableOwnerProceed(t *testing.T) {
	here := t.TempDir()
	file := filepath.Join(here, "docker-compose.yml")
	for name, r := range map[string]*fakeRunner{
		"own": {outputs: map[string]string{
			"docker compose -f " + file + " ps --all --format": "nats\t" + here + "\n",
			"docker compose -f " + file + " ps --status":       "nats-1  Up\n",
		}},
		"unreadable": {
			runErrs: map[string]error{"docker compose -f " + file + " ps --all --format": errKubectlExit1},
			outputs: map[string]string{"docker compose -f " + file + " ps --status": "nats-1  Up\n"},
		},
	} {
		group := ServiceGroup{Env: "dev", ProviderID: "compose", Services: []ResolvedService{
			{Name: "nats", Compose: &ComposeSpec{ComposeFile: file}},
		}}
		if err := (ComposeProvider{ProjectDir: t.TempDir(), Runner: r}).Deploy(context.Background(), group); err != nil {
			t.Errorf("%s: Deploy: %v", name, err)
		}
	}
}

// A SHARED stack whose containers were taken over by a linked worktree (the
// state the incident left behind) converges back to the primary: proceed,
// because the declared owner IS the primary and this run is driving from it.
func TestCompose_SharedStackReclaimsFromALinkedWorktree(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	primary := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "t"},
		{"commit", "-q", "--allow-empty", "-m", "init"},
	} {
		gitIn(t, primary, args...)
	}
	wt := filepath.Join(t.TempDir(), "chat-scroll", filepath.Base(primary))
	gitIn(t, primary, "worktree", "add", "-q", "-b", "feat", wt)

	spec := sharedSpec(primary)
	spec.EnvFile = ""
	prefix := "docker compose --project-directory " + primary + " -f " + filepath.Join(primary, "docker-compose.yml")
	r := &fakeRunner{outputs: map[string]string{
		prefix + " ps --all --format": "nats\t" + wt + "\npostgres\t" + wt + "\ntemporal\t" + primary + "\n",
		prefix + " ps --status":       "dev-infra-1  Up\n",
	}}
	group := ServiceGroup{Env: "dev", ProviderID: "compose", Services: []ResolvedService{{Name: "dev-infra", Compose: spec}}}
	if err := (ComposeProvider{ProjectDir: wt, Runner: r}).Deploy(context.Background(), group); err != nil {
		t.Fatalf("a shared stack must converge back to the primary from a linked worktree's takeover: %v", err)
	}

	// The same containers held by an UNRELATED directory are still refused,
	// shared or not: `shared` claims this repo's checkouts, not anyone's.
	stranger := t.TempDir()
	r2 := &fakeRunner{outputs: map[string]string{prefix + " ps --all --format": "nats\t" + stranger + "\n"}}
	group2 := ServiceGroup{Env: "dev", ProviderID: "compose", Services: []ResolvedService{{Name: "dev-infra", Compose: sharedSpec(primary)}}}
	if err := (ComposeProvider{Runner: r2}).Deploy(context.Background(), group2); err == nil {
		t.Errorf("a shared stack took over containers owned by %s, which is not a checkout of this repo", stranger)
	}
}

// Shared and per-checkout runs of the same file are separate groups: they
// are separate compose projects' worth of mount sources.
func TestGroupServices_ProjectDirectorySplitsComposeGroups(t *testing.T) {
	groups, err := GroupServices("dev", []RawService{
		{Name: "a", Compose: &ComposeSpec{ComposeFile: "docker-compose.yml"}},
		{Name: "b", Compose: &ComposeSpec{ComposeFile: "docker-compose.yml", ProjectDirectory: "/primary", Shared: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 {
		t.Fatalf("want 2 compose groups, got %d", len(groups))
	}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
