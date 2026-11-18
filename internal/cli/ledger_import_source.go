package cli

// Reading a ledger that lives somewhere else, so it can be imported.
//
// Two sources, one shape. `--from-git` reads the RETIRED in-checkout ledger
// (.forge/releases/*.json and .forge/promotions/<env>.jsonl) as committed at
// a rev; `--from-file-ledger` reads a machine ledger directory (§12.2).
// Both produce a ledgerSource, and everything downstream — the plan, the
// refusal, the apply — is written once against that.
//
// AT A REV, NEVER THE WORKING TREE. The retired ledger was committed, so the
// authority is what the repository holds, and the working tree is the one
// copy that can be wrong in both directions: a `rm -rf .forge/promotions`
// that was never committed would make the import see nothing while the
// history is intact, and a half-written local edit would import records
// nobody reviewed. `--rev origin/main` is the normal invocation for exactly
// that reason: it names the branch the ledger commits landed on.
//
// EVERY RECORD CARRIES WHERE IT CAME FROM. `git ls-tree` hands back the blob
// sha next to each name, so the provenance is "git:<path>@<blob sha>" — the
// file AND the exact bytes — with no extra git call. That is what makes an
// import auditable after the files are deleted (§11.1 step 9), and it is why
// the reader keeps the blob sha rather than discarding it after reading.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/pkg/release"
)

// ledgerSource is a ledger read from somewhere other than the selected
// store: every release it holds, and every env's promotions in the order
// they were made.
type ledgerSource struct {
	// Label names the source for human output ("origin/main", a path).
	Label string
	// Releases are keyed by their CONTENTS, not their filename — see
	// readGitReleases.
	Releases []sourceRelease
	// Promotions is per env, OLDEST FIRST by promoted_at.
	Promotions map[string][]sourcePromotion
}

// sourceRelease is one release plus where its bytes came from.
type sourceRelease struct {
	Release release.Release
	From    string
}

// sourcePromotion is one promotion plus where its line came from.
type sourcePromotion struct {
	Promotion release.Promotion
	From      string
}

// envs is every environment the source holds promotions for, sorted.
func (s ledgerSource) envs() []string {
	out := make([]string, 0, len(s.Promotions))
	for env := range s.Promotions {
		out = append(out, env)
	}
	sort.Strings(out)
	return out
}

// promotionCount is every promotion across every env.
func (s ledgerSource) promotionCount() int {
	n := 0
	for _, ps := range s.Promotions {
		n += len(ps)
	}
	return n
}

// empty reports whether there is nothing to import.
func (s ledgerSource) empty() bool { return len(s.Releases) == 0 && s.promotionCount() == 0 }

// ─── The git source ─────────────────────────────────────────────────────────

// readGitLedger reads the retired in-checkout ledger as committed at rev.
func readGitLedger(ctx context.Context, projectDir, rev string) (ledgerSource, error) {
	prefix, isGit := gitPrefix(ctx, projectDir)
	if !isGit {
		return ledgerSource{}, fmt.Errorf("%s is not a git checkout, so there is no rev to read a ledger at.\n"+
			"  Use `--from-file-ledger <dir>` to import a ledger directory instead", projectDir)
	}
	// Resolved up front so the error names the rev the user typed rather
	// than surfacing later as "path does not exist in tree".
	if _, err := gitLedgerCmd(ctx, projectDir, "rev-parse", "--verify", rev+"^{commit}"); err != nil {
		return ledgerSource{}, fmt.Errorf("resolve --rev %q: %w.\n"+
			"  `git fetch origin` first if you are naming a remote branch", rev, err)
	}

	src := ledgerSource{Label: rev, Promotions: map[string][]sourcePromotion{}}
	releases, err := readGitReleases(ctx, projectDir, prefix, rev)
	if err != nil {
		return ledgerSource{}, err
	}
	src.Releases = releases
	if err := readGitPromotions(ctx, projectDir, prefix, rev, src.Promotions); err != nil {
		return ledgerSource{}, err
	}
	return src, nil
}

