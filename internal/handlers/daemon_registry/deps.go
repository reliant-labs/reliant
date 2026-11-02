// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them. *db.Repo satisfies Repository.
package daemon_registry

import (
	"context"
	"time"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// Repository is the slice of *db.Repo the daemon_registry handlers use.
type Repository interface {
	GetDaemon(ctx context.Context, id string) (*db.Daemon, error)
	ListDaemonsByUserID(ctx context.Context, userID string) ([]*db.Daemon, error)
	ListFreshDaemonAttachmentsForUser(ctx context.Context, userID string, staleThreshold time.Duration) ([]*db.DaemonAttachment, error)
	ListDaemonLocalModels(ctx context.Context, userID string) (map[string]string, error)
	ListDaemonStorageState(ctx context.Context, userID string) (map[string]string, error)

	// Machine labels and pools (dev-docs/design/machine-pools.md).
	SetDaemonLabels(ctx context.Context, userID, daemonID string, labels map[string]string) (bool, error)
	ListMachinePoolsForUser(ctx context.Context, userID string) ([]*core.MachinePool, error)
	PutMachinePool(ctx context.Context, p *core.MachinePool) (*core.MachinePool, error)
	DeleteMachinePoolByName(ctx context.Context, userID, name string) error
	ListMachinePoolMembers(ctx context.Context, pool *core.MachinePool, projectID string) ([]core.MachinePoolMemberRow, error)
	SetDefaultDaemon(ctx context.Context, userID, daemonID string) error
	// UpsertDaemonIdentity writes a renamed machine's name onto its row.
	UpsertDaemonIdentity(ctx context.Context, id db.DaemonIdentity) (bool, error)
}

// MachineNames renames a machine at the control plane, which owns the name.
// controlplane.Service implements it.
type MachineNames interface {
	RenameDaemon(ctx context.Context, jwt, daemonID, name string) error
}

// MemberIdleTimeouts sets a managed machine's idle timeout at the control
// plane. controlplane.Service implements it.
type MemberIdleTimeouts interface {
	SetDaemonIdleTimeout(ctx context.Context, jwt, daemonID, idleTimeout string) error
}

// Router is the slice of toolexec.DaemonRouter the daemon_registry handlers
// use. toolexec.DaemonRouter implements it.
type Router interface {
	IsDaemonOnline(ctx context.Context, userID string, selector *toolexec.DaemonSelector) (bool, error)
	SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
	RefreshLocalModels(ctx context.Context, userID, daemonID string) (*pb.LocalModelInventory, error)
}
