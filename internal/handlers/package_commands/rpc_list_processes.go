package package_commands

import (
	"context"
	"sync"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/daemoncmd"
	"github.com/reliant-labs/reliant/internal/logging"
)

// ListProcesses lists package-command processes: one workspace's, read from
// the machine holding it, or every connected machine's.
func (s *Service) ListProcesses(
	ctx context.Context,
	req *connect.Request[reliantv1.ListPackageProcessesRequest],
) (*connect.Response[reliantv1.ListPackageProcessesResponse], error) {
	userID, err := daemoncmd.UserID(ctx)
	if err != nil {
		return nil, err
	}

	worktreeID := req.Msg.GetWorktreeId()
	var processes []*reliantv1.PackageProcess
	if worktreeID != "" {
		_, daemonID, err := s.workspace(ctx, userID, worktreeID, req.Msg.GetChatId())
		if err != nil {
			return nil, err
		}
		// A list never wakes a machine: one that is asleep has no running
		// process to show.
		if daemonID != "" && !s.online(ctx, userID, daemonID) {
			return connect.NewResponse(&reliantv1.ListPackageProcessesResponse{}), nil
		}
		listed, err := s.listOn(ctx, userID, daemonID)
		if err != nil {
			return nil, err
		}
		for _, p := range listed {
			if p.GetWorktreeId() == worktreeID {
				processes = append(processes, p)
			}
		}
	} else {
		processes = s.listEverywhere(ctx, userID)
	}
	return connect.NewResponse(&reliantv1.ListPackageProcessesResponse{Processes: processes}), nil
}

// listEverywhere lists the package-command processes of every connected
// machine of the user's, asking them at once. An asleep machine is skipped,
// and one that fails is left out and logged: the panel shows what it can.
// Without a router that reports liveness it lists the default machine.
func (s *Service) listEverywhere(ctx context.Context, userID string) []*reliantv1.PackageProcess {
	if _, ok := s.deps.Router.(livenessProbe); !ok {
		listed, err := s.listOn(ctx, userID, "")
		if err != nil {
			logging.WarnContext(ctx, "[PackageCommands] could not list processes", "error", err)
		}
		return listed
	}
	daemons, err := s.deps.Repo.ListDaemonsByUserID(ctx, userID)
	if err != nil {
		logging.WarnContext(ctx, "[PackageCommands] could not list the user's machines", "error", err)
		return nil
	}
	var (
		mu  sync.Mutex
		all []*reliantv1.PackageProcess
		wg  sync.WaitGroup
	)
	for _, d := range daemons {
		if !authz.CanReadMachine(ctx, userID, d) || !s.online(ctx, userID, d.ID) {
			continue
		}
		wg.Add(1)
		go func(daemonID string) {
			defer wg.Done()
			listed, err := s.listOn(ctx, userID, daemonID)
			if err != nil {
				logging.WarnContext(ctx, "[PackageCommands] could not list a machine's processes", "daemonID", daemonID, "error", err)
				return
			}
			mu.Lock()
			all = append(all, listed...)
			mu.Unlock()
		}(d.ID)
	}
	wg.Wait()
	return all
}