// readGitReleases reads every .forge/releases/*.json at rev.
//
// KEYED BY CONTENTS, NOT BY FILENAME, and control-plane's own ledger is why.
// The retired writer flattened a version into a filename with a lossy
// mapping, so `v1_0_0.json` holds the release `v1.0.0` — the file does not
// name its own version. Reading the version from the filename would import
// a release called "v1_0_0" that no promotion references, and prod's first
// promotion would point at a release the ledger does not contain.
func readGitReleases(ctx context.Context, projectDir, prefix, rev string) ([]sourceRelease, error) {
	entries, err := gitTreeEntries(ctx, projectDir, rev, prefix+retiredReleasesDirRel)
	if err != nil {
		// A missing directory is an empty list: a project may have
		// promotions and no release files, or neither.
		return nil, nil
	}
	var out []sourceRelease
	for _, e := range entries {
		if !strings.HasSuffix(e.name, ".json") {
			continue
		}
		data, err := gitLedgerCmd(ctx, projectDir, "cat-file", "blob", e.blob)
		if err != nil {
			return nil, fmt.Errorf("read %s at %s: %w", e.path, rev, err)
		}
		r, err := decodeRetiredRelease(data)
		if err != nil {
			return nil, fmt.Errorf("%s at %s: %w", e.path, rev, err)
		}
		out = append(out, sourceRelease{Release: r, From: gitProvenance(e.path, e.blob)})
	}
	// Sorted by version for a stable plan, since a tree listing is
	// lexical ("v1.5.10" before "v1.5.2") and a release's order does not
	// matter to the import: releases are keyed, not sequenced.
	sortSourceReleases(out)
	return out, nil
}

// readGitPromotions reads every .forge/promotions/<env>.jsonl at rev into
// out, oldest first.
func readGitPromotions(ctx context.Context, projectDir, prefix, rev string, out map[string][]sourcePromotion) error {
	entries, err := gitTreeEntries(ctx, projectDir, rev, prefix+retiredPromotionsDirRel)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.name, ".jsonl") {
			continue
		}
		data, err := gitLedgerCmd(ctx, projectDir, "cat-file", "blob", e.blob)
		if err != nil {
			return fmt.Errorf("read %s at %s: %w", e.path, rev, err)
		}
		promotions, err := decodeRetiredPromotions(data, e.path, rev, gitProvenance(e.path, e.blob))
		if err != nil {
			return err
		}
		for _, p := range promotions {
			env := p.Promotion.Env
			out[env] = append(out[env], p)
		}
	}
	for env := range out {
		sortSourcePromotions(out[env])
	}
	return nil
}

// gitProvenance is the imported_from marker the design names: the file and
// the exact bytes.
func gitProvenance(path, blob string) string { return "git:" + path + "@" + blob }

// treeEntry is one `git ls-tree` row.
type treeEntry struct {
	blob string
	name string
	path string
}

// gitTreeEntries lists one directory at a rev, with each entry's blob sha.
//
// ONE git call for the whole directory, names AND content addresses. The
// alternative — list, then `git show` each path — costs one process per file
// and still would not tell you which blob you read.
func gitTreeEntries(ctx context.Context, projectDir, rev, dir string) ([]treeEntry, error) {
	out, err := gitLedgerCmd(ctx, projectDir, "ls-tree", "-z", rev+":"+dir)
	if err != nil {
		return nil, err
	}
	var entries []treeEntry
	for _, row := range strings.Split(string(out), "\x00") {
		// `<mode> SP <type> SP <object> TAB <name>`
		tab := strings.IndexByte(row, '\t')
		if tab < 0 {
			continue
		}
		fields := strings.Fields(row[:tab])
		if len(fields) != 3 || fields[1] != "blob" {
			continue
		}
		name := row[tab+1:]
		entries = append(entries, treeEntry{blob: fields[2], name: name, path: dir + "/" + name})
	}
	return entries, nil
}

