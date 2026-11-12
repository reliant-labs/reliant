// File: internal/cli/new_next_steps_test.go
//
// The paste-ability contract for the `forge project new` next-steps block
// (newNextSteps in new.go): every forge command it prints must be a command
// that RESOLVES in the assembled command tree, with an argument list the
// target command accepts. The block is the first thing a new user reads, so a
// line that names a command the binary no longer has (the `forge project add
// entity` → `forge scaffold entity` rename is exactly that class of defect)
// is a broken first ten minutes.
//
// The check is deliberately not a regex over the strings: it resolves the
// tokens through cobra, so it fails the moment the command surface moves
// underneath the text.

package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/config"
)

// pasteableInvocations extracts every forge command a next-steps line offers
// for pasting: each run of tokens starting at the forge invocation prefix
// (Name(), which is "forge" standalone and "<host> forge" when embedded) and
// running to the first token that carries sentence punctuation. Backticks are
// treated as whitespace so an inline `forge env up` reads as a command.
//
// The returned slices are the tokens AFTER the invocation prefix — i.e. what
// the root command would receive as os.Args[1:].
func pasteableInvocations(line, name string) [][]string {
	fields := strings.Fields(strings.ReplaceAll(line, "`", " "))
	prefix := strings.Fields(name)
	var out [][]string
	for i := 0; i+len(prefix) <= len(fields); i++ {
		match := true
		for j, want := range prefix {
			if fields[i+j] != want {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		var args []string
		for _, tok := range fields[i+len(prefix):] {
			trimmed := strings.TrimRight(tok, ".,;")
			if trimmed != "" {
				args = append(args, trimmed)
			}
			if trimmed != tok {
				break // sentence punctuation ends the command
			}
		}
		out = append(out, args)
		i += len(prefix) - 1
	}
	return out
}

// resolvePasteable runs one extracted invocation through the real command
// tree. It returns the resolved command path (e.g. "forge scaffold entity")
// so callers can pin WHICH command the block names, not just that it exists.
func resolvePasteable(t *testing.T, args []string) string {
	t.Helper()
	root := newTestRootCmd(t)
	resolved, rest, err := root.Find(args)
	if err != nil {
		t.Errorf("next-steps command %q does not resolve: %v", strings.Join(args, " "), err)
		return ""
	}
	if resolved == root {
		t.Errorf("next-steps command %q matched no subcommand — it would print root usage", strings.Join(args, " "))
		return ""
	}
	if !resolved.Runnable() {
		t.Errorf("next-steps command %q resolves to %q, which is a group with no RunE — pasting it prints usage",
			strings.Join(args, " "), resolved.CommandPath())
		return ""
	}
	// --help is always accepted; skip the arity check for those so a
	// "see --help" pointer isn't judged as a real invocation.
	help := false
	for _, a := range rest {
		if a == "--help" || a == "-h" {
			help = true
		}
	}
	resolved.InitDefaultHelpFlag()
	if err := resolved.ParseFlags(rest); err != nil {
		t.Errorf("next-steps command %q has a flag %q does not accept: %v",
			strings.Join(args, " "), resolved.CommandPath(), err)
		return strings.TrimPrefix(resolved.CommandPath(), root.Name()+" ")
	}
	if !help {
		if err := resolved.ValidateArgs(resolved.Flags().Args()); err != nil {
			t.Errorf("next-steps command %q fails %q's own arg validation: %v",
				strings.Join(args, " "), resolved.CommandPath(), err)
		}
	}
	return strings.TrimPrefix(resolved.CommandPath(), root.Name()+" ")
}

// TestNewNextStepsArePasteable is the contract new.go's printNewNextSteps
// comment claims: every forge command the post-`project new` block prints
// resolves in the assembled tree, accepts the flags shown, and satisfies its
// own Args validator. It also pins that no line carries an unsubstituted
// placeholder — the block promises copy-paste, not fill-in-the-blank.
func TestNewNextStepsArePasteable(t *testing.T) {
	n := Name()

	cases := []struct {
		name        string
		kind        string
		inPlace     bool
		services    []string
		hasFrontend bool
		// wantCmds are command paths (cobra CommandPath minus the root
		// prefix) the block MUST name. Empty means "no forge commands
		// expected" (the CLI / library skeletons are prose-only).
		wantCmds []string
	}{
		{
			name:     "service kind, no services yet",
			kind:     config.ProjectKindService,
			services: nil,
			wantCmds: []string{"scaffold service", "env up"},
		},
		{
			// A frontend-first project (a static site, or a SPA over an API
			// that lives elsewhere): the frontend's dev loop, the static
			// deploy path, and — one command away — a service.
			name:        "service kind, frontends but no services",
			kind:        config.ProjectKindService,
			services:    nil,
			hasFrontend: true,
			wantCmds:    []string{"skill load", "scaffold service"},
		},
		{
			// An entity is declared in the proto, so the block names the
			// bare sweep (`forge scaffold`) rather than a per-entity
			// command with a field list.
			name:     "service kind, one service",
			kind:     config.ProjectKindService,
			services: []string{"catalog"},
			wantCmds: []string{"scaffold", "env up", "project annotations"},
		},
		{
			name:     "service kind, two services (the sweep needs --service)",
			kind:     config.ProjectKindService,
			services: []string{"catalog", "orders"},
			wantCmds: []string{"scaffold", "env up", "project annotations"},
		},
		{
			name:     "cli kind",
			kind:     config.ProjectKindCLI,
			wantCmds: nil,
		},
		{
			name:     "library kind",
			kind:     config.ProjectKindLibrary,
			wantCmds: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines := newNextSteps("demo", tc.inPlace, tc.kind, tc.services, tc.hasFrontend)
			if len(lines) == 0 {
				t.Fatal("newNextSteps returned nothing")
			}

			seen := map[string]bool{}
			for _, line := range lines {
				// No fill-in-the-blank: the block substitutes every
				// name before printing.
				for _, ph := range []string{"<", ">", "%s", "%q", "%d", "${"} {
					if strings.Contains(line, ph) {
						t.Errorf("line %q carries the unsubstituted placeholder %q", line, ph)
					}
				}
				for _, args := range pasteableInvocations(line, n) {
					if len(args) == 0 {
						t.Errorf("line %q names %q with no subcommand", line, n)
						continue
					}
					if path := resolvePasteable(t, args); path != "" {
						seen[path] = true
					}
				}
			}

			for _, want := range tc.wantCmds {
				if !seen[want] {
					t.Errorf("next-steps block never names %q; it named %v", want, keysOf(seen))
				}
			}
			if len(tc.wantCmds) == 0 && len(seen) > 0 {
				t.Errorf("expected a prose-only block, but it named forge commands %v", keysOf(seen))
			}
		})
	}
}

// TestNewNextStepsResolveHelper guards the helper itself: a bogus command
// must be reported, so a future rename cannot pass the suite by silently
// extracting nothing.
func TestNewNextStepsResolveHelper(t *testing.T) {
	got := pasteableInvocations("  forge scaffold entity item --from-proto catalog", "forge")
	want := []string{"scaffold", "entity", "item", "--from-proto", "catalog"}
	if len(got) != 1 || strings.Join(got[0], " ") != strings.Join(want, " ") {
		t.Fatalf("pasteableInvocations = %v, want one run %v", got, want)
	}

	// Inline + sentence-terminated forms both parse.
	got = pasteableInvocations("  then rerun `forge env up`. Field types: forge scaffold entity --help", "forge")
	if len(got) != 2 {
		t.Fatalf("expected two invocations from a two-command line, got %v", got)
	}
	if strings.Join(got[0], " ") != "env up" {
		t.Errorf("first invocation = %v, want [env up]", got[0])
	}
	if strings.Join(got[1], " ") != "scaffold entity --help" {
		t.Errorf("second invocation = %v, want [scaffold entity --help]", got[1])
	}

	// A command that does not exist must be caught by the resolver.
	root := newTestRootCmd(t)
	if _, _, err := root.Find([]string{"project", "add", "entity"}); err == nil {
		var found *cobra.Command
		for _, c := range root.Commands() {
			if c.Name() == "project" {
				found = c
			}
		}
		if found != nil {
			for _, c := range found.Commands() {
				if c.Name() == "add" {
					t.Fatal("`forge project add` still exists — the rename to `forge scaffold` is incomplete")
				}
			}
		}
	}
}

// A project created with frontends and no services is a frontend-first
// project — a landing page, a marketing or docs site, a SPA over an API that
// lives elsewhere. Its first step is its frontend, not a Go service: telling
// it to `scaffold service item` is how an agent ends up building a CRUD
// backend for a site that has no data. The service path stays one command
// away, so a frontend-first project that later grows an API is not stranded.
func TestNewNextSteps_FrontendFirstProjectStartsWithItsFrontend(t *testing.T) {
	lines := newNextSteps("demo", false, config.ProjectKindService, nil, true)
	block := strings.Join(lines, "\n")

	// `env up dev` is the dev loop, and it works on a fresh frontend-only
	// scaffold now that an env with nothing bound to its cluster needs none.
	for _, want := range []string{"frontends/", "env up dev", "deploy/static-site"} {
		if !strings.Contains(block, want) {
			t.Errorf("frontend-first next steps must mention %q:\n%s", want, block)
		}
	}
	// A static site and an API-backed app split here; both must be reachable,
	// and the frontend comes FIRST — the service is the fallback, not the lead.
	frontendAt := strings.Index(block, "frontends/")
	serviceAt := strings.Index(block, "scaffold service")
	if serviceAt < 0 {
		t.Errorf("frontend-first next steps must still name how to add an API (`scaffold service`):\n%s", block)
	} else if frontendAt < 0 || serviceAt < frontendAt {
		t.Errorf("a project with frontends and no services must lead with its frontend, not the service step:\n%s", block)
	}

	// The no-frontend, no-service block is unchanged: a bare service project
	// still starts with its first domain-entity service.
	bare := strings.Join(newNextSteps("demo", false, config.ProjectKindService, nil, false), "\n")
	if !strings.Contains(bare, "DOMAIN ENTITY") {
		t.Errorf("a bare service project must still lead with the domain-entity service step:\n%s", bare)
	}
}

// A scaffolded frontend means auth is live and fail-closed: every RPC 401s
// until an identity provider is wired. `forge env up` does that wiring and
// prints the credentials, but a reader who stops at this block reads the
// first 401 as a broken scaffold. So the block has to say it, and only when
// there is a frontend to sign in to.
func TestNewNextSteps_MentionsSignInOnlyWithAFrontend(t *testing.T) {
	withFE := strings.Join(newNextSteps("demo", false, config.ProjectKindService, []string{"item"}, true), "\n")
	if !strings.Contains(withFE, "sign-in") {
		t.Errorf("a project WITH a frontend must name the sign-in it was scaffolded:\n%s", withFE)
	}
	noFE := strings.Join(newNextSteps("demo", false, config.ProjectKindService, []string{"item"}, false), "\n")
	if strings.Contains(noFE, "sign-in") {
		t.Errorf("a project with NO frontend has nothing to sign in to:\n%s", noFE)
	}
}

// The README's first command is `forge project new my-app`, with no --mod.
// --mod was REQUIRED, so that command failed before writing anything. It
// must default to a path forge.yaml's module_path check accepts, and an
// explicit --mod must still win.
func TestProjectNew_ModulePathDefaultsToProjectName(t *testing.T) {
	cmd := newNewCmd()
	if ann := cmd.Flags().Lookup("mod").Annotations[cobra.BashCompOneRequiredFlag]; len(ann) > 0 {
		t.Fatal("--mod is still a required flag: `forge project new my-app` (the README's first command) fails")
	}
	got, defaulted := defaultModulePath("", "my-app")
	if got != "example.com/my-app" || !defaulted {
		t.Errorf("no --mod: got %q (defaulted=%v), want example.com/my-app", got, defaulted)
	}
	// The scaffold validates forge.yaml as it writes CI; a default the
	// validator rejects fails `project new` just as surely as a required
	// flag. (A bare `my-app` did exactly that.)
	yaml := "name: my-app\nmodule_path: " + got + "\n"
	if _, err := config.LoadProject([]byte(yaml), "forge.yaml"); err != nil && strings.Contains(err.Error(), "module_path") {
		t.Errorf("default %q is refused by forge.yaml's module_path check: %v", got, err)
	}
	if got, defaulted := defaultModulePath(" github.com/acme/my-app ", "my-app"); got != "github.com/acme/my-app" || defaulted {
		t.Errorf("explicit --mod: got %q (defaulted=%v), want it verbatim", got, defaulted)
	}
}
