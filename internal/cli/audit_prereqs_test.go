package cli

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/audittype"
	"github.com/reliant-labs/forge/internal/config"
)

// crossCheckPrereqs / byteMatchGroups stay in package cli (audit_prereqs_cli.go)
// because they depend on the KCL render entity structs; the audit command group
// reaches them through factory.AuditAPI.Prerequisites.

// TestCrossCheckPrereqs_Checklist asserts the declared external Secrets + DNS
// records surface as findings and the byte-match group is reported.
func TestCrossCheckPrereqs_Checklist(t *testing.T) {
	secrets := []ExternalSecretEntity{
		{Name: "cloudflare-api-token", Namespace: "cert-manager", Keys: []string{"api-token"}, Reason: "DNS-01 token"},
		{Name: "cf-a", Namespace: "prod", Keys: []string{"api-token"}, ValueGroup: "cf"},
		{Name: "cf-b", Namespace: "prod", Keys: []string{"api-token"}, ValueGroup: "cf"},
	}
	dns := []DNSRecordEntity{
		{Host: "*.workspaces.example.com", Type: "A", Reason: "wildcard"},
	}
	cat := crossCheckPrereqs(secrets, dns, nil)
	if cat.Status != audittype.StatusOK {
		t.Fatalf("status = %q, want ok (a multi-member byte-match group is fine)", cat.Status)
	}
	findings, ok := cat.Details["findings"].([]string)
	if !ok {
		t.Fatalf("findings missing/wrong type: %T", cat.Details["findings"])
	}
	joined := strings.Join(findings, "\n")
	if !strings.Contains(joined, "cert-manager/cloudflare-api-token") {
		t.Errorf("expected the declared Secret in the checklist:\n%s", joined)
	}
	if !strings.Contains(joined, "*.workspaces.example.com") {
		t.Errorf("expected the declared DNS record in the checklist:\n%s", joined)
	}
	if !strings.Contains(joined, `value-group "cf": 2 Secrets must carry identical bytes`) {
		t.Errorf("expected the byte-match group membership line:\n%s", joined)
	}
	if cat.Details["byte_match_groups"].(int) != 1 {
		t.Errorf("byte_match_groups = %v, want 1", cat.Details["byte_match_groups"])
	}
}

// TestCrossCheckPrereqs_SingletonGroupWarns asserts a value_group with a
// single member is a smell (matches nothing) and flips the category to warn.
func TestCrossCheckPrereqs_SingletonGroupWarns(t *testing.T) {
	secrets := []ExternalSecretEntity{
		{Name: "lonely", Namespace: "prod", Keys: []string{"k"}, ValueGroup: "solo"},
	}
	cat := crossCheckPrereqs(secrets, nil, nil)
	if cat.Status != audittype.StatusWarn {
		t.Fatalf("status = %q, want warn for a single-member byte-match group", cat.Status)
	}
	findings, _ := cat.Details["findings"].([]string)
	if !strings.Contains(strings.Join(findings, "\n"), "only one member") {
		t.Errorf("expected a single-member-group warning, got %v", findings)
	}
}

// TestCrossCheckPrereqs_Empty asserts no declarations => ok with zero counts.
func TestCrossCheckPrereqs_Empty(t *testing.T) {
	cat := crossCheckPrereqs(nil, nil, nil)
	if cat.Status != audittype.StatusOK {
		t.Fatalf("status = %q, want ok", cat.Status)
	}
	if cat.Details["external_secrets"].(int) != 0 || cat.Details["dns_records"].(int) != 0 {
		t.Errorf("expected zero counts, got %v", cat.Details)
	}
}

// TestAuditPrerequisites_CLIKindSkipped asserts a project with no deploy
// surface (kind=cli / library) reports ✓ n/a instead of warning that it
// could not evaluate a non-existent dev KCL.
func TestAuditPrerequisites_CLIKindSkipped(t *testing.T) {
	for _, kind := range []string{"cli", "library"} {
		cfg := &config.ProjectConfig{Kind: kind}
		cat := auditPrerequisites(cfg, t.TempDir())
		if cat.Status != audittype.StatusOK {
			t.Errorf("kind=%s: status = %q, want ok (n/a)", kind, cat.Status)
		}
		if !strings.Contains(cat.Summary, "n/a") {
			t.Errorf("kind=%s: summary should say n/a, got %q", kind, cat.Summary)
		}
	}
}

// TestByteMatchGroups asserts standalone (no value_group) secrets are excluded.
func TestByteMatchGroups(t *testing.T) {
	secrets := []ExternalSecretEntity{
		{Name: "standalone", Namespace: "ns", Keys: []string{"k"}},
		{Name: "a", Namespace: "ns", Keys: []string{"k"}, ValueGroup: "g"},
		{Name: "b", Namespace: "ns", Keys: []string{"k"}, ValueGroup: "g"},
	}
	groups := byteMatchGroups(secrets)
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d (%v)", len(groups), groups)
	}
	if len(groups["g"]) != 2 {
		t.Errorf("expected 2 members in group g, got %v", groups["g"])
	}
}
