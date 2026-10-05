// Copyright (c) 2025 Reliant Labs

package catalogindex

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"
)

const (
	// DefaultPageSize is the page size when a query names none.
	DefaultPageSize = 20
	// MaxPageSize caps a page.
	MaxPageSize = 100
	// MaxQueryLen bounds the query text, in bytes.
	MaxQueryLen = 256
)

var (
	// ErrInvalidPageToken: the token is malformed or belongs to a different
	// query or filter set.
	ErrInvalidPageToken = errors.New("invalid page token")
	// ErrQueryTooLong: the query text exceeds MaxQueryLen.
	ErrQueryTooLong = errors.New("query is too long")
)

// Match tiers, strongest first. A query word scores the strongest tier it
// reaches; an entry's relevance is the sum over the query's words.
const (
	tierText    = 1
	tierKeyword = 2
	tierPrefix  = 3
	tierExact   = 4
	// tierStep separates tiers so the connected boost and the name-coverage
	// bonus (together < tierStep) can only reorder entries of equal tier.
	tierStep       = 100
	connectedBoost = tierStep / 2
	// maxCoverageBonus rewards a display name that is mostly the query ("Get
	// pull request" over "Comment on issue or pull request" for "pull
	// request"). It is below connectedBoost, so a connected entry still
	// outranks a tighter name of the same tier.
	maxCoverageBonus = 40
)

// Query is one search.
type Query struct {
	// Text is free text. Empty matches every entry.
	Text string
	// Kinds keeps only these kinds; empty keeps every kind.
	Kinds []Kind
	// Category keeps only entries of integrations in this category.
	Category string
	// Integration keeps only entries of this integration id.
	Integration string
	// ConnectedOnly keeps only entries the caller can use now.
	ConnectedOnly bool
	// PageSize defaults to DefaultPageSize and is capped at MaxPageSize.
	PageSize int
	// PageToken continues a previous Result.
	PageToken string
	// Usable is the set of integration ids the caller can use now: an active
	// connection, or a delegated authority this deployment serves. An entry
	// whose integration needs no connection is usable regardless.
	Usable map[string]bool
}

// Hit is one result.
type Hit struct {
	Entry *Entry
	// Connected reports whether the caller can use the entry now.
	Connected bool
}

// Facet counts the matches carrying one value.
type Facet struct {
	Value string
	Count int
}

// Result is one page.
type Result struct {
	Hits []Hit
	// NextPageToken continues the search; empty on the last page.
	NextPageToken string
	// Total is how many entries match the query and every filter.
	Total int
	// CategoryFacets count matches per category, applying every filter except
	// Category. Largest first, then by value.
	CategoryFacets []Facet
}

// Connected reports whether the caller can use an entry now: it needs no
// connection, or its integration is in usable.
func (e *Entry) Connected(usable map[string]bool) bool {
	return !e.ConnectionRequired || usable[e.IntegrationID()]
}

type scored struct {
	entry     *Entry
	score     int
	connected bool
}

// Search runs a query against the index. It scans every entry: at the catalog
// sizes this is built for (hundreds of integrations, tens of thousands of
// entries) a linear pass over pre-lowered fields is well under a millisecond
// (see BenchmarkSearch), and it keeps ranking, filtering and facets exact
// with no secondary structure to keep consistent.
func (idx *Index) Search(q Query) (*Result, error) {
	if len(q.Text) > MaxQueryLen {
		return nil, ErrQueryTooLong
	}
	size := q.pageSize()
	offset, err := decodePageToken(q.PageToken, q.fingerprint())
	if err != nil {
		return nil, err
	}

	query := normalizeQuery(q.Text)
	kinds := map[Kind]bool{}
	for _, k := range q.Kinds {
		kinds[k] = true
	}

	var matches []scored
	facetCounts := map[string]int{}
	for _, e := range idx.entries {
		if len(kinds) > 0 && !kinds[e.Kind] {
			continue
		}
		if q.Integration != "" && e.IntegrationID() != q.Integration {
			continue
		}
		connected := e.Connected(q.Usable)
		if q.ConnectedOnly && !connected {
			continue
		}
		score, ok := query.score(e)
		if !ok {
			continue
		}
		facetCounts[e.Category()]++
		if q.Category != "" && e.Category() != q.Category {
			continue
		}
		if connected {
			score += connectedBoost
		}
		matches = append(matches, scored{entry: e, score: score, connected: connected})
	}

	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].entry.Ref < matches[j].entry.Ref
	})

	res := &Result{Total: len(matches), CategoryFacets: facets(facetCounts)}
	if offset > len(matches) {
		offset = len(matches)
	}
	end := offset + size
	if end > len(matches) {
		end = len(matches)
	}
	res.Hits = make([]Hit, 0, end-offset)
	for _, m := range matches[offset:end] {
		res.Hits = append(res.Hits, Hit{Entry: m.entry, Connected: m.connected})
	}
	if end < len(matches) {
		res.NextPageToken = encodePageToken(end, q.fingerprint())
	}
	return res, nil
}

