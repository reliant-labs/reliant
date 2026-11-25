package connections

import (
	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/store"
)

// Owners is what a use's owner is read from: the thread, chat or trigger it
// belongs to, over the generated stores. It checks no owner itself: the
// resolver reads the row to learn who the owner is.
type Owners struct {
	store.ThreadExtStore
	store.ChatsWithActivityExtStore
	store.TriggerExtStore
}

// NewOwners returns the owner reads over db.
func NewOwners(db orm.Context) Owners {
	return Owners{
		ThreadExtStore:            store.NewThreadExtStore(db),
		ChatsWithActivityExtStore: store.NewChatsWithActivityExtStore(db),
		TriggerExtStore:           store.NewTriggerExtStore(db),
	}
}

var _ runOwners = Owners{}
