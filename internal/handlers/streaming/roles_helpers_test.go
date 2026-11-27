package streaming

import (
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/store"
)

// withRoles fills the roles d leaves nil from d.Repo: the repo itself when it
// implements the role (a fake that answers the read), or the generated ext
// store of a real *db.Repo. A role neither can fill stays nil, and the test
// factory's mock answers it.
func withRoles(d Deps) Deps {
	if d.TriggerReads == nil {
		d.TriggerReads = roleOf[TriggerReads](d.Repo, func(r *db.Repo) TriggerReads { return store.NewTriggerEventExtStore(r.ORM()) })
	}
	if d.Heads == nil {
		d.Heads = roleOf[Heads](d.Repo, func(r *db.Repo) Heads { return store.NewChatUpdateExtStore(r.ORM()) })
	}
	if d.UserLog == nil {
		d.UserLog = roleOf[UserLog](d.Repo, func(r *db.Repo) UserLog { return store.NewUserUpdateExtStore(r.ORM()) })
	}
	if d.OrgFeed == nil {
		d.OrgFeed = roleOf[OrgFeed](d.Repo, func(r *db.Repo) OrgFeed { return store.NewOrgUpdateExtStore(r.ORM()) })
	}
	if d.Siblings == nil {
		d.Siblings = roleOf[Siblings](d.Repo, func(r *db.Repo) Siblings { return store.NewMessageExtStore(r.ORM()) })
	}
	if d.Senders == nil {
		d.Senders = roleOf[Senders](d.Repo, func(r *db.Repo) Senders { return store.NewThreadExtStore(r.ORM()) })
	}
	if d.ChatRows == nil {
		d.ChatRows = roleOf[ChatRows](d.Repo, func(r *db.Repo) ChatRows { return store.NewChatExtStore(r.ORM()) })
	}
	if d.Projects == nil {
		d.Projects = roleOf[Projects](d.Repo, func(r *db.Repo) Projects { return store.NewProjectExtStore(r.ORM()) })
	}
	if d.SpawnCalls == nil {
		d.SpawnCalls = roleOf[SpawnCalls](d.Repo, func(r *db.Repo) SpawnCalls { return store.NewToolCallExtStore(r.ORM()) })
	}
	return d
}

func fixtureTriggerReads(repo Repository) TriggerReads {
	return roleOf[TriggerReads](repo, func(r *db.Repo) TriggerReads { return store.NewTriggerEventExtStore(r.ORM()) })
}

func roleOf[T any](repo Repository, fromDB func(*db.Repo) T) T {
	var zero T
	if r, ok := repo.(*db.Repo); ok {
		return fromDB(r)
	}
	if r, ok := repo.(T); ok {
		return r
	}
	return zero
}
