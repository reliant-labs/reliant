// Copyright (c) 2025 Reliant Labs

// Package llmgatewaystep is the one table of what a workflow run node's
// `llm_gateway` means on the machine: for each agent CLI a step key can be
// minted for, which environment variables carry the key and the gateway to
// it, which variables must be removed so they cannot override them, and the
// budget and lifetime bounds every step key gets.
//
// The worker builds a step's environment from it (Env), the validator checks
// a declaration against it (Lookup, Names, the budget bounds), and the lint
// rule asks it which binary a command must mention (CLI.Binary). The daemon
// knows none of it: it applies whatever daemon.StepEnv the server sent.
//
//forge:exclude-contract: a pure, static table of CLI environment mappings; no state, no I/O
package llmgatewaystep

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/daemon"
)

// Budget bounds, in US dollars. The control plane enforces the ceiling too.
const (
	// DefaultBudgetUSD is a step key's cap when the node sets none.
	DefaultBudgetUSD = 5.0
	// MaxBudgetUSD is the most one step's key may spend.
	MaxBudgetUSD = 50.0
)

// Lifetime bounds.
const (
	// DefaultCommandTimeout is a run node's command timeout when it sets
	// none (the run step's own default).
	DefaultCommandTimeout = 5 * time.Minute
	// TTLSlack is how long a key outlives its command's timeout and its
	// delivery wait: room for the command to be sent and for its last call
	// to settle.
	TTLSlack = 2 * time.Minute
	// MaxTTL is the longest a step key may live.
	MaxTTL = 12 * time.Hour
)

// TTL is the lifetime of the key of a command bounded by timeout (0: the
// default) that may wait up to deliveryWait to reach its machine before it
// starts: timeout + deliveryWait + TTLSlack, at most MaxTTL. The key is
// minted before the first send, so a command held for a machine that is
// away must not find its key already expired when it finally starts.
func TTL(timeout, deliveryWait time.Duration) time.Duration {
	if timeout <= 0 {
		timeout = DefaultCommandTimeout
	}
	return min(timeout+max(deliveryWait, 0)+TTLSlack, MaxTTL)
}

// Budget is the cap of a key whose node asked for budget (0: the default).
func Budget(budget float64) float64 {
	if budget <= 0 {
		return DefaultBudgetUSD
	}
	return budget
}

// The CLI names a node's `cli` takes.
const (
	// CLIClaudeCode is Anthropic's Claude Code (`claude`).
	CLIClaudeCode = "claude-code"
)

// The variable that carries a Claude Code step key.
//
//nolint:gosec // G101: the name of an environment variable, not a credential.
const claudeCodeTokenEnv = "ANTHROPIC_AUTH_TOKEN"

// CLI is one agent CLI a step key can be minted for.
type CLI struct {
	// Name is the node's `cli` value.
	Name string
	// Binary is the program a command runs it as.
	Binary string
	// Unset are the variables removed from its command's environment: every
	// one that would make the CLI authenticate or route another way.
	Unset []string
	// sets are the variables Env sets (for Names), secret included.
	sets []string
	// env builds the step environment from a minted key.
	env func(baseURL, key string, models []string) *daemon.StepEnv
}

// claudeCodeUnset are the variables that would make Claude Code authenticate
// or route another way than the step key: a provider API key, a subscription
// token, the Bedrock and Vertex switches and endpoints, custom headers.
var claudeCodeUnset = []string{
	"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN",
	"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX",
	"ANTHROPIC_VERTEX_PROJECT_ID", "ANTHROPIC_BEDROCK_BASE_URL", "ANTHROPIC_VERTEX_BASE_URL",
	"AWS_BEARER_TOKEN_BEDROCK", "ANTHROPIC_CUSTOM_HEADERS",
}

