// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
	"github.com/reliant-labs/reliant/internal/orgfunding"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// A call that answers a chat member's message never routes to the owner's
// own account (control-plane docs/design/chat-collaboration.md §5.3 rule 2).
// Each of the owner's ways of putting the chat on their own account — a live
// funding lease, a personal_first order, a provider they picked — is honoured
// for the owner's turns and ignored for a member's: the org's account serves,
// and with none it waits with a reason a member can read, asking the owner
// nothing.
func TestCollaboratorSpan_NeverRoutesToAPersonalAccount(t *testing.T) {
	member := func(s *llmCallSpec) { s.Collaborator = true }
	lease := func(env *fundingEnv) {
		until := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
		env.store.rows["chat-1"] = &core.PersonalFunding{ChatID: "chat-1", UserID: "u1", OrgID: fundOrg,
			State: core.PersonalFundingUsePersonal, Reason: core.PersonalFundingUnserved, Model: "gpt-5.6-sol", LeaseUntil: &until, Announced: true}
	}
	personalFirst := func(env *fundingEnv) { env.store.chatOrder["chat-1"] = string(orgfunding.OrderPersonalFirst) }
	pick := func(env *fundingEnv) { env.store.personPick = map[string][2]string{"chat-1": {fundOrg, "codex"}} }

	for _, tc := range []struct {
		name  string
		setup func(*fundingEnv)
		// onLimit is the owner's per-org choice: use_personal is the
		// loosest, and must not reach a member's call either.
		onLimit orgfunding.OnOrgLimit
	}{
		{"a live personal-funding lease", lease, orgfunding.OnOrgLimitUsePersonal},
		{"a personal_first order", personalFirst, orgfunding.OnOrgLimitUsePersonal},
		{"a provider the owner picked", pick, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("the owner's own turn uses their account", func(t *testing.T) {
				withAvailableDrivers(t, tiered(myCodex("my-codex")))
				env := newFundingEnv(t, tc.onLimit, "")
				tc.setup(env)
				res, err := env.resolve(t, "root", "gpt-5.6-sol@codex")
				require.NoError(t, err)
				assert.Equal(t, "my-codex", res.Account.Config.AccountID, "the control: the owner chose their own account")
			})
			t.Run("a member's turn waits for the org, asking nothing", func(t *testing.T) {
				withAvailableDrivers(t, tiered(myCodex("my-codex")))
				env := newFundingEnv(t, tc.onLimit, "")
				tc.setup(env)
				before := env.store.row("chat-1")
				_, err := env.resolve(t, "root", "gpt-5.6-sol@codex", member)
				isWait(t, err)
				var ule *ratelimit.UsageLimitError
				require.ErrorAs(t, err, &ule)
				assert.Contains(t, err.Error(), "collaborators' messages never use a personal account")
				assert.Empty(t, env.built, "no personal driver is even built")
				assert.Equal(t, before, env.store.row("chat-1"), "the owner's funding question is never asked or touched for a member")
			})
			t.Run("a member's turn on a model the org serves uses the org's account", func(t *testing.T) {
				withAvailableDrivers(t, tiered(acmeKey("acme-key"), myCodex("my-codex")))
				env := newFundingEnv(t, tc.onLimit, "")
				tc.setup(env)
				res, err := env.resolve(t, "root", routeModel, member)
				require.NoError(t, err)
				assert.Equal(t, "acme-key", res.Account.Config.AccountID)
				assert.NotContains(t, env.built, "my-codex")
			})
		})
	}

	t.Run("a member's background call is refused, not parked", func(t *testing.T) {
		withAvailableDrivers(t, tiered(myCodex("my-codex")))
		env := newFundingEnv(t, orgfunding.OnOrgLimitUsePersonal, "")
		_, err := env.resolve(t, "root", "gpt-5.6-sol@codex", member, func(s *llmCallSpec) { s.Background = true })
		var refused *personalFundingRefused
		require.ErrorAs(t, err, &refused, "a title falls back and a compaction is skipped")
		var failure *types.UsageLimitFailure
		assert.NotErrorAs(t, err, &failure)
		assert.Empty(t, env.built)
	})
}
