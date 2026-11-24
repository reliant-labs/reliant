// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// withChatProjectParam offers `project` on every tool of the turn that routes
// by project (tools.WithProjectParam), accepting the chat's projects. The
// capability set was recorded from the same array by name, which this does
// not change.
func withChatProjectParam(available []tools.Tool, projects []chatProject) []tools.Tool {
	values := projectValues(projects)
	out := make([]tools.Tool, len(available))
	for i, t := range available {
		out[i] = tools.WithProjectParam(t, values, values[0])
	}
	return out
}

// chatProjectsNote is the system prompt's account of a chat with several
// projects: the `project` value of each, which is the primary, and where each
// is checked out on the machine the turn's tools run on (selector) — or that
// it is not, so the model knows before it calls. Next to each, its nested
// repos, which `repo` names once the project is chosen.
func (a *CallLLMActivity) chatProjectsNote(ctx context.Context, projects []chatProject, selector *toolexec.DaemonSelector) string {
	var b strings.Builder
	fmt.Fprintf(&b, "This chat works across several projects. File, search and shell tools take a `%s` parameter that picks the project a call runs in; omit it for the primary project, which your working directory above belongs to. "+
		"A call to another project runs in that project's main checkout, and `repo` then names a repo inside that project. The projects (by the `%s` value that names each):",
		tools.ProjectParam, tools.ProjectParam)
	for _, p := range projects {
		b.WriteString("\n- ")
		b.WriteString(p.value)
		var about []string
		if p.value != p.project.Name && p.project.Name != "" {
			about = append(about, fmt.Sprintf("project %q", p.project.Name))
		}
		if p.primary {
			about = append(about, "the primary")
		}
		if len(about) > 0 {
			b.WriteString(" (" + strings.Join(about, ", ") + ")")
		}
		if !p.primary {
			checkout := resolveCallCheckout(ctx, a.repo, a.machines, p.project, selector, true)
			switch {
			case checkout.main.Path != "":
				b.WriteString(": " + checkout.main.Path)
			case checkout.missing != nil:
				b.WriteString(": not checked out on this chat's machine, so calls to it are refused until it is")
			}
		}
		if repos, err := a.repo.ListReposByProject(ctx, p.project.ID); err == nil && len(repos) > 1 {
			names := []string{"root"}
			for _, r := range repos {
				if r != nil && r.Name != "" {
					names = append(names, r.Name)
				}
			}
			b.WriteString("; repos: " + strings.Join(names, ", "))
		}
	}
	return b.String()
}
