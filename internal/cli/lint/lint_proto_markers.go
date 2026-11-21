// File: internal/cli/lint/lint_proto_markers.go
//
// proto-markers — `forge lint --proto-markers`.
//
// A `forge:*` PROTO comment marker that is misspelled, or simply not a
// marker forge recognizes, does NOTHING today — the comment is valid proto,
// buf compiles it, and forge's scanners look for exact known markers and
// find none. `// forge:server-set` (the spelling forge used before the
// marker was renamed to `forge:read-only`) is the motivating case: it reads
// as an ordinary comment, the field stays client-writable on Create and
// Update, and the birth exits zero with no warning. The author believes they
// declared something; nothing did.
//
// That is the same silent-failure shape the sibling column-markers check
// (lint_column_markers.go) exists to prevent, one layer up: SQL catalog
// comments there, proto comments here.
//
// This check flags any `//` comment in a .proto file whose text contains
// `forge:` but matches none of codegen.KnownProtoMarkers — the SAME registry
// the scanners themselves spell their markers from and that
// `forge project annotations` dumps, so the marker vocabulary this check
// enforces and the vocabulary forge actually reads cannot drift apart.
//
// Severity is warning, never gating: an unrecognized marker might be a future
// forge version's, or simply prose that happens to contain "forge:" in an
// unrelated comment ("see forge:entity docs", a URL, a commit reference). The
// finding is a nudge to check the spelling, not a hard failure — the same
// trade the column check makes explicitly.
//
// FALSE POSITIVES the scan deliberately avoids, beyond that blanket
// acceptance:
//
//   - Entity-level (leading comment above `message X {`) and field-level
//     (leading OR trailing on the field) markers live in different comment
//     positions, and BOTH are legitimate. This check therefore validates the
//     marker NAME only and never its position — a placement rule would have
//     to model the proto grammar to avoid being wrong, and the birth path
//     already refuses an unconsumed forge:read-only by name and line.
//   - Different passes read different markers (forge:mutation on every
//     generate, forge:secret in the descriptor path only, the rest at birth).
//     The registry is the UNION across passes, so a marker one scanner has
//     never heard of is still recognized here.
//   - A `forge:` inside a proto STRING literal (a default value, an option
//     value) is not a comment and is never scanned.

package lint

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/linter/suppress"
)

// protoMarkerFinding is one .proto comment carrying an unrecognized
// `forge:` marker. File is as walked (project-relative in normal use).
type protoMarkerFinding struct {
	File   string
	Line   int
	Marker string // the offending forge:* token, verbatim
	// Renamed is the marker this spelling was renamed TO, when the token is
	// a spelling forge deliberately removed (codegen.RemovedProtoMarkers).
	// Empty for an ordinary unrecognized marker.
	Renamed string
	// Suggestion is the closest known marker by edit distance, when there is
	// an obvious near-match. Empty when nothing is close enough. Never set
	// alongside Renamed — a rename is a better answer than a guess.
	Suggestion string
}

// protoMarkerDumpCmd is the command that dumps the marker catalog with each
// marker's effect and placement. Deliberately NOT `--kind marker`: the proto
// markers span three of that flag's kinds (entity, field, method), so there
// is no single --kind that shows them all and citing one would send the
// reader somewhere their marker isn't.
const protoMarkerDumpCmd = "forge project annotations"

// protoMarkerFixHint renders the remediation. It is a runbook, not a label:
// a REMOVED spelling gets the rename that explains it (and an explicit note
// that the old spelling is inert, so nobody reads the message as "forge
// still honors this"), a near-miss gets the did-you-mean, and anything else
// falls back to the full known vocabulary — listed straight from
// codegen.KnownProtoMarkers so the hint can never advertise a marker the
// registry doesn't have, or omit one it does.
func protoMarkerFixHint(f protoMarkerFinding) string {
	switch {
	case f.Renamed != "":
		return fmt.Sprintf(
			"%q was renamed to %q and is no longer recognized — this comment currently does NOTHING (the field stays client-writable). Rewrite it as %q.",
			f.Marker, f.Renamed, f.Renamed)
	case f.Suggestion != "":
		return fmt.Sprintf(
			"%q is not a marker forge recognizes — did you mean %q? Known proto markers: %s (see %#q)",
			f.Marker, f.Suggestion, strings.Join(codegen.KnownProtoMarkers, ", "), protoMarkerDumpCmd)
	default:
		return fmt.Sprintf(
			"%q is not a marker forge recognizes — known proto markers: %s (see %#q)",
			f.Marker, strings.Join(codegen.KnownProtoMarkers, ", "), protoMarkerDumpCmd)
	}
}

