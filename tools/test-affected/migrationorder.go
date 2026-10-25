// Copyright (c) 2025 Reliant Labs
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// The migration-order check: a migration added on this branch must be
// versioned ABOVE the newest migration on origin/main.
//
// golang-migrate records one current version per database and applies only
// what is above it. A migration whose version sits at or below main's head is
// not pending, it is invisible: on a database that has run the head it is never
// applied, and reliant's own migrator refuses the embedded set outright
// ("embeds migration(s) the database has already passed ... NOTHING WAS
// APPLIED"), so every reliant crash-loops on deploy. reliant#1399 merged
// 20261024024221 under an already-applied 20261024035148 exactly this way.
//
// forge's `forge lint --migration-safety` has the same rule
// (migration-version-below-merged-head) and PR CI runs it. This is the local
// half, and it differs in the one way that matters: it REFRESHES origin/main
// first. #1399's author ran the forge lane against a clone whose origin/main
// predated 20261024035148 and got a pass; main had moved while the PR was in
// review. A check that cannot reach main therefore fails closed instead of
// judging a stale ref.

// defaultLanes is every lane `task test:affected` considers.
const defaultLanes = "go,web,electron,forge-generate,docs-generate,migration-order"

// migrationVersionRe matches a migration filename: <digits>_<stem>.up.sql.
var migrationVersionRe = regexp.MustCompile(`^(\d+)_(.+)\.up\.sql$`)

type orderOptions struct {
	// Root is the repository root; Dir the migrations directory under it.
	Root string
	Dir  string
	// BaseRef is the branch new migrations must clear (origin/main).
	BaseRef string
	// Fetch refreshes BaseRef from its remote before judging.
	Fetch bool
}

type orderViolation struct {
	File     string // the offending file, repo-relative
	Version  uint64
	Stem     string // the migration name, for the remedy
	Head     uint64 // BaseRef's newest version
	HeadFile string
	BaseRef  string
	Dir      string
}

func parseMigration(name string) (version uint64, stem string, ok bool) {
	m := migrationVersionRe.FindStringSubmatch(name)
	if m == nil {
		return 0, "", false
	}
	v, err := strconv.ParseUint(m[1], 10, 64)
	if err != nil {
		return 0, "", false
	}
	return v, m[2], true
}

func gitIn(root string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	// An inherited GIT_DIR (a hook) would answer about the wrong repository.
	cmd.Env = withoutGitEnv(os.Environ())
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

func withoutGitEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		switch {
		case strings.HasPrefix(e, "GIT_DIR="), strings.HasPrefix(e, "GIT_WORK_TREE="), strings.HasPrefix(e, "GIT_INDEX_FILE="):
		default:
			out = append(out, e)
		}
	}
	return out
}

