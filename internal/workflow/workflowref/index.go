// Copyright (c) 2025 Reliant Labs
package workflowref

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// File is one workflow definition a project holds.
type File struct {
	// Repo is the nested repository the file belongs to: its slash-separated
	// path relative to the project root ("control-plane"), or "" for the
	// project root's own .reliant/workflows.
	Repo string
	// Path is slash-separated and relative to the workflows directory
	// ("deploy.yaml") of Repo. It is used in messages and to say which file a
	// ref probably meant; it is never an address.
	Path    string
	Content []byte
}

// Entry is a project workflow as the index sees it.
type Entry struct {
	// Repo is the nested repository the file came from, "" for the project
	// root (see File.Repo).
	Repo    string
	Path    string
	Content []byte
	// Name is the name: field, trimmed; "" when the file declares none.
	Name string
	// Slug is Slug(Name): what project://<name> is compared against.
	Slug string
	// Address is the slug a ref names this workflow by: Slug, or
	// "<repo>/<slug>" for a nested repo's workflow whose bare name the
	// project root or another nested repo also claims (see Index). List
	// surfaces show it so the name they offer is one that resolves.
	Address string
	// Problem is why no ref can address this file (no name, a name another
	// file in the same directory also declares, a name that is another
	// file's file name), or nil.
	Problem error
	// Warning is why this workflow is reachable only by its repo-qualified
	// Address (the project root, or another nested repo, claims its bare
	// name), or nil.
	Warning error
}

// Stem is the file name without its extension.
func (e *Entry) Stem() string {
	base := path.Base(e.Path)
	return strings.TrimSuffix(base, path.Ext(base))
}

// Location is where the file is, for messages: Path for the project root's
// own workflows, and the project-relative path
// ("control-plane/.reliant/workflows/deploy.yaml") for a nested repo's.
func (e *Entry) Location() string {
	if e.Repo == "" {
		return e.Path
	}
	return RelativePath(e.Repo, e.Path)
}

// named describes the entry's name for a message.
func (e *Entry) named() string {
	if e.Name == "" {
		return "has no name: field"
	}
	return fmt.Sprintf("is named %q", e.Name)
}

// RelativePath is where a workflows-directory file sits relative to the
// project root: ".reliant/workflows/<p>" for the root, and
// "<repo>/.reliant/workflows/<p>" for a nested repo. It is the path the
// daemon syncs a file under.
func RelativePath(repo, p string) string {
	if repo == "" {
		return path.Join(Dir, p)
	}
	return path.Join(repo, Dir, p)
}

// SplitRelativePath is the inverse of RelativePath: the repo and the path
// within its workflows directory. ok is false when rel is in no workflows
// directory.
func SplitRelativePath(rel string) (repo, p string, ok bool) {
	rel = path.Clean(strings.TrimPrefix(rel, "./"))
	if rest, found := strings.CutPrefix(rel, Dir+"/"); found {
		return "", rest, true
	}
	// The LAST occurrence: a repo nested in a directory that is itself named
	// .reliant/workflows is not a thing, but a workflows dir inside a repo
	// whose path contains one is not ours to misread either.
	if i := strings.LastIndex(rel, "/"+Dir+"/"); i > 0 {
		return rel[:i], rel[i+len("/"+Dir+"/"):], true
	}
	return "", "", false
}

// group is one workflows directory — the project root's, or one nested
// repo's — as the per-directory rules see it. The name rules (two files, one
// name; a name that is another file's file name) are about what an author
// reading ONE directory sees, so they apply within a group, never across.
type group struct {
	repo   string
	bySlug map[string][]*Entry // every file that claims a slug by its name:
	byStem map[string][]*Entry // Slug(file stem) -> files, for messages
}

// Index is a project's workflows keyed by name. The CLI builds it from disk
// and the server from what the daemon synced, with this one function, so a
// ref resolves the same way on both.
//
// A project's workflows come from the project root's .reliant/workflows and
// from each nested repo's. A bare name resolves:
//
//  1. to the project root's workflow of that name, if it has one — the root
//     wins, and a nested repo's workflow of the same name is reachable only as
//     project://<repo>/<name>;
//  2. else to the one nested repo's workflow of that name;
//  3. and when two or more nested repos (and not the root) declare it, to
//     neither: each is reachable only as project://<repo>/<name>.
//
// project://<repo>/<name> always names that repo's workflow. Each shadowed or
// ambiguous workflow carries a Warning, and Warnings lists them all.
type Index struct {
	entries []*Entry
	groups  map[string]*group // RepoKey(repo) -> group; "" is the root
	byPath  map[string]*Entry // Location() -> entry
}

