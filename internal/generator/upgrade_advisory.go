// The scaffold-once advisory lane of `forge project upgrade`.
//
// Forge writes three kinds of file:
//
//   - Tier-1 — re-rendered every `forge generate`, self-certifying, gated
//     by the drift probe (drift.go).
//   - Tier-2 managed — written once, checksum-protected, and kept current
//     by `forge project upgrade` when the on-disk copy is still a pristine
//     render (buf.yaml, Taskfile.yml, Dockerfile, .golangci.yml, …).
//   - scaffold-once ("yours") — written once at birth with NO marker, and
//     never touched again. The frontend mechanism modules
//     (src/lib/query-client.ts, src/lib/events.ts, src/hooks/*) and
//     everything under .github/ are this tier.
//
// The third tier had no feedback path at all. When forge IMPROVED one of
// those templates, every existing project stayed on its birth copy and
// nothing anywhere said so. That is not hypothetical: a shipped
// query-client.ts kept a blanket `retry: 1` that retried 4xx long after
// the template replaced it with an `isRetryable` predicate, and the
// events.ts it depends on was missing the `emitToast` the new client
// imports. Both were found by a human reading templates, not by forge.
//
// This file closes that loop WITHOUT breaking the ownership contract:
//
//	report, never apply.
//
// An advisory row is rendered from the current template and compared with
// what is on disk. A difference is printed — never written — unless the
// user names that exact path after --force. Bare --force deliberately does
// NOT reach this tier: a flag that names nothing cannot express intent
// about a file forge handed over at birth, and the whole point of the tier
// is that adoption is the user's call.
//
// Membership is derived, not hand-listed. The frontend rows are exactly
// the files that win composition from the SHARED template roots
// (templates.FrontendTemplateRoots) — the roots whose stated job is
// "generic mechanism modules … rendered into ALL frontend kinds". A new
// shared mechanism module is covered the day it lands, with no second
// registry to forget to update; forgetting to update a registry is the
// bug class this whole file exists to fix.
//
// A row may only be added when the project has exactly ONE renderer for
// that path. The GitHub Actions workflows used to fail that rule: forge
// rendered .github/workflows/* from two config→data mappings (one per
// command) that disagreed on a project seconds old — a whole docker-build
// job in ci.yml, the env order in deploy.yml. They now share one,
// CIWorkflows (ci_workflows.go), pinned byte-identical by
// TestCIWorkflows_NewAndGenerateRenderIdentically, and are rows too
// (CIWorkflowAdvisoryFiles) — supplied by internal/cli, because their
// render needs a KCL render of every env.
//
// They had to be. With no row, a defect fixed in a workflow template never
// reached a single existing project: ci.yml shipped `cancel-in-progress:
// true` for every ref, so on a busy runner pool each merge cancelled the
// main run before it, and control-plane's main went 2.5 hours without one
// completed run (control-plane#831). The fix to the template changed
// nothing for the projects that already had the file.
//
// The non-workflow .github starters and pre-commit.yml have a single
// renderer each and are in AdvisoryFilesFor directly.
package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/configproto"
	"github.com/reliant-labs/forge/internal/templates"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// AdvisoryStatus is the verdict for one scaffold-once file.
type AdvisoryStatus string

const (
	// AdvisoryCurrent — the template has nothing this file lacks: either
	// the bodies are equal, or the file has grown past the template and
	// still contains all of it. Silent, because there is no advice to
	// give.
	AdvisoryCurrent AdvisoryStatus = "current"
	// AdvisoryAbsent — the template ships a file this project does not
	// have. Almost always "scaffolded before the module existed";
	// adopting it is a pure add that destroys nothing.
	AdvisoryAbsent AdvisoryStatus = "absent"
	// AdvisoryBehind — the file differs from the template, and every
	// non-blank line it has, the template also has. The delta is one-way:
	// template content this copy lacks. Adoption is a refresh.
	AdvisoryBehind AdvisoryStatus = "behind"
	// AdvisoryDiverged — the file differs AND carries non-blank lines no
	// current template line matches. Someone put those there. Adoption
	// would discard them, so the honest framing is a merge the user
	// performs, not an overwrite forge performs.
	AdvisoryDiverged AdvisoryStatus = "diverged"
)

