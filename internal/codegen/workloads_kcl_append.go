package codegen

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/naming"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// AppendWorkloadStanza adds one workload to a project's
// deploy/kcl/workloads.k — its declaration, and its name in the `ALL` list —
// and reports whether it did.
//
// ADDITIVE ONLY. Existing content is never rewritten, reformatted or
// reordered: the file is user-owned, so the only edits forge makes to it are
// the new declaration (above the ALL list's comment block) and the one new
// list entry, written in the style the list already uses. Every other byte,
// comment and line break stays where the user left it.
//
// It returns applied=false, with no error and no write, when the file is
// missing, the workload is already declared, or the ALL list cannot be
// located unambiguously. The caller then PRINTS the stanza for the user to
// paste.
func AppendWorkloadStanza(projectDir, modulePath, projectName string, c config.ComponentConfig) (applied bool, err error) {
	path := filepath.Join(projectDir, WorkloadsKCLRelPath)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	content := string(raw)

	// Already declared — by an earlier run, or by hand. Appending a second
	// declaration would shadow the first (KCL takes the last binding) and
	// silently change which workload deploys.
	if workloadDeclaredIn(content, c.Name) {
		return false, nil
	}

	updated, ok := addWorkloadDeclaration(content, naming.KCLIdentifier(c.Name), WorkloadStanza(modulePath, projectName, c))
	if !ok {
		return false, nil
	}
	if err := atomicfile.WriteFile(path, []byte(updated), 0644); err != nil {
		return false, err
	}
	return true, nil
}

// CRDListEdit is what AppendOperatorCRD did to an operator's `crds` list.
type CRDListEdit int

const (
	// CRDListAdded means the kind was appended.
	CRDListAdded CRDListEdit = iota
	// CRDListAlreadyListed means the list already names the kind; nothing
	// was written.
	CRDListAlreadyListed
	// CRDListNotEditable means there is no workloads.k, no workload of that
	// name, or no `crds = [...]` in it forge can edit unambiguously. Nothing
	// was written; the caller prints the edit.
	CRDListNotEditable
)

// workloadLiteralHead is the opening of a top-level workload declaration in
// the ORIGINAL source (the prose-stripped one shifts offsets). Top-level means
// column 0, which the indented worked examples in the scaffolded docstring
// and every `#` comment are not.
var workloadLiteralHead = regexp.MustCompile(`(?m)^[A-Za-z_][A-Za-z0-9_]*\s*=\s*fw\.Workload\s*\{`)

// crdsField is a `crds = [` field on a line of its own.
var crdsField = regexp.MustCompile(`(?m)^\s*crds\s*=\s*\[`)

// AppendOperatorCRD adds kind to the `crds` list of the operator workload
// named workloadName in deploy/kcl/workloads.k — what `forge scaffold crd`
// calls, so the CRD it writes is one the operator's derived ClusterRole
// covers. `crds` is the only list that derivation reads the kinds from, and
// an operator scaffolded before its first CRD declares `crds = []`.
//
// Additive like AppendWorkloadStanza: the one new entry, written in the
// style the list already uses (appendKCLListElement); nothing else moves.
func AppendOperatorCRD(projectDir, workloadName, kind string) (CRDListEdit, error) {
	path := filepath.Join(projectDir, WorkloadsKCLRelPath)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return CRDListNotEditable, nil
		}
		return CRDListNotEditable, err
	}
	content := string(raw)
	entry := fmt.Sprintf("%q", kind)
	for _, head := range workloadLiteralHead.FindAllStringIndex(content, -1) {
		body, ok := kclBlockBody(content, head[1])
		if !ok {
			continue
		}
		if m := workloadNameField.FindStringSubmatch(StripKCLProse(body)); len(m) < 2 || !SameWorkload(m[1], workloadName) {
			continue
		}
		fields := crdsField.FindAllStringIndex(body, -1)
		if len(fields) != 1 {
			return CRDListNotEditable, nil
		}
		open := head[1] + fields[0][1] - 1
		list, ok := scanKCLList(content, open)
		if !ok {
			return CRDListNotEditable, nil
		}
		if kclListHasEntry(list, entry) {
			return CRDListAlreadyListed, nil
		}
		updated, ok := appendKCLListElement(content, open, entry)
		if !ok {
			return CRDListNotEditable, nil
		}
		if err := atomicfile.WriteFile(path, []byte(updated), 0o644); err != nil {
			return CRDListNotEditable, err
		}
		return CRDListAdded, nil
	}
	return CRDListNotEditable, nil
}

