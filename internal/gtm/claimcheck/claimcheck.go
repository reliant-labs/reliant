// Package claimcheck finds the claims in an asset's text and matches them
// against a registry of known facts. It is pure: no I/O, no clock
// beyond the time the caller passes in.
//
// Extraction is deterministic: regexes for numbers, prices, durations, sizes
// and percentages, and phrase lists for "free", license names, superlatives,
// competitor names and absolutes. The Extractor seam is where an LLM
// extractor that writes its mentions back can plug in later; the Go backend
// never calls a model.
//
//forge:exclude-contract: pure extraction and matching over values the caller passes in; no state, no I/O, nothing to mock
package claimcheck

import (
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Mention kinds. A Mention's Kind says which rule found it.
const (
	KindPrice       = "price"
	KindDuration    = "duration"
	KindPercent     = "percent"
	KindSize        = "size"
	KindMultiplier  = "multiplier"
	KindNumber      = "number"
	KindFree        = "free"
	KindNoAccount   = "no_account"
	KindLicense     = "license"
	KindSuperlative = "superlative"
	KindCompetitor  = "competitor"
	KindAbsolute    = "absolute"
)

// Mention is one candidate claim in a text.
type Mention struct {
	Kind string
	// Text is the words matched, e.g. "$7.25/mo".
	Text string
	// Context is the text around the mention: its sentence, clipped to a
	// window so one long unpunctuated paragraph stays cheap to match.
	Context string
	// At is the byte offset of Text inside Context. An extractor that does not
	// know it may leave it zero; the matcher then searches for Text.
	At int
	// Siblings are the [start,end) offsets inside Context of the other mentions
	// in the same context. A clause that carries its own mention is its own
	// statement and is matched on its own.
	Siblings [][2]int
	// Norm, when set, is the canonical value key (a spelled-out "five minutes"
	// and "5 minutes" share one), overriding the key derived from Text.
	Norm string
}

// Extractor finds candidate claims in a text.
type Extractor interface {
	Extract(text string) []Mention
}

// Status is a registry claim's lifecycle state.
type Status string

// Registry claim statuses.
const (
	StatusVerified   Status = "verified"
	StatusUnverified Status = "unverified"
	StatusRetired    Status = "retired"
)

// Claim is a registry row, reduced to what matching needs.
type Claim struct {
	ID     string
	Text   string
	Status Status
	// RecheckAt, when set and not after the check time, makes a verified claim
	// stale.
	RecheckAt *time.Time
}

// FindingKind is what the registry says about one mention.
type FindingKind string

// Finding kinds.
const (
	MatchedVerified   FindingKind = "matched_verified"
	MatchedUnverified FindingKind = "matched_unverified"
	UnmatchedRisky    FindingKind = "unmatched_risky"
)

// Reasons a matched claim is not vouched for.
const (
	ReasonUnverified     = "unverified"
	ReasonRetired        = "retired"
	ReasonRecheckOverdue = "recheck overdue"
)

// Finding is one mention and its outcome.
type Finding struct {
	Kind    FindingKind
	Mention Mention
	// Claim is the registry claim it matched; nil for UnmatchedRisky.
	Claim *Claim
	// Reason is set on MatchedUnverified.
	Reason string
}

// Blocking reports whether the finding stops publication approval.
func (f Finding) Blocking() bool { return f.Kind != MatchedVerified }

// Blocking reports whether any finding stops publication approval.
func Blocking(fs []Finding) bool { return slices.ContainsFunc(fs, Finding.Blocking) }

// Normalize is the registry's text key: lower-cased, whitespace collapsed,
// trailing "." and "!" dropped. Callers that store a normalized key
// must derive it with these same steps so the two agree.
func Normalize(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = strings.TrimRight(s, ".!")
	return strings.ToLower(strings.TrimSpace(s))
}

// Check extracts the mentions in text and matches each against claims.
//
// A claim covers a mention only when all of these hold:
//
//   - it states the same value (same kind and canonical text: "36 hours" and
//     "36h" agree) with the same polarity (a negation scopes over its own
//     clause), so "Acme Cloud is open source" is not backed by "Acme Cloud is
//     source-available, not open source";
//   - the clause around the mention shares a subject with the claim: at least
//     one content word in common, and no more than a quarter of the clause's
//     content words missing from the claim, so "Hosting costs $4 a month" is
//     not backed by "$4 monthly minimum for hosting";
//   - for the absolute kinds (never, always, free, unlimited, superlatives,
//     competitors), also every word the clause says after the mention and every
//     quantifier in the clause ("every", "all", "any") is in the claim, so
//     "Acme Bot never fails a build" is not backed by "Acme Bot never merges".
//
// A sentence that quotes the claim whole covers the mentions inside the quoted
// words, provided the mention's clause adds no content word or quantifier
// outside the claim (a clause opening with "and", "with", "too" and the like
// extends the one before it, comma or not). A verified cover outranks an unverified one for the
// same mention, so one stale duplicate never blocks a fact that is also
// verified.
func Check(ex Extractor, text string, claims []Claim, now time.Time) []Finding {
	return Match(ex, ex.Extract(text), claims, now)
}

// Match matches already-extracted mentions against claims. Extraction is the
// costly half and depends only on the text, so a caller holding a database
// lock extracts first, outside it, and matches inside against the registry as
// it stands then.
func Match(ex Extractor, mentions []Mention, claims []Claim, now time.Time) []Finding {
	views := make([]claimView, len(claims))
	for i := range claims {
		views[i] = newClaimView(ex, &claims[i])
	}
	var out []Finding
	cache := map[string][]tok{}
	for _, m := range mentions {
		out = append(out, match(m, views, cache, now))
	}
	return out
}

type tok struct {
	s          string
	start, end int
}

type claimView struct {
	claim *Claim
	toks  []string
	// vals are the claim's mention values without polarity, keys are with it.
	vals  map[string]bool
	keys  map[string]bool
	words map[string]bool
	quant map[string]bool
}

func newClaimView(ex Extractor, c *Claim) claimView {
	toks := tokenize(c.Text)
	v := claimView{claim: c, vals: map[string]bool{}, keys: map[string]bool{}, words: map[string]bool{}, quant: map[string]bool{}}
	for _, t := range toks {
		v.toks = append(v.toks, t.s)
		if quantifiers[t.s] {
			v.quant[t.s] = true
		}
	}
	for _, t := range toks {
		if isContent(t.s) {
			v.words[stem(t.s)] = true
		}
	}
	for _, m := range ex.Extract(c.Text) {
		mi := inspect(m, toks)
		v.vals[mi.value] = true
		v.keys[mi.value+"|"+polarity(mi.negated)] = true
	}
	return v
}

// mentionInfo is what the matcher needs to know about one mention, computed
// once from its (bounded) context.
type mentionInfo struct {
	value     string
	negated   bool
	strong    bool
	lo, hi    int // token range of the mention itself
	ctx       []tok
	subject   map[string]bool
	predicate map[string]bool
	quant     map[string]bool
	// clause is every content word of the mention's clause, the mention itself
	// excluded and with no fallback to the rest of the sentence.
	clause map[string]bool
}

var strongKinds = map[string]bool{
	KindFree: true, KindNoAccount: true, KindSuperlative: true, KindAbsolute: true, KindCompetitor: true,
}

// inspect reads a mention's context: its polarity, the words of its clause,
// and the words that follow it.
func inspect(m Mention, ctx []tok) mentionInfo {
	at := m.At
	if at < 0 || at+len(m.Text) > len(m.Context) || m.Context[at:at+len(m.Text)] != m.Text {
		if i := strings.Index(m.Context, m.Text); i >= 0 {
			at = i
		} else {
			at = 0
		}
	}
	end := at + len(m.Text)
	mi := mentionInfo{
		value:     valueKey(m),
		strong:    strongKinds[m.Kind],
		ctx:       ctx,
		subject:   map[string]bool{},
		predicate: map[string]bool{},
		quant:     map[string]bool{},
		clause:    map[string]bool{},
	}
	mi.lo, mi.hi = len(ctx), len(ctx)
	for i, t := range ctx {
		if t.end > at && t.start < end {
			if mi.lo == len(ctx) {
				mi.lo = i
			}
			mi.hi = i + 1
		}
	}
	cs, ce := clauseBounds(m.Context, at, end, m.Siblings)
	for i, t := range ctx {
		if t.start < cs || t.end > ce || (i >= mi.lo && i < mi.hi) {
			continue
		}
		switch {
		case negators[t.s]:
			mi.negated = true
		case quantifiers[t.s]:
			mi.quant[t.s] = true
		case isContent(t.s):
			mi.clause[stem(t.s)] = true
			mi.subject[stem(t.s)] = true
			if i >= mi.hi {
				mi.predicate[stem(t.s)] = true
			}
		}
	}
	if len(mi.subject) == 0 {
		// A clause with only the mention in it ("and Apache-2.0"): the subject
		// is somewhere else in the sentence.
		for i, t := range ctx {
			if (i < mi.lo || i >= mi.hi) && isContent(t.s) {
				mi.subject[stem(t.s)] = true
			}
		}
	}
	return mi
}

// clauseBounds is the span of the statement a mention belongs to. A sentence
// splits into segments at commas, semicolons, "but", "and" and "or"; the
// mention's clause is its segment, plus the neighbouring segments that merely
// extend it. A segment extends its neighbour when it opens with a continuation
// word ("and so is Acme Cloud", "with GPUs included", "or touches prod") and holds
// no mention of its own: it adds to the statement, so it is part of it. One
// that holds its own mention ("and Acme Studio is MIT") is its own statement.
func clauseBounds(s string, at, end int, siblings [][2]int) (int, int) {
	cuts := segmentStarts(s)
	seg := func(i int) (int, int) {
		return cuts[i].start, cuts[i].end
	}
	own := func(lo, hi int) bool {
		for _, sb := range siblings {
			if sb[0] >= lo && sb[1] <= hi {
				return true
			}
		}
		return false
	}
	at0 := 0
	for i := range cuts {
		if cuts[i].start <= at {
			at0 = i
		}
	}
	first, last := at0, at0
	for last+1 < len(cuts) {
		lo, hi := seg(last + 1)
		if !continues(s[cuts[last+1].word:]) || own(lo, hi) {
			break
		}
		last++
	}
	for first > 0 {
		lo, _ := seg(first)
		plo, phi := seg(first - 1)
		if !continues(s[cuts[first].word:]) || own(plo, phi) || own(lo, lo) {
			break
		}
		first--
	}
	lo, _ := seg(first)
	_, hi := seg(last)
	return lo, hi
}

// segment is one stretch of a sentence between delimiters. start..end is the
// text it owns; word is where its first word begins, past any comma.
type segment struct{ start, end, word int }

var reSegmentCut = regexp.MustCompile(`(?i)[,;]|\s(?:but|and|or)\s`)

// segmentStarts cuts a sentence at commas, semicolons and the conjunctions
// "but", "and" and "or". The conjunction stays with the segment it opens.
func segmentStarts(s string) []segment {
	var out []segment
	prev := 0
	for _, l := range reSegmentCut.FindAllStringIndex(s, -1) {
		cut := l[0]
		if s[l[0]] == ',' || s[l[0]] == ';' {
			out = append(out, segment{start: prev, end: cut})
			prev = l[1]
			continue
		}
		out = append(out, segment{start: prev, end: cut})
		prev = cut + 1
	}
	out = append(out, segment{start: prev, end: len(s)})
	for i := range out {
		w := out[i].start
		for w < len(s) && (s[w] == ' ' || s[w] == '\t' || s[w] == '\n') {
			w++
		}
		out[i].word = w
	}
	// A segment's end is the next segment's start, so neighbours abut.
	for i := 0; i+1 < len(out); i++ {
		out[i].end = out[i+1].start
	}
	return out
}

// continuers open a clause that adds to the one before it.
var continuers = map[string]bool{
	"and": true, "or": true, "nor": true, "with": true, "plus": true, "including": true,
	"also": true, "too": true, "as": true, "along": true, "so": true, "alongside": true,
	"without": true, "except": true, "excluding": true, "then": true,
}

// continues reports whether s opens with a continuation word.
func continues(s string) bool {
	t := tokenize(strings.TrimLeft(s, " "))
	if len(t) == 0 || t[0].start != 0 {
		return false
	}
	return continuers[t[0].s]
}

func match(m Mention, views []claimView, cache map[string][]tok, now time.Time) Finding {
	ctx, ok := cache[m.Context]
	if !ok {
		ctx = tokenize(m.Context)
		cache[m.Context] = ctx
	}
	mi := inspect(m, ctx)
	key := mi.value + "|" + polarity(mi.negated)
	var best *Claim
	bestReason := ""
	for i := range views {
		v := &views[i]
		if !v.vals[mi.value] || len(v.toks) == 0 || !covers(v, key, mi) {
			continue
		}
		reason := notVouched(v.claim, now)
		if reason == "" {
			return Finding{Kind: MatchedVerified, Mention: m, Claim: v.claim}
		}
		if best == nil {
			best, bestReason = v.claim, reason
		}
	}
	if best != nil {
		return Finding{Kind: MatchedUnverified, Mention: m, Claim: best, Reason: bestReason}
	}
	return Finding{Kind: UnmatchedRisky, Mention: m}
}

func covers(v *claimView, key string, mi mentionInfo) bool {
	if quoted(v, mi) {
		// A word-for-word quote vouches for the quoted words, and for a clause
		// that adds nothing outside the claim. "Acme Studio is MIT and so is
		// Acme Cloud" quotes the claim and extends it; the extension is not vouched
		// for.
		for w := range mi.clause {
			if !v.words[w] {
				return false
			}
		}
		for q := range mi.quant {
			if !v.quant[q] {
				return false
			}
		}
		return true
	}
	if !v.keys[key] {
		return false
	}
	shared, missed := 0, 0
	for w := range mi.subject {
		if v.words[w] {
			shared++
		} else {
			missed++
		}
	}
	if shared == 0 || missed > 0 {
		return false
	}
	if mi.strong {
		for q := range mi.quant {
			if !v.quant[q] {
				return false
			}
		}
		for w := range mi.predicate {
			if !v.words[w] {
				return false
			}
		}
	}
	return true
}

// quoted reports whether the claim's words appear in the context as a run that
// holds the mention. Quoting a claim vouches for the words quoted, not for the
// rest of the sentence.
func quoted(v *claimView, mi mentionInfo) bool {
	n := len(v.toks)
	if n == 0 || mi.lo >= mi.hi {
		return false
	}
	for i := 0; i+n <= len(mi.ctx); i++ {
		if mi.ctx[i].s != v.toks[0] || i > mi.lo || i+n < mi.hi {
			continue
		}
		ok := true
		for j := 1; j < n; j++ {
			if mi.ctx[i+j].s != v.toks[j] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func polarity(negated bool) string {
	if negated {
		return "neg"
	}
	return "pos"
}

// notVouched is why a claim cannot back an asset, or "" when it can.
func notVouched(c *Claim, now time.Time) string {
	switch c.Status {
	case StatusRetired:
		return ReasonRetired
	case StatusVerified:
		if c.RecheckAt != nil && !c.RecheckAt.After(now) {
			return ReasonRecheckOverdue
		}
		return ""
	default:
		return ReasonUnverified
	}
}

var (
	reDurationKV    = regexp.MustCompile(`(?i)^(\d+(?:\.\d+)?)[\s-]?([a-z]+)$`)
	reLeadNumber    = regexp.MustCompile(`^\d+(?:\.\d+)?`)
	reSpaceDash     = regexp.MustCompile(`[\s,]+`)
	reLicenseFiller = regexp.MustCompile(`licen[sc](?:e|ed)|version`)
	reLicenseSep    = regexp.MustCompile(`[\s,-]+`)
)

var negators = map[string]bool{
	"not": true, "never": true, "no": true, "without": true, "cannot": true,
	"isn't": true, "doesn't": true, "don't": true, "won't": true, "can't": true,
}

// quantifiers widen a statement ("every app"); a claim about one app does not
// cover a sentence that says every.
var quantifiers = map[string]bool{
	"every": true, "all": true, "any": true, "each": true, "everything": true,
	"everyone": true, "everywhere": true, "anything": true, "anyone": true,
	"unlimited": true, "always": true, "forever": true,
}

var stopwords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "that": true, "this": true,
	"are": true, "was": true, "were": true, "you": true, "your": true, "our": true,
	"can": true, "has": true, "have": true, "had": true, "from": true, "into": true,
	"its": true, "per": true, "but": true, "out": true, "than": true, "then": true,
	"about": true, "one": true, "only": true, "also": true, "will": true, "would": true,
	"within": true, "there": true, "here": true, "what": true, "when": true,
	"where": true, "which": true, "who": true, "how": true, "why": true,
	"they": true, "them": true, "their": true, "been": true, "being": true,
	"does": true, "did": true, "over": true, "under": true, "while": true,
	"just": true, "more": true, "very": true, "still": true, "even": true,
	"first": true, "own": true, "new": true, "whole": true, "entire": true,
	"may": true, "might": true, "should": true, "could": true, "get": true, "gets": true,
}

func isContent(t string) bool {
	return len(t) >= 3 && !stopwords[t] && !negators[t] && !quantifiers[t] && !filler[t]
}

// filler hedges a number without changing what it is about.
var filler = map[string]bool{
	"about": true, "around": true, "roughly": true, "approximately": true,
	"nearly": true, "almost": true, "estimated": true, "estimate": true,
	"currently": true, "today": true, "now": true, "typically": true, "usually": true,
}

var unitCanon = map[string]string{
	"ms": "ms", "millisecond": "ms", "milliseconds": "ms",
	"s": "s", "sec": "s", "secs": "s", "second": "s", "seconds": "s",
	"m": "min", "min": "min", "mins": "min", "minute": "min", "minutes": "min",
	"h": "h", "hr": "h", "hrs": "h", "hour": "h", "hours": "h",
	"d": "d", "day": "d", "days": "d",
	"week": "w", "weeks": "w", "month": "mo", "months": "mo", "year": "y", "years": "y",
}

// valueKey is a mention's identity for matching: its kind plus its text with
// spacing, case and unit spelling made canonical.
func valueKey(m Mention) string {
	if m.Norm != "" {
		return m.Norm
	}
	t := strings.ToLower(strings.TrimSpace(m.Text))
	switch m.Kind {
	case KindDuration:
		if g := reDurationKV.FindStringSubmatch(t); g != nil {
			if u, ok := unitCanon[g[2]]; ok {
				return m.Kind + ":" + g[1] + u
			}
		}
	case KindMultiplier:
		if n := reLeadNumber.FindString(t); n != "" {
			return m.Kind + ":" + n + "x"
		}
	case KindPrice:
		t = strings.NewReplacer(" per ", "/", "month", "mo", "year", "yr", "hour", "hr").Replace(t)
	case KindLicense:
		t = reLicenseFiller.ReplaceAllString(t, "")
		return m.Kind + ":" + reLicenseSep.ReplaceAllString(t, "")
	}
	return m.Kind + ":" + reSpaceDash.ReplaceAllString(t, "")
}

// tokenize splits s into words with their byte offsets: a run that starts and
// ends with a letter or digit and may hold ' . - inside ("apache-2.0",
// "isn't"). It is called once per context, never on a whole asset.
func tokenize(s string) []tok {
	isAlnum := func(b byte) bool { return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' }
	isInner := func(b byte) bool { return b == '\'' || b == '.' || b == '-' }
	var out []tok
	for i := 0; i < len(s); {
		if !isAlnum(s[i]) {
			i++
			continue
		}
		j, last := i+1, i+1
		for j < len(s) && (isAlnum(s[j]) || isInner(s[j])) {
			j++
			if isAlnum(s[j-1]) {
				last = j
			}
		}
		out = append(out, tok{s: strings.ToLower(s[i:last]), start: i, end: last})
		i = last
	}
	return out
}

// stem folds inflections together so "service" meets "services" and "license"
// meets "licensed". It is not a linguistic stemmer; a missed fold only makes a
// match stricter, and a stricter match only asks a person to look.
func stem(t string) string {
	for _, suf := range []string{"ing", "ed", "es", "s"} {
		if len(t) > len(suf)+3 && strings.HasSuffix(t, suf) && (suf != "s" || !strings.HasSuffix(t, "ss")) {
			t = t[:len(t)-len(suf)]
			break
		}
	}
	if len(t) > 4 && strings.HasSuffix(t, "e") {
		t = t[:len(t)-1]
	}
	return t
}

// Rules is the deterministic Extractor. The zero value uses the built-in
// phrase lists; Competitors adds a tenant's own.
type Rules struct {
	// Competitors are extra names to flag, on top of the built-in list. Like the
	// built-in names they flag only in a comparison ("better than X", "X vs Y").
	Competitors []string
}

// contextWindow is how many bytes of a sentence are kept on each side of a
// mention. It bounds the work per mention on a long unpunctuated paragraph.
const contextWindow = 160

const numWord = `(?:zero|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|thirteen|fourteen|fifteen|sixteen|seventeen|eighteen|nineteen|twenty|thirty|forty|fifty|sixty|seventy|eighty|ninety|hundred|thousand|million)`
const monthName = `(?:jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|june?|july?|aug(?:ust)?|sep(?:t(?:ember)?)?|oct(?:ober)?|nov(?:ember)?|dec(?:ember)?)`

var (
	rePrice      = regexp.MustCompile(`(?i)[$€£]\s?\d[\d,]*(?:\.\d+)?(?:\s?[kKmM]\b)?(?:\s?(?:/|per\s)\s?(?:mo(?:nth)?|yr|year|hr|hour|day|week|seat|user|gb|gib)\b)?`)
	reSpelled    = regexp.MustCompile(`(?i)\b(?:a\s+)?` + numWord + `(?:[\s-]+(?:and\s+)?` + numWord + `)*\s+(?:per\s?cent|percent|times|dollars?|bucks|ms|milliseconds?|seconds?|minutes?|hours?|days?|weeks?|months?|years?)\b`)
	reDuration   = regexp.MustCompile(`(?i)\b\d+(?:\.\d+)?[\s-]?(?:ms|milliseconds?|seconds?|secs?|minutes?|mins?|hours?|hrs?|days?|weeks?|months?|years?)\b|\b\d+(?:s|m|h|d)\b`)
	rePercent    = regexp.MustCompile(`\b\d+(?:\.\d+)?\s?%`)
	reSize       = regexp.MustCompile(`(?i)\b\d+(?:\.\d+)?\s?(?:kb|mb|gb|tb|kib|mib|gib|tib|vcpus?|cpus?|cores?)\b`)
	reMultiplier = regexp.MustCompile(`(?i)\b\d+(?:\.\d+)?\s?(?:x|×)(?:\s|$|[.,;)])`)
	reNumber     = regexp.MustCompile(`\b\d[\d,]*(?:\.\d+)?\b`)
	reFree       = regexp.MustCompile(`(?i)\bfree(?:\s+forever)?\b|\bfreemium\b|\bat no cost\b|\bno cost\b|\bzero cost\b|\$0\b`)
	reNoAccount  = regexp.MustCompile(`(?i)\bno[\s-](?:account|signup|sign-up|sign\s+up|card|credit\s+card|login|registration)\b|\bwithout\s+(?:an?\s+)?(?:account|signup|sign-up|credit\s+card|card)\b`)
	reLicense    = regexp.MustCompile(`(?i)\b(?:apache[\s-]?(?:license[\s,]*)?(?:version\s+)?2(?:\.0)?|mit(?:\s+licen[sc]ed?)?|bsl(?:[\s-]?1\.1)?|business\s+source(?:\s+licen[sc]e)?|gpl(?:v?[23](?:\.0)?)?|agpl|lgpl|mpl[\s-]?2(?:\.0)?|bsd(?:[\s-][23]-clause)?|sspl|source[\s-]available|open[\s-]source|osi[\s-]approved)\b`)
	reSuper      = regexp.MustCompile(`(?i)\b(?:best|fastest|cheapest|leading|number\s+one|the\s+only|the\s+first\s+(?:ever|to|and\s+only)|first[\s-]ever|the\s+most\s+\w+|most\s+(?:reliable|secure|powerful|popular|advanced|affordable|complete|trusted|capable|flexible|scalable|efficient)|unmatched|unrivall?ed|industry[\s-]leading|world[\s-]class|better\s+than|faster\s+than|cheaper\s+than|beats|outperforms?|guaranteed?|unlimited|zero\s+(?:downtime|latency|risk)|instant(?:ly)?)\b|#1`)
	reAbsolute   = regexp.MustCompile(`(?i)\b(?:never|always|every\s+time|no\s+limits?|impossible|without\s+exception)\b`)
	reDate       = regexp.MustCompile(`(?i)\b` + monthName + `\.?\s+\d{1,2}(?:st|nd|rd|th)?(?:,?\s+\d{4})?\b|\b\d{1,2}(?:st|nd|rd|th)?\s+` + monthName + `\.?(?:,?\s+\d{4})?\b|\b` + monthName + `\.?,?\s+\d{4}\b|\b\d{4}-\d{2}-\d{2}\b`)
	reYear       = regexp.MustCompile(`^(?:19|20)\d\d$`)
	reFence      = regexp.MustCompile("(?s)```.*?```")
	reInlineCode = regexp.MustCompile("`[^`\\n]*`")
	reURL        = regexp.MustCompile(`https?://[^\s)>\]]+`)
	reSentence   = regexp.MustCompile(`(?s)(?:[^.!?\n]|[.!?][^\s])+(?:[.!?]+(?:\s|$)|\n|$)`)
	reFeelFree   = regexp.MustCompile(`(?i)feel\s+$`)
	reBestSkip   = regexp.MustCompile(`(?i)^(?:\s+|-)(?:practices?|efforts?|regards|wishes|guess|case|fit|of\s+luck)\b`)
	// A competitor is named in a comparison, not in passing.
	reComparative = regexp.MustCompile(`(?i)\b(?:better|worse|faster|slower|cheaper|pricier|more|less|fewer|simpler|easier|harder|than|unlike|versus|vs\.?|alternatives?\s+to|instead\s+of|compared?\s+(?:to|with)|comparison|switch(?:ed|ing)?\s+from|replaces?|beats|outperforms?|competitors?|rivals?)\b`)
)

// builtinCompetitors are names a comparison must source. The list is a seed,
// not a promise of coverage; a tenant extends it through Rules.Competitors.
// Words that are also ordinary English (continue, render, linear, factory,
// bolt) are left out; a tenant adds the ones it needs.
var builtinCompetitors = []string{
	"cursor", "copilot", "github copilot", "windsurf", "devin", "factory.ai", "factory ai",
	"replit", "lovable", "bolt.new", "v0.dev", "vercel", "netlify", "heroku",
	"railway", "fly.io", "supabase", "firebase", "aws", "gcp", "azure",
	"kubernetes", "terraform", "pulumi", "cline", "aider", "codex", "claude code",
	"gemini", "chatgpt", "openai", "anthropic", "jira", "zapier", "n8n", "make.com",
}

var builtinCompetitorRE = competitorRE(builtinCompetitors)

func competitorRE(names []string) *regexp.Regexp {
	var parts []string
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			parts = append(parts, regexp.QuoteMeta(n))
		}
	}
	if len(parts) == 0 {
		return nil
	}
	slices.SortFunc(parts, func(a, b string) int { return len(b) - len(a) })
	return regexp.MustCompile(`(?i)(?:^|[^\w])(` + strings.Join(parts, "|") + `)(?:$|[^\w])`)
}

// versionLike skips a bare number that is part of a version or an identifier
// already caught by a richer rule (e.g. the "2.0" in "Apache-2.0").
var versionLike = regexp.MustCompile(`^\d+(?:\.\d+)+$`)

type hit struct {
	start, end int
	kind       string
	norm       string
}

// Extract finds the mentions in text. Mentions are returned in order of
// appearance; overlapping spans are reported once, by the most specific rule.
// Inline code is read like prose: a claim does not stop being one because it
// sits in backticks.
func (r Rules) Extract(text string) []Mention {
	text = stripNoise(text)
	var hits []hit
	add := func(re *regexp.Regexp, kind string) {
		for _, loc := range re.FindAllStringIndex(text, -1) {
			h := hit{start: loc[0], end: loc[1], kind: kind}
			switch {
			case kind == KindFree && reFeelFree.MatchString(text[max(0, loc[0]-8):loc[0]]):
				continue
			case kind == KindSuperlative && strings.EqualFold(text[loc[0]:loc[1]], "best") && reBestSkip.MatchString(text[loc[1]:min(len(text), loc[1]+24)]):
				continue
			}
			hits = append(hits, h)
		}
	}
	// Most specific first: a span claimed here is not re-reported by a later,
	// broader rule. Dates come first and are dropped: "Nov 19, 2026" is not a
	// pair of numbers.
	add(reDate, "date")
	add(rePrice, KindPrice)
	for _, loc := range reSpelled.FindAllStringIndex(text, -1) {
		if k, norm, ok := spelledValue(text[loc[0]:loc[1]]); ok {
			hits = append(hits, hit{loc[0], loc[1], k, norm})
		}
	}
	add(reLicense, KindLicense)
	add(reNoAccount, KindNoAccount)
	add(reFree, KindFree)
	add(reDuration, KindDuration)
	add(rePercent, KindPercent)
	add(reSize, KindSize)
	add(reMultiplier, KindMultiplier)
	add(reSuper, KindSuperlative)
	add(reAbsolute, KindAbsolute)
	for _, re := range []*regexp.Regexp{builtinCompetitorRE, competitorRE(r.Competitors)} {
		if re == nil {
			continue
		}
		for _, loc := range re.FindAllStringSubmatchIndex(text, -1) {
			hits = append(hits, hit{loc[2], loc[3], KindCompetitor, ""})
		}
	}
	for _, loc := range reNumber.FindAllStringIndex(text, -1) {
		hits = append(hits, hit{loc[0], loc[1], KindNumber, ""})
	}

	taken := make([]bool, len(text))
	var kept []hit
	for _, h := range hits {
		if slices.Contains(taken[h.start:h.end], true) {
			continue
		}
		if h.kind == KindNumber {
			tok := text[h.start:h.end]
			if versionLike.MatchString(tok) || reYear.MatchString(tok) || isSmallCount(tok) || partOfWord(text, h.start, h.end) {
				continue
			}
		}
		for i := h.start; i < h.end; i++ {
			taken[i] = true
		}
		if h.kind != "date" {
			kept = append(kept, h)
		}
	}
	slices.SortFunc(kept, func(a, b hit) int { return a.start - b.start })

	sentences := reSentence.FindAllStringIndex(text, -1)
	out := make([]Mention, 0, len(kept))
	starts := make([]int, 0, len(kept))
	for _, h := range kept {
		lo, hi := sentenceBounds(sentences, h.start, len(text))
		lo, hi = max(lo, h.start-contextWindow), min(hi, h.end+contextWindow)
		lo, hi = runeStart(text, lo), runeStart(text, hi)
		ctx := text[lo:hi]
		if h.kind == KindCompetitor && !reComparative.MatchString(ctx) {
			continue
		}
		start, end := h.start, h.end
		for start < end && text[start] == ' ' {
			start++
		}
		for end > start && (text[end-1] == ' ' || text[end-1] == '\n') {
			end--
		}
		if trimmed := strings.TrimLeft(ctx, " \n\t"); len(trimmed) != len(ctx) {
			lo += len(ctx) - len(trimmed)
			ctx = trimmed
		}
		ctx = strings.TrimRight(ctx, " \n\t")
		out = append(out, Mention{Kind: h.kind, Text: text[start:end], Context: ctx, At: start - lo, Norm: h.norm})
		starts = append(starts, start)
	}
	// Siblings: the other emitted mentions that fall inside each one's context.
	for i := range out {
		base := starts[i] - out[i].At
		hiAbs := base + len(out[i].Context)
		for j := i - 1; j >= 0 && starts[j]+len(out[j].Text) > base; j-- {
			out[i].Siblings = append(out[i].Siblings, [2]int{starts[j] - base, starts[j] + len(out[j].Text) - base})
		}
		for j := i + 1; j < len(out) && starts[j] < hiAbs; j++ {
			out[i].Siblings = append(out[i].Siblings, [2]int{starts[j] - base, min(starts[j]+len(out[j].Text), hiAbs) - base})
		}
	}
	return out
}

// sentenceBounds is the sentence holding pos, by binary search.
func sentenceBounds(sentences [][]int, pos, n int) (int, int) {
	i := sort.Search(len(sentences), func(i int) bool { return sentences[i][1] > pos })
	if i < len(sentences) && sentences[i][0] <= pos {
		return sentences[i][0], sentences[i][1]
	}
	return 0, n
}

// runeStart moves i back to the start of the rune it is inside, and clamps it.
func runeStart(s string, i int) int {
	i = max(0, min(i, len(s)))
	for i > 0 && i < len(s) && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}

var wordValue = map[string]int{
	"zero": 0, "one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7,
	"eight": 8, "nine": 9, "ten": 10, "eleven": 11, "twelve": 12, "thirteen": 13,
	"fourteen": 14, "fifteen": 15, "sixteen": 16, "seventeen": 17, "eighteen": 18,
	"nineteen": 19, "twenty": 20, "thirty": 30, "forty": 40, "fifty": 50, "sixty": 60,
	"seventy": 70, "eighty": 80, "ninety": 90,
}

var reSpelledSplit = regexp.MustCompile(`[\s-]+`)

// spelledValue turns "ninety-nine percent" or "five minutes" into the kind and
// canonical key the digit form ("99%", "5 minutes") has, so the two match the
// same claims and a number cannot be hidden by spelling it out.
func spelledValue(s string) (kind, norm string, ok bool) {
	words := reSpelledSplit.Split(strings.ToLower(strings.TrimSpace(s)), -1)
	unit := words[len(words)-1]
	total, cur, seen := 0, 0, false
	for _, w := range words[:len(words)-1] {
		switch w {
		case "a", "and":
		case "hundred":
			cur, seen = max(cur, 1)*100, true
		case "thousand", "million":
			m := 1000
			if w == "million" {
				m = 1_000_000
			}
			total, cur, seen = total+max(cur, 1)*m, 0, true
		default:
			v, found := wordValue[w]
			if !found {
				return "", "", false
			}
			cur, seen = cur+v, true
		}
	}
	if !seen {
		return "", "", false
	}
	n := strconv.Itoa(total + cur)
	switch {
	case unit == "percent" || strings.HasPrefix(unit, "per"):
		return KindPercent, KindPercent + ":" + n + "%", true
	case unit == "times":
		return KindMultiplier, KindMultiplier + ":" + n + "x", true
	case unit == "dollar" || unit == "dollars" || unit == "bucks":
		return KindPrice, KindPrice + ":$" + n, true
	case strings.HasPrefix(unit, "cent"):
		return KindPrice, KindPrice + ":" + n + "c", true
	}
	if u, found := unitCanon[unit]; found {
		return KindDuration, KindDuration + ":" + n + u, true
	}
	return "", "", false
}

// stripNoise blanks what is not prose: fenced code blocks and URLs, so a
// number inside a link or a code sample is not a claim. Inline code is kept:
// only its backticks are blanked. Every other byte stays where it was.
func stripNoise(text string) string {
	blank := func(re *regexp.Regexp, f func(string) string) {
		text = re.ReplaceAllStringFunc(text, f)
	}
	spaces := func(m string) string {
		return strings.Map(func(r rune) rune {
			if r == '\n' {
				return '\n'
			}
			return ' '
		}, m)
	}
	blank(reFence, spaces)
	blank(reInlineCode, func(m string) string { return " " + m[1:len(m)-1] + " " })
	blank(reURL, spaces)
	return text
}

// isSmallCount is a bare 0-9: "3 steps" and list numbers are not claims worth
// a block. Anything richer (a unit, a price, 10+) is caught by its own rule or
// reported as a number.
func isSmallCount(tok string) bool { return len(tok) == 1 && tok >= "0" && tok <= "9" }

func partOfWord(text string, start, end int) bool {
	isWord := func(b byte) bool { return b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' }
	return start > 0 && isWord(text[start-1]) || end < len(text) && isWord(text[end])
}