// ─── The file-ledger source ─────────────────────────────────────────────────

// readFileLedger reads a machine ledger directory (§12.2) as a source.
//
// This is §12.4's path: an env whose KCL gained a control plane keeps a
// non-empty machine ledger, and its lines ARE the import payload — the two
// stores serialize the same canonical JSON, so nothing is translated.
func readFileLedger(dir string) (ledgerSource, error) {
	releasesPath := filepath.Join(dir, "releases.jsonl")
	promotionsDir := filepath.Join(dir, "promotions")
	_, releasesErr := os.Stat(releasesPath)
	_, promotionsErr := os.Stat(promotionsDir)
	if releasesErr != nil && promotionsErr != nil {
		// Pointing this at a CHECKOUT is the likely mistake, and it has
		// its own flag, so say which one.
		if _, err := os.Stat(filepath.Join(dir, retiredPromotionsDirRel)); err == nil {
			return ledgerSource{}, fmt.Errorf("%s looks like a checkout, not a ledger directory: it holds %s.\n"+
				"  Use `--from-git` to import a checkout's committed ledger", dir, retiredPromotionsDirRel)
		}
		return ledgerSource{}, fmt.Errorf("%s holds no ledger: expected releases.jsonl and/or promotions/<env>.jsonl (the layout under $%s/<project-id>/)",
			dir, "FORGE_LEDGER_HOME")
	}

	src := ledgerSource{Label: dir, Promotions: map[string][]sourcePromotion{}}
	if releasesErr == nil {
		data, err := os.ReadFile(releasesPath) //nolint:gosec // an operator-supplied ledger directory
		if err != nil {
			return ledgerSource{}, fmt.Errorf("read %s: %w", releasesPath, err)
		}
		for i, line := range ledgerLines(string(data)) {
			var r release.Release
			if err := json.Unmarshal([]byte(line), &r); err != nil {
				return ledgerSource{}, fmt.Errorf("%s line %d: %w", releasesPath, i+1, err)
			}
			src.Releases = append(src.Releases, sourceRelease{Release: r, From: fileProvenance(releasesPath, i+1)})
		}
	}
	if promotionsErr == nil {
		logs, err := filepath.Glob(filepath.Join(promotionsDir, "*.jsonl"))
		if err != nil {
			return ledgerSource{}, err
		}
		sort.Strings(logs)
		for _, path := range logs {
			data, err := os.ReadFile(path) //nolint:gosec // an operator-supplied ledger directory
			if err != nil {
				return ledgerSource{}, fmt.Errorf("read %s: %w", path, err)
			}
			for i, line := range ledgerLines(string(data)) {
				var p release.Promotion
				if err := json.Unmarshal([]byte(line), &p); err != nil {
					return ledgerSource{}, fmt.Errorf("%s line %d: %w", path, i+1, err)
				}
				if err := p.Validate(); err != nil {
					return ledgerSource{}, fmt.Errorf("%s line %d: %w", path, i+1, err)
				}
				src.Promotions[p.Env] = append(src.Promotions[p.Env],
					sourcePromotion{Promotion: p, From: fileProvenance(path, i+1)})
			}
		}
		for env := range src.Promotions {
			sortSourcePromotions(src.Promotions[env])
		}
	}
	// A re-cut appends, so the newest line for a version wins — the same
	// rule the store itself applies when reading releases.jsonl back.
	src.Releases = collapseByVersion(src.Releases)
	sortSourceReleases(src.Releases)
	return src, nil
}

// fileProvenance is the imported_from marker for a file-ledger line. A path
// plus a line number rather than a content address, because a jsonl file has
// no per-line object sha — which is exactly why the git form is preferred
// when both are available.
func fileProvenance(path string, line int) string {
	return fmt.Sprintf("file-ledger:%s#%d", path, line)
}

