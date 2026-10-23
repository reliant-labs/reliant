package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WorkloadKind is HOW a workload runs. It never says what language the
// workload is in, what code is inside it, or how it was built.
//
// The set and its meaning are the ones fw.Workload already used
// (kcl/workloads/capabilities.k), so one vocabulary covers every runtime:
//
//   - service  — a long-running Deployment with a ClusterIP Service.
//   - worker   — a long-running Deployment that nothing dials by name.
//   - job      — a one-shot that runs to completion, either gating other
//     workloads (Before) or as a standalone Job placed by DeployPhase.
//   - cron     — a CronJob on Schedule.
//   - operator — a controller-runtime manager: cluster RBAC, CRDs, leader
//     election.
//   - tool     — built into the image and NEVER scheduled. A tool renders no
//     manifest, so every runtime capability is refused for it.
//
// +kubebuilder:validation:Enum=service;worker;job;cron;operator;tool
type WorkloadKind string

const (
	KindService  WorkloadKind = "service"
	KindWorker   WorkloadKind = "worker"
	KindJob      WorkloadKind = "job"
	KindCron     WorkloadKind = "cron"
	KindOperator WorkloadKind = "operator"
	KindTool     WorkloadKind = "tool"
)

// DeployPhase places a standalone job (kind job, no Before) in a deploy.
//
// pre-rollout must COMPLETE before any other workload is applied, and a
// failure aborts the deploy with nothing changed. post-rollout rides with
// the workloads and is awaited after they roll out. A gating job (Before
// set) runs as an initContainer, so the pod ordering already IS its phase,
// and the field is refused there.
//
// +kubebuilder:validation:Enum=pre-rollout;post-rollout
type DeployPhase string

const (
	DeployPhasePreRollout  DeployPhase = "pre-rollout"
	DeployPhasePostRollout DeployPhase = "post-rollout"
)

// DeploymentStrategy is how a Deployment replaces its pods on a rollout.
//
// RollingUpdate starts the new pods before the old ones stop, so the
// workload stays available and two versions of it run at once. Recreate
// stops every old pod first, so exactly one version ever runs, at the cost
// of a gap with none.
//
// +kubebuilder:validation:Enum=RollingUpdate;Recreate
type DeploymentStrategy string

const (
	StrategyRollingUpdate DeploymentStrategy = "RollingUpdate"
	StrategyRecreate      DeploymentStrategy = "Recreate"
)

// BeforeAll is the BROADCAST entry for Before: gate every workload in the
// environment without naming any of them. Schema migration is the case it
// exists for. An enumerated list of dependents goes stale the day a workload
// is added, and the new workload then serves traffic against a schema it
// does not have. A wildcard has nothing to forget.
const BeforeAll = "*"

// PortProtocol is a port's transport. Lowercase because it is an authoring
// value (fw.Port always spelled it so), and the renderer upper-cases it for
// Kubernetes.
//
// +kubebuilder:validation:Enum=tcp;udp
type PortProtocol string

const (
	ProtocolTCP PortProtocol = "tcp"
	ProtocolUDP PortProtocol = "udp"
)

