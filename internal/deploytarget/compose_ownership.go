package deploytarget

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/devstack"
)

// checkComposeOwnership refuses to let this checkout take over a compose
// project whose containers another live checkout is running.
//
// # Why a compose `up` can silently take over someone else's stack
//
// Compose names a project after the basename of its project directory and
// resolves every RELATIVE bind mount against that directory. Two git
// worktrees of one repo are routinely both called `<repo>` — the nested
// layout puts each at `<container>/<repo>` — so they select the SAME
// project while resolving DIFFERENT mount sources. Compose sees a changed
// config hash and RECREATES every container with a relative mount: each
// stack using them drops its connections, and the recreated containers now
// mount files inside whichever worktree ran last. Delete that worktree and
// the shared NATS loses its config file.
//
// Nothing in compose itself notices. The ownership signal is the label it
// stamps on every container, `com.docker.compose.project.working_dir`, which
// is exactly the directory this check compares.
//
// # What it does
//
//   - Owner is this run's project directory, or the label is empty: proceed.
//   - Owner directory no longer exists: ADOPT. The checkout that created the
//     containers is gone, so nothing is running from it; recreating them
//     here is the repair, not the takeover.
//   - Owner is another checkout of the SAME repo and the stack is `shared`:
//     proceed, loudly. A shared stack's owner is DECLARED to be the primary
//     checkout, and this is the convergence back to it — exactly once.
//   - Otherwise: refuse, naming the owner, the containers and both fixes.
//
// A failure to READ the owner does not block the deploy. The `up` that
// follows talks to the same daemon and reports a dead one far more
// precisely than this pre-check could.
func checkComposeOwnership(ctx context.Context, runner commandRunner, env map[string]string, spec *ComposeSpec, svcName string) error {
	here := composeProjectDir(spec)
	if here == "" {
		return nil
	}
	args := append(composeArgs(spec), "ps", "--all", "--format",
		`{{.Service}}	{{.Label "com.docker.compose.project.working_dir"}}`)
	out, err := outputWithEnv(ctx, runner, env, "docker", args...)
	if err != nil {
		fmt.Printf("  note: could not read which checkout owns compose project for %s (%v); continuing\n", svcName, err)
		return nil
	}

	for _, owner := range foreignComposeOwners(out, here) {
		switch {
		case !dirExists(owner.dir):
			fmt.Printf("  adopting compose containers %s: they were created from %s, which no longer exists\n",
				strings.Join(owner.services, ", "), owner.dir)
		case spec.Shared && sameRepoCheckout(owner.dir, here):
			fmt.Printf("  SHARED compose stack: containers %s were created from %s, not from the primary checkout %s.\n"+
				"  Recreating any whose resolved config differs, so the stack is owned by the primary checkout again;\n"+
				"  every dev stack using them reconnects once.\n",
				strings.Join(owner.services, ", "), owner.dir, here)
		default:
			return foreignOwnerError(svcName, here, owner, spec.Shared)
		}
	}
	return nil
}

// composeOwner is one foreign project directory and the services whose
// containers carry it.
type composeOwner struct {
	dir      string
	services []string
}

// foreignComposeOwners parses `compose ps --format '{{.Service}}\t<working_dir>'`
// and returns every owner directory that is not `here`, sorted for stable
// output. A row with no working_dir label (a container compose did not
// create) says nothing about ownership and is skipped.
func foreignComposeOwners(out []byte, here string) []composeOwner {
	byDir := map[string][]string{}
	for _, line := range strings.Split(string(out), "\n") {
		service, dir, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok {
			continue
		}
		service, dir = strings.TrimSpace(service), strings.TrimSpace(dir)
		if dir == "" || sameDirectory(dir, here) {
			continue
		}
		byDir[dir] = append(byDir[dir], service)
	}
	owners := make([]composeOwner, 0, len(byDir))
	for dir, services := range byDir {
		sort.Strings(services)
		owners = append(owners, composeOwner{dir: dir, services: services})
	}
	sort.Slice(owners, func(i, j int) bool { return owners[i].dir < owners[j].dir })
	return owners
}

// foreignOwnerError is the refusal: what is running, from where, what an
// `up` from here would do to it, and the two declarations that fix it.
func foreignOwnerError(svcName, here string, owner composeOwner, shared bool) error {
	why := "a non-shared compose stack"
	if shared {
		why = "a SHARED compose stack, but that directory is not a checkout of this repo"
	}
	return fmt.Errorf("compose %s: refusing to take over containers another checkout is running.\n"+
		"  containers %s belong to the compose project run from\n"+
		"    %s\n"+
		"  and this deploy (%s) would run it from\n"+
		"    %s\n"+
		"  Compose would RECREATE them with bind mounts resolved against this directory, dropping every\n"+
		"  connection to them and leaving them mounting files from here.\n"+
		"  Fix it in the declaration:\n"+
		"    - infrastructure every git worktree shares: set `shared = True` on the workload's\n"+
		"      forge.OnCompose — forge then drives it from the repo's primary checkout, from any worktree;\n"+
		"    - a stack each checkout runs for itself: give it its own compose project name, e.g.\n"+
		"      `env = {COMPOSE_PROJECT_NAME = \"<name>-<worktree>\"}` on the forge.OnCompose",
		svcName, strings.Join(owner.services, ", "), owner.dir, why, here)
}

// sameRepoCheckout reports whether dir is a checkout of the same repo whose
// primary-checkout project directory is primaryProjectDir — i.e. a linked
// worktree (or the primary itself) rather than an unrelated clone that
// happens to share a basename.
func sameRepoCheckout(dir, primaryProjectDir string) bool {
	return sameDirectory(devstack.SharedProjectDir(dir), primaryProjectDir)
}

func dirExists(dir string) bool {
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}

// sameDirectory compares two paths tolerantly of symlinks (macOS /var vs
// /private/var) and trailing separators.
func sameDirectory(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, ea := filepath.EvalSymlinks(a)
	rb, eb := filepath.EvalSymlinks(b)
	return ea == nil && eb == nil && filepath.Clean(ra) == filepath.Clean(rb)
}
