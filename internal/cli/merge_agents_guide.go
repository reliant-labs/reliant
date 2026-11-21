package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/pathglob"
)

// mergeAgentsGuide separates the generated guide from the project's own notes.
// The guide and its ledger entry merge independently, so keep the guide named
// by the merged ledger. The next generate can then rebuild the guide from both
// sides' inputs without mistaking it for a hand edit. Without complete proof,
// merge the whole document as plain text; absence is not ownership evidence.
func mergeAgentsGuide(ctx context.Context, relPath string, base, ours, theirs []byte, markerSize int) ([]byte, bool, string, error) {
	plain := func() ([]byte, bool, string, error) {
		merged, clean, err := plainMerge(ctx, base, ours, theirs, markerSize)
		return merged, clean, "", err
	}
	rel, ok := projectRelative(relPath)
	if !ok || rel != generator.AgentsGuidePath {
		return plain()
	}
	other, bases, ok := currentMergeSides(ctx)
	if !ok || len(bases) != 1 {
		// Synthetic or ambiguous bases are not suitable provenance.
		return plain()
	}
	prefix := strings.TrimSuffix(filepath.ToSlash(relPath), rel)
	entryPath := prefix + checksums.ScaffoldEntryPath(rel)
	b, ok := provedAgentsGuide(ctx, bases[0], prefix, base)
	if !ok {
		return plain()
	}
	o, ok := provedAgentsGuide(ctx, "HEAD", prefix, ours)
	if !ok {
		return plain()
	}
	t, ok := provedAgentsGuide(ctx, other, prefix, theirs)
	if !ok {
		return plain()
	}
	entry, ok := checksums.MergeStateFile(entryPath, b.entry, o.entry, t.entry)
	if !ok {
		return plain()
	}
	guide, ok := guideForMergedCertificate(entry, o.guide, t.guide)
	if !ok {
		return plain()
	}

	var notes []byte
	clean := true
	switch {
	case bytes.Equal(o.notes, t.notes), bytes.Equal(t.notes, b.notes):
		notes = o.notes
	case bytes.Equal(o.notes, b.notes):
		notes = t.notes
	default:
		var err error
		notes, clean, err = plainMerge(ctx, b.notes, o.notes, t.notes, markerSize)
		if err != nil {
			return nil, false, "", err
		}
	}
	var note string
	if !bytes.Equal(o.guide, t.guide) {
		note = "kept a certified forge guide; run `forge generate` to refresh it from both sides' inputs (Project Notes merge as text)"
	}
	return bytes.Join([][]byte{guide, notes}, nil), clean, note, nil
}

// Choose only a prefix the resulting entry certifies, regardless of how the
// ledger represents or combines metadata. A certificate for neither side
// cannot authorize discarding either side's guide.
func guideForMergedCertificate(entry []byte, guides ...[]byte) ([]byte, bool) {
	var proof checksums.ScaffoldEntry
	if json.Unmarshal(entry, &proof) != nil || proof.Declined != "" || proof.ForgePartHash == "" {
		return nil, false
	}
	for _, guide := range guides {
		if checksums.Hash(guide) == proof.ForgePartHash {
			return guide, true
		}
	}
	return nil, false
}

type agentsGuideSide struct {
	guide, notes, entry []byte
}

// Check the blob, certificate and opt-outs in the same commit, never against
// the working tree or an already-merged ledger. Git's driver does not name
// the blobs' commits, so only use the sides when the snapshot matches exactly.
func provedAgentsGuide(ctx context.Context, commit, prefix string, blob []byte) (agentsGuideSide, bool) {
	var side agentsGuideSide
	rel := generator.AgentsGuidePath
	guide, notes, ok := generator.SplitAgentsGuide(blob)
	if !ok {
		return side, false
	}
	actual, ok := gitFileAt(ctx, commit, prefix+rel)
	if !ok || !bytes.Equal(actual, blob) {
		return side, false
	}
	record, ok := gitFileAt(ctx, commit, prefix+checksums.DisownedFile)
	if !ok || disownedOnAnySide(rel, [][]byte{record}) {
		return side, false
	}
	cfgData, ok := gitFileAt(ctx, commit, prefix+"forge.yaml")
	if !ok || len(cfgData) == 0 {
		return side, false
	}
	var cfg struct {
		Harness  string                `yaml:"harness"`
		Scaffold config.ScaffoldConfig `yaml:"scaffold"`
	}
	if yaml.Unmarshal(cfgData, &cfg) != nil || generator.Harness(cfg.Harness) == generator.HarnessNone {
		return side, false
	}
	for _, glob := range cfg.Scaffold.Exclude {
		if pathglob.Validate(glob) != nil || pathglob.Match(pathglob.Normalize(glob), rel) {
			return side, false
		}
	}
	entry, ok := gitFileAt(ctx, commit, prefix+checksums.ScaffoldEntryPath(rel))
	if !ok {
		return side, false
	}
	if _, certified := guideForMergedCertificate(entry, guide); !certified {
		return side, false
	}
	return agentsGuideSide{guide: guide, notes: notes, entry: entry}, true
}

func isAgentsGuideMergePath(relPath string) bool {
	return path.Base(filepath.ToSlash(relPath)) == generator.AgentsGuidePath
}
