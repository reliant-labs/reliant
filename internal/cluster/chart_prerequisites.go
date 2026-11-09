package cluster

import "strings"

// Without the secondary group's app ownership map, only explicit cluster
// routing and genuinely env-shared objects can be safely promoted there.
// App-owned, unrouted objects remain with their normal group apply.
func chartPrerequisitesForSecondary(manifests, kctx string) string {
	var out []string
	for _, doc := range splitDocs(manifests) {
		m, ok := parseDoc(doc)
		if !ok {
			continue
		}
		route := m.Metadata.Labels[ClusterRoutingLabel]
		if route == kctx || (route == "" && ManifestGroup(m.Metadata.Labels) == "") {
			out = append(out, doc)
		}
	}
	return strings.Join(out, docDelimiter)
}

// ChartSchedulingPrerequisites selects declared built-in scheduling objects
// needed before chart controllers can create pods. It deliberately does not
// promote arbitrary CRs (which may need the controllers we are installing).
// Callers scope the stream with ScopeManifestsToGroup first, and retain the
// original stream for normal reconciliation and pruning. Unrouted objects are
// shared, just as in the normal apply pipeline.
func ChartSchedulingPrerequisites(manifests string) string {
	var out []string
	for _, doc := range splitDocs(manifests) {
		m, ok := parseDoc(doc)
		if !ok || m.APIVersion != "scheduling.k8s.io/v1" || m.Kind != "PriorityClass" {
			continue
		}
		out = append(out, doc)
	}
	return strings.Join(out, docDelimiter)
}