// AdvisoryFile is one scaffold-once row: a project-relative path plus the
// render of the template that path was born from.
//
// Render is a closure rather than a (template name, data) pair because the
// rows span template registries with unrelated payload types — the
// composed frontend tree and the .github starters — and each row's payload
// is built where that payload's meaning already lives.
type AdvisoryFile struct {
	// Path is the project-relative destination (e.g.
	// "frontends/admin-web/src/lib/query-client.ts").
	Path string
	// Render produces the current template's bytes for this path.
	Render func() ([]byte, error)
}

// AdvisoryResult is the outcome for one advisory row.
type AdvisoryResult struct {
	Path   string
	Status AdvisoryStatus
	Diff   string
	// Missing counts non-blank lines the current template has that the
	// on-disk file does not — "how far behind", in lines.
	Missing int
	// Local counts non-blank lines the on-disk file has that the current
	// template does not — the evidence of customization.
	Local int
	// Proven is true when forge can PROVE the bytes are an unedited forge
	// render (a verifying embedded forge:hash marker). Scaffold-once
	// files carry no marker by contract, so this is normally false; it
	// stays here so a path that acquired one along the way is read as the
	// certainty it is rather than as the Local-count heuristic.
	Proven bool
	// Selected is true when the user named this exact path after --force.
	Selected bool
	// Adopted is true when this run actually wrote the template over the
	// file (Selected, and not a dry run).
	Adopted bool
}

// Behind reports whether the row has anything to say — i.e. anything but
// "current".
func (r AdvisoryResult) Behind() bool { return r.Status != AdvisoryCurrent }

// AdvisoryPaths returns the project-relative paths of a row set, in order.
// `forge project upgrade --force <path>` validates its arguments against
// this plus the Tier-2 managed set.
func AdvisoryPaths(files []AdvisoryFile) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

// AdvisoryFilesFor returns every scaffold-once row for a project: each
// frontend's SHARED-root mechanism files, plus the two .github starters
// that have a single renderer.
//
// Per-kind frontend files (connect.ts.tmpl, next.config.ts.tmpl, the app
// shell) are deliberately absent. They are the platform wiring a project
// is expected to grow into, and a shared root is forge's own declaration
// of "this file is mechanism I own the design of" — which is exactly the
// set worth reporting on.
//
// projectDir is the project root: the typed-config presence set a frontend's
// session-provider.ts / connect.ts render from is read from its proto/<project>/config/v1.
// It used to be read from the process working directory, so `forge project
// upgrade -C <dir>` — or any caller not sitting in the project — rendered the
// untyped variant of those files and reported (or adopted!) the wrong bytes.
func AdvisoryFilesFor(projectDir string, cfg *config.ProjectConfig) ([]AdvisoryFile, error) {
	if cfg == nil {
		return nil, nil
	}
	rows, err := frontendAdvisoryFiles(projectDir, cfg)
	if err != nil {
		return nil, err
	}
	return append(rows, githubStarterAdvisoryFiles(projectDir, cfg)...), nil
}

