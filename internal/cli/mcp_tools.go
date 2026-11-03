package cli

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// dirInput is embedded by every tool's input.
type dirInput struct {
	Dir string `json:"dir,omitempty" jsonschema:"forge project directory to act on; default is the directory the server was started in"`
}

type annotationsInput struct {
	dirInput
	Kind string `json:"kind,omitempty" jsonschema:"limit to one annotation kind: entity, field, column, table, service, method or go (default: all)"`
}

type librariesInput struct {
	dirInput
	Packages []string `json:"packages,omitempty" jsonschema:"packages or symbols to print full signatures for, e.g. crud, svcerr, orm.Context, or all; omit for the index of every library"`
}

type shapesInput struct {
	dirInput
	Grep          string `json:"grep,omitempty" jsonschema:"case-insensitive regex over name and detail; use a|b to ask about several entities in one call"`
	Kind          string `json:"kind,omitempty" jsonschema:"comma-separated kinds: rpc,message,enum,table,store,handler,hook,deploy-target"`
	Service       string `json:"service,omitempty" jsonschema:"only this service's rpcs and handlers (ChatService, or Chat)"`
	Unimplemented bool   `json:"unimplemented,omitempty" jsonschema:"list declared rpcs that have no handler, or only forge's unwired stub"`
}

type skillListInput struct {
	dirInput
	Group             string `json:"group,omitempty" jsonschema:"list this skill group's parent and every sub-skill with full paths, e.g. db or observability"`
	All               bool   `json:"all,omitempty" jsonschema:"print the exhaustive flat table instead of the grouped view"`
	IncludeMigrations bool   `json:"include_migrations,omitempty" jsonschema:"include one-time migration playbooks"`
}

type skillSearchInput struct {
	dirInput
	Query string `json:"query" jsonschema:"keywords; scores skill paths, descriptions and bodies"`
}

type skillLoadInput struct {
	dirInput
	Name string `json:"name" jsonschema:"skill path from skill_list or skill_search, e.g. forge, db, db/seeding"`
}

type lintInput struct {
	dirInput
	Scope []string `json:"scope,omitempty" jsonschema:"project-relative paths to lint instead of the whole project, e.g. internal/handlers/jobs; whole-project lanes are skipped and named, so run unscoped before merging"`
}

type generateInput struct {
	dirInput
	DryRun bool `json:"dry_run,omitempty" jsonschema:"print the pipeline plan and stop; writes nothing"`
	Check  bool `json:"check,omitempty" jsonschema:"generate into a temp dir and report drift against the tree; writes nothing"`
}

type reportBugInput struct {
	dirInput
	Severity string `json:"severity" jsonschema:"critical: data loss, or nothing can proceed. high: it blocks this task. medium: there is a workaround. low: cosmetic."`
	Title    string `json:"title" jsonschema:"one line naming the defect, e.g. 'forge generate drops optional list filters'; ids and numbers go in evidence"`
	Summary  string `json:"summary,omitempty" jsonschema:"what happened, in a few sentences"`
	Expected string `json:"expected,omitempty" jsonschema:"what should have happened"`
	Actual   string `json:"actual,omitempty" jsonschema:"what happened instead, with the exact error text"`
	Evidence string `json:"evidence,omitempty" jsonschema:"how to reproduce it: commands run, their output, file paths"`
}

