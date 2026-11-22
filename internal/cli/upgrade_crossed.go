package cli

// What the forge range a pin bump crosses brings, for the agent running
// `forge project upgrade`.
//
// The upgrade used to say how the PROJECT differs from the new templates and
// nothing about what changed in FORGE between the two pins: no release list,
// no changelog, no "do this". The agent read CHANGELOG.md and `git log` by
// hand to learn that generated middleware now records argument shapes, and
// two of its tests broke on generated output nobody warned it about.
//
// Sources, all offline and in this order:
//
//   - CHANGELOG.md for the released part: the target module's copy in the Go
//     module cache when forge was fetched at the target (the copy that
//     describes it), else the one embedded in this binary (repo root
//     changelog.go) when this forge is at least the target.
//   - git for the unreleased tail of a pseudo-version, and to tell which of a
//     tag's changes the old pin already had: the bare clone the go command
//     keeps of forge in $GOMODCACHE/cache/vcs when forge is a GOPRIVATE
//     module. Opportunistic — when it is absent, the report says how to see
//     the range online instead.
//
// Agent instructions and generated-output notes ride in commit trailers
// (`Agent-Instructions:`, `Generated-Change:`) of the PR that makes the
// change; the release script renders them under the PR's CHANGELOG entry
// (`  - Agent: …`, `  - Generated: …`) and this file reads both forms.

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"

	forge "github.com/reliant-labs/forge"
	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/goexec"
)

// forgeRepoURL is where the release notes and compare views of forge live.
const forgeRepoURL = "https://github.com/reliant-labs/forge"

// upgradeCrossed is one part of the range: a release tag the bump crosses, or
// (Untagged) the commits after the last tag up to a pseudo-version target.
type upgradeCrossed struct {
	// Version is the release tag; for the untagged tail, the version of
	// the target (a pseudo-version).
	Version  string `json:"version"`
	Untagged bool   `json:"untagged,omitempty"`
	// After and Commit bound an untagged tail: the commits after After (a
	// tag, or the old pin's commit) up to Commit. Commits is how many, when
	// git could count them.
	After   string `json:"after,omitempty"`
	Commit  string `json:"commit,omitempty"`
	Commits *int   `json:"commits,omitempty"`
	Date    string `json:"date,omitempty"`
	// URL is the release notes, or the compare view for a tail.
	URL string `json:"url"`
	// Source is where the entries came from: "changelog", "git", or
	// "unavailable" (nothing offline described this part; see URL).
	Source string `json:"source"`
	// InPin: the old pin already contains every change of this release
	// (its tag is an ancestor of the pin's commit), so it brings nothing.
	InPin bool   `json:"in_pin,omitempty"`
	Note  string `json:"note,omitempty"`
	// Entries by Keep-a-Changelog heading. Other holds any other heading.
	Breaking []upgradeChange            `json:"breaking"`
	Added    []upgradeChange            `json:"added"`
	Changed  []upgradeChange            `json:"changed"`
	Fixed    []upgradeChange            `json:"fixed"`
	Other    map[string][]upgradeChange `json:"other,omitempty"`
	// AgentInstructions are the steps the PRs of this part address to
	// the agent running the upgrade, in entry order.
	AgentInstructions []upgradeNote `json:"agent_instructions"`
}

// upgradeChange is one CHANGELOG entry: a merged pull request.
type upgradeChange struct {
	Scope string `json:"scope,omitempty"`
	Text  string `json:"text"`
	PR    int    `json:"pr,omitempty"`
	// Agent and Generated are the PR's Agent-Instructions and
	// Generated-Change trailers.
	Agent     []string `json:"agent,omitempty"`
	Generated []string `json:"generated,omitempty"`
}

// upgradeNote is a trailer's text with where it came from.
type upgradeNote struct {
	Text    string `json:"text"`
	PR      int    `json:"pr,omitempty"`
	Version string `json:"version,omitempty"`
}

func (c *upgradeCrossed) total() int {
	n := len(c.Breaking) + len(c.Added) + len(c.Changed) + len(c.Fixed)
	for _, es := range c.Other {
		n += len(es)
	}
	return n
}

