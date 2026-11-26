// Copyright (c) 2025 Reliant Labs
package db

import (
	"github.com/reliant-labs/reliant/internal/chatupdates"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// UpdateWriter is the writer of the chat and user update streams over this
// repository, publishing what it commits through the repository's own update
// notifiers (SetUpdateNotifiers). The facade's internals and the packages
// built from a *Repo announce through it; the servers build theirs over the
// hub publisher.
func (r *Repo) UpdateWriter() chatupdates.Service {
	return chatupdates.NewFromORM(r.ORM(), r, repoPublisher{r})
}

// repoPublisher is chatupdates.Publisher over the repository's notifiers, read
// when an update commits: they are set after the repository is built.
type repoPublisher struct{ r *Repo }

func (p repoPublisher) PublishChat(chatID string, seq int64, update core.ChatUpdate) {
	if p.r.onChatUpdate != nil {
		p.r.onChatUpdate(chatID, seq, update)
	}
}

func (p repoPublisher) PublishUser(update *core.UserUpdate) {
	if p.r.onUserUpdate != nil {
		p.r.onUserUpdate(update)
	}
}
