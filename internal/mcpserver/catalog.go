// Copyright (c) 2025 Reliant Labs
//
// Package mcpserver exposes a daemon as an MCP server, so third-party clients
// (ChatGPT, Claude, and their mobile apps) can run tools inside a user's cloud
// workspace.
//
// The value is specific to mobile: a phone cannot spawn a stdio MCP server, so
// a hosted daemon is the only way to give a phone-based assistant a real shell
// and filesystem.
//
// Two deliberate narrowings relative to the daemon's own surface:
//
//   - The daemon registers ~74 commands, most of them UI plumbing. This catalog
//     exposes a curated subset. Large tool lists measurably degrade model tool
//     selection, and commands like worktree.generate_repo_id mean nothing to an
//     outside caller.
//   - Every call carries a policy that the daemon enforces at dispatch. This
//     package decides what to OFFER; it is not the security boundary. See
//     internal/daemonpolicy.
package mcpserver

import (
	"errors"
	"fmt"
)

// Tool is one entry in the connector-facing catalog: an MCP tool name, the
// daemon command it maps onto, and the schema shown to the model.
type Tool struct {
	// Name is the MCP tool name. These are deliberately plain verbs rather
	// than the daemon's dotted command types, because the model reads them.
	Name string

	// Command is the daemon command this tool invokes.
	Command string

	// Description is shown to the model. It states what the tool does and,
	// where it matters, what it will refuse — a model that knows the shape of
	// the sandbox wastes fewer turns discovering it.
	Description string

	// Mutating marks tools that change state. Grants default to read-only, so
	// this drives which tools a connector gets unless write access is asked
	// for explicitly.
	Mutating bool

	// NeedsExec marks tools that run shell commands. These are gated by the
	// grant's exec mode in addition to the tool allowlist.
	NeedsExec bool

	// InputSchema is the JSON Schema for the tool's arguments, passed to the
	// MCP client as-is.
	InputSchema map[string]any

	// BuildPayload maps validated MCP arguments onto the daemon command's JSON
	// payload. The two differ: MCP argument names are chosen for the model,
	// daemon payloads for the handler.
	BuildPayload func(args map[string]any) (any, error)
}

// objectSchema is a small helper for the repetitive JSON Schema shape.
func objectSchema(props map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{
		"type":       "object",
		"properties": props,
		"required":   required,
	}
}

func stringProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func boolProp(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

// str reads a string argument, tolerating absence.
func str(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

// num reads a numeric argument. JSON decodes numbers as float64, so integers
// arrive needing conversion.
func num(args map[string]any, key string) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

func boolean(args map[string]any, key string) bool {
	v, _ := args[key].(bool)
	return v
}

// stringList reads an array-of-strings argument.
//
// A model that sends a bare string where an array is expected gets a
// corrective error naming the right shape, rather than a silent coercion —
// for run_command that distinction is the difference between exec'ing a
// binary and handing a string to a shell.
func stringList(args map[string]any, key string) ([]string, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil, nil
	}

	switch v := raw.(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("%s must be an array of strings", key)
			}
			out = append(out, s)
		}
		return out, nil
	case []string:
		return v, nil
	case string:
		return nil, fmt.Errorf(
			"%s must be an array of strings, e.g. [\"git\", \"status\"], not a single string", key)
	default:
		return nil, fmt.Errorf("%s must be an array of strings", key)
	}
}

