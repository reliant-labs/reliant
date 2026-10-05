// Copyright (c) 2025 Reliant Labs

// Package catalogindex is the search index over the integration catalog: every
// action and trigger type of every embedded manifest, with display name,
// summary, keywords, category and auth, searchable by free text.
//
// The catalog is embedded and read-only, so the index is built once and never
// changes; it holds no per-user state. Whether the CALLER can use an entry
// ("connected") is supplied per query, which is what lets one index serve
// every user.
//
// Ranking is lexical (INTEGRATIONS_V1_BRIEF.md §3a). Each query word is scored
// against the entry by the strongest place it matches, and an entry must match
// every word:
//
//	exact ref or id, or the word is the whole display name   (exact)
//	a prefix of the ref, id, or a display-name word           (prefix)
//	a keyword (the action's or its integration's)             (keyword)
//	a word of the summary, description or integration name    (text)
//
// Within a tier two smaller signals order entries, both together smaller than
// one tier step so neither can lift a weaker match above a stronger one:
// connected entries get a boost, and below that a display name that is mostly
// the query ("Get pull request" for "pull request") beats one that merely
// contains it. Remaining ties break on ref, which is unique, so a page
// boundary is stable across requests.
//
//forge:exclude-contract: reliant is not forge-generated; consumers declare the narrow interfaces they need
package catalogindex

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
)

// Kind is what an entry describes.
type Kind int

const (
	KindAction Kind = iota + 1
	KindTrigger
)

func (k Kind) String() string {
	switch k {
	case KindAction:
		return "action"
	case KindTrigger:
		return "trigger"
	}
	return "unknown"
}

// Entry is one action or trigger type. Entries are shared and immutable; a
// caller must not modify one.
type Entry struct {
	// Ref is "<integration>/<id>@<major>", unique across the index.
	Ref  string
	Kind Kind
	// ID is the action or trigger id within its integration.
	ID          string
	DisplayName string
	Summary     string
	Description string

	Manifest *reliantv1.IntegrationManifest
	// Action is set for KindAction.
	Action *reliantv1.ActionSpec
	// Trigger is set for KindTrigger.
	Trigger *reliantv1.TriggerSpec

	// AuthKinds are the integration's ways to connect, most preferred first.
	AuthKinds []string
	// ConnectionRequired is false when the integration takes no credential or
	// makes one optional.
	ConnectionRequired bool

	// Read on every query, so copied out of the manifest once.
	integrationID string
	category      string

	// Search fields, lower-cased at build time.
	refLower     string
	idLower      string
	nameLower    string
	nameWords    []string
	keywordWords []string
	textWords    []string
}

// IntegrationID is the entry's integration id.
func (e *Entry) IntegrationID() string { return e.integrationID }

// Category is the entry's integration category.
func (e *Entry) Category() string { return e.category }

// Index is the immutable search index. It is safe for concurrent use.
type Index struct {
	entries []*Entry
	byRef   map[string]*Entry
	// integrations maps an integration id to its connection declaration (the
	// first indexed version's, the one connections are made against) and the
	// auth kinds declared across every indexed version.
	integrations map[string]*integration
}

type integration struct {
	connection *reliantv1.ConnectionSpec
	authKinds  map[string]bool
}

