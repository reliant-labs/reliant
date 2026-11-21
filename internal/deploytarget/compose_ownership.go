package deploytarget

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/devstack"
	"github.com/reliant-labs/forge/pkg/release"
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
//   - Otherwise: refuse, naming the owner, the containers, the exact
//     commands that release them, and — only where one applies — the
//     declaration that would have avoided the clash. The owner is told
//     apart as a worktree of this repo, a separate CLONE of it (same origin,
//     or the same root commit), or an unrelated directory, because each has
//     a different cause and fix: `shared = True` never helps across clones.
//
// There is deliberately no flag to take the containers over. Their owner is
// another checkout's live stack, so releasing them is an act on THAT stack
// (`forge env down` from it, or one `docker rm -f`), which the refusal spells
// out; a per-run override here would be a permanent CLI surface whose only
// effect is to do the same thing implicitly, to a stack the command was not
// pointed at.
//
// A failure to READ the owner does not block the deploy. The `up` that
// follows talks to the same daemon and reports a dead one far more
// precisely than this pre-check could.
func checkComposeOwnership(ctx context.Context, runner commandRunner, env map[string]string, spec *ComposeSpec, svcName, envName string) error {
	here := composeProjectDir(spec)
	if here == "" {
		return nil
	}
	args := append(composeArgs(spec), "ps", "--all", "--format",
		`{{.Service}}	{{.Label "com.docker.compose.project.working_dir"}}	{{.Label "com.docker.compose.project"}}`)
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
			return foreignOwnerError(svcName, envName, here, owner, spec.Shared, classifyOwner(owner.dir, here))
		}
	}
	return nil
}

// composeOwner is one foreign project directory and the services whose
// containers carry it.
type composeOwner struct {
	dir      string
	services []string
	// project is the compose project name the containers carry
	// (com.docker.compose.project); empty when compose did not report it.
	project string
}

// foreignComposeOwners parses `compose ps --format
// '{{.Service}}\t<working_dir>\t<project>'` and returns every owner directory
// that is not `here`, sorted for stable output. A row with no working_dir
// label (a container compose did not create) says nothing about ownership
// and is skipped. The project column is optional.
func foreignComposeOwners(out []byte, here string) []composeOwner {
	byDir := map[string]*composeOwner{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Split(strings.TrimSpace(line), "\t")
		if len(fields) < 2 {
			continue
		}
		service, dir := strings.TrimSpace(fields[0]), strings.TrimSpace(fields[1])
		if dir == "" || sameDirectory(dir, here) {
			continue
		}
		o := byDir[dir]
		if o == nil {
			o = &composeOwner{dir: dir}
			byDir[dir] = o
		}
		o.services = append(o.services, service)
		if len(fields) > 2 && o.project == "" {
			o.project = strings.TrimSpace(fields[2])
		}
	}
	owners := make([]composeOwner, 0, len(byDir))
	for _, o := range byDir {
		sort.Strings(o.services)
		owners = append(owners, *o)
	}
	sort.Slice(owners, func(i, j int) bool { return owners[i].dir < owners[j].dir })
	return owners
}

// ownerKind is what a foreign owner directory is relative to this checkout.
type ownerKind int

const (
	// ownerUnrelated: not a checkout of this repository, as far as git can
	// tell (or not a git checkout at all).
	ownerUnrelated ownerKind = iota
	// ownerWorktree: a checkout of this very clone — a linked worktree, or
	// its primary.
	ownerWorktree
	// ownerClone: a separate clone of the same repository: the same origin,
	// or the same root commit.
	ownerClone
)

// classifyOwner tells a foreign owner directory apart. Swappable so the
// message selection is testable without git repositories.
var classifyOwner = func(ownerDir, here string) ownerKind {
	switch {
	case sameDirectory(devstack.SharedProjectDir(ownerDir), devstack.SharedProjectDir(here)):
		return ownerWorktree
	case sameRepository(ownerDir, here):
		return ownerClone
	}
	return ownerUnrelated
}

