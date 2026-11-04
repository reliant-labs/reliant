package workflowref

import (
	"fmt"
	"sort"
)

// MergeLayouts combines the root's workflows with nested repo layouts. Paths
// remain relative to their own workflows directory; Repo identifies that
// directory. Name resolution is recomputed across every source, once.
func MergeLayouts(root *Layout, repos map[string]*Layout) *Layout {
	out := &Layout{}
	var files []File
	add := func(repo string, l *Layout) {
		for _, e := range l.Workflows.Entries() {
			files = append(files, File{Repo: repo, Path: e.Path, Content: e.Content})
		}
		for _, s := range l.Scenarios {
			// Ownership must resolve in this directory, before global fallback
			// exists. An orphan must never bind to a root/sibling workflow.
			if _, err := l.Workflows.Lookup(s.WorkflowSlug); err != nil {
				out.Misplaced = append(out.Misplaced, fmt.Errorf("%s: scenario has no valid workflow in its source directory: %w", RelativePath(repo, s.Path), err))
				continue
			}
			s.Repo = repo
			out.Scenarios = append(out.Scenarios, s)
		}
		out.Misplaced = append(out.Misplaced, l.Misplaced...)
	}
	add("", root)
	names := make([]string, 0, len(repos))
	for name := range repos {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		add(name, repos[name])
	}
	out.Workflows = NewIndex(files)
	for i := range out.Scenarios {
		sc := &out.Scenarios[i]
		if e, err := out.Workflows.Lookup(qualify(sc.Repo, sc.WorkflowSlug)); err == nil {
			sc.WorkflowSlug = e.Address
		}
	}
	return out
}

// FilePath returns a file's location relative to the root workflows directory
// used by ReadLayout. Nested files are reached through the project root, not
// a fictitious subdirectory inside the root workflows directory.
func FilePath(repo, p string) string {
	if repo == "" {
		return p
	}
	return "../../" + RelativePath(repo, p)
}
