// Copyright (c) 2025 Reliant Labs
package practions_test

import (
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/serveraction"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// A GitHub firing of the pr workflow knows the PR's repository and number
// but not its record id: pr.next resolves it among the run's project's
// enrolled repositories, and hands every later node what it needs.
func TestNextByRepositoryAndNumber(t *testing.T) {
	h := newHarness(t, core.PipelineRepoModeShadow)
	h.gh.mu.Lock()
	pr := h.gh.prs[h.number]
	pr.HeadRef = "feat/x"
	h.gh.prs[h.number] = pr
	h.gh.mu.Unlock()

	got := h.must(serveraction.PRNext, call{params: map[string]any{"record_id": "", "repository": "ACME/app", "number": float64(h.number)}})
	if got["record_id"] != h.record || got["next"] != "review" {
		t.Fatalf("want a review of %s, got %v", h.record, got)
	}
	if got["head_ref"] != "feat/x" || got["base_ref"] != "main" || got["shadow"] != true {
		t.Fatalf("want the branches and the mode, got %v", got)
	}
	if got["repo_target"] != "app" || got["repo_subdir"] != "" {
		t.Fatalf("a project with no matching repo checks out by name at the root, got %v / %v", got["repo_target"], got["repo_subdir"])
	}
	if s, _ := got["check_script"].(string); !strings.Contains(s, "run_stage") {
		t.Fatalf("want the fixer's check script, got %q", s)
	}

	// Another project's run cannot reach the PR by name.
	other := serveraction.Principal{UserID: storetest.SeedUserID, OrgID: testOrg, ProjectID: "project-other"}
	_, err := h.do(serveraction.PRNext, call{principal: &other, params: map[string]any{"record_id": "", "repository": "acme/app", "number": float64(h.number)}})
	refused(t, err, serveraction.CodeNotFound)

	// Neither form: refused.
	_, err = h.do(serveraction.PRNext, call{params: map[string]any{"record_id": ""}})
	refused(t, err, serveraction.CodeInvalidArgument)
}