// WorkloadSpec is everything that runs, as ONE declaration (ADR 0002).
//
// It is the wire contract for every runtime. The CLI lowers an authored
// fw.Workload to this type and renders it for a cluster the author operates,
// and the hosted control plane receives it as a forge.dev Workload CR,
// validates it under ProfileRestricted, and renders it itself. The KCL
// fw.Workload is GENERATED from this type, so the author-facing field set
// and the API server's pruning schema cannot disagree.
//
// # ONE TYPE, TWO PROFILES
//
// The type describes everything a workload can be on a cluster the author
// operates. What a destination ACCEPTS is a Profile, checked by Validate:
// ProfileFull allows every field, and ProfileRestricted (hosted, shared
// nodes) allows the subset in FieldProfiles and KindProfiles. That keeps
// hosted and self-hosted as one model with one renderer, rather than a
// hosted tier that has to be outgrown.
//
// # WHAT IS NOT HERE
//
// Name is metadata.name. Namespace and cluster describe the target, not the
// app. Build and runtime are authoring facts that the lowering has already
// consumed by the time a spec exists: the build becomes Image, and the
// runtime decides where the spec goes. Hosted identity is labels. Observed
// facts are status.
type WorkloadSpec struct {
	// Kind is how the workload runs. Default service.
	// +optional
	// +kubebuilder:default=service
	Kind WorkloadKind `json:"kind,omitempty"`

	// Image is the container image, resolved by the lowering (the build
	// output, or an author-named third-party image). ProfileRestricted
	// also requires it to be registry-qualified and pinned, because the
	// platform pulls it from a registry the hosted user does not control. A
	// cluster the author operates may run a locally imported bare name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=500
	Image string `json:"image"`

	// Command overrides the image's entrypoint. Rare: a forge-built
	// project binary is selected by Args, not by replacing the entrypoint.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=4096
	Command []string `json:"command,omitempty"`

	// Args are the entrypoint's arguments, typically the project binary's
	// subcommand. The host runtime derives its command from the build plus
	// these same args, so they are stated once for every runtime.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=4096
	Args []string `json:"args,omitempty"`

	// Replicas is the pod count for a service, worker or operator. Default
	// 1. A batch kind's concurrency belongs to its Job or CronJob, and a
	// tool is never scheduled, so neither may set more than one.
	// +optional
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	Replicas int32 `json:"replicas,omitempty"`

	// Resources is the compute request and limit. Omitted means the entry
	// shape (250m / 1 GiB).
	// +optional
	Resources Resources `json:"resources,omitempty"`

	// Env is the container environment.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=256
	Env []EnvVar `json:"env,omitempty"`

	// Ports are the ports the container listens on. A service's ports are
	// all on its ClusterIP Service. An operator's and a worker's are
	// container ports only, because nothing resolves them by name.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=16
	Ports []Port `json:"ports,omitempty"`

	// Probes are the readiness and liveness probes. Omitted means the
	// policy in EffectiveProbes: TCP on the first port for a service that
	// declares ports, and none for anything else. The lowering writes HTTP
	// /readyz + /healthz explicitly for a service forge built, and for a
	// forge-built operator that declares ports.
	// +optional
	Probes *Probes `json:"probes,omitempty"`

	// StorageGiB provisions a ReadWriteOnce PersistentVolumeClaim of this
	// many GiB, mounted at /data. Zero means stateless.
	//
	// A ReadWriteOnce volume can be mounted by one pod at a time, so a
	// workload with storage runs exactly one replica with a Recreate
	// rollout. A rolling update would start the new pod while the old one
	// still holds the volume, and wedge. A bound PVC keeps existing, and on
	// hosted keeps billing, while the workload is scaled to zero. That is
	// deliberate: it is what the cloud itself charges.
	// +optional
	// +kubebuilder:validation:Minimum=0
	StorageGiB int32 `json:"storageGiB,omitempty"`

	// Schedule is a cron's schedule: a 5-field cron expression or one of
	// @hourly, @daily, @weekly, @monthly, @yearly.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	Schedule string `json:"schedule,omitempty"`

	// Before names the workloads that must not start until this job exits
	// 0, or [BeforeAll] for every workload in the environment.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	Before []string `json:"before,omitempty"`

	// DeployPhase places a standalone job in the deploy. Unset means
	// pre-rollout (EffectiveDeployPhase). There is no default marker: a
	// static default would be stamped on every kind, and Validate would
	// then refuse the CR it was handed.
	// +optional
	DeployPhase DeployPhase `json:"deployPhase,omitempty"`

	// NamespacedRBAC grants a Role + RoleBinding in the workload's own
	// namespace, in addition to forge's default config-read rules. The Role
	// is rendered whenever the workload has ANY RBAC (either tier, or kind
	// operator); with none, there is no Role at all and no mounted token.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	NamespacedRBAC []PolicyRule `json:"namespacedRBAC,omitempty"`

	// ClusterRBAC grants a ClusterRole + ClusterRoleBinding. Any scheduled
	// kind may declare it (Full profile only): an operator watches its CRDs
	// cluster-wide, and a service may legitimately resolve resources across
	// namespaces (a proxy routing to per-workspace objects) without being a
	// controller. It ADDS to the namespaced Role rather than replacing it:
	// the ClusterRole carries only cluster-scoped intent (these rules, and an
	// operator's CRD-derived rules), while the config-read defaults and an
	// operator's leader-election lease stay in the namespaced Role. Both
	// bind the one ServiceAccount, so NamespacedRBAC and ClusterRBAC may be
	// declared together, each at its own scope.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	ClusterRBAC []PolicyRule `json:"clusterRBAC,omitempty"`

	// CRDs are the custom resource kinds an operator's manager owns. May be
	// empty: an operator that owns none yet, or reconciles built-in kinds.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:Pattern=`^[A-Z][A-Za-z0-9]*$`
	CRDs []string `json:"crds,omitempty"`

	// Group is the API group of an operator's CRDs.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	Group string `json:"group,omitempty"`

	// Version is the API version of an operator's CRDs.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^v[0-9]+((alpha|beta)[0-9]+)?$`
	Version string `json:"version,omitempty"`

	// LeaderElection turns an operator's leader election on or off. Unset
	// means on (EffectiveLeaderElection). A pointer, with no default
	// marker, because the default is true only for an operator. A static
	// default would stamp it onto every kind.
	// +optional
	LeaderElection *bool `json:"leaderElection,omitempty"`

	// ServiceAccountAnnotations are stamped on the workload's generated
	// ServiceAccount and on no other object. Cloud workload identity is the
	// case it exists for: GKE reads iam.gke.io/gcp-service-account, and EKS
	// reads eks.amazonaws.com/role-arn, off the KSA the pod runs as.
	// +optional
	// +kubebuilder:validation:MaxProperties=32
	ServiceAccountAnnotations map[string]string `json:"serviceAccountAnnotations,omitempty"`

	// --- Pod-level escape hatches: FULL PROFILE ONLY (pod_types.go) ---

	// Sidecars are additional containers in the workload's pod: a Cloud SQL
	// Auth Proxy, a registry forwarder. They get the same hardened
	// securityContext as the main container.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=8
	Sidecars []Container `json:"sidecars,omitempty"`

	// Volumes are extra pod volumes, each mounted into the main container.
	// For storage forge provisions, use StorageGiB.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=32
	Volumes []Volume `json:"volumes,omitempty"`

	// ServiceAccount names an EXISTING ServiceAccount to run as, instead of
	// the one forge generates. The case is cloud workload identity bound to
	// a ServiceAccount that other infrastructure owns. forge then renders no
	// ServiceAccount, Role or binding for the workload, so this is mutually
	// exclusive with NamespacedRBAC, ClusterRBAC and
	// ServiceAccountAnnotations: each of those describes the generated
	// ServiceAccount, which no longer exists.
	//
	// The renderer leaves the pod's automountServiceAccountToken UNSET when
	// this is set, so the named ServiceAccount's own
	// automountServiceAccountToken decides and its owner controls it. forge
	// cannot know whether that identity calls the Kubernetes API, and a
	// pod-level false would silently disable one that does. (A generated
	// ServiceAccount's token mounts only when the workload has RBAC.)
	// +optional
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	ServiceAccount string `json:"serviceAccount,omitempty"`

	// NodeSelector constrains the pods to nodes with these labels.
	// +optional
	// +kubebuilder:validation:MaxProperties=32
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Tolerations let the pods schedule onto tainted nodes.
	// +optional
	// +kubebuilder:validation:MaxItems=32
	Tolerations []Toleration `json:"tolerations,omitempty"`

	// PodAnnotations are stamped on the pod template.
	// +optional
	// +kubebuilder:validation:MaxProperties=64
	PodAnnotations map[string]string `json:"podAnnotations,omitempty"`

	// Strategy is how the Deployment replaces its pods on a rollout.
	//
	// Unset is Kubernetes' default, RollingUpdate, which surges a new pod
	// before the old one stops — so a workload whose replica count is a
	// CORRECTNESS bound rather than a capacity choice (a sweeper that is not
	// idempotent under concurrency) runs twice for the length of a rollout,
	// which `replicas: 1` looks like it prevents and does not. Declare
	// Recreate there and accept the gap with no pod.
	//
	// RollingUpdate alongside storageGiB is REFUSED: a ReadWriteOnce volume
	// is mounted by one pod at a time, so the surge pod can never start and
	// the rollout wedges. Storage alone already renders Recreate.
	// +optional
	Strategy DeploymentStrategy `json:"strategy,omitempty"`

	// PriorityClassName names a PriorityClass that ranks these pods against
	// everything else competing for a node: the scheduler places a higher
	// priority pod first, and a preempting class evicts lower priority pods
	// to make room. The case is a workload sharing nodes with pods that
	// already carry a class — without one it is priority 0 and loses every
	// contest, so a rollout's surge pod can be preempted repeatedly and take
	// minutes to land.
	//
	// The class is CLUSTER-SCOPED and must already exist: Kubernetes REFUSES
	// a pod naming a PriorityClass the cluster does not have, so the class
	// has to be applied to every cluster this workload lands on, before the
	// pods that name it. Unset leaves the pod at the cluster's default
	// (globalDefault, else 0). Full profile only.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	PriorityClassName string `json:"priorityClassName,omitempty"`

	// EnableServiceLinks opts the pods IN to Kubernetes' service links: an
	// env var per Service in the namespace (<NAME>_PORT=tcp://ip:port,
	// <NAME>_SERVICE_HOST, <NAME>_SERVICE_PORT, ...). Unset (false) renders
	// pod.enableServiceLinks: false, because those names are not namespaced
	// to anything: a Service called `temporal` injects TEMPORAL_PORT, which
	// a typed config loader then parses as a port number and the process
	// CrashLoops at boot (reliant's daemon-gateway, 2026-10-21; Zitadel's
	// ZITADEL_PORT before it). Resolve a Service through DNS
	// (<svc>.<namespace>.svc) or an explicit env var instead. Set this only
	// for an image that reads the injected variables. Full profile only.
	// +optional
	EnableServiceLinks bool `json:"enableServiceLinks,omitempty"`

	// TerminationGracePeriodSeconds overrides the drain-derived grace
	// period (PRE_STOP_DELAY + SHUTDOWN_TIMEOUT + 5 from the workload's env).
	// For a process whose shutdown budget is not expressed in those env
	// vars: a Temporal worker finishing in-flight activities needs 60s.
	// Full profile only. 0 is honoured (SIGKILL immediately).
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=3600
	TerminationGracePeriodSeconds *int32 `json:"terminationGracePeriodSeconds,omitempty"`

	// ActiveDeadlineSeconds bounds a job or cron run: Kubernetes fails the
	// Job (Job.spec.activeDeadlineSeconds) once it has run this long, and
	// the host runtime kills the process. A one-shot that can hang would
	// otherwise block a deploy or `forge env up` forever. Allowed under
	// every profile: a timeout only ever shortens what runs.
	// +optional
	// +kubebuilder:validation:Minimum=1
	ActiveDeadlineSeconds *int64 `json:"activeDeadlineSeconds,omitempty"`

	// SecurityContext loosens forge's pod identity defaults for an image
	// that needs it: a Next.js standalone image that runs as uid 1000 and
	// writes into its own tree. Full profile only. It is deliberately
	// NOT a Kubernetes securityContext passthrough: no privileged, no
	// capabilities, no host namespaces, and runAsNonRoot stays forced, so
	// every pod is still Pod Security `restricted`.
	// +optional
	SecurityContext *PodSecurity `json:"securityContext,omitempty"`
}

