// Copyright (c) 2025 Reliant Labs
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/reliant/internal/llm/providerusage"
)

// Environment variables the probe reads. The credential is one YOU export in
// your own shell; the probe never reads one from a database, a keychain or a
// credentials file, and never prints it.
const (
	probeClaudeTokenEnv = "CLAUDE_OAUTH_ACCESS_TOKEN" // #nosec G101 -- env var NAME, not a credential
	probeCodexTokenEnv  = "CODEX_ACCESS_TOKEN"        // #nosec G101 -- env var NAME, not a credential
	probeCodexAcctEnv   = "CODEX_ACCOUNT_ID"
)

// probeEnv reads a credential the operator exported in their own shell.
func probeEnv(name string) string {
	return os.Getenv(name) //nolint:forbidigo // an ambient per-invocation credential, not configuration
}

func newProviderUsageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "provider-usage",
		Short: "Probe provider usage/profile APIs with your own credential",
	}
	cmd.AddCommand(newProviderUsageProbeCmd())
	return cmd
}

func newProviderUsageProbeCmd() *cobra.Command {
	var provider, saveDir string
	cmd := &cobra.Command{
		Use:   "probe",
		Short: "Call the usage (and profile) endpoint once and print the redacted response",
		Long: `Calls the provider's usage API once with a credential you export in your own
shell, and prints the response with identifying values redacted. It never reads
a credential from any database.

  claude: export ` + probeClaudeTokenEnv + `=<access token with the user:profile scope>
          GET https://api.anthropic.com/api/oauth/usage and /api/oauth/profile
  codex:  export ` + probeCodexTokenEnv + `=<ChatGPT access token>  [` + probeCodexAcctEnv + `=<chatgpt_account_id>]
          GET https://chatgpt.com/backend-api/wham/usage

A 401/403/404 means the endpoint does not take that credential: set
usageAPIEnabled[provider]=false in internal/llm/providerusage.

With --save <dir>, the redacted bodies are written as <provider>_usage.json (and
claude_profile.json) so they can replace the synthetic fixtures in
internal/llm/providerusage/testdata/. Review them before committing.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runProviderUsageProbe(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), provider, saveDir)
		},
	}
	cmd.Flags().StringVar(&provider, "provider", "", "claude or codex")
	cmd.Flags().StringVar(&saveDir, "save", "", "write the redacted bodies into this directory")
	_ = cmd.MarkFlagRequired("provider")
	return cmd
}

func runProviderUsageProbe(ctx context.Context, out, errOut io.Writer, provider, saveDir string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	type call struct {
		file string
		do   func() ([]byte, error)
	}
	var calls []call
	switch provider {
	case providerusage.ProviderClaude:
		tok := probeEnv(probeClaudeTokenEnv)
		if strings.TrimSpace(tok) == "" {
			return fmt.Errorf("export %s (your own Claude OAuth access token) first", probeClaudeTokenEnv)
		}
		calls = []call{
			{"claude_usage.json", func() ([]byte, error) { return providerusage.ClaudeUsageRaw(ctx, nil, tok) }},
			{"claude_profile.json", func() ([]byte, error) { return providerusage.ClaudeProfileRaw(ctx, nil, tok) }},
		}
	case providerusage.ProviderCodex:
		tok := probeEnv(probeCodexTokenEnv)
		if strings.TrimSpace(tok) == "" {
			return fmt.Errorf("export %s (your own ChatGPT access token) first", probeCodexTokenEnv)
		}
		acct := probeEnv(probeCodexAcctEnv)
		calls = []call{
			{"codex_usage.json", func() ([]byte, error) { return providerusage.CodexUsageRaw(ctx, nil, tok, acct) }},
		}
	default:
		return fmt.Errorf("--provider must be claude or codex, got %q", provider)
	}

	failed := false
	for _, c := range calls {
		fmt.Fprintf(out, "== %s\n", c.file)
		body, err := c.do()
		if err != nil {
			failed = true
			fmt.Fprintf(out, "FAILED: %v\n", err)
			var se *providerusage.StatusError
			switch {
			case errors.As(err, &se) && (se.StatusCode == 401 || se.StatusCode == 403 || se.StatusCode == 404):
				fmt.Fprintf(errOut, "HTTP %d: this endpoint does not take that credential — set usageAPIEnabled[%q]=false\n", se.StatusCode, provider)
			case errors.Is(err, providerusage.ErrThrottled):
				fmt.Fprintln(errOut, "throttled (HTTP 429): the endpoint is reachable; wait and retry before concluding anything")
			}
			continue
		}
		red, err := redactJSON(body)
		if err != nil {
			failed = true
			fmt.Fprintf(out, "response was not JSON (%d bytes); not printed\n", len(body))
			continue
		}
		fmt.Fprintln(out, string(red))
		if saveDir != "" {
			if err := os.MkdirAll(saveDir, 0o750); err != nil {
				return err
			}
			path := filepath.Join(saveDir, c.file)
			if err := os.WriteFile(path, append(red, '\n'), 0o600); err != nil {
				return err
			}
			fmt.Fprintf(errOut, "wrote %s\n", path)
		}
	}
	if failed {
		return errors.New("one or more probes failed")
	}
	return nil
}

var (
	probeUUIDRE  = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	probeEmailRE = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	// probeKeepStringKeys are string fields that carry no identity: enums,
	// timestamps and names of windows. Every other string is replaced.
	probeKeepStringKeys = map[string]bool{
		"kind": true, "type": true, "status": true, "plan_type": true, "organization_type": true,
		"rate_limit_tier": true, "subscription_status": true, "resets_at": true, "reset_at": true,
		"expires_at": true, "limit_name": true, "metered_feature": true, "display_name_model": true,
		"ineligible_reason": true, "slug": true,
	}
)

// redactJSON replaces everything identifying in a usage/profile body with a
// stable placeholder while keeping its shape: uuids and emails, ids, names and
// any string not on the keep-list. Numbers, booleans, nulls, the structure and
// the keep-listed enum/timestamp strings are preserved, which is exactly what
// the parsers read.
func redactJSON(body []byte) ([]byte, error) {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, err
	}
	seen := map[string]string{}
	var walk func(key string, v any) any
	walk = func(key string, v any) any {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				x[k] = walk(k, e)
			}
			return x
		case []any:
			for i, e := range x {
				x[i] = walk(key, e)
			}
			return x
		case string:
			if x == "" || probeKeepStringKeys[key] {
				return x
			}
			if r, ok := seen[x]; ok {
				return r
			}
			var r string
			switch {
			case probeEmailRE.MatchString(x):
				r = fmt.Sprintf("user%d@example.invalid", len(seen)+1)
			case probeUUIDRE.MatchString(x):
				r = fmt.Sprintf("00000000-0000-4000-8000-%012d", len(seen)+1)
			default:
				r = fmt.Sprintf("redacted-%d", len(seen)+1)
			}
			seen[x] = r
			return r
		}
		return v
	}
	return json.MarshalIndent(walk("", v), "", "  ")
}
