package v1alpha1

// Pod-level escape hatches: sidecars, volumes, a ServiceAccount override and
// node placement. They are all FULL-PROFILE ONLY (see FieldProfiles). Each is
// something a cluster the author operates genuinely needs (control-plane
// runs a Cloud SQL Auth Proxy sidecar under a workload-identity
// ServiceAccount, and mounts Secret files and scratch directories). On shared
// hosted nodes each of them would reach past the hosted user's own container.
//
// They are CLOSED, typed subsets, like the rest of the spec, and never a
// PodSpec passthrough. A field a destination must not honour has to be
// classified, not merely left out of an allowlist.

// Reserved volumes the renderer adds to every pod. Declared here, beside the
// types, so the renderer and Validate agree on them without being told: a
// declared Volume that reused one of these names or paths would make an
// invalid pod (a duplicate volume name) or shadow forge's own mount.
const (
	// TmpVolumeName / TmpMountPath are the emptyDir that makes /tmp
	// writable under forge's read-only root filesystem. Every pod gets it.
	TmpVolumeName = "tmp"
	TmpMountPath  = "/tmp"
	// DataVolumeName / DataMountPath are the StorageGiB PersistentVolumeClaim
	// mount. They are reserved only when StorageGiB > 0: a stateless
	// workload may use "data" and /data for a scratch volume of its own.
	DataVolumeName = "data"
	DataMountPath  = "/data"
)

// Container is a sidecar: a second container in the workload's pod.
//
// It is a CLOSED subset of a Kubernetes container. There is no
// securityContext field, on purpose: the renderer applies the SAME hardened
// securityContext to a sidecar as to the main container (non-root, no
// privilege escalation, all capabilities dropped, read-only root
// filesystem). A sidecar that could loosen it would make the main
// container's hardening a suggestion. There are no volume mounts either. A
// sidecar shares /tmp and nothing else, and one that needs a file gets it
// through env.
//
// Its ports are CONTAINER ports only. They are never on the workload's
// Service and are never exposed, because a Service routes to the workload,
// not to its helpers. They share the pod's network namespace with the main
// container, so a port name or number is unique across the whole pod.
type Container struct {
	// Name is the container name, unique within the pod. It must not equal
	// the workload's own name, which the main container uses.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// Image is the sidecar's image, typically third-party
	// (gcr.io/cloud-sql-connectors/cloud-sql-proxy:2.14.1).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=500
	Image string `json:"image"`

	// Command overrides the image's entrypoint.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=4096
	Command []string `json:"command,omitempty"`

	// Args are the entrypoint's arguments.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=4096
	Args []string `json:"args,omitempty"`

	// Env is the sidecar's environment. Like WorkloadSpec.Env, it has no
	// portable entry-count limit; exec byte limits depend on resolved values.
	// +optional
	// +listType=map
	// +listMapKey=name
	Env []EnvVar `json:"env,omitempty"`

	// Ports are the sidecar's container ports.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=16
	Ports []Port `json:"ports,omitempty"`

	// Resources is the sidecar's compute request and limit, with the same
	// defaults as the main container.
	// +optional
	Resources Resources `json:"resources,omitempty"`

	// Probes are the sidecar's own probes. Unset means none: unlike a
	// service, a sidecar gets no implicit TCP probe.
	// +optional
	Probes *Probes `json:"probes,omitempty"`

	// Native renders the sidecar as a Kubernetes native sidecar: an
	// initContainer with restartPolicy Always (GA in Kubernetes 1.29). The
	// kubelet starts it BEFORE the main container and waits for its startup
	// probe to pass, and stops it AFTER the main container exits. A plain
	// sidecar starts concurrently with the main container, so a main
	// process that dials the sidecar at boot (a database proxy on
	// 127.0.0.1) races it and fails until the sidecar is listening. Set
	// Probes.StartupPath so "started" means "ready to serve"; without a
	// startup probe the kubelet only waits for the process to launch.
	// +optional
	Native bool `json:"native,omitempty"`
}

// ProbePort is the port a sidecar's probes connect to: Probes.Port, else the
// port named http, else its first port, else 0.
func (c Container) ProbePort() int32 {
	return probePort(c.Probes, c.Ports)
}

// EffectiveProbes is a sidecar's probes: the declared ones with defaults
// filled and Port resolved, or nil.
func (c Container) EffectiveProbes() *Probes {
	if c.Probes == nil {
		return nil
	}
	p := c.Probes.WithDefaults()
	p.Port = c.ProbePort()
	return &p
}

// Volume is one pod volume and its mount into the MAIN container.
//
// One declaration models both the pod's spec.volumes entry and the
// container's volumeMounts entry, because they share a name and a workload
// almost always mounts a volume exactly once.
//
// # THE SOURCES ARE CLOSED: NO hostPath, projected OR csi
//
// There are exactly four sources: a Secret, a ConfigMap, an emptyDir and an
// existing PersistentVolumeClaim. The others are unreachable on purpose, even
// under ProfileFull:
//
//   - hostPath mounts the NODE's filesystem into the pod. It defeats every
//     other isolation forge renders (non-root, read-only rootfs, dropped
//     capabilities), since a writable hostPath is a node compromise, and the
//     restricted Pod Security Standard forbids it outright. A workload that
//     truly needs the node is a DaemonSet-shaped piece of infrastructure,
//     not a forge workload.
//   - projected aggregates ServiceAccount tokens, the Downward API and
//     cluster trust bundles into one mount. Token projection with a custom
//     audience is how a pod mints credentials for OTHER systems, and that
//     belongs to the ServiceAccount a workload runs as, not to a volume
//     declaration.
//   - csi hands the pod to an arbitrary storage driver, with driver-specific
//     attributes forge cannot validate (secrets-store CSI, inline
//     ephemeral volumes). A driver's configuration surface is exactly the
//     open-ended passthrough this schema exists to avoid.
//
// Needing one of them is the signal that the object belongs in the
// environment's infra block as a raw manifest, which forge applies but does
// not model.
type Volume struct {
	// Name is the volume name, unique within the pod.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// MountPath is the absolute path the volume mounts at in the main
	// container.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=4096
	// +kubebuilder:validation:Pattern=`^/.*$`
	MountPath string `json:"mountPath"`

	// ReadOnly mounts the volume read-only. Secret and ConfigMap volumes
	// are conventionally read-only; an emptyDir is usually writable scratch.
	// +optional
	ReadOnly bool `json:"readOnly,omitempty"`

	// Source is where the volume's content comes from: exactly one kind.
	Source VolumeSource `json:"source"`
}