// PodSecurity is the closed set of pod-identity knobs forge lets a workload
// change. Each unset field keeps forge's default (uid/gid 65532, fsGroup only
// when a volume needs it, a read-only root filesystem). The overrides apply to
// the pod AND to every container forge renders in it (main, sidecar,
// gating initContainer), so the pod has one identity.
type PodSecurity struct {
	// RunAsUser is the uid every container runs as. Must not be 0:
	// runAsNonRoot is always true.
	// +optional
	// +kubebuilder:validation:Minimum=1
	RunAsUser *int64 `json:"runAsUser,omitempty"`
	// RunAsGroup is the primary gid. Must not be 0.
	// +optional
	// +kubebuilder:validation:Minimum=1
	RunAsGroup *int64 `json:"runAsGroup,omitempty"`
	// FSGroup owns mounted volumes. Must not be 0.
	// +optional
	// +kubebuilder:validation:Minimum=1
	FSGroup *int64 `json:"fsGroup,omitempty"`
	// ReadOnlyRootFilesystem false gives the containers a writable root
	// filesystem. Default true.
	// +optional
	ReadOnlyRootFilesystem *bool `json:"readOnlyRootFilesystem,omitempty"`
}

// Port is one named port a workload listens on.
type Port struct {
	// Name is the port's name, referenced by Services and probes. An
	// IANA_SVC_NAME, as Kubernetes requires of a container port name: at
	// most 15 characters, lowercase alphanumerics and '-'.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=15
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// Port is the container port number.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port"`

	// Protocol is the transport. Default tcp.
	// +optional
	// +kubebuilder:default=tcp
	Protocol PortProtocol `json:"protocol,omitempty"`

	// AppProtocol is the application protocol the port speaks (h2c, grpc,
	// http, https, ws), set as the Service port's appProtocol so a gateway
	// or mesh can route it correctly. It changes no pod behaviour.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
	AppProtocol string `json:"appProtocol,omitempty"`

	// Expose asks the environment to route a PUBLIC hostname to this port:
	// the gateway when self-hosted, the platform's allocated hostname on
	// hosted. Only a service can expose a port, because only a service
	// has a Service object to route to. At most one port per workload is
	// exposed, because status reports one hostname.
	// +optional
	Expose bool `json:"expose,omitempty"`

	// Domains are CUSTOM hostnames served IN ADDITION to the default
	// hostname. They are only meaningful on the exposed port. The default
	// hostname needs no declaration: hosted allocates a collision-safe one
	// and self-hosted derives one from the env's gateway, so a public
	// workload never has to invent a domain. Hosted verifies ownership of a
	// custom domain before serving it, because on shared infrastructure an
	// unverified hostname is a takeover vector. Self-hosted trusts it.
	// +optional
	// +kubebuilder:validation:MaxItems=8
	Domains []string `json:"domains,omitempty"`
}