// Catalog is the curated tool set offered to connectors.
//
// Ordering is intentional: read and search first, since they are what a model
// reaches for most, and the destructive ones last.
var Catalog = []Tool{
	{
		Name:        "read_file",
		Command:     "fs.read_file",
		Description: "Read a text file from the workspace. Paths may be absolute or relative to the workspace root; paths outside the workspace are refused.",
		InputSchema: objectSchema(map[string]any{
			"path":   stringProp("File path to read."),
			"offset": intProp("1-based line number to start from. Omit to read from the beginning."),
			"limit":  intProp("Maximum number of lines to read."),
		}, "path"),
		BuildPayload: func(args map[string]any) (any, error) {
			payload := map[string]any{"path": str(args, "path")}
			opts := map[string]any{}
			if n := num(args, "offset"); n > 0 {
				opts["offset"] = n
			}
			if n := num(args, "limit"); n > 0 {
				opts["limit"] = n
			}
			if len(opts) > 0 {
				payload["opts"] = opts
			}
			return payload, nil
		},
	},
	{
		Name:        "list_directory",
		Command:     "fs.list_dir",
		Description: "List the entries of a directory in the workspace.",
		InputSchema: objectSchema(map[string]any{
			"path": stringProp("Directory to list."),
		}, "path"),
		BuildPayload: func(args map[string]any) (any, error) {
			return map[string]any{"path": str(args, "path")}, nil
		},
	},
	{
		Name:        "glob",
		Command:     "fs.glob",
		Description: "Find files matching a glob pattern (for example '**/*.go'). Use this to locate files by name.",
		InputSchema: objectSchema(map[string]any{
			"pattern": stringProp("Glob pattern to match."),
			"path":    stringProp("Directory to search from. Defaults to the workspace root."),
		}, "pattern"),
		BuildPayload: func(args map[string]any) (any, error) {
			payload := map[string]any{"pattern": str(args, "pattern")}
			if p := str(args, "path"); p != "" {
				payload["opts"] = map[string]any{"base_dir": p}
			}
			return payload, nil
		},
	},
	{
		Name:        "search",
		Command:     "fs.search",
		Description: "Search file contents for a regular expression. Use this to find code by what it says rather than by filename.",
		InputSchema: objectSchema(map[string]any{
			"pattern":     stringProp("Regular expression to search for."),
			"path":        stringProp("Directory to search in. Defaults to the workspace root."),
			"file_glob":   stringProp("Restrict the search to files matching this glob."),
			"ignore_case": boolProp("Match case-insensitively."),
		}, "pattern"),
		BuildPayload: func(args map[string]any) (any, error) {
			payload := map[string]any{"pattern": str(args, "pattern")}
			opts := map[string]any{}
			if p := str(args, "path"); p != "" {
				opts["base_dir"] = p
			}
			if g := str(args, "file_glob"); g != "" {
				opts["file_glob"] = g
			}
			if boolean(args, "ignore_case") {
				opts["case_insensitive"] = true
			}
			if len(opts) > 0 {
				payload["opts"] = opts
			}
			return payload, nil
		},
	},
	{
		Name:        "git_status",
		Command:     "worktree.git_status",
		Description: "Show the current git status of the workspace: branch, staged and unstaged changes.",
		InputSchema: objectSchema(map[string]any{
			"path": stringProp("Workspace path. Defaults to the workspace root."),
		}),
		BuildPayload: func(args map[string]any) (any, error) {
			return map[string]any{"worktree_path": str(args, "path")}, nil
		},
	},
	{
		Name:        "git_diff",
		Command:     "worktree.git_changes",
		Description: "Show the uncommitted changes in the workspace as a diff.",
		InputSchema: objectSchema(map[string]any{
			"path": stringProp("Workspace path. Defaults to the workspace root."),
		}),
		BuildPayload: func(args map[string]any) (any, error) {
			return map[string]any{"worktree_path": str(args, "path")}, nil
		},
	},
	{
		Name:        "write_file",
		Command:     "fs.write_file",
		Mutating:    true,
		Description: "Write a file in the workspace, replacing its entire contents. To change part of a file, prefer edit_file.",
		InputSchema: objectSchema(map[string]any{
			"path":    stringProp("File path to write."),
			"content": stringProp("Full contents to write."),
		}, "path", "content"),
		BuildPayload: func(args map[string]any) (any, error) {
			return map[string]any{
				"path":    str(args, "path"),
				"content": str(args, "content"),
			}, nil
		},
	},
	{
		Name:        "edit_file",
		Command:     "fs.patch_file",
		Mutating:    true,
		Description: "Replace exact text in a single file. Include enough surrounding context in 'find' to match uniquely; the edit fails rather than guessing if the text is ambiguous.",
		InputSchema: objectSchema(map[string]any{
			"path":        stringProp("File to edit."),
			"find":        stringProp("Exact text to replace, including whitespace and indentation."),
			"replace":     stringProp("Replacement text."),
			"replace_all": boolProp("Replace every occurrence instead of requiring exactly one."),
		}, "path", "find", "replace"),
		BuildPayload: func(args map[string]any) (any, error) {
			return map[string]any{
				"path": str(args, "path"),
				"edits": []map[string]any{{
					"old_string":  str(args, "find"),
					"new_string":  str(args, "replace"),
					"replace_all": boolean(args, "replace_all"),
				}},
			}, nil
		},
	},
	{
		Name:      "run_command",
		Command:   "exec.run",
		Mutating:  true,
		NeedsExec: true,
		Description: "Run a command in the workspace and wait for it to finish. Use this for builds, tests, and git operations. " +
			"Supply the program and each argument as separate array entries, for example [\"git\", \"status\", \"--short\"]. " +
			"There is no shell, so pipes, redirection, globbing, and && chaining are not available — run the steps as separate calls.",
		InputSchema: objectSchema(map[string]any{
			"command": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Program and arguments, e.g. [\"go\", \"test\", \"./...\"]. The first entry is the program.",
				"minItems":    1,
			},
			"working_dir": stringProp("Directory to run in. Defaults to the workspace root."),
			"timeout_ms":  intProp("Maximum run time in milliseconds. Defaults to 60000."),
		}, "command"),
		BuildPayload: func(args map[string]any) (any, error) {
			argv, err := stringList(args, "command")
			if err != nil {
				return nil, err
			}
			if len(argv) == 0 {
				return nil, errors.New(
					"command must be an array of strings, e.g. [\"git\", \"status\"] — not a single shell string")
			}

			// Sent as argv, never as a shell string: the daemon execs it
			// directly, so the program named here is the program that runs.
			payload := map[string]any{"argv": argv}
			if d := str(args, "working_dir"); d != "" {
				payload["working_dir"] = d
			}
			if t := num(args, "timeout_ms"); t > 0 {
				payload["timeout_ms"] = t
			}
			return payload, nil
		},
	},
	// The background family is the point of this product on a phone: kick off
	// a build or test suite, put the phone down, poll for the result.
	//
	// It is safe to expose because the daemon now tags each process with the
	// grant that started it and refuses cross-grant reads and kills — process
	// ids are not secrets, and without that check a connector could reach the
	// user's own build output or kill their dev server.
	{
		Name:      "start_background_command",
		Command:   "exec.bg_start",
		Mutating:  true,
		NeedsExec: true,
		Description: "Start a long-running command in the background and return immediately with a process ID. " +
			"Use this for builds, test suites, and dev servers that outlast a single request, then poll with get_background_output. " +
			"Supply the program and each argument as separate array entries, for example [\"go\", \"test\", \"./...\"].",
		InputSchema: objectSchema(map[string]any{
			"command": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Program and arguments. The first entry is the program.",
				"minItems":    1,
			},
			"working_dir": stringProp("Directory to run in. Defaults to the workspace root."),
		}, "command"),
		BuildPayload: func(args map[string]any) (any, error) {
			argv, err := stringList(args, "command")
			if err != nil {
				return nil, err
			}
			if len(argv) == 0 {
				return nil, errors.New(
					"command must be an array of strings, e.g. [\"go\", \"test\", \"./...\"] — not a single shell string")
			}
			payload := map[string]any{"argv": argv}
			if d := str(args, "working_dir"); d != "" {
				payload["working_dir"] = d
			}
			return payload, nil
		},
	},
	{
		Name:    "get_background_output",
		Command: "exec.bg_output",
		Description: "Read output from a background command started with start_background_command. " +
			"Reports whether the process is still running or has exited.",
		InputSchema: objectSchema(map[string]any{
			"process_id": stringProp("Process ID returned by start_background_command."),
		}, "process_id"),
		BuildPayload: func(args map[string]any) (any, error) {
			return map[string]any{"process_id": str(args, "process_id")}, nil
		},
	},
	{
		Name:        "stop_background_command",
		Command:     "exec.bg_kill",
		Mutating:    true,
		NeedsExec:   true,
		Description: "Stop a background command started with start_background_command.",
		InputSchema: objectSchema(map[string]any{
			"process_id": stringProp("Process ID to stop."),
		}, "process_id"),
		BuildPayload: func(args map[string]any) (any, error) {
			return map[string]any{"process_id": str(args, "process_id")}, nil
		},
	},
}