var claudeCode = CLI{
	Name:   CLIClaudeCode,
	Binary: "claude",
	Unset:  claudeCodeUnset,
	sets: []string{
		"ANTHROPIC_BASE_URL", claudeCodeTokenEnv, "ANTHROPIC_MODEL",
		"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL",
		"ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS",
	},
	env: func(baseURL, key string, models []string) *daemon.StepEnv {
		set := map[string]string{
			"ANTHROPIC_BASE_URL": baseURL,
			// The gateway does not forward Anthropic's experimental beta
			// headers to every upstream; Claude Code then sends none.
			"CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS": "1",
		}
		if len(models) > 0 {
			set["ANTHROPIC_MODEL"] = models[0]
			set["ANTHROPIC_DEFAULT_OPUS_MODEL"] = byFamily(models, "opus")
			set["ANTHROPIC_DEFAULT_SONNET_MODEL"] = byFamily(models, "sonnet")
			set["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = byFamily(models, "haiku")
			set["ANTHROPIC_SMALL_FAST_MODEL"] = byFamily(models, "haiku")
		}
		return &daemon.StepEnv{
			Set: set,
			// ANTHROPIC_AUTH_TOKEN outranks ANTHROPIC_API_KEY and a
			// subscription sign-in in Claude Code's precedence, so a machine
			// signed in as a person still spends the step's key.
			Secret: daemon.SecretEnv{claudeCodeTokenEnv: key},
			Unset:  append([]string(nil), claudeCodeUnset...),
		}
	},
}

// registry is every CLI, by name.
var registry = map[string]CLI{
	CLIClaudeCode: claudeCode,
}

// Lookup is the CLI named name.
func Lookup(name string) (CLI, bool) {
	cli, ok := registry[name]
	return cli, ok
}

// Known is every CLI name, sorted.
func Known() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Names is every variable c sets or unsets: a node's own `env:` may touch
// none of them.
func (c CLI) Names() []string {
	out := append(append([]string(nil), c.sets...), c.Unset...)
	sort.Strings(out)
	return out
}

// Env is the step environment that runs c with key against the gateway at
// baseURL, its models the key's (the first the CLI's main model).
func (c CLI) Env(baseURL, key string, models []string) (*daemon.StepEnv, error) {
	if baseURL == "" || key == "" {
		return nil, errors.New("llm_gateway: a step key needs a gateway url and a key")
	}
	return c.env(baseURL, key, models), nil
}

// MentionsBinary reports whether command runs c's binary as a word (not as a
// part of a longer one), which is what the lint rule asks.
func (c CLI) MentionsBinary(command string) bool {
	for _, word := range strings.FieldsFunc(command, func(r rune) bool { return !commandWordRune(r) }) {
		if word == c.Binary || strings.HasSuffix(word, "/"+c.Binary) {
			return true
		}
	}
	return false
}

// commandWordRune reports whether r can be part of a program's name or path.
func commandWordRune(r rune) bool {
	switch {
	case r == '-', r == '_', r == '.', r == '/':
		return true
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	}
	return false
}

// byFamily is the first of models whose name contains family, else models[0].
func byFamily(models []string, family string) string {
	for _, m := range models {
		if strings.Contains(strings.ToLower(m), family) {
			return m
		}
	}
	return models[0]
}

// Validate checks a declaration's literal values; nil when it can be minted.
func Validate(cli string, budget float64, models []string) error {
	if _, ok := Lookup(cli); !ok {
		if strings.TrimSpace(cli) == "" {
			return fmt.Errorf("llm_gateway needs a cli: one of %s", strings.Join(Known(), ", "))
		}
		return fmt.Errorf("llm_gateway cli %q is not one Reliant can set up: use one of %s", cli, strings.Join(Known(), ", "))
	}
	if budget < 0 || budget > MaxBudgetUSD {
		return fmt.Errorf("llm_gateway budget_usd must be between 0 (the default, $%g) and %g", DefaultBudgetUSD, MaxBudgetUSD)
	}
	seen := make(map[string]bool, len(models))
	for _, m := range models {
		switch {
		case strings.TrimSpace(m) == "":
			return errors.New("llm_gateway models may not contain an empty name")
		case seen[m]:
			return fmt.Errorf("llm_gateway models names %q twice", m)
		}
		seen[m] = true
	}
	return nil
}
