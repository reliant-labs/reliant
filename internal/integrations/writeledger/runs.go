package writeledger

import (
	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/store"
)

// Runs resolves the chat and trigger a call is made for, over the generated
// stores.
type Runs struct {
	store.ChatsWithActivityExtStore
	store.TriggerEventExtStore
	store.TriggerExtStore
}

// NewRuns returns the run lookup over db.
func NewRuns(db orm.Context) Runs {
	return Runs{
		ChatsWithActivityExtStore: store.NewChatsWithActivityExtStore(db),
		TriggerEventExtStore:      store.NewTriggerEventExtStore(db),
		TriggerExtStore:           store.NewTriggerExtStore(db),
	}
}

var _ RunLookup = Runs{}