// CIWorkflowAdvisoryFiles returns a scaffold-once row for every GitHub
// Actions file the CI mapper gives this project (CIWorkflowsFor) that is on
// disk, rendered exactly as `forge generate` would write it today.
//
// It is separate from AdvisoryFilesFor because two of the mapper's inputs —
// which frontends exist and which envs are hosted — come from a KCL render
// of every env, which only internal/cli can perform. The caller supplies
// them, and must not call this when an env failed to render: the rows would
// compare against a workflow set forge only guessed at.
//
// A workflow that is not on disk is not a row. One never written is
// `forge generate`'s to write (writeCIScaffold); one the user deleted
// stays deleted, and `forge project rescaffold <path>` is how to get it
// back. Reporting either as "absent" would be noise on every upgrade.
func CIWorkflowAdvisoryFiles(projectDir string, cfg *config.ProjectConfig, in CIInputs) []AdvisoryFile {
	if cfg == nil || (cfg.CI.Provider != "" && cfg.CI.Provider != "github") {
		return nil
	}
	var rows []AdvisoryFile
	for _, f := range CIWorkflowsFor(projectDir, cfg, in) {
		rel := filepath.FromSlash(f.Dest)
		if !onDisk(projectDir, rel) {
			continue
		}
		rows = append(rows, AdvisoryFile{
			Path: rel,
			Render: func() ([]byte, error) {
				return templates.CITemplates("github").Render(f.Template, f.Data)
			},
		})
	}
	return rows
}

// onDisk reports whether rel exists under projectDir.
func onDisk(projectDir, rel string) bool {
	_, err := os.Stat(filepath.Join(projectDir, rel))
	return err == nil
}

// githubStarterAdvisoryFiles returns the .github starters forge writes
// once and never re-emits: the PR template (a static file — no render data
// at all), CODEOWNERS (one field, derived from the module path exactly
// as the scaffold derives it), and the pre-commit workflow (static).
//
// All three are already exempt from the drift probe as user-owned, which
// left them with no feedback path whatsoever: a project whose PR template
// predates a whole new checklist section had no way to learn that. Forge
// renders each from one place, so what this lane compares against is what
// the scaffold would write today — no second mapping to disagree with.
func githubStarterAdvisoryFiles(projectDir string, cfg *config.ProjectConfig) []AdvisoryFile {
	if cfg.CI.Provider != "" && cfg.CI.Provider != "github" {
		return nil
	}
	const provider = "github"
	rows := []AdvisoryFile{{
		Path: filepath.Join(".github", "pull_request_template.md"),
		Render: func() ([]byte, error) {
			return templates.CITemplates(provider).Get("pull_request_template.md")
		},
	}}
	// CODEOWNERS is emitted only when a GitHub owner can be inferred;
	// forge ships no review-free stub, so there is nothing to report when
	// inference fails.
	if owner := githubOwnerFromModulePath(cfg.ModulePath); owner != "" {
		rows = append(rows, AdvisoryFile{
			Path: filepath.Join(".github", "CODEOWNERS"),
			Render: func() ([]byte, error) {
				return templates.CITemplates(provider).Render("CODEOWNERS.tmpl",
					struct{ GitHubOwner string }{owner})
			},
		})
	}
	// Only `forge project new` writes pre-commit.yml, with a plain write
	// that leaves no birth record, so an absent copy cannot be told apart
	// from one the user deleted on purpose. Report it only while it is
	// there — the same rule CIWorkflowAdvisoryFiles applies.
	if cfg.Features.CIEnabled() && onDisk(projectDir, filepath.FromSlash(preCommitWorkflowPath)) {
		rows = append(rows, AdvisoryFile{
			Path: filepath.FromSlash(preCommitWorkflowPath),
			Render: func() ([]byte, error) {
				return []byte(preCommitWorkflow), nil
			},
		})
	}
	return rows
}

