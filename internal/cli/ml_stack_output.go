package cli

// MLStackOutput is one `output.ml_stacks` entry: a declared forge.MLStack
// (kcl/ml.k), projected so tools read the stack and its decisions from
// `forge env render <env> --json` without parsing KCL. Every key is always
// present (empty when unset); keys are only ever added.
type MLStackOutput struct {
	// Cluster is the stack's kube context (ClusterTarget.cluster).
	Cluster string `json:"cluster"`
	// Namespace is the stack's ClusterTarget.namespace.
	Namespace string `json:"namespace"`
	// Observed are the component kinds the last `forge ml discover` found
	// running that forge did not install.
	Observed []string `json:"observed"`
	// ObservedAt is when that discovery ran (YYYY-MM-DD), or empty.
	ObservedAt string `json:"observed_at"`
	// Accelerators are the stack's accelerator vendors.
	Accelerators []MLAcceleratorsOutput `json:"accelerators"`
	// Components are every adopted or installed component, in slot order.
	Components []MLComponentOutput `json:"components"`
	// Decisions are the recommendations the stack dismisses.
	Decisions []MLDecisionOutput `json:"decisions"`
}

// MLAcceleratorsOutput is one forge.Accelerators of a stack.
type MLAcceleratorsOutput struct {
	// Vendor is nvidia, amd, tpu, neuron, gaudi or generic.
	Vendor string `json:"vendor"`
	// Resource is the extended resource its device plugin advertises,
	// defaulted from the vendor.
	Resource string `json:"resource"`
	// NodeSelector narrows the nodes that carry it.
	NodeSelector map[string]string `json:"node_selector"`
}

// MLComponentOutput is one component of a stack: a forge.Adopt or a
// forge.Install in one of its slots.
type MLComponentOutput struct {
	// Slot is accelerators, node_health, queue, training, serving,
	// monitoring or pull_cache.
	Slot string `json:"slot"`
	// Vendor is the Accelerators vendor of an accelerators-slot component,
	// else empty.
	Vendor string `json:"vendor"`
	// Mode is "adopt" (read where it runs) or "install" (forge's own chart).
	Mode string `json:"mode"`
	// Kind is the component id, e.g. dcgm-exporter.
	Kind string `json:"kind"`
	// Namespace is where an adopted component runs, or the Install chart's
	// namespace.
	Namespace string `json:"namespace"`
	// Version is what discovery found for an adoption, or the Install
	// chart's pinned version.
	Version string `json:"version"`
	// Selector, Port and Allow are an adoption's scrape (Port 0: none).
	Selector map[string]string `json:"selector"`
	Port     int               `json:"port"`
	Allow    []string          `json:"allow"`
	// Note is an adoption's reviewer note.
	Note string `json:"note"`
	// ServiceName is the service.name of the adoption's scrape when it
	// renders (a port, and the env's observability on), else empty.
	ServiceName string `json:"service_name"`
	// Chart is the Install chart's name, else empty.
	Chart string `json:"chart"`
	// Reason is why forge installs it (required for an adopt-only kind).
	Reason string `json:"reason"`
}

// MLDecisionOutput is one forge.RecommendationDecision.
type MLDecisionOutput struct {
	// ID is the recommendation rule's id.
	ID string `json:"id"`
	// Decision is "dismiss".
	Decision string `json:"decision"`
	// Reason is the claim a reviewer can check.
	Reason string `json:"reason"`
	// Expires is the YYYY-MM-DD past which the recommendation shows again.
	Expires string `json:"expires"`
}