// NewIndex indexes a project's workflow files.
func NewIndex(files []File) *Index {
	ix := &Index{
		groups: map[string]*group{},
		byPath: map[string]*Entry{},
	}
	for _, f := range files {
		e := &Entry{Repo: strings.Trim(f.Repo, "/"), Path: f.Path, Content: f.Content}
		where := e.Location()
		if e.Path == "" {
			where = "a synced workflow"
		}
		name, err := declaredName(f.Content)
		if err != nil {
			// A file that does not parse still claims the name it declares
			// (or, failing that, its file name), so a ref to it fails with
			// the parse error. Unclaimed, the ref would quietly resolve to
			// another directory's workflow of the same name: an older copy
			// running because the newest one is broken.
			name = recoverDeclaredName(f.Content)
			if name == "" {
				name = e.Stem()
			}
		}
		e.Name = name
		e.Slug = Slug(name)
		e.Address = e.Slug
		switch {
		case err != nil:
			e.Problem = fmt.Errorf("%s does not parse, so workflow %q cannot run until it is fixed: %w", where, name, err)
		case name == "":
			e.Problem = fmt.Errorf("%s has no name: field; a project workflow is addressed by its name: (project://<name>)", where)
		case e.Slug == "":
			e.Problem = fmt.Errorf("%s is named %q, which has no letters or digits to address it by", where, name)
		}
		ix.entries = append(ix.entries, e)
		g := ix.group(e.Repo)
		if e.Slug != "" {
			g.bySlug[e.Slug] = append(g.bySlug[e.Slug], e)
		}
		if e.Path == "" {
			// Synced without its path (an older daemon): addressable by
			// name all the same, but no file name to cross.
			continue
		}
		ix.byPath[e.Location()] = e
		if stem := Slug(e.Stem()); stem != "" {
			g.byStem[stem] = append(g.byStem[stem], e)
		}
	}
	sort.SliceStable(ix.entries, func(i, j int) bool {
		if ix.entries[i].Repo != ix.entries[j].Repo {
			return ix.entries[i].Repo < ix.entries[j].Repo
		}
		return ix.entries[i].Path < ix.entries[j].Path
	})
	for _, g := range ix.groups {
		g.applyNameRules()
	}
	ix.qualifyCrossRepoNames()
	return ix
}

func (ix *Index) group(repo string) *group {
	key := RepoKey(repo)
	g := ix.groups[key]
	if g == nil {
		g = &group{repo: repo, bySlug: map[string][]*Entry{}, byStem: map[string][]*Entry{}}
		ix.groups[key] = g
	}
	return g
}

// applyNameRules marks the files of one directory no ref can address.
func (g *group) applyNameRules() {
	for _, claimants := range g.bySlug {
		sort.Slice(claimants, func(i, j int) bool { return claimants[i].Path < claimants[j].Path })
	}

	// Two files, one name: neither is addressable. Picking the first would be
	// a guess the author never sees.
	for slug, claimants := range g.bySlug {
		if len(claimants) < 2 {
			continue
		}
		for _, e := range claimants {
			if e.Problem != nil {
				continue
			}
			var others []string
			for _, o := range claimants {
				if o != e {
					others = append(others, o.Location())
				}
			}
			e.Problem = fmt.Errorf("%s: name %q is also declared by %s, so project://%s cannot tell them apart; give each workflow its own name",
				e.Location(), e.Name, strings.Join(others, ", "), qualify(g.repo, slug))
		}
	}

	// A name that is another file's file name. project://b loads the file
	// NAMED b, but an author reading the directory sees b.yaml — exactly the
	// confusion #623 was. Refuse it rather than load the one they did not mean.
	for slug, claimants := range g.bySlug {
		if len(claimants) != 1 || claimants[0].Problem != nil {
			continue
		}
		e := claimants[0]
		for _, other := range g.byStem[slug] {
			if other == e || other.Slug == slug {
				continue
			}
			e.Problem = fmt.Errorf("%s is named %q, but %s is a different workflow (it %s); project://%s would load %s, not %s — rename one so names and file names do not cross",
				e.Location(), e.Name, other.Location(), other.named(), qualify(g.repo, slug), e.Location(), other.Location())
			break
		}
	}
}

