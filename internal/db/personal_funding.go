// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"
	"strings"
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
			cur.Reason == q.Reason && cur.Model == q.Model && cur.OrgID == q.OrgID && cur.UserID == q.UserID
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

// ThreadFunding is who paid for threadID's calls on its current account, and the run's org.
func (r *Repo) ThreadFunding(ctx context.Context, threadID string) (funding, orgID, accountID string, found bool, err error) {
	return store.GetThreadFunding(ctx, r.ORM(), threadID)
}

// RecordThreadFunding records who pays for threadID's calls from now on (a no-op when unchanged).
func (r *Repo) RecordThreadFunding(ctx context.Context, threadID, orgID, funding, accountID string) error {
	return store.PutThreadFunding(ctx, r.ORM(), threadID, orgID, funding, accountID, time.Now())
}

// ChatFundingOrder is the funding order a person chose for chatID (the chat's provider policy,
// which only a person may change), "" when it inherits.
func (r *Repo) ChatFundingOrder(ctx context.Context, chatID string) (string, error) {
	sel, err := r.GetChatSelection(ctx, chatID)
	if err != nil || sel == nil {
		return "", err
	}
	return string(sel.Policy.Funding), nil
}

// PersonPick is who picks a chat's root model and provider switch through a
// selection write: the chat's owner, signed in as a person, in the chat's org.
type PersonPick struct{ UserID, OrgID string }

type personPickKey struct{}

// WithPersonPick marks ctx's selection writes as pick's own choice:
// MutateChatSelection records the stored root model and provider switch as
// their pick (chat_person_picks), bound to the version that write leaves, in
// the same locked transaction. Only the chat handler and the launcher set it,
// for a write made by the chat's owner as a person whose root model no
// substitution moved.
func WithPersonPick(ctx context.Context, pick PersonPick) context.Context {
	return context.WithValue(ctx, personPickKey{}, pick)
}

// PersonPickFrom is the pick WithPersonPick marked ctx with.
func PersonPickFrom(ctx context.Context) (PersonPick, bool) {
	pick, ok := ctx.Value(personPickKey{}).(PersonPick)
	return pick, ok && pick.UserID != "" && pick.OrgID != ""
}

// ChatPersonPickDriver is the provider a person picked for chatID's root in
// orgID, the one named provider that is consent to spend their own account
// there (org-provider-accounts §4.4, §10). It is "" when nobody did, when the
// pick was made in another org, or when the chat's selection has been written
// since (its version moved past the pick's).
func (r *Repo) ChatPersonPickDriver(ctx context.Context, chatID, orgID string) (string, error) {
	if chatID == "" || orgID == "" {
		return "", nil
	}
	pick, err := store.CurrentChatPersonPick(ctx, r.ORM(), chatID, orgID)
	if err != nil || pick == nil {
		return "", err
	}
	if pick.ChatProvider != "" {
		return pick.ChatProvider, nil
	}
	_, driver := RootModelPick(map[string]any{"model": pick.ModelID})
	return driver, nil
}

// RootModelPick reads the root model of a chat's selection params ("model",
// a {id, providers} selector or a legacy "<model>@<driver>" string) as a
// comparable key and the one provider it names, "" when it names none or
// several.
func RootModelPick(params map[string]any) (key, driver string) {
	var id string
	var providers []any
	switch v := params["model"].(type) {
	case string:
		id = v
	case map[string]any:
		id, _ = v["id"].(string)
		providers, _ = v["providers"].([]any)
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return "", ""
	}
	if at := strings.LastIndex(id, "@"); at > 0 && at < len(id)-1 {
		return id, id[at+1:]
	}
	if len(providers) == 1 {
		if p, ok := providers[0].(string); ok && p != "" {
			return id + "@" + p, p
		}
	}
	return id, ""
}
