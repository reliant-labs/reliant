package templates

import (
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestSkillsDoNotReferenceRemovedGenFiles asserts that the live skill catalog
// (everything under project/skills/forge/) never names a `*_gen.go` filename
// that forge no longer emits. This is the meta-test that guards against the
// `tracing_gen.go` / `metrics_gen.go` class of drift the 2026-05-06 dogfood
// review caught. (`middleware_gen.go` was in that class then, but forge
// re-introduced it Wave 5 as the component-middleware decorator, so it now
// lives in allowedGenFiles above.)
//
// Migration skills (those under migrations/) are allow-listed because their
// entire job is to describe the OLD shape so users can detect and remove it.
// Touching them here would defeat their purpose.
//
// Allowed `*_gen.go` filenames are intentionally listed — when forge starts
// emitting a new `<X>_gen.go`, add it here. When forge stops emitting one,
// remove it. The list is the contract.
func TestSkillsDoNotReferenceRemovedGenFiles(t *testing.T) {
	t.Parallel()

	allowedGenFiles := map[string]struct{}{
		// Per-package mock generation off contract.go.
		"mock_gen.go": {},
		// Per-package component-middleware decorator, generated off contract.go
		// (sibling of mock_gen.go): New<Concrete>WithForgeMiddleware wraps New
		// through the observe.ComponentChain. Re-introduced Wave 5 as the
		// in-process observability wrapper (its earlier removal is the historical
		// case the comment below describes).
		"middleware_gen.go": {},
		// pkg/app/wire_gen.go — service/worker/operator Deps wiring.
		"wire_gen.go": {},
		// pkg/app/services_gen.go — per-service serviceRow constructors
		// (registration-in-code; the user-owned pkg/app/services.go
		// picks which rows the binary serves).
		"services_gen.go": {},
		// pkg/middleware/procedures_gen.go — the RPCs a caller may reach with
		// no credentials, projected from the protos' `auth_required: false`
		// declarations. The interceptor reads it; the proto declares it.
		"procedures_gen.go": {},
		// Per-handler-directory codegen off proto descriptors.
		"handlers_gen.go": {},
		// Tier-1 per-RPC CRUD op constructors (projection half of the
		// CRUD split; the user-owned handlers_crud.go delegates to them).
		"handlers_crud_ops_gen.go": {},
		// internal/db/store_gen.go — the ORM delegates given an INTERFACE
		// shape (<Entity>Store per entity, plus an aggregate Store embedding
		// them). It is what a service's Deps field names, so packages stop
		// hand-writing a passthrough adapter over the free functions.
		"store_gen.go": {},
		// Legacy pre-split CRUD implementation file. No longer emitted,
		// but live skills may still name it in "legacy / still
		// recognized" context (e.g. audit-json's crud_stubs row).
		"handlers_crud_gen.go":      {},
		"handlers_crud_test_gen.go": {},
		"handlers_scaffold_test.go": {}, // post-1.x scaffolded test file
		"webhook_routes_gen.go":     {},
		// Retired codegen outputs — forge no longer emits any of these. Auth is
		// owned code + the forge/pkg/auth + forge/pkg/apikey libraries, audit is
		// the forge/pkg/audit library, and frontend components are owned
		// scaffold. Kept because migration skills still name them in
		// historical/removal context (those skills are exempt from this walk,
		// but the entries make the intent explicit).
		"auth_gen.go":     {}, // retired auth codegen (auth is now forge/pkg/auth + owned code)
		"audit_gen.go":    {}, // retired audit codegen (audit is now forge/pkg/audit)
		"frontend_gen.go": {}, // retired frontend codegen (components are owned scaffold now)
		// ORM / entity wrapper generation.
		"<entity>_orm_gen.go": {}, // placeholder pattern referenced in skills
		// The Tier-1 renames: every hash-guarded file now says _gen in its
		// NAME, so a reader can tell it is un-editable without opening it.
		"orm_shared_gen.go":  {},
		"user_orm_gen.go":    {}, // concrete <entity>_orm_gen.go in skill prose
		"things_mock_gen.go": {}, // concrete <svc>_mock_gen.go in skill prose
		// internal/handlers/mocks/<svc>_mock_gen.go — the generated Connect
		// service mock (codegen.GenerateMock), spelled as the placeholder.
		"<svc>_mock_gen.go":      {},
		"embed_gen.go":           {},
		"config_gen.go":          {},
		"mounts_services_gen.go": {},
		// The composition graph (internal/app, graph mode): the memoized
		// accessors and roots, and the per-audience route sets.
		"graph_gen.go":  {},
		"routes_gen.go": {},
		"root_gen.go":   {},
		// One command per root (cmd/<bin>/cmd, graph mode).
		"roots_gen.go": {},
		// Generic suffix references like `*_gen.go` are valid family-level
		// references (not specific filenames) — handled below by the regex.
	}

	// Filenames forge no longer emits but which may appear in skills strictly
	// in historical / removed context. Mentions outside such context still
	// fail. Detected by paragraph-scope keyword.
	historicalOnly := map[string]struct{}{
		"tracing_gen.go": {},
		"metrics_gen.go": {},
	}
	historicalKeywords := []string{
		"removed",
		"pre-1.",
		"no longer",
		"have been removed",
	}

	// Migration skills are off-limits to this test — they describe historical
	// shapes by design.
	migrationAllowlist := []string{
		// Every per-release migration skill (migrations/<version>/) is
		// exempt: describing the Tier-1 file shapes a release moved a
		// project OFF is the whole content of the document.
		"forge/migrations/",
		// The migration-* top-level skills (migration-cli,
		// migration-service, migration-upgrade) likewise document old
		// shapes during porting/upgrade work.
		"forge/migration-upgrade/",
		"forge/migration-service/",
		"forge/migration-cli/",
	}
	isMigration := func(p string) bool {
		for _, prefix := range migrationAllowlist {
			if strings.Contains(p, prefix) {
				return true
			}
		}
		return false
	}

	// Match `<word>_gen.go` filenames inside skill bodies. We match on the
	// underscore-prefixed `_gen.go` suffix to skip over directory references
	// like `gen/` and Go imports.
	re := regexp.MustCompile(`([A-Za-z0-9_<>]+)_gen\.go`)

	skillsRoot := path.Join("project", "skills", "forge")
	violations := map[string][]string{}

	walkErr := fs.WalkDir(templateFS, skillsRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(p, "SKILL.md") {
			return nil
		}
		if isMigration(p) {
			return nil
		}

		body, err := fs.ReadFile(templateFS, p)
		if err != nil {
			return err
		}

		paragraphs := strings.Split(string(body), "\n\n")
		seen := map[string]struct{}{}
		for _, para := range paragraphs {
			lower := strings.ToLower(para)
			isHistorical := false
			for _, kw := range historicalKeywords {
				if strings.Contains(lower, kw) {
					isHistorical = true
					break
				}
			}
			for _, m := range re.FindAllStringSubmatch(para, -1) {
				fname := m[1] + "_gen.go"
				if _, ok := allowedGenFiles[fname]; ok {
					continue
				}
				// Tolerate family-suffix wildcards (`*_gen.go`, `<x>_gen.go`).
				if strings.HasPrefix(fname, "*_") || strings.HasPrefix(m[1], "*") {
					continue
				}
				if _, ok := historicalOnly[fname]; ok && isHistorical {
					continue
				}
				seen[fname] = struct{}{}
			}
		}
		if len(seen) > 0 {
			names := make([]string, 0, len(seen))
			for n := range seen {
				names = append(names, n)
			}
			sort.Strings(names)
			violations[p] = names
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk skills tree: %v", walkErr)
	}

	if len(violations) == 0 {
		return
	}

	files := make([]string, 0, len(violations))
	for f := range violations {
		files = append(files, f)
	}
	sort.Strings(files)
	t.Errorf("skill catalog references *_gen.go filenames forge no longer emits.\n" +
		"Either add the filename to allowedGenFiles in this test (if forge emits it now) " +
		"or update the skill to drop the stale reference.")
	for _, f := range files {
		t.Errorf("  %s: %s", f, strings.Join(violations[f], ", "))
	}
}
