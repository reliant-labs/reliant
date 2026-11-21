// Package benefits is the registry of every benefit forge gives a project:
// one structured list, each entry carrying the evidence that backs it.
//
// WHY THIS EXISTS. The benefits were written down four times — the README's
// "Everything included" table, docs/benefits.md, the landing page and the
// Reliant docs — and each copy drifted from the others and from the code. A
// claim nothing checks is marketing; a claim whose evidence a test resolves
// against the binary is documentation. So the list lives once, here, as
// benefits.yaml (embedded), and everything else is rendered from it:
//
//   - docs/benefits.md          RenderMarkdown, the whole page
//   - README.md                 RenderReadmeTable, spliced between the
//     `<!-- benefits:start -->` / `<!-- benefits:end -->` markers, and
//     RenderReadmeReasons between `<!-- reasons:start -->` / `<!-- reasons:end -->`
//   - docs/why-forge.md         RenderWhy, the reasons with their evidence
//
// Other sites (the landing page) read benefits.yaml itself.
//
// `task docs:benefits` rewrites the files; TestRenderedFilesAreCurrent
// fails when any is stale.
//
// This package checks only what it can check alone: the schema (Validate).
// Whether each evidence item still EXISTS — a command in the cobra tree, a
// lint flag, a marker, a skill, a file — needs the CLI, so that half lives in
// internal/cli (TestBenefitsEvidenceExists, TestEveryGradableRuleIsABenefit).
//
//forge:exclude-contract: a data registry parsed from embedded YAML plus pure renderers over it; the *Registry methods are lookups over that data, it does no I/O, and nothing substitutes it
package benefits

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

//go:embed benefits.yaml
var registryYAML []byte

// Status says how far a benefit is backed by something that acts. The words
// are deliberate: a catalog that blurs them is marketing.
type Status string

const (
	// StatusEnforced means something refuses the action (a lint, a gate, a guard).
	StatusEnforced Status = "enforced"
	// StatusRecorded means forge writes the declaration down; nothing refuses yet.
	StatusRecorded Status = "recorded"
	// StatusInProgress means designed but not shipped; it is never a headline.
	StatusInProgress Status = "in-progress"
)

// Availability says what a project needs for the benefit to apply.
type Availability string

const (
	// AvailabilityForge means open-source forge, on any infrastructure.
	AvailabilityForge Availability = "forge"
	// AvailabilityHosted needs Reliant hosting.
	AvailabilityHosted Availability = "hosted"
	// AvailabilityReliant needs the Reliant agent harness.
	AvailabilityReliant Availability = "reliant"
)

// Scope says what a repository needs before the benefit applies to it. It is
// the adoption axis: what an agent working in a repo that has never seen
// forge can use today, and what waits until the repo is a forge project.
type Scope string

const (
	// ScopeAny means the benefit works in any repository, in any language,
	// with no forge.yaml: a lint lane, a library, a command. Such an entry
	// carries Adopt and Estimate, so an agent can quote the user a cost.
	ScopeAny Scope = "any"
	// ScopeForgeProject means the benefit needs a forge project (forge.yaml,
	// and usually the code forge generates). The default.
	ScopeForgeProject Scope = "forge-project"
)

// externalRepos are the sibling repositories an `external:` evidence item may
// name. Their files are not in this checkout, so the item is format-checked
// only.
var externalRepos = map[string]bool{"control-plane": true, "reliant": true, "landing-page": true}

// Registry is the whole of benefits.yaml.
type Registry struct {
	// Categories are ordered; every rendering follows this order.
	Categories []Category `yaml:"categories" json:"categories"`
	Benefits   []Benefit  `yaml:"benefits" json:"benefits"`
	// Glance is the short "what this project gets with no setup" block the
	// entry skills and `forge start app` open with, one list per audience
	// (GlanceApp, GlanceSite). Every line names the benefits that back it and
	// the command that shows it, so it is a digest of the registry, never a
	// second set of claims.
	Glance map[string][]GlanceFact `yaml:"glance,omitempty" json:"glance,omitempty"`
	// Limits are what forge and Reliant hosting do NOT do, stated with the
	// same evidence discipline as a benefit. The comparison an agent quotes
	// (the `compare` skill) renders them, so the trade-offs are as checked as
	// the wins.
	Limits []Limit `yaml:"limits,omitempty" json:"limits,omitempty"`
	// Reasons are why forge: the cross-layer argument the benefits back,
	// each citing the entries that prove it (reasons.go). They render as
	// docs/why-forge.md, the README's "Why Forge" block and `forge why`.
	Reasons []Reason `yaml:"reasons,omitempty" json:"reasons,omitempty"`
}