// qualifyCrossRepoNames applies the cross-directory rule: the root wins a
// bare name, and a bare name two nested repos share is neither's.
func (ix *Index) qualifyCrossRepoNames() {
	root := ix.groups[""]
	for _, slug := range ix.nestedSlugs() {
		claimants := ix.nestedClaimants(slug)
		var rootClaims []*Entry
		if root != nil {
			rootClaims = root.bySlug[slug]
		}
		if len(rootClaims) == 0 && len(claimants) < 2 {
			continue
		}
		for _, g := range claimants {
			for _, e := range g.bySlug[slug] {
				e.Address = qualify(e.Repo, slug)
				if len(rootClaims) > 0 {
					e.Warning = fmt.Errorf("%s is named %q, which the project root's %s also declares; the root's wins, so this one is project://%s",
						e.Location(), e.Name, rootClaims[0].Location(), e.Address)
					continue
				}
				e.Warning = fmt.Errorf("%s is named %q, which %s also declare%s; a bare %q names neither, so this one is project://%s",
					e.Location(), e.Name, otherLocations(claimants, g, slug), pluralS(len(claimants)-1), slug, e.Address)
			}
		}
	}
}

// nestedSlugs is every slug a nested repo claims, sorted.
func (ix *Index) nestedSlugs() []string {
	seen := map[string]bool{}
	var out []string
	for key, g := range ix.groups {
		if key == "" {
			continue
		}
		for slug := range g.bySlug {
			if !seen[slug] {
				seen[slug] = true
				out = append(out, slug)
			}
		}
	}
	sort.Strings(out)
	return out
}

// nestedClaimants is every nested repo whose directory claims slug, sorted
// by repo.
func (ix *Index) nestedClaimants(slug string) []*group {
	var out []*group
	for key, g := range ix.groups {
		if key != "" && len(g.bySlug[slug]) > 0 {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].repo < out[j].repo })
	return out
}

func otherLocations(claimants []*group, self *group, slug string) string {
	var others []string
	for _, g := range claimants {
		if g != self {
			others = append(others, g.bySlug[slug][0].Location())
		}
	}
	return strings.Join(others, ", ")
}

func pluralS(n int) string {
	if n == 1 {
		return "s"
	}
	return ""
}

// qualify is the address of slug in repo: slug itself in the project root.
func qualify(repo, slug string) string {
	if key := RepoKey(repo); key != "" {
		return key + "/" + slug
	}
	return slug
}

// declaredName reads a workflow file's name: field.
func declaredName(content []byte) (string, error) {
	var doc struct {
		Name yaml.Node `yaml:"name"`
	}
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return "", err
	}
	switch doc.Name.Kind {
	case 0:
		return "", nil
	case yaml.ScalarNode:
		return strings.TrimSpace(doc.Name.Value), nil
	default:
		return "", fmt.Errorf("name: is not a string")
	}
}

// recoverDeclaredName reads the top-level name: out of a file that is not
// valid YAML, or "" when there is none to read. Only the name: line itself is
// decoded, as YAML, so quoting and comments mean what they mean in the file
// ("Release #1" is a name; # starts a comment only outside quotes).
func recoverDeclaredName(content []byte) string {
	for _, line := range strings.Split(string(content), "\n") {
		if !strings.HasPrefix(line, "name:") {
			continue
		}
		var doc struct {
			Name yaml.Node `yaml:"name"`
		}
		if err := yaml.Unmarshal([]byte(line), &doc); err != nil || doc.Name.Kind != yaml.ScalarNode {
			return ""
		}
		return strings.TrimSpace(doc.Name.Value)
	}
	return ""
}

// Entries returns every indexed file, addressable or not, sorted by repo
// (the project root first) and then path.
func (ix *Index) Entries() []*Entry {
	if ix == nil {
		return nil
	}
	return ix.entries
}

// Warnings returns every workflow reachable only by its repo-qualified name,
// and why, in Entries order.
func (ix *Index) Warnings() []error {
	if ix == nil {
		return nil
	}
	var out []error
	for _, e := range ix.entries {
		if e.Warning != nil {
			out = append(out, e.Warning)
		}
	}
	return out
}

// EntryAt returns the entry for a file's Location (its path, for the project
// root's own workflows), or nil.
func (ix *Index) EntryAt(path string) *Entry {
	if ix == nil {
		return nil
	}
	return ix.byPath[path]
}