// Probes are a workload's readiness and liveness probes, declared ONCE.
//
// # WHY ONE DECLARATION DRIVES TWO DIFFERENT PROBES
//
// The two probes answer different questions, and they must not share
// timings. Readiness gates TRAFFIC. It is fast, so a wedged replica leaves
// the Service endpoints within one budget, and it never restarts anything,
// so a dependency outage degrades the service instead of crash-looping the
// fleet. Liveness RESTARTS. Its timings are DERIVED from the readiness
// budget by Liveness(), so that it can never race a cold start however
// readiness is tuned. The derivation is the only one there is: liveness
// timings are not separately settable, because every independent knob
// forge has shipped for them was eventually set shorter than readiness and
// restart-looped a slow start.
//
// This replaced HealthCheck, whose ONE probe fed both liveness and
// readiness. That shape is how a hosted API shipped with no probes (the
// field was optional and had no default) and how, when one was set, a
// slow dependency restarted pods that only needed to stop taking traffic.
//
// An HTTP probe GETs ReadinessPath and LivenessPath (default /readyz and
// /healthz, serverkit's split). TCP probes with a connect instead, which is
// correct for a workload that serves a non-HTTP protocol. Probing "/" by
// default would restart-loop such a workload and make the loop look like
// an application fault.
type Probes struct {
	// Port is the container port probed. Unset means the port named http,
	// or the first declared port (see WorkloadSpec.ProbePort).
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port,omitempty"`

	// ReadinessPath is the HTTP readiness path. Unset means /readyz.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^/.*$`
	ReadinessPath string `json:"readinessPath,omitempty"`

	// LivenessPath is the HTTP liveness path. Unset means /healthz.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^/.*$`
	LivenessPath string `json:"livenessPath,omitempty"`

	// StartupPath is the HTTP startup-probe path. Unset means no startup
	// probe. The kubelet holds off readiness and liveness until it passes,
	// and a NATIVE sidecar (Container.Native) is only considered started,
	// so the main container only launches, once it does. Unlike the other
	// two paths it has no default: only the container knows what "started"
	// means.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^/.*$`
	StartupPath string `json:"startupPath,omitempty"`

	// TCP probes with a TCP connect instead of an HTTP GET. A TCP probe
	// sets no paths.
	// +optional
	TCP bool `json:"tcp,omitempty"`

	// InitialDelaySeconds is the READINESS initial delay. Default 0:
	// readiness is cheap and never restarts, so there is nothing to wait
	// for.
	// +optional
	// +kubebuilder:default=0
	// +kubebuilder:validation:Minimum=0
	InitialDelaySeconds int32 `json:"initialDelaySeconds,omitempty"`

	// PeriodSeconds is the READINESS period. Default 5.
	// +optional
	// +kubebuilder:default=5
	// +kubebuilder:validation:Minimum=1
	PeriodSeconds int32 `json:"periodSeconds,omitempty"`

	// TimeoutSeconds is the per-attempt timeout, for both probes. Default 3,
	// NOT 2. serverkit's readiness handler has a 2s deadline of its own, so a
	// 2s probe timeout would race it and report the handler's slow-but-correct
	// "not ready" as a probe timeout.
	// +optional
	// +kubebuilder:default=3
	// +kubebuilder:validation:Minimum=1
	TimeoutSeconds int32 `json:"timeoutSeconds,omitempty"`

	// FailureThreshold is how many consecutive READINESS failures take a
	// replica out of the endpoints. Default 3.
	// +optional
	// +kubebuilder:default=3
	// +kubebuilder:validation:Minimum=1
	FailureThreshold int32 `json:"failureThreshold,omitempty"`
}

