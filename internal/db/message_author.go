// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"strings"
)

// MessageAuthor is the person a human message saved or queued under a
// context is from (control-plane docs/design/chat-collaboration.md §6.1):
// messages.author_user_id / author_name, and the mailbox row's
// agent_messages.author_user_id / author_name, which the drain copies.
//
// It travels on the context for the reason MessageOrigin does: the send
// requests are public, and an author a client could set is an author a client
// could forge. Only the send path, after authorizing the sender, puts one on.
type MessageAuthor struct {
	// UserID is the author when that is not the chat's owner; empty for the
	// owner, whose rows keep author_user_id NULL (NULL means the owner).
	UserID string
	// Name is the author's display name at send time, the name the model's
	// transcript renders (internal/authortag). Set for the owner too, so the
	// owner's turns are named once a second person writes. Never an email or
	// an id; empty records nothing.
	Name string
}

type messageAuthorKey struct{}

// WithMessageAuthor returns ctx carrying author: the user-role messages saved
// or queued under it that no agent sent are recorded as theirs. A zero author
// returns ctx unchanged.
func WithMessageAuthor(ctx context.Context, author MessageAuthor) context.Context {
	author.Name = strings.TrimSpace(author.Name)
	if author.UserID == "" && author.Name == "" {
		return ctx
	}
	return context.WithValue(ctx, messageAuthorKey{}, author)
}

// MessageAuthorFrom reports the author ctx carries, if any.
func MessageAuthorFrom(ctx context.Context) (MessageAuthor, bool) {
	author, ok := ctx.Value(messageAuthorKey{}).(MessageAuthor)
	return author, ok
}

// columns is the author as the two nullable columns store it.
func (a MessageAuthor) columns() (userID, name *string) {
	if a.UserID != "" {
		id := a.UserID
		userID = &id
	}
	if a.Name != "" {
		n := a.Name
		name = &n
	}
	return userID, name
}

// AuthorColumns is the author ctx carries as the nullable author_user_id and
// author_name columns: both nil when it carries none, or when the message is
// an agent's (MessageOrigin), which has no human author.
func AuthorColumns(ctx context.Context) (userID, name *string) {
	if _, agent := MessageOriginFrom(ctx); agent {
		return nil, nil
	}
	author, ok := MessageAuthorFrom(ctx)
	if !ok {
		return nil, nil
	}
	return author.columns()
}