// Glance audiences: an app with an API, and a frontend-only static site.
const (
	GlanceApp  = "app"
	GlanceSite = "site"
)

// maxGlanceFacts keeps a glance block short: it opens skills an agent reads
// in full, and every line costs the reader tokens.
const maxGlanceFacts = 6

// maxGlanceFactLen bounds one fact, before its command.
const maxGlanceFactLen = 120

// GlanceFact is one line of a glance block: a concrete, checkable fact, the
// command that shows it, and the registry entries that back it.
type GlanceFact struct {
	// Fact is what the project gets, in one line, true today.
	Fact string `yaml:"fact" json:"fact"`
	// Show is the command that shows it, as a path under the `forge` root
	// with its arguments ("obs endpoints dev"). internal/cli's
	// TestGlanceCommandsParse runs it through the real command tree.
	Show string `yaml:"show" json:"show"`
	// Benefits are the ids of the shipped entries the fact digests.
	Benefits []string `yaml:"benefits" json:"benefits"`
}

// Limit is one thing forge or Reliant hosting does not do.
type Limit struct {
	ID string `yaml:"id" json:"id"`
	// Title is at most six words.
	Title string `yaml:"title" json:"title"`
	// Claim is the limit, stated plainly and true today.
	Claim string `yaml:"claim" json:"claim"`
	// Instead is what to do when the limit rules forge out for a part of the
	// app (optional).
	Instead  string     `yaml:"instead,omitempty" json:"instead,omitempty"`
	Evidence []Evidence `yaml:"evidence" json:"evidence"`
}

// Category groups benefits under one heading.
type Category struct {
	ID    string `yaml:"id" json:"id"`
	Title string `yaml:"title" json:"title"`
	// Intro is rendered under the category heading in docs/benefits.md.
	Intro string `yaml:"intro" json:"intro"`
}

// Benefit is one entry.
type Benefit struct {
	ID       string `yaml:"id" json:"id"`
	Category string `yaml:"category" json:"category"`
	// Title is at most six words: it is a README table cell.
	Title string `yaml:"title" json:"title"`
	// Claim is one sentence, user benefit first, true today.
	Claim        string       `yaml:"claim" json:"claim"`
	Status       Status       `yaml:"status" json:"status"`
	Availability Availability `yaml:"availability" json:"availability"`
	// Headline puts the entry in the README table. An in-progress entry
	// never is one.
	Headline bool `yaml:"headline" json:"headline"`
	// Use is the one-line how-to, Why the incident or rationale, Docs the
	// user-guide page. All optional; rendered verbatim (they are markdown).
	Use  string `yaml:"use,omitempty" json:"use,omitempty"`
	Why  string `yaml:"why,omitempty" json:"why,omitempty"`
	Docs string `yaml:"docs,omitempty" json:"docs,omitempty"`
	// Scope is what a repository needs before the benefit applies (any |
	// forge-project). Parse fills forge-project when it is unset.
	Scope Scope `yaml:"scope,omitempty" json:"scope"`
	// Adopt is the one-line entry recipe for a repo that is not yet a forge
	// project: the first command or edit, verbatim enough to act on.
	// Estimate is the agent's time from that first step to the first value
	// the user sees ("~5 min", "~1 hour"). Both are set exactly when Scope
	// is any. Markdown, rendered verbatim.
	Adopt    string     `yaml:"adopt,omitempty" json:"adopt,omitempty"`
	Estimate string     `yaml:"estimate,omitempty" json:"estimate,omitempty"`
	Evidence []Evidence `yaml:"evidence" json:"evidence"`
	// Rules lists the gradable lint rule ids a lint family covers.
	Rules []Rule `yaml:"rules,omitempty" json:"rules,omitempty"`
}

