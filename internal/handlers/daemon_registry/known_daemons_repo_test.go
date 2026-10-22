package daemon_registry_test

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/db"
	daemon_registry "github.com/reliant-labs/reliant/internal/handlers/daemon_registry"
)

// knownDaemonsRepo answers a daemon lookup the way db.Repo does: the row when
// it exists, the store's classified NotFound when it does not. The generated harness's stub answers
// (nil, nil) instead, which GetDaemon and ResumeDaemon read as found. No
// daemon has a fresh attachment, so every known daemon is offline. Every
// other method is unprogrammed: calling one fails with the mock's "Func not
// set" error, never a silent pass.
func knownDaemonsRepo(daemons map[string]*db.Daemon) *daemon_registry.MockRepository {
	return &daemon_registry.MockRepository{
		GetDaemonFunc: func(_ context.Context, id string) (*db.Daemon, error) {
			if d, ok := daemons[id]; ok {
				return d, nil
			}
			return nil, svcerr.NotFound("daemon")
		},
		ListFreshDaemonAttachmentsForUserFunc: func(context.Context, string, time.Duration) ([]*db.DaemonAttachment, error) {
			return nil, nil
		},
	}
}

// registryWithDaemons is a registry over knownDaemonsRepo holding "theirs"
// (another user's) and "offline" (the test caller's, not attached).
func registryWithDaemons() daemon_registry.Deps {
	return daemon_registry.Deps{Repo: knownDaemonsRepo(map[string]*db.Daemon{
		"theirs":  {ID: "theirs", UserID: "someone-else"},
		"offline": {ID: "offline", UserID: "test-user"},
	})}
}