func (c *upgradeCrossed) add(heading string, e upgradeChange) {
	switch strings.ToLower(strings.TrimSpace(heading)) {
	case "breaking changes", "breaking":
		c.Breaking = append(c.Breaking, e)
	case "added":
		c.Added = append(c.Added, e)
	case "changed":
		c.Changed = append(c.Changed, e)
	case "fixed":
		c.Fixed = append(c.Fixed, e)
	default:
		if c.Other == nil {
			c.Other = map[string][]upgradeChange{}
		}
		c.Other[heading] = append(c.Other[heading], e)
	}
	for _, a := range e.Agent {
		c.AgentInstructions = append(c.AgentInstructions, upgradeNote{Text: a, PR: e.PR, Version: c.Version})
	}
}

func newCrossed(version string) upgradeCrossed {
	return upgradeCrossed{Version: version, Breaking: []upgradeChange{}, Added: []upgradeChange{},
		Changed: []upgradeChange{}, Fixed: []upgradeChange{}, AgentInstructions: []upgradeNote{}}
}

// ── CHANGELOG.md ─────────────────────────────────────────────────────────────

// changelogRelease is one `## [vX.Y.Z] - DATE` section.
type changelogRelease struct {
	Version, Date string
	Headings      []string
	Entries       map[string][]upgradeChange
}

var (
	changelogReleaseRE = regexp.MustCompile(`^## \[(v?[0-9][^\]]*)\](?:\s*-\s*(\S+))?`)
	changelogPRLinkRE  = regexp.MustCompile(`\s*\(\[#(\d+)\]\([^)]*\)\)\s*$`)
	changelogPRPlainRE = regexp.MustCompile(`\s*\(#(\d+)\)\s*$`)
	changelogScopeRE   = regexp.MustCompile(`^\*\*([^*]+?):\*\*\s*`)
)

// parseChangelog reads forge's CHANGELOG.md: each released section's entries
// by heading, with the nested `- Agent:` and `- Generated:` bullets the
// release script writes under an entry. [Unreleased] is skipped: nothing in
// it has shipped.
func parseChangelog(md []byte) []changelogRelease {
	var out []changelogRelease
	var cur *changelogRelease
	heading := ""
	var last *upgradeChange
	inFence := false
	sc := bufio.NewScanner(bytes.NewReader(md))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if strings.HasPrefix(line, "## ") {
			last, heading = nil, ""
			if m := changelogReleaseRE.FindStringSubmatch(line); m != nil {
				out = append(out, changelogRelease{Version: canonicalTag(m[1]), Date: m[2], Entries: map[string][]upgradeChange{}})
				cur = &out[len(out)-1]
			} else {
				cur = nil
			}
			continue
		}
		if cur == nil {
			continue
		}
		if strings.HasPrefix(line, "### ") {
			heading, last = strings.TrimSpace(line[4:]), nil
			if _, seen := cur.Entries[heading]; !seen {
				cur.Headings = append(cur.Headings, heading)
				cur.Entries[heading] = nil
			}
			continue
		}
		if heading == "" {
			continue
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* "):
			e := parseChangelogEntry(trimmed[2:])
			cur.Entries[heading] = append(cur.Entries[heading], e)
			es := cur.Entries[heading]
			last = &es[len(es)-1]
		case last != nil && (strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ")):
			// A nested bullet under the entry: a trailer the release
			// script rendered, or prose that belongs to the entry.
			sub := strings.TrimSpace(trimmed[2:])
			if v, ok := cutLabel(sub, "Agent:"); ok {
				last.Agent = append(last.Agent, v)
			} else if v, ok := cutLabel(sub, "Generated:"); ok {
				last.Generated = append(last.Generated, v)
			}
		case last != nil && trimmed != "" && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")):
			// A wrapped continuation of the entry's text.
			last.Text = strings.TrimSpace(last.Text + " " + trimmed)
		case trimmed == "":
		default:
			last = nil
		}
	}
	return out
}

func cutLabel(s, label string) (string, bool) {
	if len(s) < len(label) || !strings.EqualFold(s[:len(label)], label) {
		return "", false
	}
	return strings.TrimSpace(s[len(label):]), true
}

// parseChangelogEntry splits `**scope:** text ([#N](url))` into its parts.
func parseChangelogEntry(s string) upgradeChange {
	var e upgradeChange
	if m := changelogPRLinkRE.FindStringSubmatchIndex(s); m != nil {
		e.PR, _ = strconv.Atoi(s[m[2]:m[3]])
		s = s[:m[0]]
	} else if m := changelogPRPlainRE.FindStringSubmatchIndex(s); m != nil {
		e.PR, _ = strconv.Atoi(s[m[2]:m[3]])
		s = s[:m[0]]
	}
	if m := changelogScopeRE.FindStringSubmatch(s); m != nil {
		e.Scope = m[1]
		s = s[len(m[0]):]
	}
	e.Text = strings.TrimSpace(s)
	return e
}

