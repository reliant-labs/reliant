// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

var updateTriggerGolden = flag.Bool("update-trigger-golden", false,
	"rewrite testdata/trigger/*.golden from the current output")

// checkTriggerGolden compares got with testdata/trigger/<name>.
// Regenerate with: go test ./cmd/reliant/cmd -run TestTriggerFunding -update-trigger-golden
func checkTriggerGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "trigger", name)
	if *updateTriggerGolden {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s (regenerate with -update-trigger-golden): %v", path, err)
	}
	if got != string(want) {
		t.Errorf("%s differs (regenerate with -update-trigger-golden)\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}

func orgTrigger(funding reliantv1.UnattendedFunding) *reliantv1.Trigger {
	org, worktree, conn := "acme", "wt-1", "conn-1"
	model, _ := structpb.NewValue(map[string]any{"id": "claude-opus-4-6"})
	return &reliantv1.Trigger{
		Id: "trg-1", Name: "nightly", ProjectId: "proj-1", WorktreeId: &worktree, Enabled: true,
		Workflow: "builtin://agent", Presets: map[string]string{"": "fast"},
		Params: map[string]*structpb.Value{"model": model}, Message: "Audit the dependency tree.",
		DaemonId: "daemon-1", NotifyOnComplete: true, ConnectionId: &conn, Filter: "true",
		ActingIdentity: reliantv1.ActingIdentity_ACTING_IDENTITY_OWNER,
		Source:         &reliantv1.Trigger_Schedule{Schedule: &reliantv1.ScheduleSource{Cron: []string{"0 9 * * *"}, Timezone: "UTC"}},
		CreatedAt:      "2026-11-14T00:00:00Z", UpdatedAt: "2026-11-14T00:00:00Z",
		OrgId: &org, UnattendedFunding: funding,
	}
}

// `trigger get` prints which AI accounts a trigger of an org runs on
// (org-provider-accounts §11.5); a trigger of no org has no stamp and prints
// no line, and a server that predates the field reads as any.
func TestTriggerFunding_DetailShowsTheStamp(t *testing.T) {
	var out strings.Builder
	printTriggerDetail(&out, orgTrigger(reliantv1.UnattendedFunding_UNATTENDED_FUNDING_ORG_ONLY))
	checkTriggerGolden(t, "detail_org_only.golden", out.String())

	out.Reset()
	printTriggerDetail(&out, orgTrigger(reliantv1.UnattendedFunding_UNATTENDED_FUNDING_ANY))
	checkTriggerGolden(t, "detail_any.golden", out.String())

	out.Reset()
	printTriggerDetail(&out, orgTrigger(reliantv1.UnattendedFunding_UNATTENDED_FUNDING_UNSPECIFIED))
	assert.Contains(t, out.String(), "Unattended funding:  any (")

	out.Reset()
	noOrg := orgTrigger(reliantv1.UnattendedFunding_UNATTENDED_FUNDING_UNSPECIFIED)
	noOrg.OrgId = nil
	printTriggerDetail(&out, noOrg)
	assert.NotContains(t, out.String(), "Unattended funding")
	assert.NotContains(t, out.String(), "Organization")
}

// A write that went through with a funding warning says so after the detail.
func TestTriggerFunding_PrintsTheWarning(t *testing.T) {
	var out strings.Builder
	printFundingWarning(&out, &reliantv1.TriggerFundingWarning{
		OrgId: "acme", Model: "claude-opus-4-6",
		Message: "No account of this organization can run claude-opus-4-6 yet, so its runs will fail until an admin adds one.",
	})
	checkTriggerGolden(t, "funding_warning.golden", out.String())

	out.Reset()
	printFundingWarning(&out, nil)
	assert.Empty(t, out.String())
}

// --org-accounts-only alone sends the stored definition back with only the
// stamp tightened, whatever the trigger's kind; with other flags it rides on
// the full replacement; without it nothing changes the stamp (UNSPECIFIED
// keeps the stored one); and the CLI never loosens.
func TestTriggerFunding_UpdateOrgAccountsOnly(t *testing.T) {
	run := func(t *testing.T, existing *reliantv1.Trigger, args ...string) (*reliantv1.TriggerDefinition, bool, error) {
		t.Helper()
		cmd := newTriggerUpdateCmd()
		require.NoError(t, cmd.ParseFlags(args))
		orgOnly, err := cmd.Flags().GetBool(orgAccountsOnlyFlag)
		require.NoError(t, err)
		fromFlagsCalled := false
		def, err := updateDefinition(cmd, orgOnly, existing, func() (*reliantv1.TriggerDefinition, error) {
			fromFlagsCalled = true
			return &reliantv1.TriggerDefinition{Name: "from flags"}, nil
		})
		return def, fromFlagsCalled, err
	}

	t.Run("alone, a schedule trigger", func(t *testing.T) {
		existing := orgTrigger(reliantv1.UnattendedFunding_UNATTENDED_FUNDING_ANY)
		def, fromFlags, err := run(t, existing, "--org-accounts-only")
		require.NoError(t, err)
		assert.False(t, fromFlags, "the stored definition, not a replacement from flags")
		want := &reliantv1.TriggerDefinition{
			Name: "nightly", ProjectId: "proj-1", WorktreeId: existing.WorktreeId, Workflow: "builtin://agent",
			Presets: existing.Presets, Params: existing.Params, Message: "Audit the dependency tree.",
			DaemonId: "daemon-1", NotifyOnComplete: true, ConnectionId: existing.ConnectionId, Filter: "true",
			ActingIdentity:    reliantv1.ActingIdentity_ACTING_IDENTITY_OWNER,
			Source:            &reliantv1.TriggerDefinition_Schedule{Schedule: existing.GetSchedule()},
			UnattendedFunding: reliantv1.UnattendedFunding_UNATTENDED_FUNDING_ORG_ONLY,
		}
		assert.True(t, proto.Equal(want, def), "got %v", def)
		assert.Nil(t, def.Enabled, "enabled is left as it is")
	})

	t.Run("alone, an activation names its declaration and sends no filter", func(t *testing.T) {
		existing := orgTrigger(reliantv1.UnattendedFunding_UNATTENDED_FUNDING_ANY)
		declared := "nightly"
		existing.WorkflowTrigger = &declared
		def, _, err := run(t, existing, "--org-accounts-only")
		require.NoError(t, err)
		assert.Equal(t, "nightly", def.GetWorkflowTrigger())
		assert.Empty(t, def.GetFilter())
		assert.Equal(t, reliantv1.UnattendedFunding_UNATTENDED_FUNDING_ORG_ONLY, def.GetUnattendedFunding())
	})

	t.Run("alone, an integration trigger keeps its source", func(t *testing.T) {
		existing := orgTrigger(reliantv1.UnattendedFunding_UNATTENDED_FUNDING_ANY)
		existing.Source = &reliantv1.Trigger_Integration{Integration: &reliantv1.IntegrationSource{Integration: "github"}}
		def, _, err := run(t, existing, "--org-accounts-only")
		require.NoError(t, err)
		assert.Equal(t, "github", def.GetIntegration().GetIntegration())
	})

	t.Run("alone beside a connection flag", func(t *testing.T) {
		root := newTriggerCmd()
		update, _, err := root.Find([]string{"update"})
		require.NoError(t, err)
		require.NoError(t, update.ParseFlags([]string{"--org-accounts-only", "--daemon-session"}))
		assert.True(t, onlyFundingChanged(update), "--daemon-session chooses how to connect, not what to write")
	})

	t.Run("with other flags it rides on the replacement", func(t *testing.T) {
		def, fromFlags, err := run(t, orgTrigger(reliantv1.UnattendedFunding_UNATTENDED_FUNDING_ANY), "--org-accounts-only", "--message", "x")
		require.NoError(t, err)
		assert.True(t, fromFlags)
		assert.Equal(t, "from flags", def.GetName())
		assert.Equal(t, reliantv1.UnattendedFunding_UNATTENDED_FUNDING_ORG_ONLY, def.GetUnattendedFunding())
	})

	t.Run("without it the stamp is kept", func(t *testing.T) {
		def, fromFlags, err := run(t, orgTrigger(reliantv1.UnattendedFunding_UNATTENDED_FUNDING_ORG_ONLY), "--message", "x")
		require.NoError(t, err)
		assert.True(t, fromFlags)
		assert.Equal(t, reliantv1.UnattendedFunding_UNATTENDED_FUNDING_UNSPECIFIED, def.GetUnattendedFunding())
	})

	t.Run("the CLI never loosens", func(t *testing.T) {
		_, fromFlags, err := run(t, orgTrigger(reliantv1.UnattendedFunding_UNATTENDED_FUNDING_ORG_ONLY), "--org-accounts-only=false")
		require.ErrorIs(t, err, errLoosenInApp)
		assert.False(t, fromFlags)
	})
}
