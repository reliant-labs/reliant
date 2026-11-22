package audit

import (
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/cli/audittype"
	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/codegen"
)

// rootEntry is one `//forge:root` in the shape category's `details.roots`.
type rootEntry struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Command []string `json:"command"`
	// Migrate is the declared migrate= option; absent is the default (apply
	// when the root reaches the database, otherwise nothing).
	Migrate  string          `json:"migrate,omitempty"`
	Adopt    bool            `json:"adopt,omitempty"`
	Short    string          `json:"short,omitempty"`
	Fields   []string        `json:"fields"`
	RootOnly []rootOnlyEntry `json:"root_only,omitempty"`
	File     string          `json:"file"`
	Line     int             `json:"line"`
}

// rootOnlyEntry is a `//forge:root-only` provider a root alone may reach.
type rootOnlyEntry struct {
	Provider string `json:"provider"`
	File     string `json:"file"`
	Line     int    `json:"line"`
}

// auditInventory adds the project's processes to the shape category's
// details: `roots` (every //forge:root) and, when the audit surface provides
// it, `workloads` (every declared workload, with its runtime per env). Both
// keys are always present when computed — an empty list, not an absent key —
// so `jq '... | length'` works on any project.
//
// It returns the counts to append to the summary, and whether anything could
// not be read. A failure is a warning beside its key (roots_integrity,
// workloads_integrity), never an error: the audit's exit code does not move.
func auditInventory(f *factory.Factory, projectDir string, details map[string]any) (summary string, warn bool) {
	roots, rootErr := codegen.DeclaredRoots(projectDir)
	entries := make([]rootEntry, 0, len(roots))
	for _, r := range roots {
		e := rootEntry{
			Name:    r.Name,
			Type:    r.TypeName,
			Command: r.Command,
			Migrate: r.Migrate,
			Adopt:   r.Adopt,
			Short:   r.Short,
			Fields:  append([]string{}, r.Fields...),
			File:    r.File,
			Line:    r.Line,
		}
		for _, p := range r.RootOnly {
			e.RootOnly = append(e.RootOnly, rootOnlyEntry{Provider: p.Name, File: p.File, Line: p.Line})
		}
		entries = append(entries, e)
	}
	details["roots"] = entries
	summary = fmt.Sprintf(", %d root(s)", len(entries))
	var notes []string
	if rootErr != nil {
		details["roots_integrity"] = map[string]any{
			"status": string(audittype.StatusWarn),
			"reason": "reading the //forge:root declarations in internal/app failed — roots may be missing",
			"error":  rootErr.Error(),
		}
		notes = append(notes, "roots unreadable — see roots_integrity")
	}

	if f != nil && f.Audit.Workloads != nil {
		inv := f.Audit.Workloads(projectDir)
		workloads := inv.Workloads
		if workloads == nil {
			workloads = []audittype.Workload{}
		}
		details["workloads"] = workloads
		summary += fmt.Sprintf(", %d workload(s)", len(workloads))
		if len(inv.Failed) > 0 {
			envs := make([]string, 0, len(inv.Failed))
			for env := range inv.Failed {
				envs = append(envs, env)
			}
			sort.Strings(envs)
			details["workloads_integrity"] = map[string]any{
				"status": string(audittype.StatusWarn),
				"reason": "these envs' declarations did not render, so their workloads and runtimes are missing from workloads",
				"errors": inv.Failed,
			}
			notes = append(notes, fmt.Sprintf("%s did not render — see workloads_integrity", strings.Join(envs, ", ")))
		}
	}

	if len(notes) > 0 {
		summary += " (warn: " + strings.Join(notes, "; ") + ")"
		warn = true
	}
	return summary, warn
}