// addWorkloadDeclaration makes the two edits a new workload needs: its
// declaration, and its identifier in the `ALL` list.
//
// BOTH are required: a declaration nothing references is dead code, and a
// workload missing from `ALL` would scaffold cleanly and then silently never
// deploy. If `ALL` cannot be located unambiguously — no list, two of them, or
// one that is never closed — NEITHER edit is made (ok=false) rather than half
// of them: the caller prints the stanza and the user places it, which is the
// contract this file has always had for content forge cannot safely edit.
//
// The list edit touches only the new entry (appendKCLListElement), and is
// skipped when `ALL` already names the identifier, so a workload listed ahead
// of its declaration is not listed twice.
func addWorkloadDeclaration(content, ident, stanza string) (string, bool) {
	heads := allListHead.FindAllStringIndex(content, -1)
	if len(heads) != 1 {
		return content, false
	}
	allStart, open := heads[0][0], heads[0][1]-1
	list, ok := scanKCLList(content, open)
	if !ok {
		return content, false
	}
	updated := content
	if !kclListHasEntry(list, ident) {
		updated, _ = appendKCLListElement(content, open, ident)
	}

	// The declaration goes ABOVE the comment block introducing the ALL list,
	// so `ALL` stays the last thing in the file — it reads as the summary of
	// everything above it, which is the only reason to put a list of names at
	// the bottom of a file of declarations. Walk back over the contiguous `#`
	// lines that document it, so the declaration lands above the prose rather
	// than wedged between it and the list. The list edit above only changed
	// bytes after `open`, so allStart still marks the ALL line.
	at := allStart
	for at > 0 {
		prev := lineStart(updated, at-1)
		if !strings.HasPrefix(strings.TrimSpace(updated[prev:at]), "#") {
			break
		}
		at = prev
	}
	return updated[:at] + stanza + "\n" + updated[at:], true
}

// allListHead matches the head of the `ALL: [fw.Workload] = [` aggregation
// list, up to and including its opening bracket. Anchored on the typed
// declaration at the start of a line rather than a bare `ALL`, so the word
// appearing in prose or a comment cannot be mistaken for it. The list itself
// is read by scanKCLList, not by this pattern: a list is not a regular
// language once comments may hold brackets.
var allListHead = regexp.MustCompile(`(?m)^ALL\s*:\s*\[\s*fw\.Workload\s*\]\s*=\s*\[`)

// workloadDeclaredIn reports whether the KCL source already declares a
// workload called name. It matches BOTH the binding (`billing = fw.Workload`)
// and the `name = "billing"` field, because a user may rename either half:
// the binding is what an env refines, the field is what the manifest is
// called, and a duplicate of either is a real collision.
//
// Docstrings and comments are stripped first: the scaffolded workloads.k
// documents the format with a worked example, and an example is not a
// declaration. Without this, a project whose docs happen to name the
// workload being added would silently skip the append.
//
// The field matches in any spelling of the name (SameWorkload): a component
// `engine_shadow` is declared as `name = "engine-shadow"`.
func workloadDeclaredIn(content, name string) bool {
	stripped := StripKCLProse(content)
	for _, m := range workloadNameField.FindAllStringSubmatch(stripped, -1) {
		if SameWorkload(m[1], name) {
			return true
		}
	}
	binding := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(naming.KCLIdentifier(name)) + `\s*=`)
	return binding.MatchString(stripped)
}

// WorkloadDeclared reports whether workloads.k content declares a workload
// by that name, ignoring prose that merely mentions it.
//
// Exported for callers that need to ask what a project DECLARES rather than
// what it has on disk in some other format.
func WorkloadDeclared(content, name string) bool {
	return workloadDeclaredIn(content, name)
}

// StripKCLProse removes """docstrings""" and # comments from KCL source, so a
// scan for real declarations is not fooled by prose that illustrates them.
func StripKCLProse(src string) string {
	return kclComment.ReplaceAllString(kclDocstring.ReplaceAllString(src, ""), "")
}

var (
	kclDocstring = regexp.MustCompile(`(?s)""".*?"""`)
	kclComment   = regexp.MustCompile(`(?m)#.*$`)
)

// WorkloadStanzaHint is the message shown when the stanza could not be
// appended automatically: the exact text to paste, and where.
func WorkloadStanzaHint(modulePath, projectName string, c config.ComponentConfig) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Add this workload to %s:\n\n", WorkloadsKCLRelPath)
	b.WriteString(WorkloadStanza(modulePath, projectName, c))
	return b.String()
}
