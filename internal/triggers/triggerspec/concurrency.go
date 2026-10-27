package triggerspec

import (
	"fmt"
	"strings"

	"github.com/google/cel-go/cel"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
)

// CompileConcurrency validates the private automation admission declaration.
func CompileConcurrency(c *reliantv1.WorkflowConcurrency) error {
	if c == nil {
		return nil
	}
	if c.MaxRunning == 0 {
		return fmt.Errorf("concurrency.max_running must be positive")
	}
	if c.Supersede != "latest" {
		return fmt.Errorf("concurrency.supersede must be latest")
	}
	_, err := concurrencyProgram(c.Key)
	return err
}

func concurrencyProgram(expr string) (cel.Program, error) {
	if strings.TrimSpace(expr) == "" || len(expr) > 4096 {
		return nil, fmt.Errorf("concurrency.key must be a nonempty CEL expression of at most 4096 bytes")
	}
	env, err := triggerEnv()
	if err != nil {
		return nil, err
	}
	ast, issues := env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return nil, issues.Err()
	}
	if ast.OutputType() != cel.StringType && ast.OutputType() != cel.DynType {
		return nil, fmt.Errorf("concurrency.key must return a string")
	}
	return env.Program(ast, wfcel.ProgramOptions()...)
}

// ConcurrencyKey rejects empty/oversized values rather than silently hashing
// or truncating unrelated subjects onto the same admission key.
func ConcurrencyKey(expr string, scope Scope) (string, error) {
	p, err := concurrencyProgram(expr)
	if err != nil {
		return "", err
	}
	out, _, err := p.Eval(scope.Vars())
	if err != nil {
		return "", err
	}
	key, ok := out.Value().(string)
	if !ok || strings.TrimSpace(key) == "" || len(key) > 512 {
		return "", fmt.Errorf("concurrency.key must evaluate to a nonblank string of at most 512 bytes")
	}
	return key, nil
}