// Probe defaults, mirrored by the markers above (TestDefaultMarkersMatchGoDefaults
// keeps the two equal).
const (
	DefaultProbeInitialDelaySeconds int32 = 0
	DefaultProbePeriodSeconds       int32 = 5
	DefaultProbeTimeoutSeconds      int32 = 3
	DefaultProbeFailureThreshold    int32 = 3

	// A startup probe polls every DefaultStartupPeriodSeconds for up to
	// DefaultStartupPeriodSeconds*DefaultStartupFailureThreshold (360s)
	// before the kubelet restarts the container.
	//
	// The window must cover the process's startup dependency wait: a forge
	// server retries its database connect for serverkit.DefaultDBConnectTimeout
	// (5m, DB_CONNECT_TIMEOUT) while reporting not-ready, and the kubelet
	// must not kill it inside that wait. 360s = 300s + 60s for the rest of
	// boot. TestStartupWindowCoversDBConnectTimeout pins the relation; a
	// deployment that raises DB_CONNECT_TIMEOUT past it must widen this too.
	DefaultStartupPeriodSeconds    int32 = 1
	DefaultStartupFailureThreshold int32 = 360

	DefaultReadinessPath = "/readyz"
	DefaultLivenessPath  = "/healthz"

	// LivenessMarginSeconds is added on top of the whole readiness budget
	// to get the liveness initial delay, so a start that is merely slow is
	// never mistaken for a deadlock.
	LivenessMarginSeconds int32 = 15
)