func facets(counts map[string]int) []Facet {
	out := make([]Facet, 0, len(counts))
	for v, n := range counts {
		out = append(out, Facet{Value: v, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Value < out[j].Value
	})
	return out
}

func (q Query) pageSize() int {
	switch {
	case q.PageSize <= 0:
		return DefaultPageSize
	case q.PageSize > MaxPageSize:
		return MaxPageSize
	}
	return q.PageSize
}

// normalized is a parsed query.
type normalized struct {
	// raw is the trimmed, lower-cased query, for whole-ref / whole-name matches.
	raw string
	// terms are the whitespace-separated query terms.
	terms []term
}

// term is one query term, lower-cased. A term may itself be a ref or an id
// with punctuation ("issue.create"); parts are its words, split once per
// query rather than once per entry.
type term struct {
	text  string
	parts []string
}

func normalizeQuery(text string) normalized {
	raw := strings.Join(strings.Fields(strings.ToLower(text)), " ")
	var terms []term
	for _, f := range strings.Fields(raw) {
		f = strings.Trim(f, "!?,;:\"'()[]{}")
		if f == "" {
			continue
		}
		t := term{text: f}
		if parts := words(f); len(parts) > 1 {
			t.parts = parts
		}
		terms = append(terms, t)
	}
	return normalized{raw: raw, terms: terms}
}

// score is the entry's relevance to the query, and false when some query
// term matches nowhere. An empty query matches everything with score 0.
func (n normalized) score(e *Entry) (int, bool) {
	if len(n.terms) == 0 {
		return 0, true
	}
	// The whole query as the ref, the id or the display name is the strongest
	// signal there is; credit it as an exact match on every term so it
	// outranks any per-word combination.
	if n.raw == e.refLower || n.raw == e.idLower || n.raw == e.nameLower {
		return tierExact * tierStep * len(n.terms), true
	}
	total := 0
	for _, t := range n.terms {
		tier := termTier(t.text, e)
		if tier == 0 {
			// A term with punctuation ("issue.create", "pr-get") may also be
			// written as separate words; accept it when every part matches,
			// at the weakest part's tier.
			if t.parts == nil {
				return 0, false
			}
			tier = tierExact
			for _, p := range t.parts {
				pt := termTier(p, e)
				if pt == 0 {
					return 0, false
				}
				if pt < tier {
					tier = pt
				}
			}
		}
		total += tier * tierStep
	}
	// Only a matched entry pays for the coverage scan.
	return total + n.coverageBonus(e), true
}

// coverageBonus is maxCoverageBonus scaled by the share of the display name's
// words that some query term (or term part) prefixes. Every entry pays the
// same per-term tier, so this only separates entries already tied on tier.
func (n normalized) coverageBonus(e *Entry) int {
	if len(e.nameWords) == 0 {
		return 0
	}
	covered := 0
	for _, w := range e.nameWords {
		if n.prefixes(w) {
			covered++
		}
	}
	return maxCoverageBonus * covered / len(e.nameWords)
}

// prefixes reports whether any query term, or any part of a punctuated term,
// is a prefix of w.
func (n normalized) prefixes(w string) bool {
	for _, t := range n.terms {
		if strings.HasPrefix(w, t.text) {
			return true
		}
		for _, p := range t.parts {
			if strings.HasPrefix(w, p) {
				return true
			}
		}
	}
	return false
}

// termTier is the strongest tier one query term reaches in an entry, 0 for
// none.
func termTier(t string, e *Entry) int {
	if t == e.refLower || t == e.idLower || t == e.nameLower {
		return tierExact
	}
	if strings.HasPrefix(e.refLower, t) || strings.HasPrefix(e.idLower, t) {
		return tierPrefix
	}
	for _, w := range e.nameWords {
		if strings.HasPrefix(w, t) {
			return tierPrefix
		}
	}
	for _, w := range e.keywordWords {
		if w == t || strings.HasPrefix(w, t) {
			return tierKeyword
		}
	}
	for _, w := range e.textWords {
		if w == t || strings.HasPrefix(w, t) {
			return tierText
		}
	}
	return 0
}

// fingerprint binds a page token to the query and filters it was issued for,
// so a token replayed against a different search is refused rather than
// silently skipping entries. Connection state is not part of it: a user who
// connects an integration between pages sees a re-ranked list, as they would
// on a fresh search.
func (q Query) fingerprint() uint32 {
	h := fnv.New32a()
	kinds := make([]string, 0, len(q.Kinds))
	for _, k := range q.Kinds {
		kinds = append(kinds, strconv.Itoa(int(k)))
	}
	sort.Strings(kinds)
	for _, part := range []string{
		normalizeQuery(q.Text).raw, strings.Join(kinds, ","), q.Category, q.Integration,
		strconv.FormatBool(q.ConnectedOnly), strconv.Itoa(q.pageSize()),
	} {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return h.Sum32()
}

// A page token is base64url(offset uint32 | fingerprint uint32). It is opaque
// to clients and carries nothing secret: the offset into a deterministic
// ranking, and a checksum of the query it belongs to.
func encodePageToken(offset int, fp uint32) string {
	var b [8]byte
	binary.BigEndian.PutUint32(b[:4], uint32(offset))
	binary.BigEndian.PutUint32(b[4:], fp)
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func decodePageToken(tok string, fp uint32) (int, error) {
	if tok == "" {
		return 0, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil || len(b) != 8 {
		return 0, ErrInvalidPageToken
	}
	if binary.BigEndian.Uint32(b[4:]) != fp {
		return 0, ErrInvalidPageToken
	}
	offset := binary.BigEndian.Uint32(b[:4])
	if offset == 0 || offset > 1<<24 {
		return 0, ErrInvalidPageToken
	}
	return int(offset), nil
}
