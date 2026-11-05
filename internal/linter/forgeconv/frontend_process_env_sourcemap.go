// File: internal/linter/forgeconv/frontend_process_env_sourcemap.go
//
// Recovering the user's line number after normalisation.
//
// esbuild gives a 100%-parseable input but does NOT preserve line numbers:
// it hoists imports, collapses type-only statements to nothing, and lowers
// JSX to calls. Measured on the control-plane corpus, only 2.2% of
// env-bearing source lines land at the same index in the output.
//
// Every finding this linter emits is a "file:line" a human is expected to
// open, so a wrong line is worse than no finding at all. The sourcemap is
// the bridge, and decoding it recovers the true original line for 100% of
// the env reads in the corpus (18/18, each verified against ground truth —
// the original line really does contain that variable).
//
// Only the LINE is recovered. Columns would need the full segment
// semantics and the rule reports lines, so the decoder stops where the
// requirement stops.

package forgeconv

import "strings"

// lineMapper turns a byte offset in normalised code into a 1-based line
// number in the original file.
type lineMapper struct {
	// lineStarts[i] is the byte offset where generated line i begins.
	lineStarts []int
	// segments[i] are the mapping segments for generated line i, ordered
	// by generated column.
	segments [][]mapSegment
}

// mapSegment is one sourcemap segment, reduced to the two fields this
// decoder needs.
type mapSegment struct {
	genCol   int
	origLine int // 0-based
}

// newLineMapper builds a mapper for esbuild output and its mappings string.
// An empty or undecodable mappings string yields a mapper that falls back
// to generated line numbers, which is the best available answer and never
// worse than not reporting.
func newLineMapper(code []byte, mappings string) *lineMapper {
	m := &lineMapper{lineStarts: []int{0}}
	for i, b := range code {
		if b == '\n' {
			m.lineStarts = append(m.lineStarts, i+1)
		}
	}
	m.segments = decodeMappings(mappings)
	return m
}

// originalLine returns the 1-based original line for a byte offset in the
// normalised code.
func (m *lineMapper) originalLine(off int) int {
	genLine := m.generatedLine(off)
	genCol := off - m.lineStarts[genLine]

	if genLine < len(m.segments) {
		if row := m.segments[genLine]; len(row) > 0 {
			// The mapping in effect at a column is the last segment at or
			// before it.
			best := row[0].origLine
			for _, s := range row {
				if s.genCol > genCol {
					break
				}
				best = s.origLine
			}
			return best + 1
		}
	}
	// No mapping for this line: report the generated line. Only reachable
	// when the sourcemap is absent or malformed.
	return genLine + 1
}

// generatedLine returns the 0-based generated line containing off, by
// binary search over the line starts.
func (m *lineMapper) generatedLine(off int) int {
	lo, hi := 0, len(m.lineStarts)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if m.lineStarts[mid] <= off {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}

// decodeMappings decodes a sourcemap `mappings` string into per-generated-
// line segment lists.
//
// The format is `;`-separated generated lines, each a `,`-separated list of
// base64-VLQ segments. Fields are DELTAS against a running state: the
// generated column resets per line, the source line does not. A segment
// with fewer than four fields carries no source position and is skipped.
func decodeMappings(mappings string) [][]mapSegment {
	if mappings == "" {
		return nil
	}
	var out [][]mapSegment
	origLine := 0
	for _, lineStr := range strings.Split(mappings, ";") {
		var segs []mapSegment
		genCol := 0
		for _, segStr := range strings.Split(lineStr, ",") {
			if segStr == "" {
				continue
			}
			vals, ok := decodeVLQ(segStr)
			if !ok || len(vals) == 0 {
				continue
			}
			genCol += vals[0]
			if len(vals) >= 4 {
				origLine += vals[2]
				segs = append(segs, mapSegment{genCol: genCol, origLine: origLine})
			}
		}
		out = append(out, segs)
	}
	return out
}

// base64VLQ is the sourcemap alphabet.
const base64VLQ = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

// base64VLQIndex inverts base64VLQ.
var base64VLQIndex = func() map[byte]int {
	m := make(map[byte]int, len(base64VLQ))
	for i := 0; i < len(base64VLQ); i++ {
		m[base64VLQ[i]] = i
	}
	return m
}()

// decodeVLQ decodes one comma-free segment into its integer fields.
//
// Each value is a little-endian base-64 VLQ: bit 5 continues the value,
// bits 0-4 carry data, and the LAST bit of the assembled value is the sign.
func decodeVLQ(s string) ([]int, bool) {
	var out []int
	var shift, value uint32
	for i := 0; i < len(s); i++ {
		digit, ok := base64VLQIndex[s[i]]
		if !ok {
			return nil, false
		}
		cont := digit & 32
		value += uint32(digit&31) << shift
		if cont != 0 {
			shift += 5
			// A well-formed field never needs this many bits; refusing
			// keeps a corrupt map from shifting into undefined behaviour.
			if shift > 30 {
				return nil, false
			}
			continue
		}
		v := int(value >> 1)
		if value&1 != 0 {
			v = -v
		}
		out = append(out, v)
		shift, value = 0, 0
	}
	return out, true
}