// ProbeTimings are one probe's timings, as rendered.
type ProbeTimings struct {
	InitialDelaySeconds int32
	PeriodSeconds       int32
	TimeoutSeconds      int32
	FailureThreshold    int32
}

// WithDefaults fills every unset timing, and fills the paths of an HTTP
// probe. A TCP probe's paths stay empty.
func (p Probes) WithDefaults() Probes {
	if p.PeriodSeconds == 0 {
		p.PeriodSeconds = DefaultProbePeriodSeconds
	}
	if p.TimeoutSeconds == 0 {
		p.TimeoutSeconds = DefaultProbeTimeoutSeconds
	}
	if p.FailureThreshold == 0 {
		p.FailureThreshold = DefaultProbeFailureThreshold
	}
	if !p.TCP {
		if p.ReadinessPath == "" {
			p.ReadinessPath = DefaultReadinessPath
		}
		if p.LivenessPath == "" {
			p.LivenessPath = DefaultLivenessPath
		}
	}
	return p
}

// Readiness returns the (defaulted) readiness timings.
func (p Probes) Readiness() ProbeTimings {
	d := p.WithDefaults()
	return ProbeTimings{
		InitialDelaySeconds: d.InitialDelaySeconds,
		PeriodSeconds:       d.PeriodSeconds,
		TimeoutSeconds:      d.TimeoutSeconds,
		FailureThreshold:    d.FailureThreshold,
	}
}

// Liveness returns the liveness timings, DERIVED from the readiness budget:
// the initial delay is the readiness initial delay plus the whole readiness
// failure window (period × failures) plus LivenessMarginSeconds, and the
// period is twice the readiness period. Timeout and failure threshold are
// shared. This is the one place the arithmetic lives, and renderers call it
// rather than restating it.
func (p Probes) Liveness() ProbeTimings {
	r := p.Readiness()
	return ProbeTimings{
		InitialDelaySeconds: r.InitialDelaySeconds + r.PeriodSeconds*r.FailureThreshold + LivenessMarginSeconds,
		PeriodSeconds:       r.PeriodSeconds * 2,
		TimeoutSeconds:      r.TimeoutSeconds,
		FailureThreshold:    r.FailureThreshold,
	}
}

// PolicyRule is one RBAC rule: verbs on resources in API groups.
//
// A LOCAL MIRROR of rbac/v1 PolicyRule, not the upstream type, for two
// reasons. The spec schema is closed and self-contained, so the KCL
// generator refuses a reference that leaves this package. And a mirror
// keeps the surface an allowlist: nonResourceURLs (meaningful only in a
// ClusterRole, and granting access to raw API server paths) and any future
// upstream field are unreachable until someone adds them here on purpose.
type PolicyRule struct {
	// APIGroups are the API groups; "" is the core group.
	// +kubebuilder:validation:MinItems=1
	APIGroups []string `json:"apiGroups"`

	// Resources are the resource names (plural, lowercase), optionally with
	// a subresource ("pods/log").
	// +kubebuilder:validation:MinItems=1
	Resources []string `json:"resources"`

	// Verbs are the allowed verbs, or "*".
	// +kubebuilder:validation:MinItems=1
	Verbs []string `json:"verbs"`

	// ResourceNames narrows the rule to named objects.
	// +optional
	ResourceNames []string `json:"resourceNames,omitempty"`
}

