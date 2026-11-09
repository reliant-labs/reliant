package cli

// ROLLBACK-FLOOR DECLARATIONS a release carries, captured at the cut beside
// its migrations (pkg/release floor.go has the model).
//
// Two spellings, one list:
//
//   - kind schema: a migration whose up SQL declares
//     `-- forge:breaks-rollback <why>`. The ID is the file name and the Source
//     its migrations directory, so containment is "does the target's set
//     carry this file".
//   - kind api / feature / data: a checked-in deploy/rollback-floors/<id>.yaml
//     (`kind:` and `reason:`). The Source is that directory of its
//     repository, the ID the file's base name.
//
// The project's own are read from DISK, like its migrations (go:embed and the
// build see the working tree); a sibling checkout's are read from git AT THE
// COMMIT the build recorded, like its migrations, because the checkout on
// disk may have moved since.
//
// A MALFORMED DECLARATION REFUSES THE CUT. Skipping it would record a release
// that silently lacks a floor its author asked for — and a declaration that
// exists is a statement that older releases must not come back. Unlike the
// passed-migration guard's notes, nothing is unknowable here: the file is in
// hand and does not parse.
//
// Every forward migration's adoption list (`-- forge:adopts forge …`) is
// captured too, as MigrationSet.Adopts: a baseline `forge db squash` wrote
// still carries every migration it adopted, so a floor on one of them stays
// satisfiable after the squash.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/internal/migrationgit"
	"github.com/reliant-labs/forge/pkg/migratekit/compat"
	"github.com/reliant-labs/forge/pkg/release"
)

// schemaDirectiveNeedles are the substrings a migration must contain to
// declare anything this capture reads; a sibling's files are pre-filtered by
// them in one git call.
var schemaDirectiveNeedles = []string{"forge:breaks-rollback", "forge:adopt"}

// schemaDeclarationsFrom reads one migration's up SQL into the schema
// declaration it makes (nil when it makes none) and the forge versions it
// adopts.
func schemaDeclarationsFrom(source, file string, upSQL string) (*release.FloorDeclaration, []string, error) {
	var decl *release.FloorDeclaration
	if _, breaks := compat.Declarations(upSQL); len(breaks) > 0 {
		reasons := make([]string, 0, len(breaks))
		for _, b := range breaks {
			if b.Reason != "" {
				reasons = append(reasons, b.Reason)
			}
		}
		decl = &release.FloorDeclaration{
			Source: source, ID: file, Kind: release.DeclarationSchema,
			Reason: strings.Join(reasons, "; "),
		}
	}
	adoption, err := compat.ParseAdoption(upSQL)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", file, err)
	}
	var adopts []string
	if adoption != nil && adoption.Tool == compat.ToolForge {
		for _, v := range adoption.Versions {
			if v > 0 {
				adopts = append(adopts, strconv.FormatInt(v, 10))
			}
		}
	}
	return decl, adopts, nil
}

// projectSchemaDeclarations reads every file the project's own set carries
// from disk.
func projectSchemaDeclarations(set *release.MigrationSet, projectDir string) ([]release.FloorDeclaration, error) {
	dir, ok := projectMigrationsDir(projectDir)
	if !ok || set == nil {
		return nil, nil
	}
	var decls []release.FloorDeclaration
	for _, f := range set.Files {
		data, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", filepath.Join(dir, f), err)
		}
		d, adopts, err := schemaDeclarationsFrom(set.Source, f, string(data))
		if err != nil {
			return nil, fmt.Errorf("migration %w", err)
		}
		if d != nil {
			decls = append(decls, *d)
		}
		set.Adopts = append(set.Adopts, adopts...)
	}
	return decls, nil
}

// siblingSchemaDeclarations reads a sibling's files at its recorded commit:
// only the ones that mention a directive, found in one `git grep`.
func siblingSchemaDeclarations(set *release.MigrationSet, src *buildtarget.Source, migrationsDir string) ([]release.FloorDeclaration, error) {
	if set == nil {
		return nil, nil
	}
	candidates, ok := migrationgit.FilesMatchingAt(src.Dir, migrationsDir, src.Commit, schemaDirectiveNeedles...)
	if !ok {
		return nil, fmt.Errorf("%s: commit %s could not be read, so its rollback-floor declarations cannot be recorded", src.Dir, shortSHA(src.Commit))
	}
	carried := make(map[string]bool, len(set.Files))
	for _, f := range set.Files {
		carried[f] = true
	}
	sort.Strings(candidates)
	var decls []release.FloorDeclaration
	for _, f := range candidates {
		if !carried[f] {
			continue
		}
		data, ok := migrationgit.ContentAt(src.Dir, filepath.Join(migrationsDir, f), src.Commit)
		if !ok {
			return nil, fmt.Errorf("%s: %s at %s could not be read", src.Dir, f, shortSHA(src.Commit))
		}
		d, adopts, err := schemaDeclarationsFrom(set.Source, f, string(data))
		if err != nil {
			return nil, fmt.Errorf("%s migration %w", src.Dir, err)
		}
		if d != nil {
			decls = append(decls, *d)
		}
		set.Adopts = append(set.Adopts, adopts...)
	}
	return decls, nil
}

