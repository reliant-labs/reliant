// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"os"
	"path/filepath"
	"sync"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/mergequeue/gaterun"
	"github.com/reliant-labs/reliant/internal/mergequeue/gaterun/gatehost"
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
	image, vulnDBDir, trustedGoCache string
}

func configureGates(h Host) {
	gateHost.Lock()
	defer gateHost.Unlock()
	gateHost.image, gateHost.vulnDBDir, gateHost.trustedGoCache = h.GateImage, h.GateVulnDBDir, h.GateTrustedGoCache
}

// gateJobs is this daemon's gates. The isolated (sysbox) backend joins
// Runner.Isolated once the credsandbox gate backend is back on main; until
// then every gate that is not an allowed shadow gate is refused.
var gateJobs = sync.OnceValue(func() *gatehost.Jobs {
	gateHost.Lock()
	image, vulnDB, trustedGoCache := gateHost.image, gateHost.vulnDBDir, gateHost.trustedGoCache
	gateHost.Unlock()
	return &gatehost.Jobs{Runner: &gatehost.Runner{
		NotIsolated: runcgate.New(),
		Fetcher: gatehost.GitFetcher{Env: func() []string {
			return gitCredentialEnv(os.Environ()) //nolint:forbidigo // the fetch child's inherited environment plus the per-invocation credential helper; not configuration
		}},
		WorkRoot:  filepath.Join(config.GetAppDataDir(), "gates"),
		Image:     image,
		VulnDBDir: vulnDB,
		// Checked again before every gate, so a directory that appears
		// after the daemon started is mounted with no restart.
		TrustedGoCacheDir: gatehost.TrustedGoCacheDir(trustedGoCache),
		Limits:            gatehost.DefaultLimits(),
	}}
})

func handleGateCommand(command string) CommandHandler {
	return func(ctx context.Context, payload []byte) ([]byte, error) {
		return gateJobs().Handle(ctx, command, payload)
	}
}