// Evidence is one thing that backs a claim. Exactly one field is set; the
// kind is which one.
type Evidence struct {
	// Command is a cobra path under the `forge` root, optionally ending in
	// one --flag of that command: "lint --migration-safety".
	Command string `yaml:"command,omitempty" json:"command,omitempty"`
	// Analyzer is a `forge lint` flag: "--migration-safety".
	Analyzer string `yaml:"analyzer,omitempty" json:"analyzer,omitempty"`
	// Marker is a `// forge:*` marker name: "forge:entity".
	Marker string `yaml:"marker,omitempty" json:"marker,omitempty"`
	// Skill is an embedded skill path: "db/seeding".
	Skill string `yaml:"skill,omitempty" json:"skill,omitempty"`
	// Library is a forge/pkg package directory: "pkg/svcerr".
	Library string `yaml:"library,omitempty" json:"library,omitempty"`
	// Path is any file or directory in the forge repo.
	Path string `yaml:"path,omitempty" json:"path,omitempty"`
	// External is "<repo>:<path>" in a sibling repo; format-checked only.
	External string `yaml:"external,omitempty" json:"external,omitempty"`
}

// Evidence kinds, as the YAML keys spell them.
const (
	KindCommand  = "command"
	KindAnalyzer = "analyzer"
	KindMarker   = "marker"
	KindSkill    = "skill"
	KindLibrary  = "library"
	KindPath     = "path"
	KindExternal = "external"
)

// Kind returns which field is set and its value. With none or several set it
// returns the empty kind; Validate reports which.
func (e Evidence) Kind() (kind, value string) {
	set := e.set()
	if len(set) != 1 {
		return "", ""
	}
	return set[0][0], set[0][1]
}

func (e Evidence) set() [][2]string {
	var out [][2]string
	for _, kv := range [][2]string{
		{KindCommand, e.Command}, {KindAnalyzer, e.Analyzer}, {KindMarker, e.Marker},
		{KindSkill, e.Skill}, {KindLibrary, e.Library}, {KindPath, e.Path}, {KindExternal, e.External},
	} {
		if kv[1] != "" {
			out = append(out, kv)
		}
	}
	return out
}

// Rule is one gradable lint rule id in a lint family's entry: the bug class it
// prevents and the `forge lint` flag that runs it alone. A rule no flag
// selects runs only in a bare `forge lint`; its Lane is empty (`lane: ""`).
type Rule struct {
	ID       string `yaml:"id" json:"id"`
	Lane     string `yaml:"lane" json:"lane"`
	BugClass string `yaml:"bug_class" json:"bug_class"`
}

// Load decodes and validates the embedded benefits.yaml.
func Load() (*Registry, error) {
	r, err := Parse(registryYAML)
	if err != nil {
		return nil, fmt.Errorf("internal/benefits/benefits.yaml: %w", err)
	}
	return r, nil
}

