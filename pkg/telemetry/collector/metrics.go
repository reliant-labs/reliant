package collector

import (
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/pkg/telemetry"
)

// prometheus is t's prometheus receiver: one scrape job per MetricsScrape.
func (b *builder) prometheus(t telemetry.Target) (map[string]any, error) {
	jobs := make([]any, 0, len(t.Metrics))
	for i, m := range t.Metrics {
		// An unnamed scrape's suffix is its index. Validate refuses an
		// all-digit name, so no named scrape can take the same job name.
		suffix := m.Name
		if suffix == "" {
			suffix = strconv.Itoa(i)
		}
		job := map[string]any{
			// The job name only has to be unique; service.name comes from
			// the target's transform.
			"job_name":        lit("telemetry/" + t.ServiceName + "/" + suffix),
			"scrape_interval": m.Interval,
			"scrape_timeout":  m.Timeout,
			"metrics_path":    lit(m.Path),
			"scheme":          m.Scheme,
			// Prometheus 3 refuses a response whose Content-Type it cannot
			// read; many exporters send none.
			"fallback_scrape_protocol": "PrometheusText0.0.4",
			"sample_limit":             m.SampleLimit,
			"label_limit":              m.LabelLimit,
			"label_value_length_limit": m.LabelValueLengthLimit,
		}
		if len(m.Params) > 0 {
			params := map[string]any{}
			for _, k := range sortedKeys(m.Params) {
				params[lit(k)] = strs(lits(m.Params[k])...)
			}
			job["params"] = params
		}
		if m.BearerToken != nil {
			file, err := b.mountFile(t, fmt.Sprintf("metrics[%d].bearer_token", i), m.BearerToken)
			if err != nil {
				return nil, err
			}
			job["authorization"] = map[string]any{"type": "Bearer", "credentials_file": file}
		}
		if m.TLS != nil {
			tls := map[string]any{}
			if m.TLS.InsecureSkipVerify {
				tls["insecure_skip_verify"] = true
			}
			if m.TLS.ServerName != "" {
				tls["server_name"] = lit(m.TLS.ServerName)
			}
			if m.TLS.CA != nil {
				file, err := b.mountFile(t, fmt.Sprintf("metrics[%d].tls.ca", i), m.TLS.CA)
				if err != nil {
					return nil, err
				}
				tls["ca_file"] = file
			}
			job["tls_config"] = tls
		}
		if b.pods(t) {
			job["kubernetes_sd_configs"] = []any{b.podSD(t)}
			job["relabel_configs"] = podRelabels(m.Port)
		} else {
			job["static_configs"] = []any{map[string]any{"targets": strs(lit(net.JoinHostPort(t.Address, strconv.Itoa(m.Port))))}}
		}
		if mr := metricRelabels(m); len(mr) > 0 {
			job["metric_relabel_configs"] = mr
		}
		jobs = append(jobs, job)
	}
	return map[string]any{"config": map[string]any{"scrape_configs": jobs}}, nil
}

// podRelabels turn pod discovery into one target per running pod on port.
//
// Pod discovery yields a target per declared container port, plus one per
// container that declares none, so a port the pod listens on without
// declaring it (valid in a manifest) has no target of its own. The relabels
// therefore ignore the declared ports: they keep every running, non-init
// container's target and point each at <pod IP>:port. Nothing per-container
// survives into the final labels (job, instance = the address, pod, and
// NamespaceLabel), so a pod's targets are identical and Prometheus's scrape
// pool keeps one of them: one scrape, and one `up`, per pod.
//
// A target label wins over an exporter's label of the same name (honor_labels
// is off), which Prometheus renames to exported_<name>. So the pod's namespace
// is NOT stamped as `namespace`, the label an exporter that reports on other
// objects (Flux, External Secrets, cert-manager, kube-state-metrics) uses for
// the namespace of what it reports on: it is stamped as NamespaceLabel, which
// no exporter writes, and the target's transform moves it to the resource
// attribute k8s.namespace.name. job and instance are Prometheus's own and the
// receiver moves them to the resource (service.name, service.instance.id), as
// any Prometheus does. pod is kept as it is: it is the per-pod identity the
// scrape pool collapses on, and an exporter that also writes `pod` sees it
// renamed exported_pod, as under any Prometheus pod scrape.
func podRelabels(port int) []any {
	p := strconv.Itoa(port)
	return []any{
		// A pod that is not running has no address to scrape.
		map[string]any{"source_labels": strs("__meta_kubernetes_pod_phase"), "regex": "Running", "action": "keep"},
		// An init container's target names no container that serves.
		map[string]any{"source_labels": strs("__meta_kubernetes_pod_container_init"), "regex": "true", "action": "drop"},
		// The address is the pod's IP on the declared port, declared or not
		// in the pod spec; an IPv6 address is bracketed.
		map[string]any{"source_labels": strs("__meta_kubernetes_pod_ip"), "regex": "([^:]+)", "target_label": "__address__", "replacement": lit("${1}:" + p), "action": "replace"},
		map[string]any{"source_labels": strs("__meta_kubernetes_pod_ip"), "regex": "(.+:.+)", "target_label": "__address__", "replacement": lit("[${1}]:" + p), "action": "replace"},
		map[string]any{"source_labels": strs("__meta_kubernetes_pod_name"), "target_label": "pod", "action": "replace"},
		map[string]any{"source_labels": strs("__meta_kubernetes_namespace"), "target_label": NamespaceLabel, "action": "replace"},
	}
}

