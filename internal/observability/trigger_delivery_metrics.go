//forge:exclude-contract: process-global Prometheus collectors, as in metrics.go
package observability

import "github.com/prometheus/client_golang/prometheus"

// TriggerDeliveryOutcomes counts durable receipt and worker outcomes. Labels
// come from fixed code paths, never payloads, delivery IDs or trigger IDs.
var TriggerDeliveryOutcomes = newCounterVec(prometheus.CounterOpts{
	Namespace: "reliant", Subsystem: "trigger_inbox", Name: "outcomes_total",
	Help: "Inbound trigger delivery receipts and processing results by source and outcome.",
}, []string{"source", "outcome"})