func canonicalTag(v string) string {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	return v
}

// ── commits and their trailers ───────────────────────────────────────────────

// forgeCommit is one commit of forge's history: a squash-merged pull request.
type forgeCommit struct {
	Subject, Body string
}

var conventionalRE = regexp.MustCompile(`^([A-Za-z]+)(\(([^)]+)\))?(!)?:\s+(.+)$`)

// heading and change classify a commit as scripts/changelog-release.sh does:
// feat → Added, fix → Fixed, anything else → Changed, and a `!` or a
// BREAKING CHANGE: line → Breaking changes. ok is false for a release or
// merge commit, which the changelog skips.
func (c forgeCommit) change() (heading string, e upgradeChange, ok bool) {
	subject := strings.TrimSpace(c.Subject)
	if strings.HasPrefix(subject, "chore: release v") || strings.HasPrefix(subject, "Merge ") {
		return "", e, false
	}
	if m := changelogPRPlainRE.FindStringSubmatchIndex(subject); m != nil {
		e.PR, _ = strconv.Atoi(subject[m[2]:m[3]])
		subject = subject[:m[0]]
	}
	typ, breaking := "", false
	e.Text = subject
	if m := conventionalRE.FindStringSubmatch(subject); m != nil {
		typ, e.Scope, breaking, e.Text = strings.ToLower(m[1]), m[3], m[4] == "!", m[5]
	}
	tr := commitTrailers(c.Body)
	e.Agent, e.Generated = tr.agent, tr.generated
	breaking = breaking || tr.breaking
	switch {
	case breaking:
		heading = "Breaking changes"
	case typ == "feat":
		heading = "Added"
	case typ == "fix":
		heading = "Fixed"
	default:
		heading = "Changed"
	}
	return heading, e, true
}

type trailers struct {
	agent, generated []string
	breaking         bool
}

// commitTrailers reads the trailers upgrade cares about from a commit body.
// A trailer's value continues on following lines that start with
// whitespace, as git's trailer format allows.
func commitTrailers(body string) trailers {
	var t trailers
	var cur *[]string
	for _, line := range strings.Split(body, "\n") {
		if cur != nil && len(*cur) > 0 && line != "" && (line[0] == ' ' || line[0] == '\t') {
			(*cur)[len(*cur)-1] += " " + strings.TrimSpace(line)
			continue
		}
		cur = nil
		switch {
		case strings.HasPrefix(line, "BREAKING CHANGE:") || strings.HasPrefix(line, "BREAKING-CHANGE:"):
			t.breaking = true
		default:
			if v, ok := cutLabel(line, "Agent-Instructions:"); ok && v != "" {
				t.agent = append(t.agent, v)
				cur = &t.agent
			} else if v, ok := cutLabel(line, "Generated-Change:"); ok && v != "" {
				t.generated = append(t.generated, v)
				cur = &t.generated
			}
		}
	}
	return t
}

// forgeHistory answers questions about forge's commit graph offline. nil
// when no clone of forge is at hand.
type forgeHistory interface {
	// Resolve expands a revision to its full local commit ID, or "" if unavailable.
	Resolve(rev string) string
	// Has reports whether rev names a commit the clone holds.
	Has(rev string) bool
	// IsAncestor reports whether a is an ancestor of (or equal to) b.
	IsAncestor(a, b string) bool
	// Log returns the non-merge commits in from..to, oldest first.
	Log(from, to string) ([]forgeCommit, error)
}

// gitHistory is a forgeHistory over a git directory (a bare clone).
type gitHistory struct{ dir string }

func (g gitHistory) git(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"--git-dir", g.dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return cmd.Output()
}

func (g gitHistory) Resolve(rev string) string {
	// Resolve the object prefix, not a ref of the same name. Go's cached
	// clone may contain such refs, and rev-parse <prefix> prefers them.
	out, err := g.git("rev-parse", "--disambiguate="+rev)
	if err != nil {
		return ""
	}
	var commit string
	for _, id := range strings.Fields(string(out)) {
		kind, err := g.git("cat-file", "-t", id)
		if err != nil || strings.TrimSpace(string(kind)) != "commit" {
			continue
		}
		if commit != "" {
			return "" // More than one commit matches: do not guess.
		}
		commit = id
	}
	return commit
}

func (g gitHistory) Has(rev string) bool {
	_, err := g.git("cat-file", "-e", rev+"^{commit}")
	return err == nil
}