// namespaceStatements move a pod scrape's NamespaceLabel from each datapoint
// to its resource's k8s.namespace.name. Every datapoint of a resource is one
// target's, so they agree; a datapoint without the label (a check's) is left
// alone.
func namespaceStatements() []string {
	l := ottlString(NamespaceLabel)
	return []string{
		`set(resource.attributes["k8s.namespace.name"], datapoint.attributes[` + l + `]) where datapoint.attributes[` + l + `] != nil`,
		`delete_key(datapoint.attributes, ` + l + `)`,
	}
}

// podSD is t's Kubernetes pod discovery: its namespaces, its selector
// (server-side), and on a per-node collector, its node.
func (b *builder) podSD(t telemetry.Target) map[string]any {
	sel := map[string]any{"role": "pod", "label": labelSelector(t.Selector)}
	if b.opts.Discovery == DiscoverNodePods {
		sel["field"] = "spec.nodeName=${env:" + b.opts.NodeNameEnv + "}"
	}
	sd := map[string]any{"role": "pod", "selectors": []any{sel}}
	if len(t.Namespaces) > 0 {
		sd["namespaces"] = map[string]any{"names": strs(t.Namespaces...)}
	}
	return sd
}

// labelSelector is sel as a Kubernetes equality selector, keys sorted.
func labelSelector(sel map[string]string) string {
	parts := make([]string, 0, len(sel))
	for _, k := range sortedKeys(sel) {
		parts = append(parts, k+"="+sel[k])
	}
	return strings.Join(parts, ",")
}

// metricRelabels keep `up` and the allowed names, drop the denied ones, and
// remove the dropped labels. Prometheus anchors each regex.
func metricRelabels(m telemetry.MetricsScrape) []any {
	var out []any
	if m.Allow != "" {
		out = append(out, map[string]any{"source_labels": strs("__name__"), "regex": lit("up|(?:" + m.Allow + ")"), "action": "keep"})
	}
	if m.Deny != "" {
		out = append(out, map[string]any{"source_labels": strs("__name__"), "regex": lit(m.Deny), "action": "drop"})
	}
	if len(m.DropLabels) > 0 {
		quoted := make([]string, len(m.DropLabels))
		for i, l := range m.DropLabels {
			quoted[i] = regexp.QuoteMeta(l)
		}
		sort.Strings(quoted)
		out = append(out, map[string]any{"regex": lit(strings.Join(quoted, "|")), "action": "labeldrop"})
	}
	return out
}

// checks renders t's health checks: a tcpcheck or httpcheck receiver per
// check, through a receiver_creator when t's pods are discovered. It returns
// the receivers' names.
func (b *builder) checks(t telemetry.Target) []string {
	if len(t.Checks) == 0 {
		return nil
	}
	svc := t.ServiceName
	if b.pods(t) {
		b.observerUsed = true
		templates := map[string]any{}
		for i, c := range t.Checks {
			kind := c.Kind + "check"
			templates[kind+"/"+checkName(c, i)] = map[string]any{
				"rule":   lit(podRule(t)),
				"config": checkConfig(c, podEndpoint(c.Port)),
			}
		}
		name := "receiver_creator/telemetry_" + svc + "_checks"
		b.f.Receivers[name] = map[string]any{"watch_observers": strs(ObserverExtension), "receivers": templates}
		return []string{name}
	}
	var names []string
	for i, c := range t.Checks {
		name := c.Kind + "check/telemetry_" + svc + "_" + checkName(c, i)
		b.f.Receivers[name] = checkConfig(c, lit(net.JoinHostPort(t.Address, strconv.Itoa(c.Port))))
		names = append(names, name)
	}
	return names
}

// checkName is c's receiver-name suffix: its name, else its index. Validate
// refuses an all-digit name, so the two never collide.
func checkName(c telemetry.HealthCheck, i int) string {
	if c.Name != "" {
		return c.Name
	}
	return strconv.Itoa(i)
}

// checkConfig is a check's receiver config against hostport (host:port, or
// a podEndpoint expression).
func checkConfig(c telemetry.HealthCheck, hostport string) map[string]any {
	endpoint := hostport
	target := map[string]any{"endpoint": endpoint}
	if c.Kind == "http" {
		target = map[string]any{"endpoint": c.Scheme + "://" + hostport + lit(c.Path), "method": "GET"}
	}
	return map[string]any{"collection_interval": c.Interval, "targets": []any{target}}
}

// podRule is receiver_creator's rule for t's pods: the k8s_observer's pod
// endpoints (one per pod) in its namespaces, carrying every selector label.
// It matches pods rather than their declared container ports, which a pod
// need not declare for the port it serves on; podEndpoint names the port.
func podRule(t telemetry.Target) string {
	parts := []string{`type == "pod"`}
	if len(t.Namespaces) > 0 {
		quoted := make([]string, len(t.Namespaces))
		for i, n := range t.Namespaces {
			quoted[i] = strconv.Quote(n)
		}
		parts = append(parts, "namespace in ["+strings.Join(quoted, ", ")+"]")
	}
	for _, k := range sortedKeys(t.Selector) {
		parts = append(parts, fmt.Sprintf("labels[%s] == %s", strconv.Quote(k), strconv.Quote(t.Selector[k])))
	}
	return strings.Join(parts, " && ")
}

// podEndpoint is a podRule receiver's host:port: the pod endpoint's IP
// (bracketed when it is IPv6) on port.
func podEndpoint(port int) string {
	return "`" + `endpoint contains ":" ? "[" + endpoint + "]" : endpoint` + "`:" + strconv.Itoa(port)
}
