// Copyright (c) 2025 Reliant Labs

// Package attention validates the declarative attention policy. It performs no
// state transitions or dynamic key evaluation; those belong to the runtime.
package attention

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/cel-go/cel"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
)

// Validate checks both YAML and directly constructed protobuf policies. Absence
// is intentionally valid and preserves legacy attention behavior.
func Validate(policy *reliantv1.WorkflowAttention) error {
	if policy == nil {
		return nil
	}
	if strings.TrimSpace(policy.GetKey()) == "" {
		return fmt.Errorf("attention.key must be a nonempty string")
	}
	switch policy.GetOnStart() {
	case "", "recovering", "action_required":
	default:
		return fmt.Errorf("attention.on_start must be recovering or action_required, or absent")
	}
	switch policy.GetOnSuccess() {
	case "", "resolved", "action_required":
	default:
		return fmt.Errorf("attention.on_success must be resolved or action_required, or absent")
	}
	if policy.GetOnFailure() != "" && policy.GetOnFailure() != "action_required" {
		return fmt.Errorf("attention.on_failure must be action_required or absent")
	}
	if policy.GetOnStart() == "recovering" {
		d, err := time.ParseDuration(policy.GetRecoveryTimeout())
		if err != nil || d < time.Second || d > 24*time.Hour {
			return fmt.Errorf("attention.recovery_timeout must be a Go duration between 1s and 24h")
		}
	} else if policy.GetRecoveryTimeout() != "" {
		return fmt.Errorf("attention.recovery_timeout requires on_start: recovering")
	}
	return validateKey(policy.GetKey())
}

func validateKey(key string) error {
	matches, err := wfcel.ScanTemplate(key)
	if err != nil {
		return fmt.Errorf("attention.key: %w", err)
	}
	if len(matches) == 0 {
		if len(key) > 512 {
			return fmt.Errorf("attention.key must be at most 512 bytes")
		}
		return nil
	}
	// No nodes, workflow metadata, secrets or trigger namespace: correlation
	// is an explicit template over finalized workflow inputs only.
	env, err := wfcel.NewEnv(wfcel.CELEnvConfig{
		Namespaces:    []wfcel.CELNamespace{wfcel.CELInputs},
		IncludeStdLib: true, IncludeCustomFunctions: true,
	})
	if err != nil {
		return fmt.Errorf("attention.key: %w", err)
	}
	for _, match := range matches {
		ast, issues := env.Compile(match.Expr)
		if issues != nil && issues.Err() != nil {
			return fmt.Errorf("attention.key: %w", issues.Err())
		}
		// A pure expression preserves its native type. Reject statically known
		// non-string results; dynamic inputs are checked once at run start.
		if len(matches) == 1 && strings.TrimSpace(key) == key[match.Start:match.End] {
			typ := ast.OutputType()
			if typ != cel.DynType && !typ.IsExactType(cel.StringType) {
				return fmt.Errorf("attention.key must produce a string, got %s", typ)
			}
		}
	}
	return nil
}
