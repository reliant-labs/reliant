package workflow

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

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

		// Skip generated files and code generators — they necessarily
		// contain string literals as data.
		skipFiles := map[string]struct{}{
			"yaml/bindings_generated.go":       {},
			"yaml/cmd/yamlbindingsgen/main.go": {},
		}
		if _, skip := skipFiles[relPath]; skip {
			return nil
		}

		fset := token.NewFileSet()
		parsedFile, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
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
