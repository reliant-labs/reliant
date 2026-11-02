package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/cliutil"
)

// Generate must not exit 0 on a deploy tree that does not compile.
//
// THE INCIDENT. forge #322 made `registry` a closed-schema error on
// ClusterTarget and ControlPlane. A project that still declared one was
// therefore unrenderable — but `forge generate` printed a warning and exited 0,
// so the break surfaced much later, at `forge env render`, pointing at a schema
// error with no connection to the generate that had already reported success.
// A migration that silently no-opped (internal/kclmigrate) was the first half
// of that failure; this exit code was the second, and either one alone would
// have caught it.
//
// WHY GENERATE IS THE RIGHT PLACE. generate is the command that reconciles a
// project with the forge it is pinned to, and it is what a user runs after an
// upgrade. It already refuses for retired ShellBuild tokens and for an
// unmigratable registry. A deploy tree that does not load is the same class of
// fact — the project does not match this forge — and finding it here costs one
// render, while finding it at deploy costs whatever ran in between.
//
// WHAT IS STILL OPTIONAL. Plenty of legitimate states stop an env rendering and
// are none of generate's business: a project mid-edit, a toolchain gap, an env
// scaffolded but not yet authored. Those keep degrading to a warning. What is
// NOT optional is the project's own KCL being structurally wrong against the
// schemas this binary ships — a closed-schema violation, an undefined
// attribute, a type error. Those are decidable from the error text and are
// fatal here.
//
// The split is deliberately conservative in the ONE direction that matters: a
// message this classifier does not recognise is treated as optional, so a new
// kcl diagnostic cannot start failing every project's generate. Being wrong
// that way costs a late error, which is where we already were; being wrong the
// other way blocks work on projects that are fine.

// fatalKCLErrorMarkers are the diagnostics that mean THIS PROJECT'S KCL is
// wrong against THIS FORGE'S schemas — the class no amount of waiting or
// re-running fixes, and the class a later command will hit identically.
//
// Each is a kcl compiler diagnostic, not a forge string, so they are matched on
// substrings that kcl itself emits.
var fatalKCLErrorMarkers = []string{
	// The closed-schema violation #322 produced: a field the schema no
	// longer has. This is the exact text of the incident.
	"Cannot add member",
	// The same fact under kcl's other spellings.
	"no attribute named",
	"Cannot find the attribute",
	"attribute is not defined",
	"CompileError",
	"expected type",
	"immutable variable",
}

// optionalKCLErrorMarkers are states that stop a render and are NOT the
// project's KCL being wrong: the toolchain, the environment, or a tree that is
// simply not finished. These keep degrading to a warning, because failing
// generate on them would block work forge has no standing to block.
var optionalKCLErrorMarkers = []string{
	// The module graph is not fetched / not vendored yet.
	"CannotFindModule",
	"failed to load package",
	"no such file or directory",
	// Network-bound module resolution.
	"dial tcp",
	"connection refused",
	"i/o timeout",
}

// classifyKCLLoadError decides whether a render failure is the project's KCL
// being structurally wrong (fatal) or one of the states generate tolerates.
//
// Optional markers are checked FIRST and win ties. An unfetched module graph
// can produce a message that also mentions an attribute, and in that case the
// honest reading is the module one — the attribute error is downstream of it,
// and failing generate would blame the project for its toolchain.
func classifyKCLLoadError(err error) (fatal bool, marker string) {
	if err == nil {
		return false, ""
	}
	msg := err.Error()
	for _, m := range optionalKCLErrorMarkers {
		if strings.Contains(msg, m) {
			return false, m
		}
	}
	for _, m := range fatalKCLErrorMarkers {
		if strings.Contains(msg, m) {
			return true, m
		}
	}
	// Unrecognised: treat as optional. A diagnostic forge has not seen before
	// must not start failing every project's generate — see the header.
	return false, ""
}

// stepKCLLoadable renders every environment in deploy/kcl and FAILS generate
// when one of them does not compile against this forge's schemas.
//
// It runs late, after the emitters, because the tree it checks is the tree
// generate has just finished producing: config_gen.k, the per-env config.k and
// workloads.k are all written by earlier steps, and checking before them would
// report a first-scaffold project as broken.
func stepKCLLoadable(ctx *pipelineContext) error {
	kclDir := filepath.Join(ctx.ProjectDir, "deploy", "kcl")
	if _, err := os.Stat(kclDir); err != nil {
		return nil // no deploy tree: nothing to check
	}
	envs, err := ListEnvs(ctx.ProjectDir)
	if err != nil || len(envs) == 0 {
		return nil
	}

	var broken []string
	var skipped int
	for _, env := range envs {
		// renderKCLPure, not RenderKCL: evaluating a module fires any
		// file.write in it, and a write that lands on a tracked file makes
		// `forge generate` non-reproducible. See kcl_render_purity.go.
		_, restored, rerr := renderKCLPure(context.Background(), ctx.ProjectDir, env)
		if rerr == nil {
			reportImpureRender(env, restored)
			continue
		}
		fatal, _ := classifyKCLLoadError(rerr)
		if !fatal {
			skipped++
			continue
		}
		broken = append(broken, fmt.Sprintf("deploy/kcl/%s/main.k does not compile:\n%s",
			env, indentLines(rerr.Error(), "      ")))
	}
	if len(broken) == 0 {
		return nil
	}

	var b strings.Builder
	b.WriteString("this project's deploy KCL does not compile against the forge it is pinned to.\n\n")
	b.WriteString("Every command that renders — `forge env render`, `forge build`, `forge env deploy`,\n")
	b.WriteString("`forge env up` — will fail with the error below until it is fixed. forge generate\n")
	b.WriteString("fails here rather than reporting success, because a generate that exits 0 on an\n")
	b.WriteString("unrenderable tree moves the discovery to whichever command runs next, with\n")
	b.WriteString("nothing to connect it back to this run.\n\n")
	for _, s := range broken {
		b.WriteString("  • " + s + "\n\n")
	}
	if skipped > 0 {
		b.WriteString(fmt.Sprintf("  (%d other environment(s) could not be rendered for reasons that are not a KCL\n", skipped))
		b.WriteString("   error — an unfetched module graph, or a toolchain gap — and were not judged.)\n")
	}
	return cliutil.UserErr("forge generate", "deploy KCL does not compile", "deploy/kcl/", b.String())
}

func indentLines(s, indent string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = indent + l
	}
	return strings.Join(lines, "\n")
}
