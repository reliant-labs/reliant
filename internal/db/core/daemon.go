package core

import "time"

// Daemon represents a persisted tools daemon identity registration.
// Lifecycle state (connection, heartbeat) lives in daemon_attachment.
// Capabilities and ProjectPaths are JSON-encoded arrays.
type Daemon struct {
	ID           string
	UserID       string
	Hostname     *string
	Platform     *string
	Capabilities *string
	ProjectPaths *string
	// DaemonType is the type of daemon: "managed" (cloud-hosted) or
	// "self_hosted" (user-run local daemon). Empty/nil if undetermined.
	DaemonType *string
	// Name is the name the owner gave the machine, mirrored from the control
	// plane (which owns it). Empty until the control plane has reported one —
	// a daemon that registered here before any control-plane event, or a
	// build with no control plane at all. Display falls back to Hostname.
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time

	// Lifecycle state mirrored from the control plane over
	// daemon.v1.state.<id>.lifecycle. This service does NOT own these — the
	// Workspace CR does, and only the control-plane operator watches it. They
	// are stored so the registry can serve one list carrying both liveness
	// (derived from daemon_attachment, which this service does own) and
	// lifecycle. All nil/zero for self-hosted daemons, permanently.
	//
	// LifecyclePhase holds the public vocabulary (daemonwire.LifecyclePhase
	// values), never a raw Kubernetes phase.
	LifecyclePhase *string
	Size           *string
	// LastStatusMessage is shown verbatim in the UI.
	LastStatusMessage string
	// LastStatusChangedAt is the ordering key for lifecycle writes: an event
	// older than this is dropped, which is what makes out-of-order delivery
	// safe on a newest-wins stream with no redelivery.
	LastStatusChangedAt *time.Time
	LastOOMKilledAt     *time.Time
	OOMKillCount        int32

	// Labels are the machine's durable labels, set by its owner. They decide
	// machine-pool membership (dev-docs/design/machine-pools.md) and, unlike
	// the labels a daemon reports when it connects, hold while it sleeps.
	Labels map[string]string

	// IsDefault: the owner chose this machine as their default
	// (SetDefaultDaemon). Work that names no machine runs here first; at most
	// one of a user's daemons carries it.
	IsDefault bool
}

// DaemonLifecycleUpdate is one control-plane lifecycle observation, as applied
// to a daemons row by ApplyDaemonLifecycle.
//
// ChangedAt is required and load-bearing: it is the ordering key the
// conditional write compares against, not merely a timestamp to store.
type DaemonLifecycleUpdate struct {
	DaemonID        string
	Phase           string
	Size            string
	StatusMessage   string
	ChangedAt       time.Time
	LastOOMKilledAt *time.Time
	OOMKillCount    int32
}

type DaemonAttachmentSource string

const (
	DaemonAttachmentSourceInbound  DaemonAttachmentSource = "inbound"
	DaemonAttachmentSourceOutbound DaemonAttachmentSource = "outbound"
)

type DaemonAttachment struct {
	DaemonID           string
	UserID             string
	Source             DaemonAttachmentSource
	PodIP              *string
	PodPort            *int
	AttachedAt         time.Time
	LastStreamActivity time.Time
	// Workspace memory telemetry from the daemon heartbeat (cloud daemons in
	// a cgroup-limited pod). MemoryLimitBytes == 0 means "not reported"
	// (local daemons without cgroup accounting). MemoryUsedBytes is the
	// cgroup's unreclaimable memory (memory.current minus the file cache and
	// reclaimable slab), the basis of MemoryPressure, the daemon's
	// hysteresis-smoothed pressure bit (asserts at >= 85% of the limit or on
	// sustained PSI stalls, clears below 75%).
	MemoryUsedBytes  int64
	MemoryLimitBytes int64
	MemoryPressure   bool
	// DetectedPorts are the loopback/wildcard LISTEN ports the daemon
	// reported in its heartbeat (what the in-pod preview forwarder can
	// reach). JSON-encoded in the detected_ports column. Nil/empty when
	// nothing is listening or the daemon doesn't report (local daemons).
	DetectedPorts []uint32
	// Build is the build the daemon reported when its stream registered.
	// The zero value is "unknown build".
	Build DaemonBuild
}

// DaemonBuild is the build a daemon reported in DaemonRegister. Every field is
// empty/zero for a daemon that predates the report or an unstamped build,
// which is an unknown build, not an error. CommitTimeUnix is the ordering key
// (0 = unordered); Version is display only.
type DaemonBuild struct {
	Version        string
	Commit         string
	CommitTimeUnix int64
	ForgeVersion   string
}

// Known reports whether the daemon identified its build at all.
func (b DaemonBuild) Known() bool {
	return b.Version != "" || b.Commit != ""
}

// DaemonIdentity is what the control plane knows about a daemon's existence.
type DaemonIdentity struct {
	DaemonID   string
	UserID     string
	Name       string
	DaemonType string
	// CreatedAt is the control plane's creation time. Zero means "now".
	CreatedAt time.Time
}
