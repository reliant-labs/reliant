package toolexec

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/mcp"
)

// mcpLoadWaitBudget is the most a caller waits for mcp.ensure_loaded before it
// carries on without the servers that load would have started. The load keeps
// running in the background and its servers' tools join the next listing.
//
// Every turn of an MCP-reaching node asks for the load before call_llm, so this
// budget is added to that turn's latency whenever a machine is slow. Prod, 2026-11-03/04:
// one overloaded machine never answered mcp.ensure_loaded at all, 960 times in
// under three hours, and every one of those turns waited the command's full two
// minutes before reaching the model.
var mcpLoadWaitBudget = 3 * time.Second

// mcpLoadFailureCooldown is how long a machine that failed to answer an MCP
// command is left alone. Inside it, a turn neither asks it to load nor waits on
// a listing; the tool list stays the last good one (tools.MCPToolSet).
var mcpLoadFailureCooldown = 2 * time.Minute

// mcpLoadCommandTimeout is how long the daemon is given to answer
// mcp.ensure_loaded. First-time npx/uvx installs are slow, and nobody waits on
// it beyond mcpLoadWaitBudget any more, so it stays generous.
const mcpLoadCommandTimeout = 120 * time.Second

// errMCPMachineUnresponsive is the per-project error a caller gets when its
// machine is in cooldown or still working through an earlier load.
var errMCPMachineUnresponsive = errors.New("the machine is not answering MCP commands in time; continuing without its MCP servers for now")

// mcpLoads makes MCP loading a background concern of a turn rather than a
// wait. It is shared by every runtime one binder hands out, so all turns
// against one machine see one in-flight load and one failure record.
//
// Keys: a machine is a daemonMCPRuntime.MCPListIdentity (user + daemon
// selector); a load is a machine plus a project path.
type mcpLoads struct {
	mu       sync.Mutex
	inflight map[string]*mcpLoad
	// failedAt records when a machine last failed to answer an MCP command.
	failedAt map[string]time.Time
	now      func() time.Time
}

type mcpLoad struct {
	machine string
	started time.Time
	done    chan struct{}
	result  *mcp.ProjectServerLoadResult
}

func newMCPLoads() *mcpLoads {
	return &mcpLoads{
		inflight: map[string]*mcpLoad{},
		failedAt: map[string]time.Time{},
		now:      time.Now,
	}
}

// unresponsive reports why machine should not be asked or waited on now: it
// failed an MCP command inside the cooldown, or a load it was given has already
// outrun the wait budget. Nil when it is worth asking.
func (l *mcpLoads) unresponsive(machine string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if at, ok := l.failedAt[machine]; ok {
		if now.Sub(at) < mcpLoadFailureCooldown {
			return fmt.Errorf("%w (failed %s ago)", errMCPMachineUnresponsive, now.Sub(at).Round(time.Second))
		}
		delete(l.failedAt, machine)
	}
	for _, load := range l.inflight {
		if load.machine == machine && now.Sub(load.started) > mcpLoadWaitBudget {
			return fmt.Errorf("%w (a load has been running for %s)", errMCPMachineUnresponsive, now.Sub(load.started).Round(time.Second))
		}
	}
	return nil
}

// recordFailure marks machine as not answering; recordAnswer clears it.
func (l *mcpLoads) recordFailure(machine string) {
	l.mu.Lock()
	l.failedAt[machine] = l.now()
	l.mu.Unlock()
}

func (l *mcpLoads) recordAnswer(machine string) {
	l.mu.Lock()
	delete(l.failedAt, machine)
	l.mu.Unlock()
}

// ensure returns the load result for (machine, projectPath) if it is ready
// within the wait budget, starting the load if none is in flight. Otherwise the
// load continues in the background and the result says so.
//
// run reports a failure to reach the machine as its error, separately from
// servers the machine tried and failed to start.
func (l *mcpLoads) ensure(ctx context.Context, machine, projectPath string, run func(context.Context) (*mcp.ProjectServerLoadResult, error)) *mcp.ProjectServerLoadResult {
	if err := l.unresponsive(machine); err != nil {
		return failedMCPLoad(projectPath, err)
	}

	key := machine + "\x00" + projectPath
	l.mu.Lock()
	load, ok := l.inflight[key]
	if !ok {
		load = &mcpLoad{machine: machine, started: l.now(), done: make(chan struct{})}
		l.inflight[key] = load
		// Detached from the turn: it must outlive the caller that gave up on
		// it, or the next turn starts the same load from scratch.
		bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), mcpLoadCommandTimeout+5*time.Second)
		go func() {
			defer cancel()
			result, err := run(bg)
			switch {
			case err != nil && mcpMachineAbsent(err):
				// Not connected costs no wait, and the cooldown would
				// outlive its reconnect: the next turn asks again.
				l.recordAnswer(machine)
				result = failedMCPLoad(projectPath, err)
			case err != nil:
				l.recordFailure(machine)
				logging.WarnContext(bg, "MCP load did not reach the machine; leaving it alone for the cooldown",
					"projectPath", projectPath, "cooldown", mcpLoadFailureCooldown,
					"elapsed", l.now().Sub(load.started), "error", err)
				result = failedMCPLoad(projectPath, err)
			default:
				l.recordAnswer(machine)
			}
			l.mu.Lock()
			load.result = result
			delete(l.inflight, key)
			l.mu.Unlock()
			close(load.done)
		}()
	}
	l.mu.Unlock()

	timer := time.NewTimer(mcpLoadWaitBudget)
	defer timer.Stop()
	select {
	case <-load.done:
		return load.result
	case <-timer.C:
		return failedMCPLoad(projectPath, fmt.Errorf("MCP servers still loading after %s; continuing without them for this turn", mcpLoadWaitBudget))
	case <-ctx.Done():
		return failedMCPLoad(projectPath, ctx.Err())
	}
}

// mcpMachineAbsent reports whether err proves the command never reached a
// connected machine: none resolved, it is starting, asleep or reconnecting, or
// NATS had no responder. Such a failure is immediate, so it is not what the
// cooldown guards against (a connected machine that holds every caller for the
// command's full timeout), and a cooldown recorded for it would keep the
// machine's MCP servers away for up to mcpLoadFailureCooldown after it came
// back.
func mcpMachineAbsent(err error) bool {
	return errors.Is(err, ErrNotDispatched) || IsDaemonPending(err) || IsNoDaemon(err) ||
		IsDaemonReconnecting(err) || errors.Is(err, errDaemonNotConnected)
}

func failedMCPLoad(projectPath string, err error) *mcp.ProjectServerLoadResult {
	return &mcp.ProjectServerLoadResult{
		LoadedServers: []string{},
		FailedServers: []string{projectPath},
		Errors:        map[string]error{projectPath: err},
	}
}