// checkMigrationOrder returns every migration in opts.Dir that is not on
// BaseRef and whose version is at or below BaseRef's newest. An error means
// the order could not be judged, which callers must treat as a failure.
func checkMigrationOrder(opts orderOptions) ([]orderViolation, error) {
	remote, branch, hasRemote := strings.Cut(opts.BaseRef, "/")
	if opts.Fetch {
		if !hasRemote {
			return nil, fmt.Errorf("cannot fetch %q: it names no remote (want origin/main). Set MIGRATION_ORDER_OFFLINE=1 to judge the local ref as it is", opts.BaseRef)
		}
		refspec := "+refs/heads/" + branch + ":refs/remotes/" + remote + "/" + branch
		if _, err := gitIn(opts.Root, "fetch", "-q", remote, refspec); err != nil {
			return nil, fmt.Errorf("cannot refresh %s, so a migration's order against main's newest cannot be judged: %w\nFix the network or remote and re-run; MIGRATION_ORDER_OFFLINE=1 judges the possibly stale local ref instead (never in a merge gate)", opts.BaseRef, err)
		}
	}
	if _, err := gitIn(opts.Root, "rev-parse", "--verify", "--quiet", opts.BaseRef+"^{commit}"); err != nil {
		return nil, fmt.Errorf("no %s to compare against: fetch it (git fetch origin main) or run from a clone of the repository", opts.BaseRef)
	}

	dir := filepath.ToSlash(opts.Dir)
	// A directory that did not exist on main has no head: every migration is
	// new and none can be below it.
	listing, lsErr := gitIn(opts.Root, "ls-tree", "--name-only", opts.BaseRef+":"+dir)
	onBase := map[string]bool{}
	var head uint64
	var headFile string
	if lsErr == nil {
		for _, name := range strings.Split(listing, "\n") {
			name = strings.TrimSpace(name)
			v, _, ok := parseMigration(name)
			if !ok {
				continue
			}
			onBase[name] = true
			if v >= head {
				head, headFile = v, name
			}
		}
	}

	// "Added on this branch" means absent from the merge-base too. A branch
	// that is merely behind main still carries files main has since renamed
	// or removed; the branch did not add them and a squash merge will not
	// bring them back.
	atMergeBase := map[string]bool{}
	if mb, err := gitIn(opts.Root, "merge-base", "HEAD", opts.BaseRef); err == nil {
		if listing, err := gitIn(opts.Root, "ls-tree", "--name-only", strings.TrimSpace(mb)+":"+dir); err == nil {
			for _, name := range strings.Split(listing, "\n") {
				atMergeBase[strings.TrimSpace(name)] = true
			}
		}
	}

	entries, err := os.ReadDir(filepath.Join(opts.Root, opts.Dir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []orderViolation
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		v, stem, ok := parseMigration(e.Name())
		if !ok {
			continue
		}
		// Merged history is below the head by definition and cannot be
		// renamed: only a file that neither main nor the merge-base carries,
		// by name, is a candidate. The same version under another name is a
		// collision, as unapplyable as a lower one.
		if onBase[e.Name()] || atMergeBase[e.Name()] || v > head {
			continue
		}
		out = append(out, orderViolation{
			File: dir + "/" + e.Name(), Version: v, Stem: stem,
			Head: head, HeadFile: headFile, BaseRef: opts.BaseRef, Dir: dir,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out, nil
}

func formatOrderViolations(w io.Writer, vs []orderViolation) {
	for _, v := range vs {
		fmt.Fprintf(w, "migration-order: %s\n", v.File)
		fmt.Fprintf(w, "  its version %d is at or below %s's newest migration %d (%s).\n", v.Version, v.BaseRef, v.Head, v.HeadFile)
		fmt.Fprintf(w, "  golang-migrate applies only versions above the one a database has recorded, so on any database that has run %d this migration is never applied, and reliant's migrator refuses to start on it (a crash loop on deploy).\n", v.Head)
		fmt.Fprintf(w, "  It has not been applied anywhere, so re-create it above main's head:\n")
		fmt.Fprintf(w, "    git fetch origin main && git merge origin/main\n")
		fmt.Fprintf(w, "    git rm %s        # keep a copy of its SQL\n", v.File)
		fmt.Fprintf(w, "    task db:migration NAME=%s   # then paste the SQL back into the new file\n", v.Stem)
		fmt.Fprintf(w, "  (or re-version it in place: forge db migration rebase %s)\n", v.File)
	}
}

// runMigrationOrder is the `migration-order` subcommand. Exit 0: in order;
// 1: a migration is below main's head, or the order could not be judged.
func runMigrationOrder(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("migration-order", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", ".", "repository root")
	dir := fs.String("dir", "db/migrations", "migrations directory, relative to the root")
	base := fs.String("base", "origin/main", "branch a new migration must be versioned above")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	abs, err := filepath.Abs(*root)
	if err != nil {
		fmt.Fprintln(stderr, "migration-order:", err)
		return 2
	}
	offline := os.Getenv("MIGRATION_ORDER_OFFLINE") == "1"
	if offline {
		fmt.Fprintf(stderr, "migration-order: MIGRATION_ORDER_OFFLINE=1: judging the local %s without refreshing it\n", *base)
	}
	vs, err := checkMigrationOrder(orderOptions{Root: abs, Dir: *dir, BaseRef: *base, Fetch: !offline})
	if err != nil {
		fmt.Fprintln(stderr, "migration-order:", err)
		return 1
	}
	if len(vs) > 0 {
		formatOrderViolations(stderr, vs)
		return 1
	}
	fmt.Fprintf(stdout, "migration-order: every migration on this branch is above %s's newest\n", *base)
	return 0
}