// frontendAdvisoryFiles returns the shared-root mechanism rows for every
// frontend the project declares.
func frontendAdvisoryFiles(projectDir string, cfg *config.ProjectConfig) ([]AdvisoryFile, error) {
	layout := NewFrontendWorkspaceLayout(cfg.Name)
	workspaces := cfg.IsFrontendWorkspacesEnabled()

	var out []AdvisoryFile
	for _, fe := range cfg.Frontends {
		tree := frontendTemplateTreeFor(fe)
		files, err := templates.ListFrontendTree(tree)
		if err != nil {
			return nil, fmt.Errorf("list frontend templates for %s: %w", fe.Name, err)
		}
		shared := sharedFrontendRoots(tree)

		// Only .tmpl files consult this payload, and across every shared
		// root the tags in play are {{.FrontendName}} and {{.Platform}} —
		// both fixed by the frontend being inspected. The remaining fields
		// are populated so a shared template that starts reading one
		// renders the same here as it does at scaffold time.
		data := templates.FrontendTemplateData{
			FrontendName: fe.Name,
			ProjectName:  cfg.Name,
			Platform:     tree,
			Module:       cfg.ModulePath,
			Workspaces:   workspaces,
		}

		// The typed-config presence set must match what the scaffold used,
		// or this lane reports drift on a file forge itself just wrote:
		// session-provider.ts renders one of two forms depending on whether
		// the frontend has a config message, and a zero value here would
		// render the build-time env-var form against a scaffold that emitted
		// the typed-module form — a diff on every fresh project, which is
		// exactly the false positive this lane must never produce.
		tc := frontendAdvisoryTypedConfig(projectDir, fe.Name)
		data.HasTypedConfig = tc.Bound
		data.HasMockAPI = tc.HasMockAPI
		if workspaces {
			data.APIPackage = layout.APIPackage
			data.HooksPackage = layout.HooksPackage
			data.UIWebPackage = layout.UIWebPackage
			data.UINativePackage = layout.UINativePackage
		}

		// Per-kind templates read fields the shared ones never do —
		// next.config.ts renders differently per Output and BasePath — so
		// the payload has to carry the frontend's own build shape or the
		// comparison copy differs from what the scaffold wrote.
		data.Output = frontendAdvisoryOutput(fe)
		data.BasePath = fe.BasePath

		// Paths are project-RELATIVE here (the advisory rows are joined
		// against the project root by InspectAdvisories), so containment
		// is asked against ".".
		//
		// !ok means the frontend has no directory in this repository: a
		// cross-repo `source:` pin, or a path pointing at a sibling
		// checkout. SKIP it. This is a deliberate behavior change from
		// the retired EffectivePath, which handed back the conventional
		// frontends/<name> label for such a frontend — so the advisory
		// lane compared forge's templates against a tree that is not
		// there and reported every file as drifted-or-missing. An upgrade
		// advisory about a directory that does not exist is noise, and
		// the files in question belong to a repository whose own forge
		// upgrades them.
		dir, ok := fe.Dir(".")
		if !ok {
			continue
		}
		base := filepath.FromSlash(dir)
		// The Dockerfile builds at the frontend's repo path, so the
		// comparison copy must be rendered for the directory the file
		// actually sits in.
		data.RepoPath = dir
		for _, file := range files {
			root := templateRootOf(file.Path)
			// A shared root is forge declaring "this file is mechanism I
			// own the design of". A per-kind root is forge declaring "this
			// is the platform wiring" — a different statement about
			// OWNERSHIP, but not about REPORTING: both were written from a
			// template, so drift in both is reportable. Per-kind files that
			// a second renderer owns are excluded by name below.
			if !shared[root] && !perKindAdvisoryEligible(tree, file.Rel) {
				continue
			}
			tmplPath := file.Path
			out = append(out, AdvisoryFile{
				Path: filepath.Join(base, filepath.FromSlash(strings.TrimSuffix(file.Rel, ".tmpl"))),
				Render: func() ([]byte, error) {
					return templates.FrontendTemplates().Render(tmplPath, data)
				},
			})
		}
	}
	return out, nil
}

