package cmd

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing/fstest"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/triggers/triggerspec"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// Activations of a workflow's declared triggers live in the server's
// database, one per person who turned a trigger on, and name the trigger they
// activate. A change to the declarations that those rows cannot follow — the
// workflow deleted, a trigger renamed or removed, a trigger turned into
// another kind, an integration trigger moved to another integration — leaves
// every such activation BROKEN: each firing is refused. Validating the files
// alone cannot see that, because the file that breaks them is valid. Given the
// revision the files are changing from (--base), the CLI can: it compares the
// declarations there with the ones here.
//
// They are reported as warnings, not errors. The CLI cannot see the
// activations, so it cannot know whether any exist, and removing a trigger
// nobody turned on is an ordinary edit. The server is what knows: it lists an
// enabled activation that a synced change broke in its owner's Inbox.

// declaredTrigger is what an activation of a declared trigger is bound to.
type declaredTrigger struct {
	kind        string
	integration string
}

// activationBreaks compares the root project workflows of current with those
// in dir at the git revision base. byName maps a current workflow's name to
// what its edit breaks; deleted lists workflows that are gone, each with what
// that breaks. dir is a workflows directory inside a git work tree.
func activationBreaks(dir, base string, current *workflowref.Layout) (byName map[string][]string, deleted []validateResult, err error) {
	before, err := layoutAtRevision(dir, base)
	if err != nil {
		return nil, nil, err
	}
	now := map[string]*workflowref.Entry{}
	for _, e := range current.Workflows.Entries() {
		if e.Repo == "" && e.Slug != "" {
			now[e.Slug] = e
		}
	}
	byName = map[string][]string{}
	for _, old := range before.Workflows.Entries() {
		if old.Slug == "" {
			continue
		}
		was := declaredTriggers(old.Content)
		if len(was) == 0 {
			continue
		}
		cur := now[old.Slug]
		if cur == nil {
			deleted = append(deleted, validateResult{
				File:  old.Path,
				Name:  old.Name,
				Valid: true,
				ActivationBreaks: []string{fmt.Sprintf(
					"deleted: at %s this workflow declared %s; every activation of %s turns BROKEN and fires nothing. "+
						"Turn those activations off or remove them (reliant trigger list) as part of this change",
					base, quotedList(sortedKeys(was)), pronoun(len(was)))},
			})
			continue
		}
		is := declaredTriggers(cur.Content)
		var breaks []string
		for _, name := range sortedKeys(was) {
			w := was[name]
			n, ok := is[name]
			switch {
			case !ok:
				declares := "none"
				if len(is) > 0 {
					declares = quotedList(sortedKeys(is))
				}
				breaks = append(breaks, fmt.Sprintf(
					"trigger %q is gone (it now declares %s): every activation of it turns BROKEN. "+
						"Keep the name, or remove its activations (reliant trigger list)", name, declares))
			case n.kind != w.kind:
				breaks = append(breaks, fmt.Sprintf(
					"trigger %q changed from a %s to a %s trigger: an activation cannot follow that and turns BROKEN until it is activated again",
					name, w.kind, n.kind))
			case n.integration != w.integration:
				breaks = append(breaks, fmt.Sprintf(
					"trigger %q now listens to %s instead of %s: an activation's connection is for %s, so it turns BROKEN until it is activated again",
					name, n.integration, w.integration, w.integration))
			}
		}
		if len(breaks) > 0 {
			byName[cur.Name] = breaks
		}
	}
	return byName, deleted, nil
}

// declaredTriggers is name -> binding for what a workflow file declares. A
// file that does not parse declares nothing here: its parse error is already
// the file's validation error.
func declaredTriggers(content []byte) map[string]declaredTrigger {
	wf, err := wfyaml.ParseWorkflow(content)
	if err != nil {
		return nil
	}
	out := map[string]declaredTrigger{}
	for _, wt := range wf.GetTriggers() {
		out[wt.GetName()] = bindingOf(wt)
	}
	return out
}

func bindingOf(wt *reliantv1.WorkflowTrigger) declaredTrigger {
	src, err := triggerspec.FromWorkflowTrigger(wt)
	if err != nil {
		return declaredTrigger{kind: "invalid"}
	}
	return declaredTrigger{kind: string(src.Kind), integration: src.Integration}
}

// layoutAtRevision reads dir as it is at rev, the way the app reads a
// workflows directory (workflowref.ReadLayout).
func layoutAtRevision(dir, rev string) (*workflowref.Layout, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	// The directory may be gone in this change (every workflow deleted): ask
	// git from the nearest directory that still exists.
	existing, missing := abs, ""
	for {
		if _, statErr := os.Stat(existing); statErr == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		missing = filepath.Join(filepath.Base(existing), missing)
		existing = parent
	}
	top, err := gitOutput(existing, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("--base needs %s to be inside a git work tree: %w", dir, err)
	}
	top = strings.TrimSpace(top)
	if resolved, err := filepath.EvalSymlinks(existing); err == nil {
		existing = resolved
	}
	abs = filepath.Join(existing, missing)
	if resolved, err := filepath.EvalSymlinks(top); err == nil {
		top = resolved
	}
	rel, err := filepath.Rel(top, abs)
	if err != nil {
		return nil, err
	}
	prefix := filepath.ToSlash(rel)
	if _, err := gitOutput(top, "rev-parse", "--verify", "--quiet", rev+"^{commit}"); err != nil {
		return nil, fmt.Errorf("--base %s is not a commit in %s", rev, top)
	}
	listing, err := gitOutput(top, "ls-tree", "-r", "-z", "--name-only", rev, "--", prefix)
	if err != nil {
		return nil, fmt.Errorf("list %s at %s: %w", prefix, rev, err)
	}
	files := fstest.MapFS{}
	for _, name := range strings.Split(listing, "\x00") {
		if name == "" || !workflowref.IsYAML(name) {
			continue
		}
		content, err := gitOutput(top, "cat-file", "blob", rev+":"+name)
		if err != nil {
			return nil, fmt.Errorf("read %s at %s: %w", name, rev, err)
		}
		inDir := strings.TrimPrefix(name, prefix+"/")
		if prefix == "." {
			inDir = name
		}
		files[path.Clean(inDir)] = &fstest.MapFile{Data: []byte(content)}
	}
	return workflowref.ReadLayout(files)
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("git %s: %s", args[0], msg)
		}
		return "", err
	}
	return string(out), nil
}

func sortedKeys(m map[string]declaredTrigger) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func quotedList(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = fmt.Sprintf("%q", n)
	}
	noun := "triggers"
	if len(names) == 1 {
		noun = "trigger"
	}
	return noun + " " + strings.Join(quoted, ", ")
}

func pronoun(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}