// collapseByVersion keeps the LAST release recorded for each version.
func collapseByVersion(all []sourceRelease) []sourceRelease {
	byVersion := map[string]sourceRelease{}
	order := make([]string, 0, len(all))
	for _, r := range all {
		if _, seen := byVersion[r.Release.Version]; !seen {
			order = append(order, r.Release.Version)
		}
		byVersion[r.Release.Version] = r
	}
	out := make([]sourceRelease, 0, len(order))
	for _, v := range order {
		out = append(out, byVersion[v])
	}
	return out
}

// ─── Decoding the retired formats ───────────────────────────────────────────

// decodeRetiredRelease reads one .forge/releases/<stem>.json.
//
// The retired writer wrapped a release in a statefile envelope on some
// versions and wrote it bare on others, so both are accepted — and both
// carry the version INSIDE, which is what makes the file's own name
// irrelevant.
func decodeRetiredRelease(data []byte) (release.Release, error) {
	var envelope struct {
		Release *release.Release `json:"release"`
	}
	// The bare form's `release` field is the VERSION STRING, so the
	// envelope decode fails on it with a type error — which is the
	// discriminator, not a problem.
	if err := json.Unmarshal(data, &envelope); err == nil && envelope.Release != nil && envelope.Release.Version != "" {
		return *envelope.Release, nil
	}
	var r release.Release
	if err := json.Unmarshal(data, &r); err != nil {
		return release.Release{}, fmt.Errorf("decode as a release: %w", err)
	}
	if r.Version == "" {
		return release.Release{}, fmt.Errorf("names no release version, so nothing can reference it")
	}
	return r, nil
}

// decodeRetiredPromotions reads one .forge/promotions/<env>.jsonl.
//
// EVERY LINE MUST DECODE. The unimported-checkout detector in
// ledger_unimported.go deliberately SKIPS a malformed line, because its job
// is only to notice that history exists and refusing to run over one archived
// line would wedge a project. The import is the opposite: it is the thing
// that makes the history authoritative, so a line it cannot read is a line
// that would be silently lost, and the record it was about would be gone for
// good once §11.1 step 9 deletes the files.
func decodeRetiredPromotions(data []byte, path, rev, from string) ([]sourcePromotion, error) {
	var out []sourcePromotion
	for i, line := range ledgerLines(string(data)) {
		var p release.Promotion
		if err := json.Unmarshal([]byte(line), &p); err != nil {
			return nil, fmt.Errorf("%s at %s, line %d: %w.\n"+
				"  Every line must be readable: an import is what makes this history authoritative, "+
				"so a line it skipped would be lost", path, rev, i+1, err)
		}
		if err := p.Validate(); err != nil {
			return nil, fmt.Errorf("%s at %s, line %d: %w", path, rev, i+1, err)
		}
		if p.ID == "" {
			return nil, fmt.Errorf("%s at %s, line %d: the promotion has no id.\n"+
				"  An import preserves ids, because that is how a re-run recognises what it already recorded",
				path, rev, i+1)
		}
		out = append(out, sourcePromotion{Promotion: p, From: from})
	}
	return out, nil
}

// ─── Ordering ───────────────────────────────────────────────────────────────

// sortSourcePromotions orders one env's promotions OLDEST FIRST by
// promoted_at.
//
// THE ORDER IS THE RECORD. An append-only log's current binding is its last
// line, so importing prod's history out of order would leave prod reading
// whichever release happened to sort last — and `forge env status prod` would
// name a release prod stopped running months ago. promoted_at is the
// authority (§11.1 step 6), with the id as a tiebreak so two promotions in
// the same second import deterministically on every machine.
func sortSourcePromotions(ps []sourcePromotion) {
	sort.SliceStable(ps, func(i, j int) bool {
		a, b := ps[i].Promotion, ps[j].Promotion
		if !a.PromotedAt.Equal(b.PromotedAt) {
			return a.PromotedAt.Before(b.PromotedAt)
		}
		return a.ID < b.ID
	})
}

// sortSourceReleases orders releases by version, for a stable plan.
func sortSourceReleases(rs []sourceRelease) {
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].Release.Version < rs[j].Release.Version })
}