// protoDirDefault is the conventional proto tree forge scaffolds and every
// forge project keeps its .proto files under.
const protoDirDefault = "proto"

// collectProtoMarkerFindings is the shared engine behind text mode and
// `forge lint --json`. It walks protoDir for *.proto files and flags every
// `//` comment whose text contains `forge:` but matches none of
// codegen.KnownProtoMarkers. Findings come back sorted by (file, line) so
// output is deterministic.
//
// A missing or empty proto directory is not an error — CLI and library
// projects have no proto tree at all — it just yields no findings.
func collectProtoMarkerFindings(protoDir string) ([]protoMarkerFinding, error) {
	if _, err := os.Stat(protoDir); os.IsNotExist(err) {
		return nil, nil
	}

	var files []string
	if err := filepath.WalkDir(protoDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".proto") {
			return nil
		}
		files = append(files, path)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("walk %s: %w", protoDir, err)
	}
	sort.Strings(files)

	var findings []protoMarkerFinding
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", file, err)
		}
		findings = append(findings, findUnknownProtoMarkers(file, string(data))...)
	}
	return findings, nil
}

// findUnknownProtoMarkers scans one .proto file's content for comments
// carrying an unrecognized forge:* marker.
//
// Line-oriented on purpose, matching the raw scanner's own grade (see
// internal/codegen/proto_rawscan.go): everything after the first `//` on a
// line is comment text, which covers both marker positions that matter —
// a leading full-line comment and a trailing one after a field's `;`.
// Taking the FIRST `//` is also what keeps a `forge:` inside a string
// literal out of scope, since a literal containing `//` would have to open
// before it and the marker check only ever looks at text the compiler is
// already ignoring.
func findUnknownProtoMarkers(file, content string) []protoMarkerFinding {
	var findings []protoMarkerFinding

	for i, line := range strings.Split(content, "\n") {
		idx := strings.Index(line, "//")
		if idx < 0 {
			continue
		}
		comment := line[idx:]
		for _, token := range forgeTokenRe.FindAllString(comment, -1) {
			if f, ok := unknownProtoMarkerFinding(file, i+1, token); ok {
				findings = append(findings, f)
			}
		}
	}

	sort.SliceStable(findings, func(i, j int) bool { return findings[i].Line < findings[j].Line })
	return findings
}

// unknownProtoMarkerFinding builds a finding when token matches none of
// codegen.KnownProtoMarkers.
//
// The token is compared for EXACT equality against the registry — a
// substring/prefix check would let `forge:entityset` silently pass as
// `forge:entity`, the same discipline the column check enforces. Trailing
// punctuation is trimmed first so the natural prose spellings
// (`// forge:entity, then…`, `// forge:read-only.`) are not reported as
// typos of themselves; `-` and `:` survive the trim because they are part
// of real marker names (forge:read-only, forge:soft-delete).
func unknownProtoMarkerFinding(file string, line int, token string) (protoMarkerFinding, bool) {
	token = strings.TrimRight(token, ".,;)]}\"'`")
	if token == "" || token == "forge:" {
		return protoMarkerFinding{}, false
	}
	if codegen.IsKnownProtoMarker(token) {
		return protoMarkerFinding{}, false
	}
	// A suppression directive is forge vocabulary too, just not a schema
	// marker: without this, silencing a proto rule would itself draw an
	// "unrecognized marker" warning on the directive's own line.
	if suppress.IsDirectiveToken(token) {
		return protoMarkerFinding{}, false
	}
	f := protoMarkerFinding{File: file, Line: line, Marker: token}
	// A deliberately-removed spelling gets the rename, which is a better
	// answer than edit distance could produce. This does NOT make the old
	// marker work again — nothing consults this map but the message.
	if renamed, ok := codegen.RemovedProtoMarkers[token]; ok {
		f.Renamed = renamed
		return f, true
	}
	f.Suggestion = codegen.ClosestProtoMarker(token)
	return f, true
}