// frontendAdvisoryTypedConfig reports which typed config fields the named
// frontend has, for rendering the comparison copy of a shared template.
//
// It reads the project's config protos when a descriptor exists, so a
// project whose frontend config was hand-narrowed compares against the
// form it actually scaffolded. Before `forge generate` has ever run there
// is no descriptor — and on a fresh scaffold that is the normal state — so
// it falls back to the set the scaffolder itself declares, which is what
// the on-disk file was rendered from.
//
// The fallback is keyed on the config proto EXISTING rather than on the
// descriptor, because the descriptor is derived and the proto is the
// declaration. A project with no frontend config proto at all (one
// scaffolded by an older forge) correctly gets the zero value and compares
// against the build-time env-var form its file still has.
func frontendAdvisoryTypedConfig(root, frontend string) FrontendTypedConfig {
	if messages, err := codegen.ParseConfigProtosFromDir(filepath.Join(root, "proto", "config")); err == nil {
		for _, fc := range codegen.FrontendConfigsFromMessages(messages) {
			if fc.Frontend != frontend {
				continue
			}
			envVars := make([]string, 0, len(fc.Fields))
			for _, f := range fc.Fields {
				if f.EnvVar != "" {
					envVars = append(envVars, f.EnvVar)
				}
			}
			return FrontendTypedConfigFrom(envVars)
		}
	}

	if _, err := os.Stat(filepath.Join(root, FrontendConfigProtoRelPath(configproto.ForProject(root), frontend))); err == nil {
		return ScaffoldedFrontendTypedConfig()
	}
	return FrontendTypedConfig{}
}

// frontendTemplateTreeFor resolves a frontend's type to its template tree
// name. The inventory spells the type as detected from disk ("nextjs" /
// "react-native" / "vite-spa") or as a KCL declaration wrote it ("nextjs" /
// "vite" / "rn"); both reach this function, so both are resolved here.
func frontendTemplateTreeFor(fe config.FrontendConfig) string {
	switch strings.ToLower(strings.TrimSpace(fe.Type)) {
	case "react-native", "react_native", "mobile", "rn":
		return "react-native"
	case "vite-spa", "vite":
		return "vite-spa"
	default:
		return "nextjs"
	}
}

// sharedFrontendRoots returns the composition roots for a tree that are
// NOT the tree's own directory — i.e. the roots whose files are generic
// mechanism rendered into every kind that claims them.
func sharedFrontendRoots(tree string) map[string]bool {
	shared := map[string]bool{}
	for _, root := range templates.FrontendTemplateRoots(tree) {
		if root != tree {
			shared[root] = true
		}
	}
	return shared
}

// templateRootOf returns the first path segment of a composed template
// path ("shared/src/lib/events.ts" -> "shared").
func templateRootOf(templatePath string) string {
	if i := strings.IndexByte(templatePath, '/'); i >= 0 {
		return templatePath[:i]
	}
	return templatePath
}

