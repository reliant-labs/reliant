// forge:deps — consumer-declared interfaces for this package's Deps.
// yours: scaffolded once, never touched again — forge will not overwrite this file
// Dependency interfaces; canonical pattern in `forge skill load service-layer`.
package daemonmirror

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// The user update a removed daemon's unpinned chats are announced with.
const (
	userUpdateChatConfigChanged = reliantv1.UserUpdateType_USER_UPDATE_TYPE_CHAT_CONFIG_CHANGED
	entityTypeChat              = reliantv1.EntityType_ENTITY_TYPE_CHAT
)

// ─── Dependency interfaces ──────────────────────────────────────────
//
// Declare here the *behaviour* this package needs from each thing it
// calls — one interface per dep, named for the role it plays, NOT for
// the concrete type that will satisfy it. Each one gets a matching
// field in Deps (service.go), and that field must be the interface,
// not the concrete type: `Store Store`, never `Store *db.Postgres`.
// That is what makes New(Deps{...}) constructible from mocks alone,
// and it is lint-enforced for every package
// (forgeconv-deps-are-interfaces).
//
// Keep each one minimal — only the methods this package calls. When
// the far end is another internal package, its own Service interface
// is usually the right type to use directly, and its mock_gen.go
// already has a mock of it.
//
// `forge generate` writes a mock for EVERY interface in this file,
// alongside the Service mock, into mock_gen.go — the first line above
// is what tells it to. Never hand-roll a fake for something declared
// here. `forge fix narrow-deps internal/daemonmirror` appends the
// interfaces it narrows a wide dep to here too.
//
//	// Store is this package's persistence boundary.
//	type Store interface {
//		Get(ctx context.Context, id string) (Thing, error)
//	}

// Daemons is the registry rows and what is bound to a daemon.
// store.DaemonExtStore and store.ChatExtStore together satisfy it.
type Daemons interface {
	UpsertDaemonIdentity(ctx context.Context, id core.DaemonIdentity, createdAt, now time.Time) (bool, error)
	DeleteDaemonRow(ctx context.Context, daemonID string) (userID string, removed bool, err error)
	LockDaemonsAbsentFromSnapshot(ctx context.Context, userID string, keep []string, keepCreatedAfter, keepAttachedSince time.Time) ([]string, error)
	ReleaseDaemonFromWorkspaces(ctx context.Context, daemonID string) error
	UnpinChatsFromDaemon(ctx context.Context, daemonID string) ([]store.UnpinnedChat, error)
}

// Lifecycle applies a control-plane lifecycle event to a registry row, newest
// wins, refusing an event with no daemon id or no time. *db.Repo satisfies it.
type Lifecycle interface {
	ApplyDaemonLifecycle(ctx context.Context, lc core.DaemonLifecycleUpdate) (bool, error)
}

// UserUpdates writes a user update and tells the open clients.
// *db.Repo satisfies it.
type UserUpdates interface {
	CreateUserUpdate(ctx context.Context, update *core.UserUpdate) error
}

// TxRunner opens the transaction a write runs in. *db.Repo satisfies it.
type TxRunner interface {
	RunTx(ctx context.Context, policy orm.TxPolicy, fn func(ctx context.Context) error) error
}

// Stores is Daemons over one orm handle: the daemon queries and the one chat
// query a removal needs, which live in separate generated stores.
type Stores struct {
	store.DaemonExtStore
	chats store.ChatExtStore
}

// NewStores returns Stores over db: the *orm.Client for ordinary use.
func NewStores(db orm.Context) Stores {
	return Stores{DaemonExtStore: store.NewDaemonExtStore(db), chats: store.NewChatExtStore(db)}
}

// UnpinChatsFromDaemon clears every chat's pin to a removed daemon.
func (s Stores) UnpinChatsFromDaemon(ctx context.Context, daemonID string) ([]store.UnpinnedChat, error) {
	return s.chats.UnpinChatsFromDaemon(ctx, daemonID)
}