// addForgeTools registers every tool. Read-only ones come first; lint and
// generate are the two that touch the project (lint: its caches and slot;
// generate: the generated files), and are annotated so.
func addForgeTools(srv *mcp.Server, r *mcpRunner) {
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(false)}

	addCmdTool(srv, r, &mcp.Tool{
		Name:        "capabilities",
		Description: "Everything forge can do, in one call: every command, every lint analyzer (including ones hidden from --help) and every // forge:* marker. Read this before hand-writing something forge may already scaffold.",
		Annotations: readOnly,
	}, func(in dirInput) cmdSpec {
		return cmdSpec{dir: in.Dir, args: []string{"project", "capabilities"}}
	})

	addCmdTool(srv, r, &mcp.Tool{
		Name:        "annotations",
		Description: "The entity-authoring vocabulary: the // forge:* markers in proto, SQL and Go, how each proto field type becomes a column, the projected buf.validate rules, and the (forge.v1.service) / (forge.v1.method) options.",
		Annotations: readOnly,
	}, func(in annotationsInput) cmdSpec {
		args := []string{"project", "annotations"}
		if in.Kind != "" {
			args = append(args, "--kind", in.Kind)
		}
		return cmdSpec{dir: in.Dir, args: args}
	})

	addCmdTool(srv, r, &mcp.Tool{
		Name:        "libraries",
		Description: "forge's runtime libraries (forge/pkg for Go, @reliantlabs/forge-web-runtime for the frontend). With no packages, an index. Name packages or symbols (crud, svcerr, orm.Context) for every exported signature, which 'go doc' cannot give you. Adopt before porting a utility.",
		Annotations: readOnly,
	}, func(in librariesInput) cmdSpec {
		return cmdSpec{dir: in.Dir, args: append([]string{"project", "libraries"}, in.Packages...)}
	})

	addCmdTool(srv, r, &mcp.Tool{
		Name:        "audit",
		Description: "What THIS project currently is, as JSON: forge version pin, services, rpcs, entities, lint roll-ups, codegen drift, stubs, ownership. Findings are reported in the result, not as a tool failure.",
		Annotations: readOnly,
	}, func(in dirInput) cmdSpec {
		return cmdSpec{dir: in.Dir, args: []string{"project", "audit", "--json"}, findings: true}
	})

	addCmdTool(srv, r, &mcp.Tool{
		Name:        "shapes",
		Description: "Where every symbol is: each rpc, message, enum, table, store, handler and hook with file:line, derived live from proto, migrations and source. Start recon here instead of grepping. grep takes a regex, so ask about several entities at once.",
		Annotations: readOnly,
	}, func(in shapesInput) cmdSpec {
		args := []string{"project", "shapes"}
		if in.Grep != "" {
			args = append(args, "--grep", in.Grep)
		}
		if in.Kind != "" {
			args = append(args, "--kind", in.Kind)
		}
		if in.Service != "" {
			args = append(args, "--service", in.Service)
		}
		if in.Unimplemented {
			args = append(args, "--unimplemented")
		}
		return cmdSpec{dir: in.Dir, args: args}
	})

	addCmdTool(srv, r, &mcp.Tool{
		Name:        "skill_list",
		Description: "The skill catalog (forge-shipped, project and user skills): playbooks for architecture, proto, db, api, services, testing, frontend, deploy and more. Start with skill_load name=forge.",
		Annotations: readOnly,
	}, func(in skillListInput) cmdSpec {
		args := []string{"skill", "list"}
		if in.Group != "" {
			args = append(args, in.Group)
		}
		if in.All {
			args = append(args, "--all")
		}
		if in.IncludeMigrations {
			args = append(args, "--include-migrations")
		}
		return cmdSpec{dir: in.Dir, args: args}
	})

	addCmdTool(srv, r, &mcp.Tool{
		Name:        "skill_search",
		Description: "Find skills by keyword. Scores paths, descriptions and bodies, so it finds a skill whose title never mentions your term.",
		Annotations: readOnly,
	}, func(in skillSearchInput) cmdSpec {
		return cmdSpec{dir: in.Dir, args: []string{"skill", "search", in.Query}}
	})

	addCmdTool(srv, r, &mcp.Tool{
		Name:        "skill_load",
		Description: "Print one skill, authoritative for the forge version running. Call with name=forge first: it is the entry point and names the rest. Skills quote the forge CLI; each command has the tool of the same name here.",
		Annotations: readOnly,
	}, func(in skillLoadInput) cmdSpec {
		return cmdSpec{dir: in.Dir, args: []string{"skill", "load", in.Name}}
	})

	// report_bug is the one tool that reaches outside the machine: it files
	// a defect in forge with the Reliant team (`forge report-bug`, which is
	// the only way forge ever sends anything).
	addCmdTool(srv, r, &mcp.Tool{
		Name:        "report_bug",
		Description: reportBugUsage,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: ptr(false), IdempotentHint: true, OpenWorldHint: ptr(true)},
	}, func(in reportBugInput) cmdSpec {
		args := []string{"report-bug", "--severity", in.Severity, "--title", in.Title}
		for _, f := range []struct{ flag, value string }{
			{"--summary", in.Summary}, {"--expected", in.Expected}, {"--actual", in.Actual}, {"--evidence", in.Evidence},
		} {
			if f.value != "" {
				args = append(args, f.flag, f.value)
			}
		}
		return cmdSpec{dir: in.Dir, args: args}
	})

	addCmdTool(srv, r, &mcp.Tool{
		Name:        "lint",
		Description: "Run forge lint --quiet: only what failed, then the verdict as the last line. Report-only (never --fix), but it holds a host slot and refreshes caches. A failing gate is reported in the result. Scope to your slice while iterating, run unscoped before merging.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: ptr(false), IdempotentHint: true, OpenWorldHint: ptr(false)},
	}, func(in lintInput) cmdSpec {
		args := []string{"lint", "--quiet"}
		for _, s := range in.Scope {
			args = append(args, "--scope", s)
		}
		return cmdSpec{dir: in.Dir, args: args, findings: true}
	})

	addCmdTool(srv, r, &mcp.Tool{
		Name:        "generate",
		Description: "Run forge generate: re-project the generated code (gen/, *_gen.go, hooks, mocks, deploy config) from the protos and applied migrations. Rewrites generated files; never touches hand-written business logic. Use dry_run to see the plan or check to see drift without writing. Refuses when this forge is not the version forge.yaml pins.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: ptr(false), IdempotentHint: true, OpenWorldHint: ptr(false)},
	}, func(in generateInput) cmdSpec {
		args := []string{"generate"}
		if in.DryRun {
			args = append(args, "--dry-run")
		}
		if in.Check {
			args = append(args, "--check")
		}
		return cmdSpec{dir: in.Dir, args: args}
	})
}

// cmdSpec is one forge invocation a tool call maps to.
type cmdSpec struct {
	dir  string
	args []string
	// findings: a non-zero exit means the command found problems, which is
	// its answer (the report is in the output), not a failure of the call.
	findings bool
}

func addCmdTool[In any](srv *mcp.Server, r *mcpRunner, tool *mcp.Tool, spec func(In) cmdSpec) {
	mcp.AddTool(srv, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		s := spec(in)
		out, err := r.exec(ctx, s.dir, s.args...)
		if err != nil && ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		res := &mcp.CallToolResult{}
		text := strings.TrimRight(out, "\n")
		switch {
		case err == nil:
		case s.findings && text != "":
			text += "\n\nforge exited non-zero: " + err.Error()
		default:
			if text != "" {
				text += "\n\n"
			}
			text += "Error: " + err.Error()
			res.IsError = true
		}
		if text == "" {
			text = "(no output)"
		}
		res.Content = []mcp.Content{&mcp.TextContent{Text: text}}
		return res, nil, nil
	})
}

func ptr[T any](v T) *T { return &v }
