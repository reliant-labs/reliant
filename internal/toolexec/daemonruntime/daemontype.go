// Copyright (c) 2025 Reliant Labs

package daemonruntime

import (
	"os"
	"strings"
)

// Daemon type as self-reported at registration.
//
// This is not cosmetic. The server records it on the daemon row, and it gates
// real behaviour:
//
//   - the control plane refuses to resume anything that is not managed
//     ("cannot resume external daemon"), so a mislabelled workspace can never
//     be woken from a suspended state;
//   - the connector consent screen uses it to tell a disposable cloud sandbox
//     apart from someone's laptop, which is the difference between a
//     permissive grant being cheap and it being someone's SSH keys.
//
// It used to be hard-coded per transport: the dial-out path (runtime.go) said
// "local" and the dial-in path (server.go) said "cloud". That was a proxy for
// the real question — dial-IN meant the gateway reached in, which only
// happened for managed pods — and it stopped being true when managed
// workspaces moved to dialing OUT (control-plane's DAEMON_DIAL_OUT). Every
// managed workspace in a dial-out environment then registered as
// "local"/self_hosted, and became unresumable.
//
// So the transport no longer decides. The environment does, and the transport
// is only the fallback for a daemon that says nothing.
const (
	// DaemonTypeEnvVar lets the platform state what it is running. The
	// workspace operator sets it on managed pods; nothing sets it on a
	// user's own machine.
	DaemonTypeEnvVar = "RELIANT_DAEMON_TYPE"

	daemonTypeManaged    = "managed"
	daemonTypeSelfHosted = "self_hosted"
)

// resolveDaemonType returns the daemon_type to self-report at registration.
//
// fallback is what the caller's transport implies, used only when the
// environment is silent. The server normalizes the vocabulary
// ("cloud"/"managed", "local"/"self_hosted"), so either spelling is accepted
// here; unrecognized values fall through to the caller's default rather than
// being passed on as a third vocabulary the server would discard.
func resolveDaemonType(fallback string) string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(DaemonTypeEnvVar))) {
	case "cloud", "managed":
		return daemonTypeManaged
	case "local", "self_hosted", "self-hosted":
		return daemonTypeSelfHosted
	default:
		return fallback
	}
}
