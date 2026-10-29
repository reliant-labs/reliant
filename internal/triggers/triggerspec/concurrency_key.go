// Copyright (c) 2025 Reliant Labs
package triggerspec

import (
	"fmt"
	"strings"
)

// A workflow's `concurrency: {waiting: latest, key: ...}` names, per firing,
// the group of waiting firings it replaces. It is a template over `trigger`,
// the same language and root as a webhook dedupe_key:
//
//	concurrency: {max_running: 1, waiting: latest, key: '{{ trigger.payload.attributes.repository }}'}

// CompileConcurrencyKey checks that every {{ }} in key compiles over
// `trigger`. Empty is valid: every firing shares one key.
func CompileConcurrencyKey(key string) error {
	if strings.TrimSpace(key) == "" {
		return nil
	}
	exprs, err := InputExpressions(key)
	if err != nil {
		return err
	}
	if len(exprs) == 0 {
		return fmt.Errorf("key has no {{ }} expression, so every firing would share one key: omit key for that, or name what groups firings, e.g. {{ trigger.payload.attributes.repository }}")
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

// RenderConcurrencyKey renders key against a firing. An empty template is the
// pool's one key, "". A template that renders empty is an error, like a
// dedupe_key: it would silently merge unrelated firings.
func RenderConcurrencyKey(key string, scope Scope) (string, error) {
	if strings.TrimSpace(key) == "" {
		return "", nil
	}
	rendered, err := RenderPrompt(key, scope)
	if err != nil {
		return "", fmt.Errorf("concurrency key: %s", strings.TrimPrefix(err.Error(), "prompt: "))
	}
	rendered = strings.TrimSpace(rendered)
	if rendered == "" {
		return "", fmt.Errorf("concurrency key rendered empty")
	}
	if len(rendered) > maxRenderedKey {
		rendered = rendered[:maxRenderedKey]
	}
	return rendered, nil
}
