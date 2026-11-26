package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
)

// The first backend component of a project born frontend-only.
//
// A project created with no service and no database gets envs in the
// frontend-only shape (kcl/frontend-only/main.k.tmpl): no `_api`, no
// `_workloads` list, no database. That is deliberate (a frontend needs no
// cluster or database). Those env files are scaffold-once and the user's, so
// the first `forge scaffold service` cannot grow them into the backend shape,
// and it used to print a "Bind workload … add to its `_workloads` list" line
// for a list the file does not have (forge bugs aee44198, e208f389): the
// service then ran in no env, and nothing said so.
//
// reportFrontendOnlyEnvs says it instead, with the commands that DO give an
// env the backend shape: move the file aside and re-scaffold it, together with
// the KCL modules the backend shape imports, then carry the user's own edits
// back. When the project already has migrations, it also names the two other
// files born before the database existed: serve.go, which passes no database
// to serverkit, and workloads.k, which declares no migrate job.

// reportFrontendOnlyEnvs prints the guidance for the envs bindWorkloadInEnvs
// found in the frontend-only shape. No-op when there are none.
func reportFrontendOnlyEnvs(root string, cfg *config.ProjectConfig, comp config.ComponentConfig, envs []string) {
	if len(envs) == 0 || cfg == nil {
		return
	}
	fmt.Print(frontendOnlyEnvsNotice(root, cfg, comp, envs))
}

// frontendOnlyEnvsNotice is reportFrontendOnlyEnvs' text.
func frontendOnlyEnvsNotice(root string, cfg *config.ProjectConfig, comp config.ComponentConfig, envs []string) string {
	cli := cmdutil.Name()
	kind := codegen.WorkloadKindFor(comp.EffectiveKind())
	var files []string
	for _, env := range envs {
		files = append(files, "deploy/kcl/"+env+"/main.k")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\n⚠️  %s %s scaffolded for a frontend with no backend: no `_api`, no `_workloads` list, no database.\n",
		strings.Join(files, ", "), isAre(len(files)))
	fmt.Fprintf(&b, "   %s '%s' runs in none of them until each has the backend shape. They are yours, so forge does not rewrite them.\n", kind, comp.Name)
	b.WriteString("   To give them that shape, move each aside and re-scaffold it with the modules it imports:\n\n")

	rescaffold := append([]string(nil), files...)
	for _, f := range files {
		fmt.Fprintf(&b, "     mv %s %s.frontend-only\n", f, f)
	}
	rescaffold = append(rescaffold, missingBackendEnvModules(root, envs)...)
	var serveGo string
	if hasMigrations(root) {
		if rel := dblessServeGo(root, cfg.Name); rel != "" {
			serveGo = rel
			fmt.Fprintf(&b, "     mv %s %s.frontend-only\n", rel, rel)
			rescaffold = append(rescaffold, rel)
		}
	}
	fmt.Fprintf(&b, "     %s project rescaffold %s\n\n", cli, strings.Join(rescaffold, " "))
	b.WriteString("   then carry your own edits over from each *.frontend-only copy (your frontend's declaration among them),\n")
	fmt.Fprintf(&b, "   run `%s generate` (it writes the deploy/kcl/config_gen.k the backend shape imports),\n", cli)
	fmt.Fprintf(&b, "   and check each env with `%s env render <env>`.\n", cli)
	if serveGo != "" {
		fmt.Fprintf(&b, "   %s is re-scaffolded too: it was born before the project had a database, so it passes no\n", serveGo)
		b.WriteString("   DatabaseURL or AutoMigrate to serverkit, and the server skips its database (\"DATABASE_URL is not set\").\n")
	}
	if hasMigrations(root) {
		if content, ok := readWorkloadsKCL(root); ok && !codegen.WorkloadDeclared(content, codegen.MigrateWorkloadName) {
			fmt.Fprintf(&b, "\n   %s declares no `%s` job, so no env applies %s before the server starts. Add:\n\n",
				codegen.WorkloadsKCLRelPath, codegen.MigrateWorkloadName, config.MigrationsDirIn(root))
			b.WriteString(indentLines(codegen.MigrateWorkloadStanza(cfg.ModulePath, cfg.Name), "     "))
			b.WriteString("\n")
		}
	}
	return b.String()
}

// missingBackendEnvModules lists the KCL modules the backend env shape
// imports that a frontend-only project does not have: the deployed envs'
// shared base and the ingress topology, its own and each env's. Only absent
// files are listed — rescaffold refuses a path that exists.
func missingBackendEnvModules(root string, envs []string) []string {
	want := []string{"deploy/kcl/ingress.k"}
	for _, env := range envs {
		if env != codegen.DevEnvName {
			want = append([]string{"deploy/kcl/base/env.k"}, want...)
			break
		}
	}
	for _, env := range envs {
		want = append(want, "deploy/kcl/"+env+"/ingress.k")
	}
	var out []string
	for _, rel := range want {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); os.IsNotExist(err) {
			out = append(out, rel)
		}
	}
	return out
}

// hasMigrations reports whether the project's migrations directory holds a
// migration.
func hasMigrations(root string) bool {
	matches, _ := filepath.Glob(filepath.Join(root, filepath.FromSlash(config.MigrationsDirIn(root)), "*.up.sql"))
	return len(matches) > 0
}

// dblessServeGo returns the project-relative cmd/<bin>/cmd/serve.go when it
// exists and hands serverkit no database URL — the serve.go a project born
// without a database got. "" otherwise.
func dblessServeGo(root, bin string) string {
	rel := filepath.ToSlash(filepath.Join("cmd", bin, "cmd", "serve.go"))
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil || strings.Contains(string(data), "DatabaseURL:") {
		return ""
	}
	return rel
}

func isAre(n int) string {
	if n == 1 {
		return "was"
	}
	return "were"
}

func indentLines(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "\n") + "\n"
}
