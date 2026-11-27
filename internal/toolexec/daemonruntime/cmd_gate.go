// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"os"
	"path/filepath"
	"sync"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/mergequeue/gaterun"
	"github.com/reliant-labs/reliant/internal/mergequeue/gaterun/runcgate"
)

// The merge queue's gate commands (internal/mergequeue/gaterun). The server
// sends them only to a member of a repository's gate pool, and nothing sends
// them until the queue's scheduler exists. A machine runs a gate only in an
// isolated backend, or, for a shadow gate the server allowed, in runc.
func init() {
	for _, c := range []string{gaterun.CommandStart, gaterun.CommandStatus, gaterun.CommandCancel} {
		RegisterCommand(c, handleGateCommand(c))
	}
}

// gateHost is the machine's gate configuration (DaemonConfig gate_*), set
// when the daemon client is built and read when the first gate command
// arrives.
var gateHost struct {
	sync.Mutex
	image, vulnDBDir string
}

func configureGates(h Host) {
	gateHost.Lock()
	defer gateHost.Unlock()
	gateHost.image, gateHost.vulnDBDir = h.GateImage, h.GateVulnDBDir
}

// gateJobs is this daemon's gates. The isolated (sysbox) backend joins
// Runner.Isolated once the credsandbox gate backend is back on main; until
// then every gate that is not an allowed shadow gate is refused.
var gateJobs = sync.OnceValue(func() *gaterun.Jobs {
	gateHost.Lock()
	image, vulnDB := gateHost.image, gateHost.vulnDBDir
	gateHost.Unlock()
	return &gaterun.Jobs{Runner: &gaterun.Runner{
		NotIsolated: runcgate.New(),
		Fetcher: gaterun.GitFetcher{Env: func() []string {
			return gitCredentialEnv(os.Environ()) //nolint:forbidigo // the fetch child's inherited environment plus the per-invocation credential helper; not configuration
		}},
		WorkRoot:  filepath.Join(config.GetAppDataDir(), "gates"),
		Image:     image,
		VulnDBDir: vulnDB,
		Limits:    gaterun.DefaultLimits(),
	}}
})

func handleGateCommand(command string) CommandHandler {
	return func(ctx context.Context, payload []byte) ([]byte, error) {
		return gateJobs().Handle(ctx, command, payload)
	}
}