// WorkloadStatus is the observed state of a Workload.
type WorkloadStatus struct {
	TierStatus `json:",inline"`

	// ServiceName is the Service the hostname routes to. Empty for a kind
	// with no Service object.
	// +optional
	ServiceName string `json:"serviceName,omitempty"`

	// WorkloadName is the Deployment, Job or CronJob carrying the pods.
	// +optional
	WorkloadName string `json:"workloadName,omitempty"`

	// ReadyReplicas is the API server's own observed ready count. It is
	// this resource's liveness proof, and it is why a workload that never
	// started bills nothing.
	// +optional
	// +kubebuilder:validation:Minimum=0
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// UpdatedReplicas is how many replicas are running the CURRENT pod
	// template — the one carrying ObservedImage — and are ready.
	//
	// It exists because ReadyReplicas alone cannot tell a finished rollout
	// from a failing one. Under RollingUpdate the old replicas stay ready
	// while the new ones crash-loop, so ReadyReplicas ≥ desired holds
	// throughout, and a reader that gates on it alone passes a release
	// that never served a request. UpdatedReplicas is the count that goes
	// to zero in exactly that case, which is why it is the field a rollout
	// gate reads.
	// +optional
	// +kubebuilder:validation:Minimum=0
	UpdatedReplicas int32 `json:"updatedReplicas,omitempty"`

	// DesiredReplicas is how many the controller is working toward. It is
	// reported rather than inferred from spec.replicas because the two can
	// legitimately differ — mid-scale, or while a status describes a
	// previous generation — and comparing UpdatedReplicas against the
	// spec in that window would read a normal scale as a stalled rollout.
	// +optional
	// +kubebuilder:validation:Minimum=0
	DesiredReplicas int32 `json:"desiredReplicas,omitempty"`

	// ObservedImage is the image the running workload was CONFIRMED to
	// carry. It lags spec.image until a rollout completes, and that gap is
	// the drift signal.
	//
	// CONFIRMED means the rollout to that template finished — the same
	// completion test `kubectl rollout status` applies. Until it does, the
	// field keeps its PREVIOUS value, the image last confirmed running,
	// because reporting the pod template's image the moment it is written
	// would make "observed" mean "declared" and leave nothing to compare
	// a desired digest against.
	// +optional
	ObservedImage string `json:"observedImage,omitempty"`

	// LastReadyAt is nil until the workload has had a ready replica at
	// least once. A nil value is the machine-readable form of "declared,
	// never ran".
	// +optional
	LastReadyAt *metav1.Time `json:"lastReadyAt,omitempty"`
}