// VolumeSource is exactly one of the four closed volume sources.
//
// +kubebuilder:validation:ExactlyOneOf=secret;configMap;emptyDir;pvc
type VolumeSource struct {
	// Secret projects a Secret in the workload's namespace as files.
	// +optional
	Secret *SecretVolumeSource `json:"secret,omitempty"`

	// ConfigMap projects a ConfigMap in the workload's namespace as files.
	// +optional
	ConfigMap *ConfigMapVolumeSource `json:"configMap,omitempty"`

	// EmptyDir is pod-lifetime scratch space, which is the way to give a
	// workload a writable path under forge's read-only root filesystem.
	// +optional
	EmptyDir *EmptyDirVolumeSource `json:"emptyDir,omitempty"`

	// PVC mounts an EXISTING PersistentVolumeClaim. For storage forge
	// provisions, use WorkloadSpec.StorageGiB.
	// +optional
	PVC *PVCVolumeSource `json:"pvc,omitempty"`
}

// SecretVolumeSource names a Secret, and optionally which keys to project.
type SecretVolumeSource struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
	// Items selects keys and their file paths. Unset projects every key as
	// a file named after it.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	Items []KeyToPath `json:"items,omitempty"`
	// Optional lets the pod start while the Secret does not exist yet; the
	// kubelet fills the volume in once it appears. A Secret the control plane
	// delivers after the first deploy (forge.ControlPlaneSecret) is mounted
	// this way, so a first deploy never waits on it.
	// +optional
	Optional bool `json:"optional,omitempty"`
}

// ConfigMapVolumeSource names a ConfigMap, and optionally which keys to
// project.
type ConfigMapVolumeSource struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
	// Items selects keys and their file paths. Unset projects every key as
	// a file named after it.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	Items []KeyToPath `json:"items,omitempty"`
}

// KeyToPath projects one key to a file path relative to the mount.
type KeyToPath struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Key string `json:"key"`
	// Path is RELATIVE to the mount and may not contain "..", so a key
	// can never be written outside the volume.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=4096
	Path string `json:"path"`
}

// EmptyDirVolumeSource is pod-lifetime scratch space.
//
// The size limit is in BYTES, following Resources: a Kubernetes quantity
// string ("1Gi") is a rendering concern and never appears in a spec.
type EmptyDirVolumeSource struct {
	// SizeLimitBytes caps the volume. Unset means no cap beyond the node's
	// ephemeral storage.
	// +optional
	// +kubebuilder:validation:Minimum=1
	SizeLimitBytes int64 `json:"sizeLimitBytes,omitempty"`
}

// PVCVolumeSource names an existing PersistentVolumeClaim in the workload's
// namespace.
type PVCVolumeSource struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	ClaimName string `json:"claimName"`
}

// TolerationOperator is how a toleration matches a taint's value.
//
// +kubebuilder:validation:Enum=Exists;Equal
type TolerationOperator string

const (
	TolerationOpExists TolerationOperator = "Exists"
	TolerationOpEqual  TolerationOperator = "Equal"
)

// TaintEffect is the taint effect a toleration matches. Empty matches every
// effect.
//
// +kubebuilder:validation:Enum=NoSchedule;PreferNoSchedule;NoExecute
type TaintEffect string

const (
	TaintNoSchedule       TaintEffect = "NoSchedule"
	TaintPreferNoSchedule TaintEffect = "PreferNoSchedule"
	TaintNoExecute        TaintEffect = "NoExecute"
)

// Toleration lets the workload's pods schedule onto nodes with a matching
// taint. It is a local mirror of core/v1 Toleration, for the same reasons as
// PolicyRule: the schema is closed and self-contained.
//
// The spellings are Kubernetes' own (Exists, NoSchedule), not lowercased.
// A toleration is copied from a node pool's taint, and a second spelling
// would be a translation step where the two could disagree.
type Toleration struct {
	// Key is the taint key. Empty, with operator Exists, tolerates every
	// taint.
	// +optional
	// +kubebuilder:validation:MaxLength=316
	Key string `json:"key,omitempty"`

	// Operator is Exists or Equal. Unset means Equal (Kubernetes' default).
	// +optional
	Operator TolerationOperator `json:"operator,omitempty"`

	// Value is the taint value to match, for operator Equal.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	Value string `json:"value,omitempty"`

	// Effect is the taint effect to match. Unset matches every effect.
	// +optional
	Effect TaintEffect `json:"effect,omitempty"`

	// TolerationSeconds bounds how long a NoExecute taint is tolerated
	// before eviction. Unset means forever.
	// +optional
	// +kubebuilder:validation:Minimum=0
	TolerationSeconds *int64 `json:"tolerationSeconds,omitempty"`
}