// declarationSource is the Source of a repository's declarations directory.
func declarationSource(repo, fallback, repoRoot, dir string) string {
	rel, ok := migrationgit.RepoRelative(repoRoot, dir)
	if !ok {
		rel = release.FloorDeclarationsDir
	}
	return migrationSourceID(repo, fallback, rel)
}

// projectFileDeclarations reads the project's own deploy/rollback-floors from
// disk. Absent is none.
func projectFileDeclarations(projectDir string, prov release.Provenance) ([]release.FloorDeclaration, error) {
	dir, err := filepath.Abs(filepath.Join(projectDir, release.FloorDeclarationsDir))
	if err != nil {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	source := declarationSource(prov.Repo, hostedProjectName(), migrationgit.RepoRoot(projectDir), dir)
	var decls []release.FloorDeclaration
	for _, e := range entries {
		if e.IsDir() || !isDeclarationFile(e.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", filepath.Join(dir, e.Name()), err)
		}
		d, err := release.ParseFloorDeclarationFile(e.Name(), data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", release.FloorDeclarationsDir, err)
		}
		d.Source = source
		decls = append(decls, d)
	}
	return decls, nil
}

// siblingFileDeclarations reads a sibling's deploy/rollback-floors at its
// recorded commit. A commit this clone cannot read is a note, as it is for
// the sibling's migrations: the same gap, said once more.
func siblingFileDeclarations(src *buildtarget.Source) ([]release.FloorDeclaration, string, error) {
	dir := filepath.Join(src.Dir, release.FloorDeclarationsDir)
	names, ok := migrationgit.FilesAt(src.Dir, dir, src.Commit)
	if !ok {
		return nil, fmt.Sprintf("%s: commit %s could not be read, so its rollback-floor declarations were not recorded", src.Dir, shortSHA(src.Commit)), nil
	}
	source := declarationSource(src.Repo, filepath.Base(src.Dir), src.Dir, dir)
	sort.Strings(names)
	var decls []release.FloorDeclaration
	for _, n := range names {
		if !isDeclarationFile(n) {
			continue
		}
		data, ok := migrationgit.ContentAt(src.Dir, filepath.Join(dir, n), src.Commit)
		if !ok {
			return nil, "", fmt.Errorf("%s: %s/%s at %s could not be read", src.Dir, release.FloorDeclarationsDir, n, shortSHA(src.Commit))
		}
		d, err := release.ParseFloorDeclarationFile(n, data)
		if err != nil {
			return nil, "", fmt.Errorf("%s %s: %w", src.Dir, release.FloorDeclarationsDir, err)
		}
		d.Source = source
		decls = append(decls, d)
	}
	return decls, "", nil
}

// siblingReadable is a sibling checkout whose declarations the cut reads: a
// forge project (only those embed forge state) with a recorded commit.
func siblingReadable(src *buildtarget.Source) bool {
	if src == nil || src.Project || src.Dir == "" || !release.ValidObjectID(src.Commit) {
		return false
	}
	_, err := os.Stat(filepath.Join(src.Dir, "forge.yaml"))
	return err == nil
}

// dedupeDeclarations keeps the first of each (Source, ID): two build uses of
// one sibling read the same directory.
func dedupeDeclarations(ds []release.FloorDeclaration) []release.FloorDeclaration {
	seen := map[release.DeclarationRef]bool{}
	var out []release.FloorDeclaration
	for _, d := range ds {
		if seen[d.Ref()] {
			continue
		}
		seen[d.Ref()] = true
		out = append(out, d)
	}
	return out
}

func dedupeVersions(vs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range vs {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// isDeclarationFile is a file the declarations directory counts: `<id>.yaml`.
// A README or a `.gitkeep` beside them is not one.
func isDeclarationFile(name string) bool {
	return strings.HasSuffix(name, ".yaml") && !strings.HasPrefix(name, ".")
}
