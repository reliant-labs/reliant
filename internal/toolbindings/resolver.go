// Copyright (c) 2025 Reliant Labs

// Package toolbindings resolves a tool's bound parameters across the
// configuration scopes a human can write them in, and applies the result to
// the tool list assembled for one LLM call.
//
// The scopes, least specific first:
//
//	tool default  →  global setting  →  workflow YAML  →  preset
//
// Most specific wins, per parameter — not per tool. A preset that binds only
// `model` leaves a global binding of `save_to` in place. That is why the
// layering primitive is tools.Bindings.Merge rather than "take the first
// non-empty scope".
//
// The tool's OWN defaults are deliberately absent from Scopes. ToolWrapper
// already folds DefaultBindings into whatever WithBindings is handed, so a
// scope layer supplies only what it overrides and this package never has to
// know what a tool's default is. That keeps the invariant intact: a tool with
// zero configuration is fully functional, and nothing here has to succeed for
// that to be true.
package toolbindings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// GlobalSettingKeyPrefix namespaces one settings row per tool. The tool name
// is the suffix, so `tool.bindings.generate_image` holds every globally bound
// parameter of generate_image as a single JSON object.
//
// One row per tool rather than one per parameter: a tool's bindings are read
// and written together, and a per-parameter key would turn a single read into
// an unbounded fan-out with no way to enumerate what a tool has configured.
const GlobalSettingKeyPrefix = "tool.bindings."

// globalSettingKeyPattern is the SQL LIKE pattern ListSettingsByKey takes, so
// every tool's bindings arrive in ONE query rather than one per tool.
const globalSettingKeyPattern = GlobalSettingKeyPrefix + "%"

// GlobalSettingKey is the settings key holding one tool's global bindings.
func GlobalSettingKey(toolName string) string { return GlobalSettingKeyPrefix + toolName }

// ToolNameFromGlobalSettingKey returns the tool a global-binding key belongs
// to, and whether the key is one of ours at all.
func ToolNameFromGlobalSettingKey(key string) (string, bool) {
	name, ok := strings.CutPrefix(key, GlobalSettingKeyPrefix)
	if !ok || name == "" {
		return "", false
	}
	return name, true
}

// SettingsReader is the one thing this package needs from the repository.
// Declared here, at the consumer, so nothing has to depend on the full
// db.Repository surface to resolve a binding.
type SettingsReader interface {
	ListSettingsByKey(ctx context.Context, userID string, keyPattern string) ([]*db.Setting, error)
}

// ByTool maps a tool name to the bindings one scope contributes for it.
type ByTool map[string]tools.Bindings

// Scopes holds each configuration scope's contribution, and knows the order
// they layer in.
//
// Every field is optional. A zero Scopes resolves to no bindings for every
// tool, which leaves each tool on its own declared defaults.
type Scopes struct {
	// Global is the user's account-wide preference, stored in settings and
	// read SERVER-SIDE (see LoadGlobal) so it reaches every consumer of the
	// tool — a workflow run, a spawned sub-agent, a scheduled job — and not
	// just whichever frontend happened to write it.
	Global ByTool
	// Workflow is what the workflow YAML binds for this node.
	Workflow ByTool
	// Preset is the most specific scope: the preset the user picked for this
	// particular run.
	Preset ByTool
}

// For returns the bindings in effect for one tool, with the more specific
// scope winning parameter by parameter.
func (s Scopes) For(toolName string) tools.Bindings {
	return s.Global[toolName].
		Merge(s.Workflow[toolName]).
		Merge(s.Preset[toolName])
}