// Parse decodes a registry strictly — an unknown field is an error, so a
// misspelled `headlien:` cannot silently drop an entry from the README —
// fills the defaults, and validates it.
func Parse(data []byte) (*Registry, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var r Registry
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	for i := range r.Benefits {
		if r.Benefits[i].Status == "" {
			r.Benefits[i].Status = StatusEnforced
		}
		if r.Benefits[i].Scope == "" {
			r.Benefits[i].Scope = ScopeForgeProject
		}
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return &r, nil
}

var kebabID = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// reservedAnchors are the ids docs/benefits.md already uses for its own
// headings; an entry anchored at one would make two targets of one link.
var reservedAnchors = map[string]bool{"contents": true, AnyRepoAnchor: true}

// Validate checks the schema: unique kebab-case ids, known categories,
// non-empty prose, the status and availability enums, no in-progress
// headline, and evidence items with exactly one well-formed key. It reports
// every violation at once, each naming the entry.
func (r *Registry) Validate() error {
	var errs []error
	fail := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if len(r.Categories) == 0 {
		fail("no categories")
	}
	cats := map[string]bool{}
	for i, c := range r.Categories {
		where := fmt.Sprintf("categories[%d] %q", i, c.ID)
		switch {
		case !kebabID.MatchString(c.ID):
			fail("%s: id must be kebab-case", where)
		case cats[c.ID]:
			fail("%s: duplicate category id", where)
		case reservedAnchors[c.ID]:
			fail("%s: id %q is reserved for a docs/benefits.md heading", where, c.ID)
		}
		cats[c.ID] = true
		if strings.TrimSpace(c.Title) == "" {
			fail("%s: title is empty", where)
		}
		if strings.TrimSpace(c.Intro) == "" {
			fail("%s: intro is empty", where)
		}
	}

	ids := map[string]bool{}
	ruleOwner := map[string]string{}
	for i, b := range r.Benefits {
		where := fmt.Sprintf("benefits[%d] %q", i, b.ID)
		switch {
		case !kebabID.MatchString(b.ID):
			fail("%s: id must be kebab-case", where)
		case ids[b.ID]:
			fail("%s: duplicate benefit id", where)
		case cats[b.ID] || reservedAnchors[b.ID]:
			fail("%s: id collides with a docs/benefits.md heading anchor", where)
		}
		ids[b.ID] = true
		if !cats[b.Category] {
			fail("%s: category %q is not a categories[].id", where, b.Category)
		}
		if strings.TrimSpace(b.Title) == "" {
			fail("%s: title is empty", where)
		} else if n := len(strings.Fields(b.Title)); n > 6 {
			fail("%s: title has %d words; at most 6 (it is a README table cell)", where, n)
		}
		if strings.ContainsAny(b.Title, "|\n") {
			fail("%s: title may not contain '|' or a newline (it is a table cell)", where)
		}
		if strings.TrimSpace(b.Claim) == "" {
			fail("%s: claim is empty", where)
		}
		switch b.Status {
		case StatusEnforced, StatusRecorded, StatusInProgress:
		default:
			fail("%s: status %q is not one of enforced, recorded, in-progress", where, b.Status)
		}
		switch b.Availability {
		case AvailabilityForge, AvailabilityHosted, AvailabilityReliant:
		default:
			fail("%s: availability %q is not one of forge, hosted, reliant", where, b.Availability)
		}
		if b.Status == StatusInProgress && b.Headline {
			fail("%s: an in-progress entry may not be a headline (the README states only what ships)", where)
		}
		if b.Docs != "" && !strings.HasPrefix(b.Docs, "https://") {
			fail("%s: docs %q is not an https:// URL", where, b.Docs)
		}
		validateScope(b, func(format string, args ...any) { fail("%s: "+format, append([]any{where}, args...)...) })
		if len(b.Evidence) == 0 {
			fail("%s: no evidence; every claim names at least one thing that backs it", where)
		}
		for j, e := range b.Evidence {
			if msg := validateEvidence(e); msg != "" {
				fail("%s: evidence[%d]: %s", where, j, msg)
			}
		}
		for j, rule := range b.Rules {
			rwhere := fmt.Sprintf("%s: rules[%d] %q", where, j, rule.ID)
			if rule.ID == "" {
				fail("%s: id is empty", rwhere)
			} else if owner, dup := ruleOwner[rule.ID]; dup {
				fail("%s: rule already listed by %q; a rule belongs to one family", rwhere, owner)
			} else {
				ruleOwner[rule.ID] = b.ID
			}
			if rule.Lane != "" && !strings.HasPrefix(rule.Lane, "--") && !strings.HasPrefix(rule.Lane, "forge ") {
				fail("%s: lane %q is not a `forge lint` flag (--<lane>), a command that reports the rule (forge <command>), or \"\" for a rule only a bare `forge lint` runs", rwhere, rule.Lane)
			}
			if strings.TrimSpace(rule.BugClass) == "" {
				fail("%s: bug_class is empty", rwhere)
			}
			if strings.ContainsAny(rule.ID+rule.Lane+rule.BugClass, "|\n") {
				fail("%s: id, lane and bug_class may not contain '|' or a newline (they are table cells)", rwhere)
			}
		}
	}
	r.validateGlance(ids, fail)
	r.validateLimits(ids, cats, fail)
	r.validateReasons(fail)
	return errors.Join(errs...)
}

// validateGlance checks every glance block: a known audience, one to
// maxGlanceFacts one-line facts, each with a command and backed by shipped
// benefits. An in-progress entry cannot back a fact: a glance states only
// what is true today.
func (r *Registry) validateGlance(ids map[string]bool, fail func(format string, args ...any)) {
	status := map[string]Status{}
	for _, b := range r.Benefits {
		status[b.ID] = b.Status
	}
	for audience, facts := range r.Glance {
		where := fmt.Sprintf("glance.%s", audience)
		if audience != GlanceApp && audience != GlanceSite {
			fail("%s: audience %q is not one of %s, %s", where, audience, GlanceApp, GlanceSite)
		}
		if len(facts) == 0 || len(facts) > maxGlanceFacts {
			fail("%s: %d facts; a glance holds 1 to %d (it opens skills an agent reads in full)", where, len(facts), maxGlanceFacts)
		}
		for i, f := range facts {
			fwhere := fmt.Sprintf("%s[%d]", where, i)
			switch fact := strings.TrimSpace(f.Fact); {
			case fact == "":
				fail("%s: fact is empty", fwhere)
			case strings.Contains(f.Fact, "\n"):
				fail("%s: fact must be one line", fwhere)
			case len(fact) > maxGlanceFactLen:
				fail("%s: fact is %d bytes; at most %d", fwhere, len(fact), maxGlanceFactLen)
			}
			if f.Show == "" {
				fail("%s: show is empty; name the command that shows the fact", fwhere)
			} else if msg := validateEvidence(Evidence{Command: f.Show}); msg != "" {
				fail("%s: show: %s", fwhere, msg)
			}
			if len(f.Benefits) == 0 {
				fail("%s: benefits is empty; name the entries the fact digests", fwhere)
			}
			for _, id := range f.Benefits {
				switch {
				case !ids[id]:
					fail("%s: benefit %q is not a benefits[].id", fwhere, id)
				case status[id] == StatusInProgress:
					fail("%s: benefit %q is in-progress; a glance states only what ships", fwhere, id)
				}
			}
		}
	}
}

// validateLimits checks the limits like benefits: unique kebab-case ids that
// share no anchor with a benefit or category, a short title, a claim, and
// well-formed evidence.
func (r *Registry) validateLimits(benefitIDs, cats map[string]bool, fail func(format string, args ...any)) {
	seen := map[string]bool{}
	for i, l := range r.Limits {
		where := fmt.Sprintf("limits[%d] %q", i, l.ID)
		switch {
		case !kebabID.MatchString(l.ID):
			fail("%s: id must be kebab-case", where)
		case seen[l.ID]:
			fail("%s: duplicate limit id", where)
		case benefitIDs[l.ID] || cats[l.ID] || reservedAnchors[l.ID]:
			fail("%s: id collides with a benefit, category or heading anchor", where)
		}
		seen[l.ID] = true
		if strings.TrimSpace(l.Title) == "" {
			fail("%s: title is empty", where)
		} else if n := len(strings.Fields(l.Title)); n > 6 {
			fail("%s: title has %d words; at most 6", where, n)
		}
		if strings.TrimSpace(l.Claim) == "" {
			fail("%s: claim is empty", where)
		}
		if len(l.Evidence) == 0 {
			fail("%s: no evidence; every limit names at least one thing that shows it", where)
		}
		for j, e := range l.Evidence {
			if msg := validateEvidence(e); msg != "" {
				fail("%s: evidence[%d]: %s", where, j, msg)
			}
		}
	}
}

// Benefit returns the entry with id.
func (r *Registry) Benefit(id string) (Benefit, bool) {
	for _, b := range r.Benefits {
		if b.ID == id {
			return b, true
		}
	}
	return Benefit{}, false
}

// estimateShape is "~" then a number then a unit: "~5 min", "~1 hour",
// "~2-3 hours". The tilde is the point: it is an estimate an agent quotes.
var estimateShape = regexp.MustCompile(`^~\d+(-\d+)? (min|mins|hour|hours|day|days)$`)

// validateScope checks the adoption axis: the scope enum, and that adopt and
// estimate are set exactly when the benefit works in any repo — an any-scope
// entry an agent cannot start or cost is a claim without a recipe, and a
// forge-project one with a recipe would read as working standalone. An
// in-progress entry is never any-scope: the front door sells what ships.
func validateScope(b Benefit, fail func(format string, args ...any)) {
	switch b.Scope {
	case ScopeAny, ScopeForgeProject:
	default:
		fail("scope %q is not one of any, forge-project", b.Scope)
		return
	}
	if b.Scope != ScopeAny {
		if b.Adopt != "" || b.Estimate != "" {
			fail("adopt and estimate are only for scope: any (the recipe for a repo that is not a forge project)")
		}
		return
	}
	if b.Status == StatusInProgress {
		fail("an in-progress entry may not be scope: any (the front door offers only what ships)")
	}
	if strings.TrimSpace(b.Adopt) == "" {
		fail("scope: any needs adopt: the one-line entry recipe for a repo with no forge.yaml")
	}
	if b.Estimate == "" {
		fail("scope: any needs estimate: the agent's time to first value, e.g. \"~5 min\"")
	} else if !estimateShape.MatchString(b.Estimate) {
		fail("estimate %q is not \"~<n> <unit>\" (min, hour or day; e.g. \"~10 min\", \"~1-2 hours\")", b.Estimate)
	}
	if strings.ContainsAny(b.Adopt, "|\n") {
		fail("adopt may not contain '|' or a newline (it is a table cell)")
	}
}

// AppliesTo returns the benefits a repository can use: every shipped one in
// a forge project, and only the scope: any ones elsewhere. In-progress
// entries are never offered.
func (r *Registry) AppliesTo(forgeProject bool) []Benefit {
	var out []Benefit
	for _, b := range r.Benefits {
		if b.Status == StatusInProgress {
			continue
		}
		if !forgeProject && b.Scope != ScopeAny {
			continue
		}
		out = append(out, b)
	}
	return out
}

// validateEvidence returns what is wrong with one evidence item, or "".
func validateEvidence(e Evidence) string {
	set := e.set()
	switch len(set) {
	case 0:
		return "no key set; use exactly one of command, analyzer, marker, skill, library, path, external"
	case 1:
	default:
		keys := make([]string, len(set))
		for i, kv := range set {
			keys[i] = kv[0]
		}
		return fmt.Sprintf("%d keys set (%s); an item has exactly one", len(set), strings.Join(keys, ", "))
	}
	kind, v := set[0][0], set[0][1]
	if v != strings.TrimSpace(v) {
		return fmt.Sprintf("%s %q has surrounding whitespace", kind, v)
	}
	switch kind {
	case KindCommand:
		if f := strings.Fields(v); f[0] == "forge" {
			return fmt.Sprintf("command %q: write the path under the root, without the leading `forge`", v)
		} else if f[0] == "" || strings.HasPrefix(f[0], "-") {
			return fmt.Sprintf("command %q: names no command", v)
		}
	case KindAnalyzer:
		if !strings.HasPrefix(v, "--") {
			return fmt.Sprintf("analyzer %q is not a `forge lint` flag (--<lane>)", v)
		}
	case KindMarker:
		if !strings.HasPrefix(v, "forge:") {
			return fmt.Sprintf("marker %q does not start with forge:", v)
		}
	case KindLibrary:
		if !strings.HasPrefix(v, "pkg/") {
			return fmt.Sprintf("library %q is not a forge/pkg package dir (pkg/<name>)", v)
		}
	case KindPath, KindSkill:
		if strings.HasPrefix(v, "/") || strings.Contains(v, "..") {
			return fmt.Sprintf("%s %q must be relative and stay inside the forge repo", kind, v)
		}
	case KindExternal:
		repo, p, ok := strings.Cut(v, ":")
		if !ok || p == "" {
			return fmt.Sprintf("external %q is not <repo>:<path>", v)
		}
		if !externalRepos[repo] {
			return fmt.Sprintf("external %q: repo %q is not one of control-plane, reliant, landing-page", v, repo)
		}
		if strings.HasPrefix(p, "/") || strings.Contains(p, "..") {
			return fmt.Sprintf("external %q: path must be relative to the repo root", v)
		}
	}
	return ""
}

// Filter returns a copy holding only the benefits in category (all when
// empty), and only headlines when headlineOnly. Categories are kept whole so
// the renderers keep their order and intros; they skip a category with no
// entries.
func (r *Registry) Filter(category string, headlineOnly bool) *Registry {
	out := &Registry{Categories: r.Categories}
	for _, b := range r.Benefits {
		if category != "" && b.Category != category {
			continue
		}
		if headlineOnly && !b.Headline {
			continue
		}
		out.Benefits = append(out.Benefits, b)
	}
	return out
}

// HasCategory reports whether id is a category.
func (r *Registry) HasCategory(id string) bool {
	for _, c := range r.Categories {
		if c.ID == id {
			return true
		}
	}
	return false
}

// InCategory returns the benefits of one category, in registry order.
func (r *Registry) InCategory(id string) []Benefit {
	var out []Benefit
	for _, b := range r.Benefits {
		if b.Category == id {
			out = append(out, b)
		}
	}
	return out
}