// Workload is one runnable thing: a service, worker, job, cron, operator or
// tool.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=wl,categories=forge
// +kubebuilder:printcolumn:name="Kind",type=string,JSONPath=`.spec.kind`
// +kubebuilder:printcolumn:name="Replicas",type=integer,JSONPath=`.spec.replicas`
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyReplicas`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Hostname",type=string,JSONPath=`.status.hostname`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Workload struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkloadSpec   `json:"spec"`
	Status WorkloadStatus `json:"status,omitempty"`
}

// WorkloadList contains a list of Workload.
//
// +kubebuilder:object:root=true
type WorkloadList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Workload `json:"items"`
}

// Default workload values, mirrored by the markers above.
const (
	DefaultWorkloadKind       = KindService
	DefaultReplicas     int32 = 1
	DefaultPortProtocol       = ProtocolTCP
	// DefaultHTTPPortName is the port name a probe, a Service route and
	// the host runtime look for first.
	DefaultHTTPPortName = "http"
)

// WithDefaults fills every unset value that has a static default: the kind,
// replicas, resources, port protocols and (when present) probe timings,
// including each sidecar's.
// DeployPhase and LeaderElection are resolved by their Effective* helpers
// instead, because their defaults depend on the kind.
func (s WorkloadSpec) WithDefaults() WorkloadSpec {
	if s.Kind == "" {
		s.Kind = DefaultWorkloadKind
	}
	if s.Replicas == 0 {
		s.Replicas = DefaultReplicas
	}
	s.Resources = s.Resources.WithDefaults()
	s.Ports = defaultPorts(s.Ports)
	if s.Probes != nil {
		p := s.Probes.WithDefaults()
		s.Probes = &p
	}
	if len(s.Sidecars) > 0 {
		sidecars := make([]Container, len(s.Sidecars))
		for i, c := range s.Sidecars {
			c.Resources = c.Resources.WithDefaults()
			c.Ports = defaultPorts(c.Ports)
			if c.Probes != nil {
				p := c.Probes.WithDefaults()
				c.Probes = &p
			}
			sidecars[i] = c
		}
		s.Sidecars = sidecars
	}
	return s
}

func defaultPorts(in []Port) []Port {
	if len(in) == 0 {
		return in
	}
	out := make([]Port, len(in))
	for i, p := range in {
		if p.Protocol == "" {
			p.Protocol = DefaultPortProtocol
		}
		out[i] = p
	}
	return out
}

// EffectiveKind resolves an unset Kind to service.
func (s WorkloadSpec) EffectiveKind() WorkloadKind {
	if s.Kind == "" {
		return DefaultWorkloadKind
	}
	return s.Kind
}

// EffectiveDeployPhase is a standalone job's phase: DeployPhase, or
// pre-rollout. Empty for anything that is not a standalone job.
func (s WorkloadSpec) EffectiveDeployPhase() DeployPhase {
	if s.EffectiveKind() != KindJob || len(s.Before) > 0 {
		return ""
	}
	if s.DeployPhase == "" {
		return DeployPhasePreRollout
	}
	return s.DeployPhase
}

// EffectiveLeaderElection is whether an operator elects a leader: true
// unless explicitly disabled. Always false for other kinds.
func (s WorkloadSpec) EffectiveLeaderElection() bool {
	if s.EffectiveKind() != KindOperator {
		return false
	}
	return s.LeaderElection == nil || *s.LeaderElection
}

// ExposedPort is the port a public hostname routes to, or nil when nothing
// is exposed. Its Domains are the custom hostnames served beside the default
// one. Validate guarantees at most one exposed port, and that Domains
// appear on no other port.
func (s WorkloadSpec) ExposedPort() *Port {
	for i := range s.Ports {
		if s.Ports[i].Expose {
			return &s.Ports[i]
		}
	}
	return nil
}

// ProbePort is the port the probes connect to: Probes.Port if set, else the
// port named http, else the first declared port, else 0 (nothing to probe).
func (s WorkloadSpec) ProbePort() int32 { return probePort(s.Probes, s.Ports) }

func probePort(probes *Probes, ports []Port) int32 {
	if probes != nil && probes.Port != 0 {
		return probes.Port
	}
	for _, p := range ports {
		if p.Name == DefaultHTTPPortName {
			return p.Port
		}
	}
	if len(ports) > 0 {
		return ports[0].Port
	}
	return 0
}

// UsesGeneratedServiceAccount reports whether forge renders the workload's
// ServiceAccount (named after the workload). False when ServiceAccount names
// an existing one.
func (s WorkloadSpec) UsesGeneratedServiceAccount() bool { return s.ServiceAccount == "" }

// EffectiveProbes is the probe policy of ADR 0002 §5, defined once:
//
//   - declared probes are used as declared, with defaults filled;
//   - a service that declares ports but no probes gets a TCP probe on its
//     first port, because a Service routing to a pod nobody checks is how a
//     hosted API ran unprobed;
//   - anything else gets none: a worker's, operator's or job's health is
//     its own business unless it says otherwise.
//
// A service or operator that forge BUILT gets HTTP /readyz + /healthz: both
// run under serverkit, which serves them on the process's PORT. The lowering
// knows the build, so it writes that probe into the spec explicitly, and it
// arrives here as declared.
//
// The returned probe has Port resolved, so a renderer never re-derives it.
func (s WorkloadSpec) EffectiveProbes() *Probes {
	kind := s.EffectiveKind()
	if kind != KindService && kind != KindWorker && kind != KindOperator {
		return nil
	}
	if s.Probes != nil {
		p := s.Probes.WithDefaults()
		p.Port = s.ProbePort()
		return &p
	}
	if kind != KindService || len(s.Ports) == 0 {
		return nil
	}
	p := Probes{TCP: true, Port: s.Ports[0].Port}.WithDefaults()
	return &p
}
