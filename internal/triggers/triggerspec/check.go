// Copyright (c) 2025 Reliant Labs
package triggerspec

import (
	"fmt"
	"strings"
)

// A workflow's `check:` names the repository and commit its GitHub check run
// is posted on as templates over `trigger`, the same language and root as a
// declared trigger's inputs, rendered when the firing is recorded:
//
//	check: {name: reliant/ci, head_sha: '{{ trigger.payload.data.check_suite.head_sha }}'}

// CompileCheckTemplate checks that every {{ }} in value compiles over
// `trigger`. Plain text (a literal repository or sha) always compiles.
func CompileCheckTemplate(value string) error {
	exprs, err := InputExpressions(value)
	if err != nil {
		return err
	}
	if len(exprs) == 0 {
		return nil
	}
	env, err := triggerEnv()
	if err != nil {
		return fmt.Errorf("build environment: %w", err)
	}
	for _, expr := range exprs {
		if _, issues := env.Compile(expr); issues != nil && issues.Err() != nil {
			return fmt.Errorf("{{ %s }}: %v", expr, issues.Err())
		}
	}
	return nil
}

// RenderCheckTemplate renders value against a firing, trimmed. A value with
// no {{ }} is returned as written; an expression that fails (a key the
// payload lacks) is an error.
func RenderCheckTemplate(value string, scope Scope) (string, error) {
	rendered, err := RenderPrompt(value, scope)
	if err != nil {
		return "", fmt.Errorf("%s", strings.TrimPrefix(err.Error(), "prompt: "))
	}
	return strings.TrimSpace(rendered), nil
}