// ToolNames lists every tool any scope has an opinion about, in a stable
// order. Used for logging and to keep test assertions deterministic.
func (s Scopes) ToolNames() []string {
	seen := map[string]struct{}{}
	for _, scope := range []ByTool{s.Global, s.Workflow, s.Preset} {
		for name, bindings := range scope {
			if len(bindings) > 0 {
				seen[name] = struct{}{}
			}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// LoadGlobal reads every global tool binding for a user in one query.
//
// This runs on the SERVER, against the repository, rather than in the browser
// beside the component that wrote it. That is the whole point: a preference
// read only by the chat composer never reaches a tool the server runs on the
// user's behalf, which is exactly the flaw in the existing
// `model.tag_config.*` keys — written by the frontend, read by the frontend,
// invisible to Go.
//
// A malformed or stale row is reported but never fatal. The settings table is
// user-writable and outlives the code that wrote it, so one bad row must
// degrade to "that tool keeps its defaults", not "no tool works".
func LoadGlobal(ctx context.Context, reader SettingsReader, userID string) (ByTool, error) {
	if reader == nil || userID == "" {
		return nil, nil
	}

	rows, err := reader.ListSettingsByKey(ctx, userID, globalSettingKeyPattern)
	if err != nil {
		return nil, fmt.Errorf("loading global tool bindings: %w", err)
	}

	loaded := make(ByTool, len(rows))
	var problems []error
	for _, row := range rows {
		if row == nil {
			continue
		}
		toolName, ok := ToolNameFromGlobalSettingKey(row.Key)
		if !ok {
			continue
		}
		bindings, err := DecodeBindings(row.Value)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", row.Key, err))
			continue
		}
		if len(bindings) == 0 {
			continue
		}
		loaded[toolName] = bindings
	}

	if len(problems) > 0 {
		return loaded, errors.Join(problems...)
	}
	return loaded, nil
}

// DecodeBindings parses one settings row's value into bindings.
//
// The wire shape is the natural JSON of tools.Bindings — a parameter name to
// an object carrying either `literal` or `expr`:
//
//	{"model": {"literal": {"tags": ["image-gen"], "providers": ["reliant"]}}}
//	{"save_to": {"expr": "\"assets/\" + nodes.plan.slug + \".png\""}}
//
// An empty or whitespace-only value decodes to no bindings rather than an
// error: that is how a UI clears a preference without deleting the row.
func DecodeBindings(value string) (tools.Bindings, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed == "null" || trimmed == "{}" {
		return nil, nil
	}
	var decoded tools.Bindings
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return nil, fmt.Errorf("not a valid bindings object: %w", err)
	}
	for name, bound := range decoded {
		if bound.Literal == nil && bound.Expr == "" {
			delete(decoded, name)
		}
	}
	if len(decoded) == 0 {
		return nil, nil
	}
	return decoded, nil
}

// EncodeBindings renders bindings back into a settings row's value.
func EncodeBindings(bindings tools.Bindings) (string, error) {
	if len(bindings) == 0 {
		return "{}", nil
	}
	encoded, err := json.Marshal(bindings)
	if err != nil {
		return "", fmt.Errorf("encoding tool bindings: %w", err)
	}
	return string(encoded), nil
}

// Apply layers the resolved bindings onto each tool and returns the bound
// list, plus the non-fatal problems it hit.
//
// Errors are RETURNED, not raised: a stale global setting naming a parameter
// a tool no longer declares must not fail the whole LLM call. The caller logs
// them and proceeds with a working tool list, which is the same posture
// BindTool takes when it hands back the original tool alongside
// ErrBindingsUnsupported.
//
// The input slice is never mutated — tool instances are shared with the
// factory and the registry, and WithBindings already returns a copy.
func Apply(toolsList []tools.Tool, scopes Scopes) ([]tools.Tool, []error) {
	if len(toolsList) == 0 {
		return toolsList, nil
	}

	var problems []error
	bound := make([]tools.Tool, 0, len(toolsList))
	for _, tool := range toolsList {
		if tool == nil {
			continue
		}
		bindings := scopes.For(tool.Name())
		if len(bindings) == 0 {
			bound = append(bound, tool)
			continue
		}
		boundTool, err := tools.BindTool(tool, bindings)
		if err != nil {
			problems = append(problems, fmt.Errorf("binding %v on %s: %w", bindings.Names(), tool.Name(), err))
		}
		if boundTool == nil {
			boundTool = tool
		}
		bound = append(bound, boundTool)
	}
	return bound, problems
}
