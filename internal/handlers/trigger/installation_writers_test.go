// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The rollout gate is only as good as the claim that bindOrgGitHubApp is the
// ONE writer of a trigger's installation binding: with it refused, no trigger
// is bound, so a replica of the previous release (whose routing ignores a
// binding and whose projection write erases one) is safe during a rollout or
// a rollback. This pins that claim. Reading the key, and the store keeping a
// row's existing value, are allowed where they are.
func TestOnlyTheGatedBindWritesAnInstallation(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	allowed := map[string]string{
		"internal/db/core/trigger.go":                     "declares the key; CarryActivationConfig copies an existing binding",
		"internal/store/trigger_repo_ext.go":              "routing reads it; the projection write keeps the row's own",
		"internal/triggers/reconcile.go":                  "carries the snapshot's existing binding (the store decides)",
		"internal/handlers/trigger/trigger_org_github.go": "bindOrgGitHubApp, behind the rollout gate; renderTrigger reads it",
	}
	uses := regexp.MustCompile(`ActivationInstallationKey|CarryActivationConfig|\bInstallation:\s|\.Installation\s*=[^=]|"installation"\s*\]\s*=`)
	var found []string
	seen := map[string]bool{}
	for _, dir := range []string{"internal", "cmd", "pkg"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, "_gen.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if !strings.Contains(string(src), "IntegrationConfig") && !strings.Contains(string(src), "ActivationInstallation") && !strings.Contains(string(src), "CarryActivationConfig") {
				return nil
			}
			if uses.Match(src) {
				seen[rel] = true
				if _, ok := allowed[rel]; !ok {
					found = append(found, rel)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for rel := range allowed {
		if !seen[rel] {
			t.Errorf("%s no longer touches the binding: drop it from the allowlist (or the scan stopped seeing it)", rel)
		}
	}
	if len(found) > 0 {
		t.Fatalf("these files may write a trigger's installation binding outside the gated bind: %v. "+
			"Route it through bindOrgGitHubApp (orgs.github_installation_triggers_enabled), or add the file here with why it cannot bind", found)
	}
}
