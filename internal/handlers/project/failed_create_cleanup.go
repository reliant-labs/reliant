package project

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/workspacecreate"
)

// failedCreateCleanupTimeout bounds one connect's failed-create cleanup: one
// daemon request per project with FAILED workspaces on the machine.
const failedCreateCleanupTimeout = 10 * time.Minute

// startFailedCreateCleanup asks daemonID, which just connected, to remove the
// checkouts its failed workspace creates left behind
// (workspacecreate.RemoveFailedCreatesOn; reliant #2413). It runs in the
// background, after the connect event is acked: it can take longer than the
// event's ack wait, and nothing about the connect depends on it. One runs per
// machine at a time; a connect that finds one running adds nothing.
func (s *Service) startFailedCreateCleanup(userID, daemonID string) {
	if daemonID == "" || s.deps.Router == nil {
		return
	}
	if _, running := s.failedCreateCleanups.LoadOrStore(daemonID, struct{}{}); running {
		return
	}
	go func() {
		defer s.failedCreateCleanups.Delete(daemonID)
		ctx, cancel := context.WithTimeout(context.Background(), failedCreateCleanupTimeout)
		defer cancel()
		workspacecreate.RemoveFailedCreatesOn(ctx, s.deps.Repo, pinnedMachine{s: s, userID: userID, daemonID: daemonID}, daemonID)
	}()
}

// pinnedMachine is a workspacecreate.Machine that sends to one named daemon:
// the cleanup is about the machine that connected, and default resolution
// may name another.
type pinnedMachine struct {
	s        *Service
	userID   string
	daemonID string
}

func (m pinnedMachine) Send(ctx context.Context, commandType string, payload, resp any, timeoutMs int32) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	out, err := m.s.deps.Router.SendDaemonCommandToDaemon(ctx, m.userID, m.daemonID, commandType, b, timeoutMs)
	if err != nil {
		return fmt.Errorf("daemon command %s: %w", commandType, err)
	}
	if resp != nil {
		if err := json.Unmarshal(out, resp); err != nil {
			return fmt.Errorf("unmarshal response for %s: %w", commandType, err)
		}
	}
	return nil
}

var _ workspacecreate.Machine = pinnedMachine{}