// foreignOwnerError is the refusal: what is running, from where, what an
// `up` from here would do to it, the exact commands that release it, and the
// declaration that would avoid the clash where there is one.
func foreignOwnerError(svcName, envName, here string, owner composeOwner, shared bool, kind ownerKind) error {
	var b strings.Builder
	switch kind {
	case ownerClone:
		fmt.Fprintf(&b, "compose %s: refusing to take over containers another CLONE of this repository is running.\n", svcName)
	default:
		fmt.Fprintf(&b, "compose %s: refusing to take over containers another checkout is running.\n", svcName)
	}
	fmt.Fprintf(&b, "  containers %s belong to the compose project %srun from\n    %s\n", strings.Join(owner.services, ", "), quotedOrEmpty(owner.project), owner.dir)
	switch {
	case kind == ownerClone:
		fmt.Fprintf(&b, "  a separate clone of this repository, not a worktree of this one, and this deploy would run it from\n    %s\n", here)
		if shared {
			b.WriteString("  `shared = True` (already declared) shares the stack between the worktrees of ONE clone; each clone\n" +
				"  drives it from its own directory, so the two clones contend for the same containers.\n")
		}
	case shared:
		fmt.Fprintf(&b, "  which is not a checkout of this repository (the stack is already `shared = True`, which claims this\n  repository's checkouts only), and this deploy would run it from\n    %s\n", here)
	default:
		fmt.Fprintf(&b, "  and this deploy (a non-shared compose stack) would run it from\n    %s\n", here)
	}
	b.WriteString("  Compose would RECREATE them with bind mounts resolved against this directory, dropping every\n" +
		"  connection to them and leaving them mounting files from here.\n")
	b.WriteString("  Release them, then re-run:\n")
	env := firstNonEmpty(envName, "<env>")
	fmt.Fprintf(&b, "    - stop that stack from its own checkout:  cd %s && forge env down %s\n", shellQuote(owner.dir), env)
	if owner.project != "" {
		fmt.Fprintf(&b, "    - or remove just these containers (named volumes are kept, so their data survives):\n"+
			"        docker rm -f $(docker ps -aq --filter label=com.docker.compose.project=%s)\n", shellQuote(owner.project))
	}
	if !shared {
		b.WriteString("  Or fix it in the declaration:\n")
		if kind != ownerClone {
			b.WriteString("    - infrastructure every git worktree shares: set `shared = True` on the workload's\n" +
				"      forge.OnCompose — forge then drives it from the repo's primary checkout, from any worktree;\n")
		}
		b.WriteString("    - a stack each checkout runs for itself: give it its own compose project name, e.g.\n" +
			"      `env = {COMPOSE_PROJECT_NAME = \"<name>-<worktree>\"}` on the forge.OnCompose")
	}
	return fmt.Errorf("%s", strings.TrimRight(b.String(), "\n"))
}

func quotedOrEmpty(project string) string {
	if project == "" {
		return ""
	}
	return fmt.Sprintf("%q ", project)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// shellQuote single-quotes s when it holds anything a POSIX shell would
// split or expand, so a printed command pastes as is.
func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-+=:@,", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// sameRepository reports whether two git checkouts are clones of one
// repository: the same canonical origin URL (release.CanonicalRepo, so
// git@host:o/r.git and https://host/o/r agree), or, for clones with no
// origin, a shared root commit.
func sameRepository(a, b string) bool {
	if oa, ob := release.CanonicalRepo(gitQuery(a, "remote", "get-url", "origin")), release.CanonicalRepo(gitQuery(b, "remote", "get-url", "origin")); oa != "" && oa == ob {
		return true
	}
	ra := strings.Fields(gitQuery(a, "rev-list", "--max-parents=0", "HEAD"))
	if len(ra) == 0 {
		return false
	}
	for _, r := range strings.Fields(gitQuery(b, "rev-list", "--max-parents=0", "HEAD")) {
		if slices.Contains(ra, r) {
			return true
		}
	}
	return false
}

// gitQuery runs a read-only git query in dir, "" on any failure.
func gitQuery(dir string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Output() // #nosec G204 -- fixed git subcommands
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
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