func (g gitHistory) IsAncestor(a, b string) bool {
	_, err := g.git("merge-base", "--is-ancestor", a, b)
	return err == nil
}

func (g gitHistory) Log(from, to string) ([]forgeCommit, error) {
	out, err := g.git("log", "--no-merges", "--reverse", "--format=%s%x1f%b%x1e", from+".."+to)
	if err != nil {
		return nil, err
	}
	var cs []forgeCommit
	for _, rec := range strings.Split(string(out), "\x1e") {
		rec = strings.TrimLeft(rec, "\n")
		if rec == "" {
			continue
		}
		subject, body, _ := strings.Cut(rec, "\x1f")
		cs = append(cs, forgeCommit{Subject: subject, Body: body})
	}
	return cs, nil
}

// findForgeHistory is the clone of forge the go command keeps in its module
// cache, or nil. A variable so tests supply their own.
var findForgeHistory = func() forgeHistory {
	cache := goModCache()
	if cache == "" {
		return nil
	}
	dirs, _ := filepath.Glob(filepath.Join(cache, "cache", "vcs", "*"))
	for _, d := range dirs {
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			continue
		}
		cfg, err := os.ReadFile(filepath.Join(d, "config"))
		if err != nil || !bytes.Contains(cfg, []byte("github.com/reliant-labs/forge")) {
			continue
		}
		// Not github.com/reliant-labs/forge-something: the url ends there.
		for _, line := range strings.Split(string(cfg), "\n") {
			line = strings.TrimSpace(line)
			if v, ok := strings.CutPrefix(line, "url = "); ok {
				v = strings.TrimSuffix(strings.TrimSuffix(v, "/"), ".git")
				if strings.HasSuffix(v, "github.com/reliant-labs/forge") || strings.HasSuffix(v, "github.com:reliant-labs/forge") {
					return gitHistory{dir: d}
				}
			}
		}
	}
	return nil
}

