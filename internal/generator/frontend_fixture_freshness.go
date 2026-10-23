package generator

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/reliant-labs/forge/internal/templates"
	"github.com/reliant-labs/forge/pkg/seedplan"
)

// fixtureFreshnessData is what the manifest + guard templates render from.
type fixtureFreshnessData struct {
	// SeedFingerprint is the digest of the INPUT FILES the fixtures were
	// generated from (migrations + vocab), or "" when there were none to
	// read.
	//
	// This is deliberately the file-only digest, NOT the config-folded one:
	// it is the value the emitted TypeScript guard recomputes, and the
	// guard can only read files. Recording a digest with the seed config
	// folded in would be a value nothing at `npm test` time can reproduce,
	// so the comparison would fail on every clean tree — which is exactly
	// what happened the first time this shipped.
	SeedFingerprint string
	// SeedConfigFingerprint folds the seed config (salt, row counts) into
	// the digest above. Salt and row counts reshuffle every fixture value
	// without touching a migration, so the file digest alone cannot see
	// them move. It is compared Go-side, on the next `forge generate`,
	// rather than by the TypeScript guard.
	SeedConfigFingerprint string
	// SeedFingerprintFiles is how many input files that digest covers.
	SeedFingerprintFiles int
	// ProjectRootRelative is the relative path from <frontend>/src/mocks/
	// back to the project root.
	ProjectRootRelative string
	// HasSchema selects the real input-hashing guard over the
	// no-migrations-yet placeholder.
	HasSchema bool
}

// EmitFixtureFreshnessSurface writes the two files that make a stale fixture a
// LOUD failure instead of a silent one, into <feRel>/src/mocks/:
//
//   - fixture-manifest_gen.ts — the provenance record: which schema the
//     fixtures beside it were generated from.
//   - fixture-freshness_gen.test.ts — a vitest that re-derives that
//     fingerprint from db/migrations on disk and fails when the two disagree.
//
// # Why a fingerprint rather than fixtures computed at runtime
//
// Computing the fixtures where they are USED would be strictly better, and it
// is not possible. Their values come from seedplan.Plan, which is built from
// []schemadef.Table, which exists only by applying db/migrations to a real
// postgres and introspecting it. The two consumers — a browser in mock mode
// and vitest under `npm test` — have no database and, in the browser's case,
// cannot have one. Emitting the fixtures as literals is therefore forced.
//
// What is NOT forced is emitting them with no way to tell when they went out
// of date. The values need a database; the QUESTION they answer is a hash of
// two files, and anything that can read the project directory can recompute
// that. So the fixtures stay a cache, and this is the cache's validity tag: a
// schema edit without a regenerate now fails `npm test` (and `task test` in
// CI) with the fix command, instead of silently serving rows for a schema the
// project no longer has.
//
// # Why it is emitted even when there is nothing to check
//
// A project with no migrations gets the placeholder branch, which asserts the
// manifest records no fingerprint. Emitting the pair unconditionally keeps the
// scaffold's shape stable from birth — the frontend's file set does not change
// shape the first time someone writes a migration — and means the claim "no
// schema here" is CHECKED rather than assumed.
//
// files is the digest of the seed INPUT FILES alone, and cfg the same digest
// with the seed config folded in (codegen.SeedFingerprints returns the pair).
// They are separate parameters, and separate constants in the emitted
// manifest, because only ONE of them is reproducible without a database:
//
//   - files is what the TypeScript guard recomputes. It hashes migrations and
//     vocab.yaml, which a vitest process can read.
//   - cfg additionally covers the salt and row counts out of forge.yaml, which
//     the guard has no way to reach.
//
// Recording cfg where the guard expects files makes the comparison fail on
// every clean tree. That is not hypothetical: this shipped that way once, and
// `npm test` on a freshly generated project reported STALE MOCK FIXTURES
// against fixtures that had just been written.
func EmitFixtureFreshnessSurface(root, feRel string, files, cfg seedplan.Fingerprint, cs *FileChecksums) error {
	mocksRel := filepath.Join(feRel, "src", "mocks")

	data := fixtureFreshnessData{
		SeedFingerprint:       files.Hex,
		SeedConfigFingerprint: cfg.Hex,
		SeedFingerprintFiles:  files.Files,
		ProjectRootRelative:   projectRootFromMocksDir(mocksRel),
		HasSchema:             !files.Empty(),
	}

	for _, f := range []struct{ tmpl, out string }{
		{"fixture-manifest.ts.tmpl", "fixture-manifest_gen.ts"},
		{"fixture-freshness.test.ts.tmpl", "fixture-freshness_gen.test.ts"},
	} {
		content, err := templates.FrontendTemplates().Get(filepath.Join("mocks", f.tmpl))
		if err != nil {
			return fmt.Errorf("read %s: %w", f.tmpl, err)
		}
		tmpl, err := template.New(f.tmpl).Funcs(templates.FuncMap()).Parse(string(content))
		if err != nil {
			return fmt.Errorf("parse %s: %w", f.tmpl, err)
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, data); err != nil {
			return fmt.Errorf("render %s: %w", f.tmpl, err)
		}
		if _, err := WriteGeneratedFileTier1(root, filepath.Join(mocksRel, f.out), buf.Bytes(), cs, true); err != nil {
			return fmt.Errorf("write %s: %w", f.out, err)
		}
	}
	return nil
}

// projectRootFromMocksDir returns the relative path from <feRel>/src/mocks/
// back to the project root — one ".." per path segment.
//
// Derived from the frontend's ACTUAL configured path rather than hardcoded to
// "../../../..", because forge.yaml can place a frontend at any depth. A wrong
// number of segments is the worst possible bug here: the guard would look for
// db/migrations somewhere it isn't, find nothing, and pass — a staleness
// detector that has been silently disabled.
func projectRootFromMocksDir(mocksRel string) string {
	depth := len(strings.Split(filepath.ToSlash(filepath.Clean(mocksRel)), "/"))
	return strings.TrimSuffix(strings.Repeat("../", depth), "/")
}
