// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// The store over a migrated Postgres: the composite foreign keys and CHECKs
// come back as the package's errors, writes are revision-checked, and the
// service's email path writes a SENT row the table accepts.
func TestPGStore(t *testing.T) {
	env := storetest.New(t)
	st := NewStore(env.ORM)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	sb := &store.GtmStrategy{OrgID: orgB, Name: "B", Status: string(StrategyDraft), Revision: 1, CreatedBy: "u", UpdatedBy: "u", CreatedAt: now, UpdatedAt: now}
	require.NoError(t, st.CreateStrategy(ctx, sb))
	cb := &store.GtmCampaign{OrgID: orgB, Name: "B", Status: string(CampaignDraft), StrategyID: sb.ID, Revision: 1, CreatedAt: now, UpdatedAt: now}
	require.NoError(t, st.CreateCampaign(ctx, cb))
	require.NotEmpty(t, cb.ID)

	t.Run("a link to another org's campaign is a LinkError", func(t *testing.T) {
		err := st.CreateItem(ctx, &store.GtmItem{
			OrgID: orgA, CampaignID: &cb.ID, Format: string(FormatLongPost), Title: "x",
			Status: string(ItemDraft), Revision: 1, CreatedAt: now, UpdatedAt: now,
		})
		var link *LinkError
		require.ErrorAs(t, err, &link)
		assert.Equal(t, "campaign_id", link.Field)
		_, err = st.GetCampaign(ctx, orgA, cb.ID)
		require.ErrorIs(t, err, ErrNotFound, "and reads are org-scoped")
	})

	t.Run("a CHECK violation is a ValidationError naming it", func(t *testing.T) {
		err := st.CreateItem(ctx, &store.GtmItem{
			OrgID: orgA, Format: string(FormatEmail), Title: "x",
			Status: string(ItemSent), Revision: 1, CreatedAt: now, UpdatedAt: now,
		})
		var ve *ValidationError
		require.ErrorAs(t, err, &ve)
		assert.Equal(t, store.GtmItemConstraintSentHasRecord, ve.Field)
	})

	t.Run("an org holds a claim text once", func(t *testing.T) {
		mk := func(org, text string) error {
			return st.CreateClaim(ctx, &store.GtmClaim{
				OrgID: org, Text: text, Category: string(ClaimPricing), Status: string(ClaimUnverified),
				SourceKind: string(ClaimSourceNone), Revision: 1, CreatedAt: now, UpdatedAt: now,
			})
		}
		require.NoError(t, mk(orgA, "Hosting costs $5 a month."))
		require.ErrorIs(t, mk(orgA, "hosting  costs $5 a MONTH!"), ErrClaimExists)
		require.NoError(t, mk(orgB, "Hosting costs $5 a month."))
		rows, err := st.ListClaims(ctx, ClaimFilter{OrgID: orgA, Search: "COSTS $5"})
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.Equal(t, "hosting costs $5 a month", *rows[0].TextNorm)
	})

	t.Run("an update is checked against the revision read", func(t *testing.T) {
		i := &store.GtmIdea{
			OrgID: orgA, Kind: string(IdeaKindGTM), Title: "v1", Evidence: []byte("[]"),
			Status: string(IdeaProposed), Source: string(IdeaSourceHuman), CreatedBy: "u", Revision: 1,
			CreatedAt: now, UpdatedAt: now,
		}
		require.NoError(t, st.CreateIdea(ctx, i))
		i.Title = "v2"
		require.NoError(t, st.UpdateIdea(ctx, i, 1))
		got, err := st.GetIdea(ctx, orgA, i.ID)
		require.NoError(t, err)
		assert.Equal(t, int32(2), got.Revision)
		assert.Equal(t, "v2", got.Title)
		require.ErrorIs(t, st.UpdateIdea(ctx, i, 1), ErrRevisionConflict)
		other := *i
		other.OrgID = orgB
		require.ErrorIs(t, st.UpdateIdea(ctx, &other, 2), ErrRevisionConflict, "another org's write matches no row")
	})

	t.Run("a send hold is taken only when empty and released only by its hash, on an approved row, at its revision", func(t *testing.T) {
		row := &store.GtmItem{
			OrgID: orgA, Format: string(FormatEmail), Title: "Mail", Status: string(ItemApproved),
			EmailTo: []string{"a@example.com"}, EmailSubject: "S", Revision: 1, CreatedAt: now, UpdatedAt: now,
		}
		require.NoError(t, st.CreateItem(ctx, row))
		got, err := st.GetItem(ctx, orgA, row.ID)
		require.NoError(t, err)
		assert.Equal(t, []string{"a@example.com"}, got.EmailTo, "the app role reads the forge:secret email_to")

		heldAt := now.Add(-time.Hour)
		require.ErrorIs(t, st.HoldItemSend(ctx, orgA, row.ID, 2, "h1", heldAt), ErrRevisionConflict, "revision must match")
		require.ErrorIs(t, st.HoldItemSend(ctx, orgB, row.ID, 1, "h1", heldAt), ErrRevisionConflict, "org must match")
		require.NoError(t, st.HoldItemSend(ctx, orgA, row.ID, 1, "h1", heldAt))
		require.ErrorIs(t, st.HoldItemSend(ctx, orgA, row.ID, 1, "h2", heldAt), ErrRevisionConflict, "a second hold loses: approval_hash must be empty")
		got, err = st.GetItem(ctx, orgA, row.ID)
		require.NoError(t, err)
		assert.Equal(t, "h1", got.ApprovalHash)
		assert.Equal(t, int32(1), got.Revision, "a hold never moves the revision")
		assert.True(t, got.UpdatedAt.Equal(heldAt), "the hold is stamped with its own time: %s", got.UpdatedAt)

		require.ErrorIs(t, st.ReleaseItemSend(ctx, orgA, row.ID, 1, "h2", now, true), ErrRevisionConflict, "approval_hash = the hold's hash")
		require.ErrorIs(t, st.ReleaseItemSend(ctx, orgA, row.ID, 2, "h1", now, true), ErrRevisionConflict, "revision must match")
		require.NoError(t, st.ReleaseItemSend(ctx, orgA, row.ID, 1, "h1", now, true))
		got, err = st.GetItem(ctx, orgA, row.ID)
		require.NoError(t, err)
		assert.Empty(t, got.ApprovalHash)
		assert.Equal(t, int32(2), got.Revision, "an invalidating release moves the revision")

		require.NoError(t, st.HoldItemSend(ctx, orgA, row.ID, 2, "h3", now))
		require.NoError(t, st.ReleaseItemSend(ctx, orgA, row.ID, 2, "h3", now, false))
		got, err = st.GetItem(ctx, orgA, row.ID)
		require.NoError(t, err)
		assert.Equal(t, int32(2), got.Revision, "a plain release does not")

		draft := &store.GtmItem{OrgID: orgA, Format: string(FormatEmail), Title: "D", Status: string(ItemDraft), Revision: 1, CreatedAt: now, UpdatedAt: now}
		require.NoError(t, st.CreateItem(ctx, draft))
		require.ErrorIs(t, st.HoldItemSend(ctx, orgA, draft.ID, 1, "h", now), ErrRevisionConflict, "only an approved row is held")
	})

	t.Run("an email is never scheduled or published, at the table too", func(t *testing.T) {
		for _, st2 := range []ItemStatus{ItemScheduled, ItemPublished} {
			err := st.CreateItem(ctx, &store.GtmItem{
				OrgID: orgA, Format: string(FormatEmail), Title: "x", Status: string(st2), ScheduledAt: &now,
				Revision: 1, CreatedAt: now, UpdatedAt: now,
			})
			var ve *ValidationError
			require.ErrorAs(t, err, &ve, st2)
		}
	})

	t.Run("the service end to end", func(t *testing.T) {
		em := &MockEmail{SendFunc: sendsAs("gmail-pg")}
		svc := New(Deps{Store: st, Gate: allowGate(orgA), Email: em})
		strat, err := svc.CreateStrategy(ctx, person, CreateStrategyInput{Name: "Grow", PeriodStart: "2026-12-01", PeriodEnd: "2027-01-31"})
		require.NoError(t, err)
		assert.Equal(t, "2027-01-31", strat.PeriodEnd, "a DATE round-trips")
		c, err := svc.CreateCampaign(ctx, person, CreateCampaignInput{Name: "Launch", StrategyID: strat.ID})
		require.NoError(t, err)
		idea, err := svc.CreateIdea(ctx, agent, CreateIdeaInput{
			Kind: IdeaKindContent, Title: "Post", CampaignID: c.ID,
			Evidence: []Evidence{{Kind: "analytics", Title: "signups up", ObservedAt: &now}},
		})
		require.NoError(t, err)
		require.Len(t, idea.Evidence, 1)
		assert.True(t, idea.Evidence[0].ObservedAt.Equal(now))

		when := now.Add(48 * time.Hour)
		_, err = svc.CreateItem(ctx, agent, CreateItemInput{Format: FormatLongPost, Title: "later", CampaignID: c.ID})
		require.NoError(t, err)
		_, err = svc.CreateItem(ctx, agent, CreateItemInput{Format: FormatLongPost, Title: "dated", CampaignID: c.ID, ScheduledAt: &when})
		require.NoError(t, err)
		mail, err := svc.CreateItem(ctx, agent, CreateItemInput{
			Format: FormatEmail, Title: "Mail", BodyMD: "Hi", IdeaID: idea.ID,
			EmailTo: []string{"a@example.com"}, EmailSubject: "News",
		})
		require.NoError(t, err)

		d, err := svc.GetCampaign(ctx, person, GetCampaignInput{ID: c.ID})
		require.NoError(t, err)
		require.Len(t, d.Items, 2)
		assert.Equal(t, "dated", d.Items[0].Title, "scheduled first")
		require.Len(t, d.Ideas, 1)

		mail, err = svc.UpdateItem(ctx, person, UpdateItemInput{ID: mail.ID, ExpectedRevision: mail.Revision, Status: ptr(ItemApproved)})
		require.NoError(t, err)
		sent, err := svc.SendItemEmail(ctx, agent, SendItemEmailInput{ItemID: mail.ID, ExpectedRevision: mail.Revision, ApprovalHash: ApprovalHash(mail), Run: EmailRun{RunID: "run"}})
		require.NoError(t, err)
		assert.Equal(t, ItemSent, sent.Status)
		assert.Equal(t, "gmail-pg", sent.GmailMessageID)
		assert.Equal(t, mail.Revision+1, sent.Revision, "the hold kept the revision; the record advanced it")
		_, err = svc.SendItemEmail(ctx, agent, SendItemEmailInput{ItemID: mail.ID, ExpectedRevision: mail.Revision, ApprovalHash: ApprovalHash(mail), Run: EmailRun{RunID: "run"}})
		require.ErrorIs(t, err, ErrAlreadySent)
		assert.Equal(t, 1, em.CallCount("Send"))

		pos, err := svc.UpdatePositioning(ctx, person, UpdatePositioningInput{PositioningMD: "p", Audiences: []string{"devs"}})
		require.NoError(t, err)
		assert.Equal(t, int32(1), pos.Revision)
		pos, err = svc.UpdatePositioning(ctx, agent, UpdatePositioningInput{ExpectedRevision: 1, PositioningMD: "p2"})
		require.NoError(t, err)
		assert.Equal(t, int32(2), pos.Revision)
		assert.Empty(t, pos.Audiences)
	})
}
