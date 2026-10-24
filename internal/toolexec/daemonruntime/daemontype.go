// Copyright (c) 2025 Reliant Labs

package daemonruntime

import (
	"runtime"

	"github.com/reliant-labs/reliant/internal/llm/tools"
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
// So the transport no longer decides. The platform does (Host.DaemonType), and
// the transport is only the fallback for a daemon that says nothing.
const (
	daemonTypeManaged    = "managed"
	daemonTypeSelfHosted = "self_hosted"
)

// Host is what the platform declares about where this daemon runs. The
// workspace operator stamps it on the pods it creates, through `reliant daemon
// start`'s config (DaemonConfig, proto/reliant/config/v1/daemon_config.proto); a
// daemon on a user's machine — or the in-process runtime `reliant open`
// starts — leaves it zero.
type Host struct {
	// DaemonType is "managed", "self_hosted", or "" when the platform said
	// nothing. The config loader has already rejected any other value.
	DaemonType string
	// WorkingDir is the directory the daemon serves; "" means the process's
	// working directory.
	WorkingDir string
	// RuntimeType is the container runtime ("kata", "gvisor") the daemon runs
	// under, advertised at registration; "" off a pod.
	RuntimeType string
	// FrontendPort is the preview forwarder's pod-facing port. Non-zero is
	// what starts the forwarder outside server mode; 0 means unset.
	FrontendPort int
	// ProjectPath is the project announced at connect; "" means WorkingDir.
	ProjectPath string
	// AllowPrivateFetch is the user's opt-in to let fetch reach this
	// machine's localhost and private network (FetchAllowPrivateSetting).
	// Ignored on a managed daemon.
	AllowPrivateFetch bool
}

// Managed reports whether the PLATFORM has declared this process a managed
// daemon — a workspace pod the operator created, not a machine someone is
// sitting at.
//
// Unlike resolveDaemonType there is no fallback: a daemon that says nothing is
// not managed. Saying nothing is the normal case for a laptop, and guessing
// "managed" from a transport is the mistake this file's history documents.
func (h Host) Managed() bool { return h.DaemonType == daemonTypeManaged }

// resolveDaemonType returns the daemon_type to self-report at registration:
// what the platform declared, else fallback — what the caller's transport
// implies. The server normalizes the transport vocabulary ("cloud"/"local")
// onto the platform's ("managed"/"self_hosted").
func resolveDaemonType(declared, fallback string) string {
	if declared != "" {
		return declared
	}
	return fallback
}

// FetchAllowPrivateSetting is the setting that lets fetch reach this machine's
// own localhost and private network. It is named in fetch's refusal, so the
// user can choose to opt in.
const FetchAllowPrivateSetting = "RELIANT_FETCH_ALLOW_PRIVATE_NETWORK"

// fetchNetwork is where fetch may connect from this daemon (tools.FetchNetwork
// documents the tiers). Link-local and cloud-metadata destinations are refused
// everywhere; loopback and private ranges are refused unless the user opted in
// (Host.AllowPrivateFetch) on a machine that is theirs.
//
// A managed workspace pod is the platform's network, not the user's, so it is
// strict whatever the setting says and offers no opt-in; its namespace
// NetworkPolicy (public 80/443 only) sits behind this as the second layer.
func fetchNetwork(host Host) tools.FetchNetwork {
	if host.Managed() {
		return tools.FetchNetwork{}
	}
	return tools.FetchNetwork{AllowPrivate: host.AllowPrivateFetch, OptInSetting: FetchAllowPrivateSetting}
}

// newDaemonToolsFactory builds the registry this daemon executes tools from.
// This process IS the machine that runs shell commands, so its own GOOS is
// the right shell platform here, unlike on the server.
func newDaemonToolsFactory(host Host) *tools.ToolsFactory {
	network := fetchNetwork(host)
	return tools.NewToolsFactory(&tools.ToolsOptions{
		ShellPlatform: tools.ShellPlatformFromGOOS(runtime.GOOS),
		// A daemon is the user's own machine, where localhost and LAN
		// addresses are what its other web tools mean. fetch does not take
		// this: it has its own, stricter policy below.
		AllowPrivateEgress: true,
		FetchNetwork:       &network,
	})
}
