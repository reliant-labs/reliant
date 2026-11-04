package hostinfra

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Status is a READ of one declared instance: is it running, where, and
// which build.
//
// It exists as a separate verb rather than a return value of Start
// because the two answer different questions. Start reports whether a
// TRANSITION succeeded, and it may create a data directory, download a
// binary and run a bootstrap to get there. Status never does any of
// that, and a caller may run it against an instance that was never
// started — which is exactly what `forge env observe` needs, and exactly
// what calling Start to find out would get wrong (it would start it).
type Status struct {
	// Running reports whether a server this project owns is serving.
	//
	// Ownership is load-bearing and is why this cannot be a port probe.
	// On a shared dev box the declared port may be held by ANOTHER
	// project's postgres or a system one, and reporting that as "our
	// instance is up" is the same adoption mistake Start refuses to
	// make — it would show green for a database this project has never
	// written to.
	Running bool

	// Foreign reports the specific case of "something is listening on
	// the declared port, and it is not ours". Distinct from !Running
	// with nothing there: one means "start it", the other means "a
	// human must free the port", and collapsing them loses the only
	// actionable half.
	Foreign bool

	// Port is where the instance is ACTUALLY serving, which is not
	// necessarily the declared port: an instance started before the
	// declaration moved is still running on the old one. Zero when not
	// running, and -1 when it is alive but has not published a port yet
	// (mid-boot).
	Port int

	// Version is the engine build the instance is running, empty when
	// unknown. For postgres it is read from the cluster's own PG_VERSION
	// file — the server's record of what initdb created, rather than the
	// declaration's record of what was asked for. Those disagree exactly
	// when someone changed the declared version without recreating the
	// data directory, and that disagreement is the thing worth seeing.
	Version string

	// Ready reports whether the instance ANSWERS, as opposed to merely
	// having a live process. The two come apart during boot and stay
	// apart when a server wedges, and that gap is the whole difference
	// between "a process exists" and "the thing the app is about to dial
	// will respond" — the distinction Start's doc-comment already draws
	// for the write path.
	//
	// Meaningless when Running is false.
	Ready bool

	// DataDir is the resolved data directory, so a report can name the
	// thing on disk it is talking about.
	DataDir string
}

// StatusOf reports the current state of one declared instance, without
// starting, stopping or modifying anything.
func StatusOf(ctx context.Context, projectDir string, spec Spec) (Status, error) {
	dataDir := spec.dataDir(projectDir)
	switch spec.Engine {
	case EnginePostgres:
		return statusPostgres(ctx, spec, dataDir), nil
	case EngineZitadel:
		return statusZitadel(ctx, spec, dataDir), nil
	case EngineNATS:
		return natsEngine.status(ctx, spec, dataDir), nil
	case EngineTemporal:
		return temporalEngine.status(ctx, spec, dataDir), nil
	default:
		return Status{DataDir: dataDir}, unsupportedEngine(spec)
	}
}

// statusPostgres asks the DATA DIRECTORY first and the port second, in
// the same order and for the same reason startPostgres does: the data
// directory identifies this instance, the port is only where it currently
// answers.
func statusPostgres(ctx context.Context, spec Spec, dataDir string) Status {
	st := Status{DataDir: dataDir, Version: pgVersion(dataDir)}
	if port, alive := runningPort(dataDir); alive {
		st.Running = true
		st.Port = port
		// A live postmaster is not yet a server that answers: postgres
		// spends its recovery phase with the pid file written and
		// connections refused. identifyHolder settles it by actually
		// connecting, and returns holderOurs only when the server it
		// reached reports OUR data directory.
		st.Ready = identifyHolder(ctx, spec, dataDir) == holderOurs
		return st
	}
	// Nothing of ours. Something else on the port is worth reporting, and
	// identifyHolder draws the ours/foreign line from the running
	// server's own data directory rather than from the connection.
	switch identifyHolder(ctx, spec, dataDir) {
	case holderOurs:
		st.Running = true
		st.Ready = true
		st.Port = spec.Port
	case holderForeign:
		st.Foreign = true
	}
	return st
}

// statusZitadel reads the pidfile forge writes, then confirms the process
// is alive and the instance answers its own readiness endpoint.
func statusZitadel(ctx context.Context, spec Spec, dataDir string) Status {
	st := Status{DataDir: dataDir, Version: ZitadelVersion}
	pid, port, recorded := readZitadelPID(dataDir)
	if recorded && processIsAlive(pid) {
		st.Running = true
		st.Port = port
		// Alive but not ready is a real and common state — it is
		// mid-boot, running its declarative bootstrap — and
		// identifyZitadelHolder deliberately treats it as ours. An
		// observation must still distinguish it, because "the IdP is up"
		// and "the IdP will answer the token request you are about to
		// make" are the claim a caller acts on.
		st.Ready = zitadelReadyAt(ctx, port)
		return st
	}
	// No live instance of ours. A listening port now belongs to someone
	// else — the same refusal-to-adopt startZitadel makes.
	if portListening(spec.Port) {
		st.Foreign = true
	}
	return st
}

// pgVersion reads the major version out of the cluster's PG_VERSION file
// — the file initdb writes and the server reads on every boot, so it is
// the cluster's own record rather than forge's.
//
// Empty when there is no cluster yet, which is not an error: an instance
// that has never been started has no version to report, and saying so is
// more honest than echoing back the declared one.
func pgVersion(dataDir string) string {
	b, err := os.ReadFile(filepath.Join(dataDir, "data", "PG_VERSION")) // #nosec G304 -- path derived from the project's own data dir
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// zitadelReadyAt is the readiness probe Status uses. Kept separate from
// the boot-time wait: this one asks once with a short timeout, because an
// observation must return promptly even when the target is wedged.
func zitadelReadyAt(ctx context.Context, port int) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	return zitadelReady(ctx, client, fmt.Sprintf("http://localhost:%d/debug/ready", port))
}
