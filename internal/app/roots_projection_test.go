// Copyright (c) 2025 Reliant Labs
package app

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// kclRoot is one root of deploy/kcl/config_gen.k's ROOTS, as the deployed
// workloads consume it (config_gen.workloadEnvMap projects env from these).
type kclRoot struct {
	env, secrets, required []string
}

var (
	rootHeaderRE = regexp.MustCompile(`^    "([a-z-]+)" = Root \{$`)
	listHeadRE   = regexp.MustCompile(`^        (env|secrets|required_secrets) = \[$`)
	listItemRE   = regexp.MustCompile(`^\s+"([A-Z0-9_]+)",$`)
)

// parseKCLRoots reads ROOTS from the checked-in config_gen.k text. The file is
// read by this process, so the test cache sees an edit to it.
func parseKCLRoots(t *testing.T) map[string]*kclRoot {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "kcl", "config_gen.k"))
	require.NoError(t, err)
	roots := map[string]*kclRoot{}
	var cur *kclRoot
	var list *[]string
	in := false
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "ROOTS: ") {
			in = true
			continue
		}
		if !in {
			continue
		}
		if line == "}" {
			break
		}
		if m := rootHeaderRE.FindStringSubmatch(line); m != nil {
			cur = &kclRoot{}
			roots[m[1]] = cur
			list = nil
			continue
		}
		if m := listHeadRE.FindStringSubmatch(line); m != nil && cur != nil {
			switch m[1] {
			case "env":
				list = &cur.env
			case "secrets":
				list = &cur.secrets
			default:
				list = &cur.required
			}
			continue
		}
		if strings.TrimSpace(line) == "]" {
			list = nil
			continue
		}
		if m := listItemRE.FindStringSubmatch(line); m != nil && list != nil {
			*list = append(*list, m[1])
		}
	}
	require.NotEmpty(t, roots, "no ROOTS block parsed from config_gen.k")
	return roots
}

func sorted(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

// TestKCLRootsDeployedCredentials pins the credentials each production root
// (api, gateway, worker) is delivered by deploy/kcl/config_gen.k: the secrets
// it reaches, the ones it requires, that no secret leaks into plain env, and
// the env every root must carry. Regenerating config_gen.k (a forge pin bump,
// a graph refactor that makes components optional) must not drop or
// reclassify one of them.
func TestKCLRootsDeployedCredentials(t *testing.T) {
	roots := parseKCLRoots(t)
	require.ElementsMatch(t, []string{"api", "gateway", "worker"}, keys(roots))

	base := []string{"DATABASE_URL", "DATABASE_DIRECT_URL", "INTERNAL_SERVICE_SECRET", "NATS_URL", "NATS_PASSWORD"}
	integrations := []string{"RELIANT_VAULT_KEY", "RELIANT_SLACK_SIGNING_SECRET", "RELIANT_GITHUB_WEBHOOK_SECRET", "RELIANT_TEST_INTEGRATION_SECRET"}
	want := map[string][]string{
		// IDP_BROKER_TOKEN and IDP_LOGIN_POLICY: the login broker's sensitive
		// managed-identity credentials (ApiServerConfig, served on /auth/*).
		"api":     append(append([]string{"JWT_JWKS_SNAPSHOT", "AUTH_API_KEY", "AUTH_DEV_JWT_SECRET", "IDP_BROKER_TOKEN", "IDP_LOGIN_POLICY"}, base...), integrations...),
		"gateway": base,
		// GITHUB_QUEUE_APP_CALLER_SECRET: the merge queue's proof to
		// control-plane that it is the worker (Queue App tokens).
		"worker": append(append([]string{"GITHUB_QUEUE_APP_CALLER_SECRET"}, base...), integrations...),
	}
	for name, r := range roots {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, sorted(want[name]), sorted(r.secrets), "secrets delivered to %s", name)
			require.Equal(t, []string{"DATABASE_URL", "NATS_URL"}, sorted(r.required), "required secrets of %s", name)
			secret := map[string]bool{}
			for _, s := range r.secrets {
				secret[s] = true
			}
			for _, e := range r.env {
				require.False(t, secret[e], "%s: secret %s is projected as plain env", name, e)
			}
			for _, e := range []string{"RELIANT_LOG_LEVEL", "LOG_FORMAT", "RELIANT_ENV"} {
				require.Contains(t, r.env, e, "%s env", name)
			}
		})
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
