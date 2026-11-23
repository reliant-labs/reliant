// Copyright (c) 2025 Reliant Labs

package chatselection

import "context"

// PersonPick is who picks a chat's root model and provider switch through a
// selection write: the chat's owner, signed in as a person, in the chat's org.
type PersonPick struct{ UserID, OrgID string }

type personPickKey struct{}

// WithPersonPick marks ctx's selection writes as pick's own choice: the write
// records the stored root model and provider switch as their pick
// (chat_person_picks), bound to the version that write leaves, in the same
// locked transaction. Only the chat handler and the launcher set it, for a
// write made by the chat's owner as a person whose root model no substitution
// moved.
func WithPersonPick(ctx context.Context, pick PersonPick) context.Context {
	return context.WithValue(ctx, personPickKey{}, pick)
}

// PersonPickFrom is the pick WithPersonPick marked ctx with.
func PersonPickFrom(ctx context.Context) (PersonPick, bool) {
	pick, ok := ctx.Value(personPickKey{}).(PersonPick)
	return pick, ok && pick.UserID != "" && pick.OrgID != ""
}
