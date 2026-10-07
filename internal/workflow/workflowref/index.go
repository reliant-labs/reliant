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
	// Path is slash-separated and relative to the workflows directory
	// ("deploy.yaml"). It is used in messages and to say which file a ref
	// probably meant; it is never an address.
	Path    string
	Content []byte
}

// Entry is a project workflow as the index sees it.
type Entry struct {
	Path    string
	Content []byte
	// Name is the name: field, trimmed; "" when the file declares none.
	Name string
	// Slug is Slug(Name): what project://<name> is compared against.
	Slug string
	// Problem is why no ref can address this file (no name, a name another
	// file also declares, a name that is another file's file name), or nil.
	Problem error
}

// Stem is the file name without its extension.
func (e *Entry) Stem() string {
	base := path.Base(e.Path)
	return strings.TrimSuffix(base, path.Ext(base))
}

// named describes the entry's name for a message.
func (e *Entry) named() string {
	if e.Name == "" {
		return "has no name: field"
	}
	return fmt.Sprintf("is named %q", e.Name)
}

// Index is a project's workflows keyed by name. The CLI builds it from disk
// and the server from what the daemon synced, with this one function, so a
// ref resolves the same way on both.
type Index struct {
	entries []*Entry
	bySlug  map[string][]*Entry // every file that claims a slug by its name:
	byStem  map[string][]*Entry // Slug(file stem) -> files, for messages
	byPath  map[string]*Entry
}

// NewIndex indexes a project's workflow files.
func NewIndex(files []File) *Index {
	ix := &Index{
		bySlug: map[string][]*Entry{},
		byStem: map[string][]*Entry{},
		byPath: map[string]*Entry{},
	}
	for _, f := range files {
		e := &Entry{Path: f.Path, Content: f.Content}
		where := e.Path
		if where == "" {
			where = "a synced workflow"
		}
		name, err := declaredName(f.Content)
		e.Name = name
		e.Slug = Slug(name)
		switch {
		case err != nil:
			e.Problem = fmt.Errorf("%s: cannot read its name: %w", where, err)
		case name == "":
			e.Problem = fmt.Errorf("%s has no name: field; a project workflow is addressed by its name: (project://<name>)", where)
		case e.Slug == "":
			e.Problem = fmt.Errorf("%s is named %q, which has no letters or digits to address it by", where, name)
		}
		ix.entries = append(ix.entries, e)
		if e.Slug != "" {
			ix.bySlug[e.Slug] = append(ix.bySlug[e.Slug], e)
		}
		if e.Path == "" {
			// Synced without its path (an older daemon): addressable by
			// name all the same, but no file name to cross.
			continue
		}
		ix.byPath[e.Path] = e
		if stem := Slug(e.Stem()); stem != "" {
			ix.byStem[stem] = append(ix.byStem[stem], e)
		}
	}
	sort.Slice(ix.entries, func(i, j int) bool { return ix.entries[i].Path < ix.entries[j].Path })
	for _, claimants := range ix.bySlug {
		sort.Slice(claimants, func(i, j int) bool { return claimants[i].Path < claimants[j].Path })
	}

	// Two files, one name: neither is addressable. Picking the first would be
	// a guess the author never sees.
	for slug, claimants := range ix.bySlug {
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
					others = append(others, o.Path)
				}
			}
			e.Problem = fmt.Errorf("%s: name %q is also declared by %s, so project://%s cannot tell them apart; give each workflow its own name",
				e.Path, e.Name, strings.Join(others, ", "), slug)
		}
	}

	// A name that is another file's file name. project://b loads the file
	// NAMED b, but an author reading the directory sees b.yaml — exactly the
	// confusion #623 was. Refuse it rather than load the one they did not mean.
	for slug, claimants := range ix.bySlug {
		if len(claimants) != 1 || claimants[0].Problem != nil {
			continue
		}
		e := claimants[0]
		for _, other := range ix.byStem[slug] {
			if other == e || other.Slug == slug {
				continue
			}
			e.Problem = fmt.Errorf("%s is named %q, but %s is a different workflow (it %s); project://%s would load %s, not %s — rename one so names and file names do not cross",
				e.Path, e.Name, other.Path, other.named(), slug, e.Path, other.Path)
			break
		}
	}
	return ix
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

// Entries returns every indexed file, addressable or not, sorted by path.
func (ix *Index) Entries() []*Entry {
	if ix == nil {
		return nil
	}
	return ix.entries
}

// EntryAt returns the entry for a file path, or nil.
func (ix *Index) EntryAt(path string) *Entry {
	if ix == nil {
		return nil
	}
	return ix.byPath[path]
}

// Lookup returns the one workflow named slug. A miss matches ErrNotFound and
// says which file the caller probably meant; a name two files share, or a
// name that crosses another file's file name, is an error of its own.
func (ix *Index) Lookup(slug string) (*Entry, error) {
	if ix == nil {
		return nil, notFound("no project workflow is named %q (there is no project to look in)", slug)
	}
	if claimants := ix.bySlug[slug]; len(claimants) > 0 {
		if len(claimants) == 1 && claimants[0].Problem == nil {
			return claimants[0], nil
		}
		return nil, claimants[0].Problem
	}
	// A miss. When a FILE is called this, say what it is really named: that
	// is the mistake the name: rule most often catches.
	files := ix.byStem[slug]
	switch {
	case len(files) == 0:
		return nil, notFound("no project workflow is named %q", slug)
	case files[0].Name == "":
		return nil, notFound("no project workflow is named %q; %s has no name: field, and a project workflow is addressed by its name:", slug, files[0].Path)
	default:
		return nil, notFound("no project workflow is named %q; %s is named %q — project:// addresses a workflow by its name:, not its file name (use project://%s)",
			slug, files[0].Path, files[0].Name, files[0].Slug)
	}
}
