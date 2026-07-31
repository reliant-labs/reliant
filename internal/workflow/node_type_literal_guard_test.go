package workflow

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// generatedFileMarker matches the standard Go "generated code" header
// (see `go help generate`). Files bearing this marker are machine-generated
// and legitimately contain node-type strings as data, so they are excluded
// from the raw-literal guard just like the explicitly listed generators.
var generatedFileMarker = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)

// isGeneratedFile reports whether the parsed file carries the standard
// generated-code marker in a comment before the package clause.
func isGeneratedFile(file *ast.File) bool {
	for _, group := range file.Comments {
		if group.Pos() >= file.Package {
			break
		}
		for _, comment := range group.List {
			if generatedFileMarker.MatchString(comment.Text) {
				return true
			}
		}
	}
	return false
}

// TestNoRawNodeTypeLiteralsInProductionFiles guards against introducing new raw
// node-type string literals in production code.
//
// Keep the allowlist explicit and minimal. If you must add an allowlist entry,
// document why the literal cannot be replaced by model.NodeType* constants.
func TestNoRawNodeTypeLiteralsInProductionFiles(t *testing.T) {
	nodeTypeLiterals := map[string]struct{}{
		"call_llm":        {},
		"execute_tools":   {},
		"compact":         {},
		"approval":        {},
		"save_message":    {},
		"create_worktree": {},
		"run":             {},
		"workflow":        {},
		"loop":            {},
		"join":            {},
	}

	allowlist := map[string]map[string]struct{}{
		// Canonical definitions.
		"model/constants.go": {
			"call_llm":        {},
			"execute_tools":   {},
			"compact":         {},
			"approval":        {},
			"save_message":    {},
			"create_worktree": {},
			"run":             {},
			"workflow":        {},
			"loop":            {},
			"join":            {},
		},
		// Internal workflow key/name uses "compact" as a workflow identifier.
		"builtin/internal.go": {
			"compact": {},
		},
		// Non-node-type semantic enums/keys that intentionally share these words.
		"cel/types.go": {
			"workflow": {},
		},
		"runtime/cel_env.go": {
			"workflow": {},
		},
		"runtime/inline_workflow_executor.go": {
			"workflow": {},
		},
		"runtime/loop_executor.go": {
			"loop": {},
		},
		"runtime/simulator.go": {
			"loop": {},
		},
		"runtime/node_output_store.go": {
			"workflow": {},
		},
		"runtime/workflow_templates.go": {
			"workflow":     {},
			"save_message": {},
			"run":          {},
		},
		// CEL builtins/namespace labels, not workflow node types.
		"cel/env.go": {
			"join": {},
		},
		"reference/stub.go": {
			"workflow": {},
			"join":     {},
		},
		// Runtime metadata/update keys, not workflow node types.
		"runtime/activities/handlers/approval.go": {
			"approval": {},
		},
		"runtime/activities/handlers/cleanup.go": {
			"approval": {},
		},
		"runtime/activities/handlers/call_llm.go": {
			"workflow": {},
		},
		// CEL context key for workflow metadata, not a node type.
		"runtime/context.go": {
			"workflow": {},
		},
		// JSON map key for router_decision serialization, not a node type.
		"runtime/activities/handlers/workflow_status.go": {
			"workflow": {},
		},
		// Same router_decision JSON map key, on the thread-lifecycle activity.
		"runtime/activities/handlers/thread_status.go": {
			"workflow": {},
		},
		// Well-known input key for loop iteration context, not a node type.
		"runtime/loop_executor_parallel.go": {
			"loop": {},
		},
		"reconciliation/reconciler.go": {
			"workflow": {},
		},
		"validation/cel.go": {
			"save_message": {},
			"workflow":     {},
		},
		"yaml/nodes.go": {
			"save_message": {},
		},
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to resolve caller path")
	}
	workflowRoot := filepath.Dir(thisFile)

	var violations []string
	walkErr := filepath.WalkDir(workflowRoot, func(path string, dEntry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if dEntry.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		relPath, err := filepath.Rel(workflowRoot, path)
		if err != nil {
			return err
		}
		relPath = filepath.ToSlash(relPath)

		// Skip hand-written code generators — they necessarily contain
		// node-type string literals as data. Machine-generated files are
		// skipped separately below via their generated-code marker.
		skipFiles := map[string]struct{}{
			"yaml/cmd/yamlbindingsgen/main.go": {},
		}
		if _, skip := skipFiles[relPath]; skip {
			return nil
		}

		fset := token.NewFileSet()
		parsedFile, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return err
		}

		// Skip machine-generated files (e.g. docgen/v3schema output). They
		// carry the standard "// Code generated ... DO NOT EDIT." marker and
		// contain node-type strings as data, not production references.
		if isGeneratedFile(parsedFile) {
			return nil
		}

		ast.Inspect(parsedFile, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			literalValue, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if _, isNodeTypeLiteral := nodeTypeLiterals[literalValue]; !isNodeTypeLiteral {
				return true
			}
			allowedLiteralsForFile, hasAllowlist := allowlist[relPath]
			if hasAllowlist {
				if _, isAllowed := allowedLiteralsForFile[literalValue]; isAllowed {
					return true
				}
			}

			line := fset.Position(lit.Pos()).Line
			violations = append(violations, relPath+":"+strconv.Itoa(line)+": raw node type literal \""+literalValue+"\"")
			return true
		})

		return nil
	})
	if walkErr != nil {
		t.Fatalf("failed to scan workflow package: %v", walkErr)
	}

	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("found raw node-type literals in production files:\n%s", strings.Join(violations, "\n"))
	}
}
