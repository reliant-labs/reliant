package generator

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/templates"
	"github.com/reliant-labs/forge/pkg/seedplan"
)

// fixtureFreshnessData is what the manifest + guard templates render from.
// Every field is constant for a frontend: the committed manifest and guard
// never change when a migration does (see FixtureMarker for where the
// fingerprint went).
type fixtureFreshnessData struct {
	// ProjectRootRelative is the relative path from <frontend>/src/mocks/
	// back to the project root.
	ProjectRootRelative string
	// MarkerRel is the project-relative path of this frontend's marker
	// (FixtureMarkerPath).
	MarkerRel string
	// HasSchema selects the real input-hashing guard over the
	// no-migrations-yet placeholder.
	HasSchema bool
}

// FixtureMarkerDir holds, per frontend, the record of what the last `forge
// generate` IN THIS CHECKOUT wrote its mock fixtures from. It is under .forge/,
// which every forge project ignores, so it is never committed.
//
// WHY NOT COMMITTED. The record used to be SEED_FINGERPRINT in the committed
// fixture-manifest_gen.ts: one digest over every migration. Every branch that
// added a migration rewrote that one line, so any two such branches conflicted
// there — control-plane's manifest conflicted in 45 merges in the week to
// 2026-10-30 — and a digest of a list cannot be split per input, because two
// new migrations always sort to the same place (the end). It is tooling-only
// state, needed by no compiler, so it follows the descriptor out of git
// (#1572): the committed manifest keeps only constants, and the fingerprint
// lives here.
//
// The guard stays useful without it. The marker also records the bytes of the
// fixture files it describes, so the guard can tell its two cases apart:
//
//   - the fixtures on disk are the ones this checkout's last generate wrote,
//     and the seed inputs moved since → STALE, fail with the fix;
//   - no marker (a fresh clone, CI), or fixtures that differ from what the
//     marker recorded (pulled or checked out since) → cannot verify; skip,
//     naming `forge generate`. Committed fixtures are checked against their
//     inputs by `forge ci verify-generated`, which regenerates them.
const FixtureMarkerDir = ".forge/fixtures"

// FixtureMarkerPath is the project-relative marker of the frontend at feRel.
func FixtureMarkerPath(feRel string) string {
	return FixtureMarkerDir + "/" + filepath.ToSlash(filepath.Clean(feRel)) + "/generated-from.json"
}

// fixtureMarker is the marker's shape. The guard reads seed_fingerprint and
// fixtures; seed_config_fingerprint (which folds in forge.yaml's seed salt and
// row counts, which no file-reading process can see) is kept for a person or
// a later generate asking which dataset the fixtures came from.
type fixtureMarker struct {
	SeedFingerprint       string            `json:"seed_fingerprint"`
	SeedFingerprintFiles  int               `json:"seed_fingerprint_files"`
	SeedConfigFingerprint string            `json:"seed_config_fingerprint"`
	Fixtures              map[string]string `json:"fixtures"`
}

// writeFixtureMarker records files/cfg and the sha256 of every *_gen.* file
// this run wrote into mocksRel (by base name), so it must run after
// everything this run writes there.
func writeFixtureMarker(root, feRel, mocksRel string, files, cfg seedplan.Fingerprint) error {
	m := fixtureMarker{
		SeedFingerprint:       files.Hex,
		SeedFingerprintFiles:  files.Files,
		SeedConfigFingerprint: cfg.Hex,
		Fixtures:              map[string]string{},
	}
	entries, err := os.ReadDir(filepath.Join(root, mocksRel))
	if err != nil {
		return err
	}
	full := filepath.Join(root, filepath.FromSlash(FixtureMarkerPath(feRel)))
	checksums.RecordPreWriteAbs(full)
	for _, e := range entries {
		if e.IsDir() || !strings.Contains(e.Name(), "_gen.") {
			continue
		}
		rel := filepath.Join(mocksRel, e.Name())
		if !checksums.WasWrittenThisRun(rel) {
			if checksums.IsTier1Target(rel) {
				// This run rendered the file but left the bytes on disk
				// alone (a pristine older render, without --heal). They
				// are not what these inputs produce, so nothing can be
				// vouched for: no marker, and the guard skips.
				_ = os.Remove(full)
				return nil
			}
			continue // not this run's output (a leftover the sweep retires)
		}
		body, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		m.Fixtures[e.Name()] = hex.EncodeToString(sum[:])
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return atomicfile.WriteFile(full, append(data, '\n'), 0o644)
}

// fixtureFreshnessFiles are the manifest and the guard, under <fe>/src/mocks/.
var fixtureFreshnessFiles = [2]string{"fixture-manifest_gen.ts", "fixture-freshness_gen.test.ts"}

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
// # Why it is emitted only beside fixtures
//
// fixtures is how many per-entity fixture modules EmitFrontendMockSurface just
// wrote for this frontend. With none, the pair is not emitted, and a copy left
// by an earlier run is retired through the ownership test (codegen.
// RetireGenerated): a certified copy goes, a disowned or hand-edited one stays.
//
// The guard is a cache's validity tag, and a frontend with no fixtures has no
// cache. That is the common shape of an ADOPTED frontend: its services are
// custom rpcs with no CRUD entity, so its mock transport dispatches nothing
// from src/mocks/<entity>_gen.ts. Emitting the pair anyway did not check
// anything; it made every migration a change to a generated frontend file, and
// a branch that merged main's migrations without a regenerate turned the
// frontend's `npm test` red over a fingerprint that described nothing. With
// parallel lanes landing migrations, that was every few merges.
//
// The file set therefore changes shape when the first entity fixture lands —
// the same generate that writes the fixtures writes their tag.
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
func EmitFixtureFreshnessSurface(root, feRel string, fixtures int, files, cfg seedplan.Fingerprint, cs *FileChecksums) error {
	mocksRel := filepath.Join(feRel, "src", "mocks")

	marker := filepath.Join(root, filepath.FromSlash(FixtureMarkerPath(feRel)))
	if fixtures == 0 {
		for _, out := range fixtureFreshnessFiles {
			codegen.RetireGenerated(root, filepath.Join(mocksRel, out), cs)
		}
		checksums.RecordPreWriteAbs(marker)
		_ = os.Remove(marker)
		return nil
	}

	data := fixtureFreshnessData{
		ProjectRootRelative: projectRootFromMocksDir(mocksRel),
		MarkerRel:           FixtureMarkerPath(feRel),
		HasSchema:           !files.Empty(),
	}

	for _, f := range []struct{ tmpl, out string }{
		{"fixture-manifest.ts.tmpl", fixtureFreshnessFiles[0]},
		{"fixture-freshness.test.ts.tmpl", fixtureFreshnessFiles[1]},
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
	if err := writeFixtureMarker(root, feRel, mocksRel, files, cfg); err != nil {
		return fmt.Errorf("write %s: %w", FixtureMarkerPath(feRel), err)
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
