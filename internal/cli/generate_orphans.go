package cli

// generate_orphans.go — the files a switched-off feature leaves behind.
//
// Pinning a feature off (`features: {observability: false}`, `codegen:
// false`, ...) stops forge writing that feature's outputs. It does not delete
// what forge wrote before: the stale-file sweep keeps a file whose owning step
// is gated off, because "not written this run" proves nothing about a step
// that did not run, and the sweep itself is a codegen step. So those files sat
// on disk — compiled, linted, shipped — with nothing to say forge had stopped
// maintaining them.
//
// A full `forge generate` now lists them, and records the list in
// .forge/orphans.json (per checkout, not committed) together with the
// forge.yaml it was computed against. `forge project prune --orphans` reads
// that record and deletes only the files whose bytes are still exactly what
// forge rendered (the embedded forge:hash marker or the recorded certificate
// verifies). A file anyone edited is listed and kept, whatever the flags.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/config"
)

// orphansRel is the per-checkout record of the last full generate's orphans.
const orphansRel = ".forge/orphans.json"

// orphanRecord is .forge/orphans.json.
type orphanRecord struct {
	// ForgeYAML is the content hash of forge.yaml when the record was made.
	// Prune refuses a record made against a different forge.yaml: a
	// feature switched back on since owns its files again.
	ForgeYAML string `json:"forge_yaml"`
	// FeaturesOff names the features pinned off, for the report.
	FeaturesOff []string     `json:"features_off,omitempty"`
	Files       []orphanFile `json:"files"`
}

type orphanFile struct {
	Path string `json:"path"`
	// Edited is true when the bytes no longer verify as forge's render.
	// Prune never deletes an edited file.
	Edited bool `json:"edited,omitempty"`
}

// pinnedOffFeatures lists the features forge.yaml pins off, sorted.
func pinnedOffFeatures(cfg *config.ProjectConfig) []string {
	if cfg == nil {
		return nil
	}
	var off []string
	for _, name := range config.FeatureNames() {
		check, ok := featureChecks[string(name)]
		if ok && cfg.Features.Pinned(name) && !check(cfg.Features) {
			off = append(off, string(name))
		}
	}
	sort.Strings(off)
	return off
}

// recordOrphans computes this full run's orphans, writes .forge/orphans.json
// (or removes it when there are none) and prints the report. It runs only when
// a feature is pinned off: with every feature on, the stale sweep has already
// removed everything no step writes.
func recordOrphans(w io.Writer, ctx *pipelineContext) {
	path := filepath.Join(ctx.AbsPath, orphansRel)
	off := pinnedOffFeatures(ctx.Cfg)
	if len(off) == 0 {
		_ = os.Remove(path)
		return
	}
	var files []orphanFile
	for _, c := range staleCandidates(ctx) {
		files = append(files, orphanFile{Path: c.rel, Edited: !c.pristine})
	}
	if len(files) == 0 {
		_ = os.Remove(path)
		return
	}
	rec := orphanRecord{ForgeYAML: forgeYAMLHash(ctx.AbsPath), FeaturesOff: off, Files: files}
	if data, err := json.MarshalIndent(rec, "", "  "); err == nil {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
			_ = atomicfile.WriteFile(path, append(data, '\n'), 0o644)
		}
	}
	printOrphans(w, rec)
}

func printOrphans(w io.Writer, rec orphanRecord) {
	pristine := 0
	for _, f := range rec.Files {
		if !f.Edited {
			pristine++
		}
	}
	fmt.Fprintf(w, "\n🗂  %d orphaned file(s) — written by an earlier run; no step writes them while %s pinned off in forge.yaml:\n",
		len(rec.Files), featureList(rec.FeaturesOff))
	for _, f := range rec.Files {
		if f.Edited {
			fmt.Fprintf(w, "   - %s (edited — prune keeps it)\n", f.Path)
			continue
		}
		fmt.Fprintf(w, "   - %s\n", f.Path)
	}
	if pristine > 0 {
		fmt.Fprintf(w, "   Remove the %d unmodified one(s): %s project prune --orphans   (--dry-run to preview)\n", pristine, Name())
	}
}

func featureList(off []string) string {
	quoted := make([]string, len(off))
	for i, f := range off {
		quoted[i] = "features." + f
	}
	if len(quoted) == 1 {
		return quoted[0] + " is"
	}
	return strings.Join(quoted, ", ") + " are"
}

func forgeYAMLHash(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "forge.yaml"))
	if err != nil {
		return ""
	}
	return checksums.Hash(data)
}

// pruneResult is what `forge project prune --orphans` did (or would do).
type pruneResult struct {
	Removed []string
	Kept    []string // edited since forge wrote it, excluded, or disowned
	Gone    []string // already deleted
}

// pruneOrphans deletes the unmodified orphans .forge/orphans.json records.
// Each file is re-verified at delete time: its forge:hash marker (or its
// certificate in .forge/hashes/) must still certify the bytes, and it must not
// have become excluded or disowned since the record was made.
func pruneOrphans(root string, dryRun bool) (pruneResult, error) {
	var res pruneResult
	data, err := os.ReadFile(filepath.Join(root, orphansRel))
	if os.IsNotExist(err) {
		return res, nil
	} else if err != nil {
		return res, err
	}
	var rec orphanRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return res, fmt.Errorf("%s: %w — run `%s generate` to rebuild it", orphansRel, err, Name())
	}
	if rec.ForgeYAML != forgeYAMLHash(root) {
		return res, fmt.Errorf("forge.yaml changed since the last `%s generate` listed the orphans — run `%s generate` first, "+
			"so a feature switched back on keeps its files", Name(), Name())
	}
	cs, err := checksums.Load(root)
	if err != nil {
		return res, err
	}
	var left []orphanFile
	for _, f := range rec.Files {
		full := filepath.Join(root, filepath.FromSlash(f.Path))
		content, rerr := os.ReadFile(full)
		if os.IsNotExist(rerr) {
			res.Gone = append(res.Gone, f.Path)
			continue
		} else if rerr != nil {
			return res, rerr
		}
		_, excluded := checksums.ExcludedBy(root, f.Path)
		if excluded || cs.IsDisowned(f.Path) || !orphanPristine(cs, f.Path, content) {
			res.Kept = append(res.Kept, f.Path)
			left = append(left, orphanFile{Path: f.Path, Edited: true})
			continue
		}
		if !dryRun {
			if err := os.Remove(full); err != nil {
				return res, fmt.Errorf("remove %s: %w", f.Path, err)
			}
			pruneEmptyDirsUpTo(filepath.Dir(full), root)
			delete(cs.Unstampable, f.Path)
		}
		res.Removed = append(res.Removed, f.Path)
	}
	if dryRun {
		return res, nil
	}
	if err := checksums.Save(root, cs); err != nil {
		return res, err
	}
	rec.Files = left
	if len(left) == 0 {
		return res, os.Remove(filepath.Join(root, orphansRel))
	}
	out, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return res, err
	}
	return res, atomicfile.WriteFile(filepath.Join(root, orphansRel), append(out, '\n'), 0o644)
}

// orphanPristine reports whether content is still exactly what forge
// rendered at relPath: its embedded marker verifies, or — for a format that
// carries none — its body matches the recorded certificate.
func orphanPristine(cs *checksums.FileChecksums, relPath string, content []byte) bool {
	switch checksums.Verify(content) {
	case checksums.Pristine:
		return true
	case checksums.Modified:
		return false
	}
	recorded, ok := cs.Unstampable[relPath]
	return ok && checksums.BodyHash(content) == recorded
}