// goModCache is GOMODCACHE as the go command resolves it, without running go
// when the environment already says.
func goModCache() string {
	if v := os.Getenv("GOMODCACHE"); v != "" {
		return v
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "env", "GOMODCACHE")
	cmd.Env = goexec.Env()
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ── the range ────────────────────────────────────────────────────────────────

// changelogSource is the CHANGELOG.md that describes target, and what it is.
// A variable so tests supply their own.
var changelogSource = func(target string) (md []byte, from string) {
	if cache := goModCache(); cache != "" && semverKey(target) != "" {
		esc, err := module.EscapePath("github.com/reliant-labs/forge")
		if err == nil {
			p := filepath.Join(cache, filepath.FromSlash(esc)+"@"+canonicalTag(target), "CHANGELOG.md")
			if b, err := os.ReadFile(p); err == nil {
				return b, "the CHANGELOG.md of forge " + canonicalTag(target) + " in the Go module cache"
			}
		}
	}
	bin := semverKey(buildinfo.Version())
	if bin == "" || semverKey(target) == "" || semver.Compare(bin, semverKey(target)) >= 0 || forgeIdentity(buildinfo.Version()) == forgeIdentity(target) {
		return forge.Changelog(), "the CHANGELOG.md built into this forge"
	}
	return nil, ""
}

// crossedRange is what forge from..to brings: one entry per release tag in
// (from, to], oldest first, then the untagged tail when to is a
// pseudo-version. hist may be nil.
func crossedRange(from, to string, releases []changelogRelease, hist forgeHistory) []upgradeCrossed {
	fromKey, toKey := semverKey(from), semverKey(to)
	if fromKey == "" || toKey == "" || semver.Compare(fromKey, toKey) >= 0 {
		return nil
	}
	fromRev, toRev := revOf(from), revOf(to)
	resolved := true
	// Pseudo-versions carry abbreviated hashes. Expand them in this clone
	// before asking about membership, ancestry or commit ranges.
	if hist != nil && module.IsPseudoVersion(fromKey) {
		rev := hist.Resolve(fromRev)
		resolved = rev != ""
		if rev != "" {
			fromRev = rev
		}
	}
	if hist != nil && module.IsPseudoVersion(toKey) {
		rev := hist.Resolve(toRev)
		resolved = resolved && rev != ""
		if rev != "" {
			toRev = rev
		}
	}
	haveGit := hist != nil && resolved && fromRev != "" && toRev != "" && hist.Has(fromRev) && hist.Has(toRev)

	// PRs the old pin already had beyond its base tag: a pseudo-version pin
	// is a commit some way into the next release.
	inFrom := map[int]bool{}
	fromBase, fromIsPseudo := pseudoBase(fromKey)
	if haveGit && fromIsPseudo && fromBase != "" && hist.Has(fromBase) {
		if cs, err := hist.Log(fromBase, fromRev); err == nil {
			for _, c := range cs {
				if _, e, ok := c.change(); ok && e.PR > 0 {
					inFrom[e.PR] = true
				}
			}
		}
	}

	var out []upgradeCrossed
	var tags []changelogRelease
	for _, rel := range releases {
		k := semverKey(rel.Version)
		if k == "" || semver.Prerelease(k) != "" {
			continue
		}
		if semver.Compare(k, fromKey) > 0 && semver.Compare(k, toKey) <= 0 {
			tags = append(tags, rel)
		}
	}
	sort.SliceStable(tags, func(i, j int) bool { return semver.Compare(semverKey(tags[i].Version), semverKey(tags[j].Version)) < 0 })
	for _, rel := range tags {
		c := newCrossed(rel.Version)
		c.Date, c.Source, c.URL = rel.Date, "changelog", forgeRepoURL+"/releases/tag/"+rel.Version
		if haveGit && hist.Has(rel.Version) && hist.IsAncestor(rel.Version, fromRev) {
			// A pin spelled as a pseudo-version can name the very commit
			// a later tag was cut on: semver says it is crossed, the
			// history says the pin already has it.
			c.InPin = true
			c.Note = fmt.Sprintf("already in your pin: %s's commit is in the history of %s, so this release brings nothing new", rel.Version, from)
			out = append(out, c)
			continue
		}
		omitted := 0
		for _, h := range rel.Headings {
			for _, e := range rel.Entries[h] {
				if e.PR > 0 && inFrom[e.PR] {
					omitted++
					continue
				}
				c.add(h, e)
			}
		}
		switch {
		case omitted > 0:
			c.Note = fmt.Sprintf("%d %s already in your pin %s and left out", omitted, plural(omitted, "entry was", "entries were"), from)
		case fromIsPseudo && !haveGit && semver.Compare(fromKey, semverKey(rel.Version)) < 0 && fromBase != "" && semver.Compare(fromBase, semverKey(rel.Version)) < 0:
			c.Note = fmt.Sprintf("your pin %s is a commit after %s, so some of these entries may already be in it (no local clone of forge to tell which)", from, fromBase)
		}
		out = append(out, c)
	}

	// The untagged tail of a pseudo-version target.
	toBase, toIsPseudo := pseudoBase(toKey)
	if !toIsPseudo {
		return out
	}
	after := toBase
	if after == "" || semver.Compare(fromKey, toBase) > 0 {
		after = fromRev // both pins sit after the same tag
	}
	tail := newCrossed(canonicalTag(to))
	displayAfter := after
	if after == fromRev {
		displayAfter = revOf(from)
	}
	tail.Untagged, tail.After, tail.Commit = true, displayAfter, shortRev(toRev)
	tail.URL = fmt.Sprintf("%s/compare/%s...%s", forgeRepoURL, shortRev(after), shortRev(toRev))
	if haveGit && after != "" && hist.Has(after) {
		cs, err := hist.Log(after, toRev)
		if err == nil {
			n := len(cs)
			tail.Commits, tail.Source = &n, "git"
			for _, cm := range cs {
				if h, e, ok := cm.change(); ok && !(e.PR > 0 && inFrom[e.PR]) {
					tail.add(h, e)
				}
			}
			out = append(out, tail)
			return out
		}
	}
	tail.Source = "unavailable"
	tail.Note = fmt.Sprintf("no local clone of forge holds %s, so the commits after %s are not listed; see %s", shortRev(toRev), shortRev(after), tail.URL)
	return append(out, tail)
}

// pseudoBase is the release a pseudo-version follows ("" when it follows no
// tag: v0.0.0-…), and whether v is a pseudo-version at all.
func pseudoBase(v string) (string, bool) {
	if !module.IsPseudoVersion(v) {
		return "", false
	}
	base, err := module.PseudoVersionBase(v)
	if err != nil {
		return "", true
	}
	return base, true
}

// revOf is the git revision a version names: a pseudo-version's commit, or
// the tag itself.
func revOf(v string) string {
	k := semverKey(v)
	if k == "" {
		return ""
	}
	if module.IsPseudoVersion(k) {
		rev, err := module.PseudoVersionRev(k)
		if err != nil {
			return ""
		}
		return rev
	}
	return k
}

// crossedFor is crossedRange from the sources at hand.
func crossedFor(from, to string) []upgradeCrossed {
	md, _ := changelogSource(to)
	var hist forgeHistory
	if h := findForgeHistory(); h != nil {
		hist = h
	}
	out := crossedRange(from, to, parseChangelog(md), hist)
	if md == nil {
		for i := range out {
			if !out[i].Untagged {
				out[i].Source = "unavailable"
				out[i].Note = "no CHANGELOG.md describing " + to + " is at hand offline (this forge is older, and the module cache has no copy); see " + out[i].URL
			}
		}
	}
	return out
}

// ── printing ─────────────────────────────────────────────────────────────────

// crossedTitlesShown caps the entry titles the text report lists per part;
// --json carries every entry.
const crossedTitlesShown = 8

// printCrossed is the "what this range brings" section: the agent
// instructions first (what to DO), then each part with its counts, its
// breaking changes, and its first titles.
func printCrossed(w io.Writer, from, to string, crossed []upgradeCrossed) {
	if len(crossed) == 0 {
		return
	}
	fmt.Fprintf(w, "\nWhat forge %s → %s brings:\n", from, to)
	var agent []upgradeNote
	for _, c := range crossed {
		agent = append(agent, c.AgentInstructions...)
	}
	if len(agent) > 0 {
		fmt.Fprintln(w, "  Do this, from the changes you crossed:")
		for _, a := range agent {
			fmt.Fprintf(w, "    - %s%s\n", a.Text, prRef(a.PR))
		}
	}
	for _, c := range crossed {
		fmt.Fprintf(w, "  %s\n", crossedHeadline(c))
		if c.Note != "" && !c.InPin {
			fmt.Fprintf(w, "    note: %s\n", c.Note)
		}
		if c.InPin || c.total() == 0 {
			continue
		}
		for _, e := range c.Breaking {
			fmt.Fprintf(w, "    BREAKING %s\n", changeTitle(e))
		}
		shown := len(c.Breaking)
		rest := append(append(append([]upgradeChange{}, c.Added...), c.Fixed...), c.Changed...)
		for _, e := range rest {
			if shown >= crossedTitlesShown {
				break
			}
			fmt.Fprintf(w, "    - %s\n", changeTitle(e))
			shown++
		}
		if more := c.total() - shown; more > 0 {
			fmt.Fprintf(w, "    … %d more (crossed[] in --json; %s)\n", more, c.URL)
		}
	}
}

func crossedHeadline(c upgradeCrossed) string {
	counts := countsPhrase(c)
	switch {
	case c.InPin:
		return fmt.Sprintf("%s: already in your pin, nothing new", c.Version)
	case c.Untagged && c.Commits != nil:
		return fmt.Sprintf("%d %s after %s, up to %s (git): %s", *c.Commits, plural(*c.Commits, "commit", "commits"), shortRev(c.After), c.Commit, counts)
	case c.Untagged:
		return fmt.Sprintf("commits after %s, up to %s: not known offline — %s", shortRev(c.After), c.Commit, c.URL)
	case c.Source == "unavailable":
		return fmt.Sprintf("%s: release notes not at hand offline — %s", c.Version, c.URL)
	}
	head := c.Version
	if c.Date != "" {
		head += " (" + c.Date + ")"
	}
	return head + ": " + counts
}

func countsPhrase(c upgradeCrossed) string {
	var parts []string
	for _, p := range []struct {
		n    int
		what string
	}{{len(c.Breaking), "breaking"}, {len(c.Added), "added"}, {len(c.Fixed), "fixed"}, {len(c.Changed), "changed"}} {
		if p.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", p.n, p.what))
		}
	}
	other := make([]string, 0, len(c.Other))
	for h := range c.Other {
		other = append(other, h)
	}
	sort.Strings(other)
	for _, h := range other {
		parts = append(parts, fmt.Sprintf("%d %s", len(c.Other[h]), strings.ToLower(h)))
	}
	if len(parts) == 0 {
		return "no entries"
	}
	return strings.Join(parts, ", ")
}

func changeTitle(e upgradeChange) string {
	t := e.Text
	if e.Scope != "" {
		t = e.Scope + ": " + t
	}
	return shorten(t, 140) + prRef(e.PR)
}

func prRef(pr int) string {
	if pr <= 0 {
		return ""
	}
	return fmt.Sprintf(" (#%d)", pr)
}
