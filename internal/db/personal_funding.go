// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// A chat's question before it moves from its org's accounts to the person's own
// (chat_personal_funding; control-plane docs/design/org-provider-accounts.md §10).

// ErrNoPersonalFundingQuestion is an answer to a chat that has no open question.
var ErrNoPersonalFundingQuestion = svcerr.FailedPrecondition("the chat has no open question about using your own account")

// GetChatPersonalFunding returns the chat's question, or (nil, nil) when it has none. It is not
// scoped to a user: callers read it for a chat they already authorized.
func (r *Repo) GetChatPersonalFunding(ctx context.Context, chatID string) (*core.PersonalFunding, error) {
	return store.GetChatPersonalFunding(ctx, r.ORM(), chatID)
}

// AskPersonalFunding records that the chat asks q (State is set to asked). One question per
// chat: an open question for the same reason and model is kept as it is (its first asked_at,
// and a keep_waiting answer, stand), so every thread of the chat that meets the case shares
// it; a different reason or model, or a use_personal answer whose lease is over, asks anew.
// asked reports a new question.
func (r *Repo) AskPersonalFunding(ctx context.Context, q core.PersonalFunding, now time.Time) (asked bool, err error) {
	if q.ChatID == "" || q.UserID == "" || q.OrgID == "" {
		return false, fmt.Errorf("chat, user and org are required to ask")
	}
	next := q
	next.State, next.LeaseUntil, next.AnsweredAt, next.Announced = core.PersonalFundingAsked, nil, nil, false
	next.AskedAt, next.UpdatedAt = now.UTC(), now.UTC()
	err = r.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		cur, err := store.LockChatPersonalFunding(ctx, r.ORM(), q.ChatID)
		if err != nil {
			return err
		}
		if cur == nil {
			// A concurrent first ask of another thread writes the same question.
			inserted, err := store.InsertChatPersonalFunding(ctx, r.ORM(), next)
			asked = inserted
			return err
		}
		open := (cur.State == core.PersonalFundingAsked || cur.State == core.PersonalFundingKeepWaiting) &&
			cur.Reason == q.Reason && cur.Model == q.Model && cur.OrgID == q.OrgID
		if open {
			if q.OrgResetAt != nil && (cur.OrgResetAt == nil || !cur.OrgResetAt.Equal(*q.OrgResetAt)) {
				cur.OrgResetAt, cur.UpdatedAt = q.OrgResetAt, now.UTC()
				return store.PutChatPersonalFunding(ctx, r.ORM(), *cur)
			}
			return nil
		}
		asked = true
		return store.PutChatPersonalFunding(ctx, r.ORM(), next)
	})
	return asked, err
}

// AnswerPersonalFunding records the person's answer to the chat's open question: state is
// use_personal or keep_waiting. A use_personal answer to a 'limited' question lasts until the
// org's reset. userID must own the question; ErrNoPersonalFundingQuestion when there is none open.
func (r *Repo) AnswerPersonalFunding(ctx context.Context, chatID, userID, state string, now time.Time) (*core.PersonalFunding, error) {
	if state != core.PersonalFundingUsePersonal && state != core.PersonalFundingKeepWaiting {
		return nil, svcerr.InvalidArgument("the answer must be to use your own account or to keep waiting")
	}
	var out *core.PersonalFunding
	err := r.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		cur, err := store.LockOwnChatPersonalFunding(ctx, r.ORM(), chatID, userID)
		if err != nil {
			return err
		}
		if cur == nil || cur.State == core.PersonalFundingUsePersonal {
			return ErrNoPersonalFundingQuestion
		}
		at := now.UTC()
		cur.State, cur.AnsweredAt, cur.UpdatedAt, cur.Announced = state, &at, at, false
		cur.LeaseUntil = nil
		if state == core.PersonalFundingUsePersonal && cur.Reason == core.PersonalFundingLimited {
			until := at.Add(time.Hour) // no reset known: look again within the hour
			if cur.OrgResetAt != nil && cur.OrgResetAt.After(at) {
				until = *cur.OrgResetAt
			}
			cur.LeaseUntil = &until
		}
		out = cur
		return store.PutChatPersonalFunding(ctx, r.ORM(), *cur)
	})
	return out, err
}

// GrantPersonalFunding writes a use_personal answer the person gave ahead of time (their
// remembered preference): q is recorded answered at now.
func (r *Repo) GrantPersonalFunding(ctx context.Context, q core.PersonalFunding, now time.Time) error {
	at := now.UTC()
	q.State, q.AnsweredAt, q.UpdatedAt = core.PersonalFundingUsePersonal, &at, at
	if q.AskedAt.IsZero() {
		q.AskedAt = at
	}
	return store.PutChatPersonalFunding(ctx, r.ORM(), q)
}

// MarkPersonalFundingAnnounced records that the chat said who serves under its use_personal
// answer; reports whether this call marked it (so exactly one call posts the notice).
func (r *Repo) MarkPersonalFundingAnnounced(ctx context.Context, chatID string) (bool, error) {
	return store.MarkChatPersonalFundingAnnounced(ctx, r.ORM(), chatID)
}

// ClearPersonalFunding ends the chat's question or answer; reports whether there was one.
func (r *Repo) ClearPersonalFunding(ctx context.Context, chatID string) (bool, error) {
	return store.DeleteChatPersonalFundingRow(ctx, r.ORM(), chatID)
}

// ProviderAccountOrgs is which org owns accountID and which it is attributed to (metadata
// only); found is false for an account that no longer exists.
func (r *Repo) ProviderAccountOrgs(ctx context.Context, accountID string) (ownerOrg, attributedOrg string, found bool, err error) {
	return store.ProviderAccountOrgs(ctx, r.ORM(), accountID)
}

// LatestChatCallFunding is who paid for userID's newest successful call in chatID, and the
// chat's org at the time; found is false when there is none.
func (r *Repo) LatestChatCallFunding(ctx context.Context, userID, chatID string) (funding, orgID string, found bool, err error) {
	return store.LatestChatCallFunding(ctx, r.ORM(), userID, chatID)
}
