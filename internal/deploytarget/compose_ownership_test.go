package deploytarget

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// The refusal names a different cause, and a different fix, for each kind
// of foreign owner. The incident: a SHARED stack's containers were held by a
// separate clone of the same repo, and the refusal called that directory
// "not a checkout of this repo" and suggested `shared = True` — already
// declared — and a per-checkout project name. What released them was one
// `docker rm -f`, which it did not mention.
func TestForeignOwnerError_MessageSelection(t *testing.T) {
	owner := composeOwner{dir: "/src/control-plane", services: []string{"nats", "postgres"}, project: "control-plane"}
	here := "/src/trio/control-plane"
	release := []string{
		"cd /src/control-plane && forge env down dev",
		"docker rm -f $(docker ps -aq --filter label=com.docker.compose.project=control-plane)",
		"named volumes are kept",
		`compose project "control-plane" run from`,
	}
	for _, tc := range []struct {
		name    string
		shared  bool
		kind    ownerKind
		want    []string
		notWant []string
	}{
		{
			name:    "shared stack held by another clone",
			shared:  true,
			kind:    ownerClone,
			want:    append([]string{"another CLONE of this repository", "a separate clone of this repository", "already declared"}, release...),
			notWant: []string{"set `shared = True`", "COMPOSE_PROJECT_NAME", "not a checkout of this repository"},
		},
		{
			name:    "non-shared stack held by another clone",
			kind:    ownerClone,
			want:    append([]string{"another CLONE of this repository", "COMPOSE_PROJECT_NAME"}, release...),
			notWant: []string{"set `shared = True`"},
		},
		{
			name:    "shared stack held by an unrelated directory",
			shared:  true,
			kind:    ownerUnrelated,
			want:    append([]string{"another checkout is running", "not a checkout of this repository", "already `shared = True`"}, release...),
			notWant: []string{"set `shared = True`", "COMPOSE_PROJECT_NAME", "CLONE"},
		},
		{
			name: "non-shared stack held by a worktree of this clone",
			kind: ownerWorktree,
			want: append([]string{"another checkout is running", "a non-shared compose stack", "set `shared = True`", "COMPOSE_PROJECT_NAME"}, release...),
		},
	} {
		msg := foreignOwnerError("dev-infra", "dev", here, owner, tc.shared, tc.kind).Error()
		for _, w := range tc.want {
			if !strings.Contains(msg, w) {
				t.Errorf("%s: missing %q:\n%s", tc.name, w, msg)
			}
		}
		for _, w := range tc.notWant {
			if strings.Contains(msg, w) {
				t.Errorf("%s: must not say %q:\n%s", tc.name, w, msg)
			}
		}
	}

	// No project label: no docker rm line to give, and a path with a space
	// pastes as one argument.
	msg := foreignOwnerError("dev-infra", "", here, composeOwner{dir: "/src/my repo", services: []string{"nats"}}, true, ownerClone).Error()
	if strings.Contains(msg, "docker rm") {
		t.Errorf("no project name, no docker rm filter:\n%s", msg)
	}
	if !strings.Contains(msg, "cd '/src/my repo' && forge env down <env>") {
		t.Errorf("the env down line must quote the path:\n%s", msg)
	}
}

// The project column is read from compose ps, and an older two-column row
// still parses.
func TestForeignComposeOwners_ReadsTheProject(t *testing.T) {
	out := "nats\t/a\tcontrol-plane\npostgres\t/a\tcontrol-plane\nredis\t/b\n"
	got := foreignComposeOwners([]byte(out), "/here")
	if len(got) != 2 || got[0].project != "control-plane" || strings.Join(got[0].services, ",") != "nats,postgres" || got[1].project != "" {
		t.Errorf("owners = %+v", got)
	}
}

// Deploy wires the classification into the refusal.
func TestCompose_RefusalNamesAnotherClone(t *testing.T) {
	prev := classifyOwner
	t.Cleanup(func() { classifyOwner = prev })
	classifyOwner = func(string, string) ownerKind { return ownerClone }

	primary, owner := t.TempDir(), t.TempDir()
	prefix := "docker compose --project-directory " + primary + " -f " + filepath.Join(primary, "docker-compose.yml")
	spec := sharedSpec(primary)
	spec.EnvFile = ""
	r := &fakeRunner{outputs: map[string]string{prefix + " ps --all --format": "nats\t" + owner + "\tcontrol-plane\n"}}
	group := ServiceGroup{Env: "dev", ProviderID: "compose", Services: []ResolvedService{{Name: "dev-infra", Compose: spec}}}
	err := (ComposeProvider{Runner: r}).Deploy(context.Background(), group)
	if err == nil {
		t.Fatal("Deploy took over another clone's containers")
	}
	for _, want := range []string{"another CLONE", owner, "forge env down dev", "label=com.docker.compose.project=control-plane"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal missing %q:\n%v", want, err)
		}
	}
}

// Two clones of one repo are told apart from a worktree of this clone and
// from an unrelated repo, by real git.
func TestClassifyOwner_RealGit(t *testing.T) {
	testkit.Slow(t, "builds real git repositories, clones and worktrees")
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("git not on PATH (%v): forge requires git (`forge doctor` checks it), and this test classifies real git checkouts", err)
	}
	initRepo := func(dir string) {
		for _, args := range [][]string{
			{"init", "-q"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "t"},
			// The message is unique: two empty commits made in the same
			// second would otherwise share a hash, and so a root commit.
			{"commit", "-q", "--allow-empty", "-m", "init " + dir},
		} {
			gitIn(t, dir, args...)
		}
	}
	primary := t.TempDir()
	initRepo(primary)
	clone := filepath.Join(t.TempDir(), "clone")
	gitIn(t, primary, "clone", "-q", primary, clone)
	wt := filepath.Join(t.TempDir(), "wt")
	gitIn(t, primary, "worktree", "add", "-q", "-b", "feat", wt)
	other := t.TempDir()
	initRepo(other)

	for _, tc := range []struct {
		owner string
		want  ownerKind
	}{
		{clone, ownerClone}, // same root commit (its origin is primary's path, which primary lacks)
		{wt, ownerWorktree}, // a worktree of this clone
		{other, ownerUnrelated},
		{t.TempDir(), ownerUnrelated}, // not a git checkout
	} {
		if got := classifyOwner(tc.owner, primary); got != tc.want {
			t.Errorf("classifyOwner(%s) = %v, want %v", tc.owner, got, tc.want)
		}
	}

	// Same origin URL, spelled two ways, with no common commit.
	a, b := t.TempDir(), t.TempDir()
	initRepo(a)
	initRepo(b)
	gitIn(t, a, "remote", "add", "origin", "git@github.com:acme/app.git")
	gitIn(t, b, "remote", "add", "origin", "https://github.com/acme/app")
	if got := classifyOwner(a, b); got != ownerClone {
		t.Errorf("same canonical origin: classifyOwner = %v, want ownerClone", got)
	}
}
