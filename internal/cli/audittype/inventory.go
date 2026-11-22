package audittype

// Workload is one declared workload in the shape category's
// `details.workloads`: what every env that declares it renders, plus where
// its literal is in the project's KCL.
//
// Kind, Build and Image are as the first env (by name) that renders the
// workload declares them — or, for a workload no env renders, as its literal
// writes them.
type Workload struct {
	Name string `json:"name"`
	// Kind is the workload kind, a free string (service, worker, job, …).
	Kind string `json:"kind"`
	// Build is go, docker, shell, remote, or none when forge builds nothing
	// for it (a third-party image).
	Build string `json:"build"`
	// Image is the artifact forge builds the workload into, else the image it
	// runs; omitted when there is neither.
	Image string `json:"image,omitempty"`
	// Runtimes maps each env that renders the workload to its runtime there:
	// host, compose, cluster, hosted, build-only or external. Empty when no
	// env renders it.
	Runtimes map[string]string `json:"runtimes"`
	// File and Line locate the workload's literal; omitted when the
	// workload is not written as a literal with a name (a comprehension).
	File string `json:"file,omitempty"`
	Line int    `json:"line,omitempty"`
}

// WorkloadInventory is every declared workload, and the envs whose
// declaration could not be rendered (env → error). A failed env is a warning,
// never an error: the workloads the other envs render are still reported.
type WorkloadInventory struct {
	Workloads []Workload
	Failed    map[string]string
}
