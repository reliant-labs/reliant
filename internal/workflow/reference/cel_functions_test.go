package reference

import (
	"os"
	"strings"
	"testing"

	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
)

// Every advertised function example must compile in the environment workflows
// run in; a function the reference lists but the env lacks fails here.
func TestCELFunctionExamplesCompile(t *testing.T) {
	env, err := wfcel.NewEnv(wfcel.CELEnvConfig{
		Namespaces:             wfcel.AllNamespaces(),
		IncludeStdLib:          true,
		IncludeCustomFunctions: true,
	})
	if err != nil {
		t.Fatalf("NewEnv: %v", err)
	}
	if len(CELFunctions) == 0 {
		t.Fatal("no CEL functions registered")
	}
	for _, fn := range CELFunctions {
		t.Run(fn.Name, func(t *testing.T) {
			if fn.Example == "" {
				t.Fatalf("%s has no example", fn.Name)
			}
			if _, iss := env.Compile(fn.Example); iss != nil && iss.Err() != nil {
				t.Errorf("example %q does not compile: %v", fn.Example, iss.Err())
			}
		})
	}
}

func TestCELDocsExpressionsCompile(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/reference/cel-expressions.mdx")
	if err != nil {
		t.Fatal(err)
	}
	env, err := wfcel.NewEnv(wfcel.CELEnvConfig{Namespaces: wfcel.AllNamespaces(), IncludeStdLib: true, IncludeCustomFunctions: true})
	if err != nil {
		t.Fatal(err)
	}
	// Single-expression table cells and {{ }} holes inside fenced yaml blocks.
	var exprs []string
	inYAML := false
	wrong := false
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inYAML = trimmed == "```yaml"
			continue
		}
		cells := strings.Split(trimmed, "|")
		if len(cells) == 5 && strings.HasPrefix(strings.TrimSpace(cells[3]), "`") &&
			strings.Count(cells[3], "`") == 2 && strings.TrimSpace(cells[1]) != "Function" {
			exprs = append(exprs, strings.Trim(strings.TrimSpace(cells[3]), "`"))
		}
		if strings.HasPrefix(trimmed, "#") {
			wrong = strings.HasPrefix(trimmed, "# Wrong")
			continue
		}
		if inYAML && !wrong {
			ms, _ := wfcel.ScanTemplate(line)
			for _, m := range ms {
				if m.Expr == "" {
					continue
				}
				exprs = append(exprs, m.Expr)
			}
		}
	}
	for _, e := range exprs {
		if _, iss := env.Compile(e); iss != nil && iss.Err() != nil {
			t.Errorf("docs expression %q does not compile: %v", e, iss.Err())
		}
	}
}
