package tierguard

// Classification of Tier-1 files into derived (correctly Tier-1) and
// constant (mis-tier candidates), plus the documented allow-list.

import (
	"bytes"
	"sort"
	"strings"
)

// Verdict is the classification of one Tier-1 path.
type Verdict int

const (
	// Derived means the file's bytes responded to the change in user inputs.
	// Tier-1 is correct — regenerating it is the only way the file can
	// track what the user declared.
	Derived Verdict = iota
	// Constant means both renders produced byte-identical content despite
	// materially different inputs. The file is not a function of user
	// input; it is library code behind a "do not edit" banner.
	Constant
	// OnlyInOne means the path was a Tier-1 target in some fixtures and not
	// others. That existence itself is input-dependent (a per-service or
	// per-entity file, or one emitted only once the user writes their first
	// migration), which is derivation — reported separately so the asymmetry
	// is visible rather than silently counted as Derived.
	OnlyInOne
	// Unreadable marks a Tier-1 target with no readable bytes in one or both
	// renders. Classification impossible; never silently a pass.
	Unreadable
)

func (v Verdict) String() string {
	switch v {
	case Derived:
		return "derived"
	case Constant:
		return "CONSTANT"
	case OnlyInOne:
		return "derived (presence)"
	case Unreadable:
		return "UNREADABLE"
	default:
		return "unknown"
	}
}

// Classification is one path's verdict plus the evidence for it.
type Classification struct {
	Path string
	// Verdict is the outcome.
	Verdict Verdict
	// Evidence states what actually differed (or that nothing did), in
	// terms a reader can check against the rendered trees.
	Evidence string
	// SizeBytes is the file's size in the first fixture that has it.
	// Reported so mis-tier candidates can be ranked by how much library
	// code is sitting in user trees.
	SizeBytes int
	// Lines is the newline count of the same content.
	Lines int
	// IdentityDependent is meaningful only for a Constant verdict: the
	// file's bytes did not respond to any user DECLARATION, but they do
	// change with the project's module path / binary name (fixture C).
	// It does not rescue Tier-1 — the body is still not derived — but it
	// changes the remedy from "move verbatim to forge/pkg" to "library
	// code in forge/pkg plus a small user-owned scaffold that wires the
	// module-specific names".
	IdentityDependent bool
}