// CatalogByName indexes Catalog for lookup during a tools/call.
var CatalogByName = func() map[string]Tool {
	m := make(map[string]Tool, len(Catalog))
	for _, t := range Catalog {
		m[t.Name] = t
	}
	return m
}()

// ReadOnlyToolNames returns the tools safe for a read-only grant. This is the
// default grant shape, so it is derived from the catalog rather than
// maintained as a second list that could drift.
func ReadOnlyToolNames() []string {
	var names []string
	for _, t := range Catalog {
		if !t.Mutating {
			names = append(names, t.Name)
		}
	}
	return names
}

// AllToolNames returns every catalog tool name.
func AllToolNames() []string {
	names := make([]string, 0, len(Catalog))
	for _, t := range Catalog {
		names = append(names, t.Name)
	}
	return names
}

// CommandsForTools maps MCP tool names onto the daemon commands they need.
// Grants are authored in terms of tools the user recognizes, but enforced in
// terms of daemon commands, and this is the single place that translation
// happens.
func CommandsForTools(toolNames []string) []string {
	seen := make(map[string]bool)
	var commands []string
	for _, name := range toolNames {
		tool, ok := CatalogByName[name]
		if !ok {
			continue
		}
		if !seen[tool.Command] {
			seen[tool.Command] = true
			commands = append(commands, tool.Command)
		}
	}
	return commands
}