// Build indexes every action of every manifest, plus every trigger type the
// manifests declare. Manifests are expected to have passed the loader; a
// duplicate ref is still refused, since it would make results ambiguous.
func Build(ms []*reliantv1.IntegrationManifest) (*Index, error) {
	idx := &Index{byRef: map[string]*Entry{}, integrations: map[string]*integration{}}
	for _, m := range ms {
		authKinds := make([]string, 0, len(m.GetConnection().GetAuth()))
		for _, a := range m.GetConnection().GetAuth() {
			authKinds = append(authKinds, manifest.AuthKind(a))
		}
		in, ok := idx.integrations[m.GetId()]
		if !ok {
			in = &integration{connection: m.GetConnection(), authKinds: map[string]bool{}}
			idx.integrations[m.GetId()] = in
		}
		for _, k := range authKinds {
			in.authKinds[k] = true
		}
		required := len(authKinds) > 0 && !m.GetConnection().GetAuthOptional()
		// Only the integration's NAME is searchable text on each entry. Its
		// description lists everything the integration does ("repositories,
		// issues and pull requests"), so indexing it on every action would
		// make "issue" match the pull-request actions too. What the
		// integration is about is carried by its keywords instead.
		integrationText := words(m.GetDisplayName())

		for _, a := range m.GetActions() {
			e := &Entry{
				Ref: Ref(m.GetId(), a.GetId(), m.GetVersion()), Kind: KindAction, ID: a.GetId(),
				DisplayName: orDefault(a.GetDisplayName(), a.GetId()),
				Summary:     summaryOf(a.GetSummary(), a.GetDescription()),
				Description: a.GetDescription(),
				Manifest:    m, Action: a, AuthKinds: authKinds, ConnectionRequired: required,
			}
			e.index(append(append([]string{}, a.GetKeywords()...), m.GetKeywords()...), integrationText)
			if err := idx.add(e); err != nil {
				return nil, err
			}
		}
		// SEAM (stream B): TriggerSpec is still a reserved stub ({id}) on main
		// and the manifest loader refuses any manifest that declares triggers,
		// so no trigger entry exists yet. When TriggerSpec gains display_name /
		// summary / description / keywords / payload schema, index them here as
		// actions are above; the KindTrigger slot, the proto kind and the
		// payload_schema field are already in place.
		for _, t := range m.GetTriggers() {
			e := &Entry{
				Ref: Ref(m.GetId(), t.GetId(), m.GetVersion()), Kind: KindTrigger, ID: t.GetId(),
				DisplayName: t.GetId(),
				Manifest:    m, Trigger: t, AuthKinds: authKinds, ConnectionRequired: required,
			}
			e.index(m.GetKeywords(), integrationText)
			if err := idx.add(e); err != nil {
				return nil, err
			}
		}
	}
	sort.Slice(idx.entries, func(i, j int) bool { return idx.entries[i].Ref < idx.entries[j].Ref })
	return idx, nil
}

func (idx *Index) add(e *Entry) error {
	if _, dup := idx.byRef[e.Ref]; dup {
		return fmt.Errorf("catalog index: duplicate ref %q", e.Ref)
	}
	idx.byRef[e.Ref] = e
	idx.entries = append(idx.entries, e)
	return nil
}

// Ref formats an entry ref.
func Ref(integration, id string, version int32) string {
	return fmt.Sprintf("%s/%s@%d", integration, id, version)
}

// Get returns the entry for a ref. Refs are matched exactly, after trimming
// surrounding space.
func (idx *Index) Get(ref string) (*Entry, bool) {
	e, ok := idx.byRef[strings.TrimSpace(ref)]
	return e, ok
}

// Len is the number of entries.
func (idx *Index) Len() int { return len(idx.entries) }

func (e *Entry) index(keywords []string, integrationText []string) {
	e.integrationID = e.Manifest.GetId()
	e.category = e.Manifest.GetCategory()
	e.refLower = strings.ToLower(e.Ref)
	e.idLower = strings.ToLower(e.ID)
	e.nameLower = strings.ToLower(strings.TrimSpace(e.DisplayName))
	e.nameWords = words(e.DisplayName)
	for _, k := range keywords {
		e.keywordWords = append(e.keywordWords, words(k)...)
	}
	e.keywordWords = dedupe(e.keywordWords)
	text := words(e.Summary + " " + e.Description)
	text = append(text, integrationText...)
	// The integration id and the id's own segments ("issue", "create") are
	// text-tier words, so "github issue" finds github/issue.create@1 even when
	// neither appears in prose.
	text = append(text, words(e.IntegrationID()+" "+e.ID)...)
	e.textWords = dedupe(text)
}

// words splits on anything that is not a letter or digit and lower-cases.
func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

func dedupe(ws []string) []string {
	seen := make(map[string]bool, len(ws))
	out := ws[:0]
	for _, w := range ws {
		if !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}

// summaryOf is the explicit summary, else the description's first sentence.
func summaryOf(summary, description string) string {
	if s := strings.TrimSpace(summary); s != "" {
		return s
	}
	d := strings.Join(strings.Fields(description), " ")
	if i := strings.Index(d, ". "); i >= 0 {
		return d[:i+1]
	}
	return d
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}
