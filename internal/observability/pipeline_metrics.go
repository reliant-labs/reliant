package observability

import "github.com/prometheus/client_golang/prometheus"

// PipelineGitHubWrites counts the GitHub writes the pipeline's server actions
// make (internal/pipeline/practions): written, failed, or suppressed by a
// repository's shadow mode. kind is a fixed write kind (comment, close).
var PipelineGitHubWrites = newCounterVec(prometheus.CounterOpts{
	Namespace: "reliant", Subsystem: "pipeline", Name: "github_writes_total",
	Help: "GitHub writes by the pipeline's server actions, by kind and outcome (written, failed, suppressed in shadow mode).",
}, []string{"kind", "outcome"})
