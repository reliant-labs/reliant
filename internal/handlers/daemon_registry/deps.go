// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them. *db.Repo satisfies Repository.
package daemon_registry

import (
	"context"
	"time"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// Repository is the slice of *db.Repo the daemon_registry handlers use.
type Repository interface {
	GetDaemon(ctx context.Context, id string) (*db.Daemon, error)
	ListDaemonsByUserID(ctx context.Context, userID string) ([]*db.Daemon, error)
	ListFreshDaemonAttachmentsForUser(ctx context.Context, userID string, staleThreshold time.Duration) ([]*db.DaemonAttachment, error)
	ListDaemonLocalModels(ctx context.Context, userID string) (map[string]string, error)
	ListDaemonStorageState(ctx context.Context, userID string) (map[string]string, error)
	SetDefaultDaemon(ctx context.Context, userID, daemonID string) error
}

// Router is the slice of toolexec.DaemonRouter the daemon_registry handlers
// use. toolexec.DaemonRouter implements it.
type Router interface {
	IsDaemonOnline(ctx context.Context, userID string, selector *toolexec.DaemonSelector) (bool, error)
	SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
	RefreshLocalModels(ctx context.Context, userID, daemonID string) (*pb.LocalModelInventory, error)
}
