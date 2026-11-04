package tools_daemon

import (
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/observability"
	"github.com/reliant-labs/reliant/pkg/daemonwire"
)

// daemonBuildFromRegister is the build a daemon reported when it registered.
// A daemon that predates the fields sends none of them, which reads as the
// zero value: an unknown build, stored and served as such.
func daemonBuildFromRegister(reg *reliantv1.DaemonRegister) db.DaemonBuild {
	return db.DaemonBuild{
		Version:        reg.GetVersion(),
		Commit:         reg.GetCommit(),
		CommitTimeUnix: reg.GetCommitTimeUnix(),
		ForgeVersion:   reg.GetForgeVersion(),
	}
}

// wireBuild is the build as the connected event carries it.
func wireBuild(b db.DaemonBuild) daemonwire.Build {
	return daemonwire.Build{
		Version:        b.Version,
		Commit:         b.Commit,
		CommitTimeUnix: b.CommitTimeUnix,
		ForgeVersion:   b.ForgeVersion,
	}
}

// fleetBuilds counts the daemons this replica holds a stream for, per reported
// build: reliant_daemon_fleet, read at scrape time.
func (s *Service) fleetBuilds() []observability.DaemonFleetBuild {
	s.mu.RLock()
	defer s.mu.RUnlock()
	type key struct{ version, commit string }
	counts := make(map[key]int)
	for _, conn := range s.connections {
		counts[key{conn.build.Version, conn.build.Commit}]++
	}
	out := make([]observability.DaemonFleetBuild, 0, len(counts))
	for k, n := range counts {
		out = append(out, observability.DaemonFleetBuild{Version: k.version, Commit: k.commit, Count: n})
	}
	return out
}
