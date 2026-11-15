package cluster

// The render-output keys this package decodes itself, as named types.
//
// ExtractManifests reads a few keys of the env output that internal/cli's
// entity parser does not: the env NetworkPolicy, the StatefulSet resize
// tuning and the bring-your-own YAML sources. They are exported so the env
// document schema (internal/cli env_document_schema.go) is generated from the
// same types this package decodes with, rather than from a copy.

// NetworkPolicyOutput is `output.network_policy`: the inputs of the env-wide
// NetworkPolicy bundle (forge.NetworkPolicy). null when the env declares none.
type NetworkPolicyOutput struct {
	// EgressPorts are the external TCP ports every workload may dial.
	EgressPorts []int32 `json:"egress_ports"`
	// TelemetryNamespace is where the telemetry agent runs ("" for none).
	TelemetryNamespace string `json:"telemetry_namespace"`
	// IngressNamespace is where the ingress controller runs ("" for none).
	IngressNamespace string `json:"ingress_namespace"`
}

// StatefulSetResizeOutput is `output.statefulset_resize`: the tuning of the
// Job that converges a StatefulSet whose declared volume grew. Present only
// when the env declares Bundle.statefulset_resize.
type StatefulSetResizeOutput struct {
	// Disabled opts the env out of the resize Job.
	Disabled bool `json:"disabled"`
	// Image replaces the Job's default image ("" for the default).
	Image string `json:"image"`
	// ReleaseOwner is the release-inputs owner the Job's image is pinned by.
	ReleaseOwner string `json:"release_owner"`
}

// GeneratedOutput is one `output.generated` entry (forge.Generated): a
// command whose stdout is Kubernetes YAML forge joins to the apply stream.
type GeneratedOutput struct {
	Name      string   `json:"name"`
	Command   []string `json:"command"`
	Dir       string   `json:"dir"`
	Cluster   string   `json:"cluster"`
	Namespace string   `json:"namespace"`
}

// BundledChartOutput is one `output.bundled_charts` entry (a HelmChart whose
// delivery is "bundle"), in the projection _render_helm_chart emits.
type BundledChartOutput struct {
	Name      string         `json:"name"`
	Chart     string         `json:"chart"`
	Repo      string         `json:"repo"`
	OCI       string         `json:"oci"`
	Version   string         `json:"version"`
	Namespace string         `json:"namespace"`
	Values    map[string]any `json:"values"`
	Cluster   string         `json:"cluster"`
}
