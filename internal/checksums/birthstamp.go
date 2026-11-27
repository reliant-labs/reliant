// The birth checksum: how a scaffold-once SQL migration says, by itself,
// whether it is still exactly what forge wrote.
//
// A birth migration (`forge scaffold` of a `// forge:entity` message) is
// yours from the moment it is written, and forge never rewrites it. One
// question still needs its provenance: `forge lint`'s crud-table-unowned
// rule holds a table that is still in its scaffolded state at a "pending:
// choose an owner" warning, and gates once a person has edited it. "Still
// scaffolded" has to be a fact, not a guess, so the birth carries the hash
// of its own bytes on one comment line:
//
//	-- Birth checksum: <sha256-hex> (…)
//
// ── Why in the file, and not in the scaffold ledger ──────────────────────
//
// .forge/scaffolded/ can record a birth hash (RecordScaffoldWithHash), but
// it keys the record by PATH, and a migration's path is not stable:
// `forge db migration rebase` re-versions a pending migration, so its
// birth record would describe a file that no longer exists (and the
// absent-scaffold notice would report a renamed migration as "deleted on
// purpose" with a rescaffold hint no renderer can honour). A checksum that
// travels with the file survives the rename, a clone and a cherry-pick:
// the same reason Tier-1 files certify themselves (stamp.go) instead of a
// global manifest. It is not the Tier-1 marker: a `forge:hash=` line would
// make the file read as forge-OWNED output, and the migration is the
// author's. The line carries no `forge:` token at all, because the
// migration-text lint lanes read every one of those as a declaration.
//
// ── Hash spec ────────────────────────────────────────────────────────────
//
// Hash over the content with every birth-checksum line removed, CRLF folded
// to LF and trailing newlines normalized to one (BodyHash's normalization,
// without its Tier-1 marker stripping). Every other byte is content: an
// edited comment is an edited file, and reads as one. No evidence (no checksum line, a malformed one, a mismatch) reads as
// edited, never as scaffolded: the rule then gates, as it did before the
// checksum existed.
package checksums

import (
	"bytes"
	"regexp"
	"strings"
)

// birthChecksumKey opens the birth-checksum comment line.
const birthChecksumKey = "-- Birth checksum: "

// birthChecksumNote follows the hash on the line, so a reader of the
// migration knows what the number is for without looking it up.
const birthChecksumNote = " (forge lint reads the table as freshly scaffolded while the rest of this file is unchanged)"

// birthChecksumRE matches one birth-checksum line, capturing the hash.
var birthChecksumRE = regexp.MustCompile(`^` + regexp.QuoteMeta(birthChecksumKey) + `([0-9a-f]{64})\b`)

// StampSQLBirth returns sql with a birth-checksum line inserted after its
// first line (the birth header), certifying every other byte. An sql that
// already carries one is re-stamped: the old line is dropped first, so
// stamping twice yields the same bytes as stamping once.
func StampSQLBirth(sql string) string {
	body := stripBirthChecksumLines(normalizeNewlines([]byte(sql)))
	line := birthChecksumKey + birthBodyHash(body) + birthChecksumNote + "\n"
	at := bytes.IndexByte(body, '\n')
	if at < 0 {
		return string(body) + "\n" + line
	}
	return string(body[:at+1]) + line + string(body[at+1:])
}

// SQLBirthIsPristine reports whether content carries a birth checksum
// (stamped) and whether every other byte still matches it (pristine).
// pristine is never true without stamped.
func SQLBirthIsPristine(content []byte) (stamped, pristine bool) {
	norm := normalizeNewlines(content)
	var want string
	for _, line := range splitLines(norm) {
		if m := birthChecksumRE.FindSubmatch(line); m != nil {
			if want != "" && want != string(m[1]) {
				return true, false // two different certificates certify nothing
			}
			want = string(m[1])
		}
	}
	if want == "" {
		return false, false
	}
	return true, birthBodyHash(stripBirthChecksumLines(norm)) == want
}

// birthBodyHash hashes LF-normalized content with its trailing newlines
// normalized to exactly one.
func birthBodyHash(body []byte) string {
	body = bytes.TrimRight(body, "\n")
	if len(body) > 0 {
		body = append(body[:len(body):len(body)], '\n')
	}
	return Hash(body)
}

// stripBirthChecksumLines removes every birth-checksum line from
// LF-normalized content.
func stripBirthChecksumLines(content []byte) []byte {
	if !bytes.Contains(content, []byte(strings.TrimSpace(birthChecksumKey))) {
		return content
	}
	var out []byte
	for _, line := range splitLines(content) {
		if birthChecksumRE.Match(line) {
			continue
		}
		out = append(out, line...)
	}
	return out
}
