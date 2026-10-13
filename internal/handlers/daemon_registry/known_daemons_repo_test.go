package daemon_registry_test

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	daemon_registry "github.com/reliant-labs/reliant/internal/handlers/daemon_registry"
)

// knownDaemonsRepo answers a daemon lookup the way db.Repo does: the row when
// it exists, an error when it does not. The generated harness's stub answers
// (nil, nil) instead, which GetDaemon and ResumeDaemon read as found. No
// daemon has a fresh attachment, so every known daemon is offline. Every
// other method is unreachable from the rows that use it: the embedded nil
// Repository makes calling one a panic, never a silent pass.
type knownDaemonsRepo struct {
	daemon_registry.Repository
	daemons map[string]*db.Daemon
}

func (r knownDaemonsRepo) GetDaemon(_ context.Context, id string) (*db.Daemon, error) {
	if d, ok := r.daemons[id]; ok {
		return d, nil
	}
	return nil, fmt.Errorf("daemon not found: %s", id)
}

func (knownDaemonsRepo) ListFreshDaemonAttachmentsForUser(context.Context, string, time.Duration) ([]*db.DaemonAttachment, error) {
	return nil, nil
}

// registryWithDaemons is a registry over knownDaemonsRepo holding "theirs"
// (another user's) and "offline" (the test caller's, not attached).
func registryWithDaemons() daemon_registry.Deps {
	return daemon_registry.Deps{Repo: knownDaemonsRepo{daemons: map[string]*db.Daemon{
		"theirs":  {ID: "theirs", UserID: "someone-else"},
		"offline": {ID: "offline", UserID: "test-user"},
	}}}
}