// InspectAdvisories compares every advisory row with the project on disk.
//
// It is read-only EXCEPT for rows the caller's force selection names
// explicitly: `--force <path>` is the adoption gesture, and it is the only
// thing that writes here. checkOnly suppresses even that.
//
// A disowned path is skipped outright. Disowning is a recorded, one-way
// statement that the file is the user's; continuing to offer it as
// adoptable would be forge asking for it back every run.
func InspectAdvisories(projectDir string, cs *FileChecksums, files []AdvisoryFile, force ForceSelection, checkOnly bool) ([]AdvisoryResult, error) {
	var out []AdvisoryResult
	for _, f := range files {
		if cs.IsDisowned(f.Path) {
			continue
		}
		expected, err := f.Render()
		if err != nil {
			return nil, fmt.Errorf("render %s: %w", f.Path, err)
		}

		selected := force.Names(f.Path)
		existing, readErr := os.ReadFile(filepath.Join(projectDir, f.Path))
		if readErr != nil {
			if !os.IsNotExist(readErr) {
				return nil, fmt.Errorf("read %s: %w", f.Path, readErr)
			}
			// An absent row carries a diff too, so `upgrade --check
			// <path>` on a file the project does not have shows what
			// adopting it would add rather than reporting a count and
			// nothing to look at.
			res := AdvisoryResult{
				Path:     f.Path,
				Status:   AdvisoryAbsent,
				Diff:     simpleDiff(f.Path, nil, expected),
				Missing:  len(lineBag(expected)),
				Selected: selected,
			}
			if selected && !checkOnly {
				if werr := writeAdvisoryFile(projectDir, f.Path, expected); werr != nil {
					return nil, werr
				}
				res.Adopted = true
			}
			out = append(out, res)
			continue
		}

		if checksums.BodyHash(existing) == checksums.BodyHash(expected) {
			out = append(out, AdvisoryResult{Path: f.Path, Status: AdvisoryCurrent})
			continue
		}

		missing, local := lineDelta(existing, expected)
		// The template has nothing this file lacks — the bytes differ only
		// because the file has grown past it. There is no advice to give,
		// and giving it anyway ("behind by 0 lines") is how a report earns
		// its way into the part of the output people scroll past.
		if missing == 0 {
			out = append(out, AdvisoryResult{Path: f.Path, Status: AdvisoryCurrent})
			continue
		}
		res := AdvisoryResult{
			Path:     f.Path,
			Diff:     simpleDiff(f.Path, existing, expected),
			Missing:  missing,
			Local:    local,
			Proven:   checksums.Verify(existing) == checksums.Pristine,
			Selected: selected,
		}
		// A verifying marker is proof the bytes are an untouched forge
		// render, which outranks the line heuristic. Without one, lines
		// the template cannot account for are the only evidence there is
		// — and it is evidence of authorship, so it decides the verdict.
		switch {
		case res.Proven, local == 0:
			res.Status = AdvisoryBehind
		default:
			res.Status = AdvisoryDiverged
		}
		if selected && !checkOnly {
			if werr := writeAdvisoryFile(projectDir, f.Path, expected); werr != nil {
				return nil, werr
			}
			res.Adopted = true
		}
		out = append(out, res)
	}
	return out, nil
}

// writeAdvisoryFile adopts a template over a scaffold-once path.
//
// The bytes go down exactly as the scaffold would have written them: NO
// forge:hash marker, no manifest entry. Certifying the result would make
// the user's NEXT edit to their own file register as generated-file drift
// in `forge lint` / `forge generate`'s stomp guard — adopting a better
// template must not cost a project the ownership of the file it adopted
// it into.
func writeAdvisoryFile(root, relPath string, content []byte) error {
	full := filepath.Join(root, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("create dir for %s: %w", relPath, err)
	}
	if err := atomicfile.WriteFile(full, content, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", relPath, err)
	}
	checksums.MarkWrittenThisRun(relPath)
	return nil
}

// lineBag is the multiset of non-blank, whitespace-trimmed lines in
// content. Trimming absorbs re-indentation; blanks carry no content.
func lineBag(content []byte) map[string]int {
	bag := map[string]int{}
	for _, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		bag[trimmed]++
	}
	return bag
}

// lineDelta measures the two-way difference between an on-disk file and
// the current template, as line counts:
//
//	missing — lines the template has that the file does not (how stale)
//	local   — lines the file has that the template does not (how customized)
//
// This is a multiset comparison, not a diff: it deliberately ignores
// ordering, so moving a function does not read as rewriting it. `local ==
// 0` means every line in the file is accounted for by the current
// template — the file is a subset of it, which is what "just old" looks
// like. Any local line is content someone added, and adopting the template
// wholesale would drop it.
//
// It is evidence, not proof — an old template's lines also fail to match
// the current one — which is why the report says what it detected and
// leaves the decision with the user rather than acting on it.
func lineDelta(onDisk, template []byte) (missing, local int) {
	diskBag := lineBag(onDisk)
	tmplBag := lineBag(template)
	for line, want := range tmplBag {
		if have := diskBag[line]; want > have {
			missing += want - have
		}
	}
	for line, have := range diskBag {
		if want := tmplBag[line]; have > want {
			local += have - want
		}
	}
	return missing, local
}