// Lookup returns the one workflow a slug names: a bare slug by the rules on
// Index, or a "<repo>/<slug>" (QualifiedSlug) in that repo. A miss matches
// ErrNotFound and says which file the caller probably meant; a name two files
// share, or a name that crosses another file's file name, is an error of its
// own.
func (ix *Index) Lookup(slug string) (*Entry, error) {
	if ix == nil {
		return nil, notFound("no project workflow is named %q (there is no project to look in)", slug)
	}
	if repo, base, qualified := SplitQualifiedSlug(slug); qualified {
		g := ix.groups[repo]
		if g == nil || repo == "" {
			return nil, notFound("no project workflow is named %q: no nested repo %q holds project workflows%s", slug, repo, ix.repoList())
		}
		return g.lookup(base)
	}

	if root := ix.groups[""]; root != nil && len(root.bySlug[slug]) > 0 {
		return root.lookup(slug)
	}
	switch claimants := ix.nestedClaimants(slug); len(claimants) {
	case 0:
	case 1:
		return claimants[0].lookup(slug)
	default:
		var refs []string
		for _, g := range claimants {
			refs = append(refs, "project://"+qualify(g.repo, slug))
		}
		return nil, fmt.Errorf("%q is the name of workflows in %d nested repos, so a bare name cannot tell them apart; address one by its repo: %s",
			slug, len(claimants), strings.Join(refs, " or "))
	}

	// A miss. When a FILE is called this, say what it is really named: that
	// is the mistake the name: rule most often catches. The root's files
	// first, then each nested repo's.
	for _, g := range ix.sortedGroups() {
		if err := g.stemHint(slug); err != nil {
			return nil, err
		}
	}
	return nil, notFound("no project workflow is named %q", slug)
}

// FileError is a ref that names a project workflow file which cannot run as
// written: it does not parse, or it claims a name it cannot hold. It names the
// file and carries the content that was refused, so a caller can say which
// version of which file broke, not only that something did. Its message is
// Err's.
type FileError struct {
	// Path is the file, relative to the project root
	// (".reliant/workflows/nightly.yaml", "<repo>/.reliant/workflows/…").
	Path string
	// Content is the file as it was synced: the version that was refused.
	Content []byte
	Err     error
}

func (e *FileError) Error() string { return e.Err.Error() }
func (e *FileError) Unwrap() error { return e.Err }

// fileError is err as a FileError naming e; nil stays nil.
func fileError(e *Entry, err error) error {
	if err == nil {
		return nil
	}
	rel := ""
	if e.Path != "" {
		rel = RelativePath(e.Repo, e.Path)
	}
	return &FileError{Path: rel, Content: e.Content, Err: err}
}

// ProjectRelative is a Resolved.Path (Entry.Location) relative to the project
// root: a root workflow's location is relative to its workflows directory, a
// nested repo's already to the root.
func ProjectRelative(location string) string {
	if location == "" {
		return ""
	}
	if _, _, ok := SplitRelativePath(location); ok {
		return location
	}
	return RelativePath("", location)
}

// lookup is Lookup within one directory.
func (g *group) lookup(slug string) (*Entry, error) {
	if claimants := g.bySlug[slug]; len(claimants) > 0 {
		if len(claimants) == 1 && claimants[0].Problem == nil {
			return claimants[0], nil
		}
		return nil, fileError(claimants[0], claimants[0].Problem)
	}
	if err := g.stemHint(slug); err != nil {
		return nil, err
	}
	if g.repo != "" {
		return nil, notFound("no project workflow is named %q in %s", slug, path.Join(g.repo, Dir))
	}
	return nil, notFound("no project workflow is named %q", slug)
}

// stemHint is the miss for a slug that is one of this directory's FILE
// names, or nil when none is.
func (g *group) stemHint(slug string) error {
	files := g.byStem[slug]
	switch {
	case len(files) == 0:
		return nil
	case files[0].Name == "":
		return notFound("no project workflow is named %q; %s has no name: field, and a project workflow is addressed by its name:", slug, files[0].Location())
	default:
		return notFound("no project workflow is named %q; %s is named %q — project:// addresses a workflow by its name:, not its file name (use project://%s)",
			slug, files[0].Location(), files[0].Name, files[0].Address)
	}
}

// sortedGroups is the project root's group first, then each nested repo's.
func (ix *Index) sortedGroups() []*group {
	keys := make([]string, 0, len(ix.groups))
	for k := range ix.groups {
		keys = append(keys, k)
	}
	sort.Strings(keys) // "" (the root) sorts first
	out := make([]*group, 0, len(keys))
	for _, k := range keys {
		out = append(out, ix.groups[k])
	}
	return out
}

// repoList names the nested repos that do hold workflows, for a message.
func (ix *Index) repoList() string {
	var repos []string
	for _, g := range ix.sortedGroups() {
		if g.repo != "" {
			repos = append(repos, g.repo)
		}
	}
	if len(repos) == 0 {
		return ""
	}
	return " (those that do: " + strings.Join(repos, ", ") + ")"
}
