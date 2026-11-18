// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them. *db.Repo satisfies Repository
// and *toolexec.NATSDaemonRouter satisfies ProcessRouter.
package background

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// ProcessRouter is what the background handlers ask of the caller's
// machines. A router that also reports liveness (IsDaemonOnline) has lists
// skip asleep machines instead of waking them.
type ProcessRouter interface {
	SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
	// SendDaemonCommandToDaemon reaches the machine a process runs on, which
	// is not always the default one (a chat in a workspace another machine
	// holds, a process the list read from a second machine).
	SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
	SubscribeProcessOutput(ctx context.Context, userID, processID string, newOnly bool) (<-chan *toolexec.ProcessOutputEvent, func(), error)
}

// Repository is the slice of *db.Repo the background handlers use: the chat
// a list is scoped to and the machine it runs on, and the machines a request
// names or an unscoped list reads.
type Repository interface {
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	// GetWorktree completes daemonplacement.WorktreeReader: a chat in a
	// workspace a machine owns runs on that machine.
	GetWorktree(ctx context.Context, id string) (*db.Worktree, error)
	GetDaemon(ctx context.Context, id string) (*db.Daemon, error)
	ListDaemonsByUserID(ctx context.Context, userID string) ([]*db.Daemon, error)
}
