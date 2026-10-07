package toolexec

import (
	"context"

	"github.com/reliant-labs/reliant/internal/daemon"
)

// RemoteDaemonClients returns the DaemonClientFactory a worker uses: a
// RemoteClient that reaches, over router, the daemon the run's tools execute
// on. view, write and edit run on the worker and touch the user's files only
// through this client, so it must name the same machine a daemon-placed tool
// such as shell is sent to.
func RemoteDaemonClients(router DaemonRouter) DaemonClientFactory {
	return func(userID string, selector *DaemonSelector) daemon.Client {
		return daemon.NewRemoteClient(runSelectorSender{router: router, selector: selector}, userID)
	}
}

// runSelectorSender sends every command to the daemon selector names,
// with default resolution for a nil selector.
type runSelectorSender struct {
	router   DaemonRouter
	selector *DaemonSelector
}

func (s runSelectorSender) SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return SendDaemonCommandForSelector(ctx, s.router, userID, s.selector, commandType, payload, timeoutMs)
}