// Classify compares the input-varied renders and returns one
// Classification per path any of them targeted as Tier-1, sorted by path.
//
// inputs is the set of fixtures that differ in the user's DECLARATIONS
// while sharing one project identity. Two was the original shape; it is
// a set rather than a pair because "did anything the user wrote move
// this file" is a question over the whole input space, and a file that
// only responds to an input no fixture exercised is indistinguishable
// from a constant. Adding a fixture is therefore how an unexercised
// input becomes real evidence, and the classifier must not cap that at
// two.
//
// The comparison is over marker-stripped bytes. The forge:hash marker is
// a hash of the content, so it cannot make equal content look different;
// stripping it keeps the reported evidence about the body.
//
// identity, when non-nil, is the identity control (fixture C: fixture
// A's inputs under a different module path). It never changes a verdict
// — only annotates Constant findings with whether the file embeds the
// user's module identity, which determines the remedy.
func Classify(inputs []*renderResult, identity *renderResult) []Classification {
	paths := map[string]bool{}
	for _, r := range inputs {
		for p := range r.Tier1 {
			paths[p] = true
		}
	}

	var out []Classification
	for p := range paths {
		c := classifyOne(p, inputs)
		if c.Verdict == Constant && identity != nil && len(inputs) > 0 {
			c.IdentityDependent = differsUnderIdentity(p, inputs[0], identity)
			if c.IdentityDependent {
				c.Evidence += "; does vary with the module path (fixture " + identity.Label +
					"), so it embeds the user's own import paths but no declaration of theirs"
			}
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// differsUnderIdentity reports whether path's bytes change between the
// baseline render and the identity-varied render. Absent from the
// identity render (or unreadable) counts as not differing: the annotation
// must never invent a distinction it did not observe.
func differsUnderIdentity(path string, base, identity *renderResult) bool {
	bodyBase, okBase := base.Bodies[path]
	bodyAlt, okAlt := identity.Bodies[path]
	if !okBase || !okAlt {
		return false
	}
	return !bytes.Equal(bodyBase, bodyAlt)
}

func classifyOne(p string, inputs []*renderResult) Classification {
	// Partition the fixtures by how they relate to this path, keeping the
	// LABELS rather than just counts: every verdict below has to name the
	// fixtures it is drawn from, or its evidence cannot be checked against
	// the rendered trees.
	var targeting, absent, unreadable []string
	var bodies [][]byte
	var bodyOwners []string
	for _, r := range inputs {
		if !r.Tier1[p] {
			absent = append(absent, r.Label)
			continue
		}
		targeting = append(targeting, r.Label)
		body, ok := r.Bodies[p]
		if !ok {
			unreadable = append(unreadable, r.Label)
			continue
		}
		bodies = append(bodies, body)
		bodyOwners = append(bodyOwners, r.Label)
	}

	size, lines := 0, 0
	if len(bodies) > 0 {
		size, lines = len(bodies[0]), bytes.Count(bodies[0], []byte("\n"))
	}

	// Targeted by some fixtures and not others: its very EXISTENCE tracks
	// the inputs. Per-service and per-entity files land here, as does a
	// file emitted only once the user writes their first migration.
	if len(absent) > 0 && len(targeting) > 0 {
		return Classification{
			Path:    p,
			Verdict: OnlyInOne,
			Evidence: "emitted for " + strings.Join(targeting, ", ") +
				" but absent from " + strings.Join(absent, ", ") +
				" — the path itself is input-dependent",
			SizeBytes: size,
			Lines:     lines,
		}
	}

	// A target nobody could read cannot be classified, and must never be
	// silently counted as a pass.
	if len(unreadable) > 0 {
		return Classification{
			Path:    p,
			Verdict: Unreadable,
			Evidence: "Tier-1 target but unreadable in " + strings.Join(unreadable, ", ") +
				" (disowned-skip, or emitted outside the project root) — cannot be classified",
			SizeBytes: size,
			Lines:     lines,
		}
	}

	// One differing pair anywhere in the set is enough: the file responded
	// to something the user declared.
	for i := 1; i < len(bodies); i++ {
		if !bytes.Equal(bodies[0], bodies[i]) {
			return Classification{
				Path:      p,
				Verdict:   Derived,
				Evidence:  describeDiff(bodyOwners[0], bodies[0], bodyOwners[i], bodies[i]),
				SizeBytes: size,
				Lines:     lines,
			}
		}
	}

	return Classification{
		Path:    p,
		Verdict: Constant,
		Evidence: "byte-identical across all " + itoa(len(bodies)) + " input-varied fixtures (" +
			strings.Join(bodyOwners, "; ") + "): every declaration channel moved and this file did not",
		SizeBytes: size,
		Lines:     lines,
	}
}

// describeDiff summarizes how two renders of the same path differ, so a
// Derived verdict carries checkable evidence rather than an assertion.
// It reports the first differing line with both sides' text, which is
// enough to see WHICH user input the file tracks.
//
// The two fixtures are NAMED rather than called "A" and "B": with more
// than two input-varied fixtures, the differing pair is not necessarily
// the first two, and evidence that misattributes which inputs moved the
// file is worse than none.
func describeDiff(labelA string, a []byte, labelB string, b []byte) string {
	la := bytes.Split(a, []byte("\n"))
	lb := bytes.Split(b, []byte("\n"))
	for i := 0; i < len(la) && i < len(lb); i++ {
		if !bytes.Equal(la[i], lb[i]) {
			return "differs from line " + itoa(i+1) + ": " +
				labelA + "=" + trunc(string(la[i])) + " | " + labelB + "=" + trunc(string(lb[i]))
		}
	}
	return "identical prefix, differing length: " + labelA + "=" + itoa(len(la)) +
		" lines, " + labelB + "=" + itoa(len(lb)) + " lines"
}

func trunc(s string) string {
	const max = 90
	s = trimSpace(s)
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

func trimSpace(s string) string {
	start := 0
	for start < len(s) && (s[start] == ' ' || s[start] == '\t' || s[start] == '\r') {
		start++
	}
	end := len(s)
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
