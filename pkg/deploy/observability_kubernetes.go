package deploy

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// The cluster collector (kcl/schema.k KubernetesInventory): one collector
// per cluster that opts in, beside the node agent and exporting to the same
// destination with the same credential. Where the node agent collects what
// each node sees (pods' telemetry, kubelet stats, logs), this one reads the
// cluster's own objects through the API server: Nodes, DaemonSets and
// Deployments, and Events. It is a single replica, because k8sobjects read
// by two would ship every row twice.
//
// Every row it ships is a log record carrying the resource attribute
// ClusterNameAttribute (the agent's cluster, its kube context), k8sobjects'
// own `k8s.resource.name` attribute ("nodes", "daemonsets", "deployments",
// "events") and, as its body, the object projected onto inventoryFields (an
// Event: the watch envelope, `{type, object}`, with the object projected).
// It sets no service.name.
const (
	// ClusterCollectorName names the cluster collector's Deployment,
	// ConfigMap and ServiceAccount; its ClusterRole and binding are
	// "<namespace>-cluster-collector".
	ClusterCollectorName = "cluster-collector"
	// ClusterNameAttribute is the resource attribute naming the cluster
	// (its kube context) on the cluster collector's rows and, with
	// KubernetesInventory, on the node agent's scrapes.
	ClusterNameAttribute = "k8s.cluster.name"
	// NodeNameAttribute is the resource attribute naming the node on the
	// node agent's scrapes, with KubernetesInventory.
	NodeNameAttribute = "k8s.node.name"
	// scrapeClusterProcessor is the node agent's processor stamping the
	// cluster and node on its scrape pipelines.
	scrapeClusterProcessor    = "resource/k8s_cluster"
	clusterCollectorMemoryMiB = 256
	// inventoryProcessor projects each object onto inventoryFields.
	inventoryProcessor = "transform/inventory_allowlist"
	// clusterCollectorConfigAnnotation is the pod-template annotation
	// holding the SHA-256 of the collector's config, so a config change
	// rolls the pod: the collector reads its config only at startup.
	clusterCollectorConfigAnnotation = "forge.dev/config-sha256"
)

// inventoryFields are the only object fields the cluster collector ships,
// as paths into the object. A whole Deployment or DaemonSet carries what
// must never reach telemetry: literal credentials in container env values
// and args, and the last-applied-configuration annotation copying them. So
// the body is wiped and rebuilt from this allowlist, and a field a new
// Kubernetes release adds, an annotation, an env value or a status message
// is dropped because it is not named here. A map copied whole holds only
// resource quantities (a Node's capacity and allocatable).
var inventoryFields = [][]string{
	{"kind"}, {"apiVersion"},
	{"metadata", "name"}, {"metadata", "namespace"}, {"metadata", "uid"},
	{"metadata", "creationTimestamp"}, {"metadata", "deletionTimestamp"}, {"metadata", "generation"},
	// Nodes.
	{"spec", "unschedulable"}, {"spec", "providerID"},
	{"status", "capacity"}, {"status", "allocatable"},
	{"status", "nodeInfo", "architecture"}, {"status", "nodeInfo", "operatingSystem"},
	{"status", "nodeInfo", "osImage"}, {"status", "nodeInfo", "kernelVersion"},
	{"status", "nodeInfo", "kubeletVersion"}, {"status", "nodeInfo", "containerRuntimeVersion"},
	// Deployments and DaemonSets.
	{"spec", "replicas"},
	{"status", "observedGeneration"}, {"status", "replicas"}, {"status", "readyReplicas"},
	{"status", "availableReplicas"}, {"status", "updatedReplicas"}, {"status", "unavailableReplicas"},
	{"status", "desiredNumberScheduled"}, {"status", "currentNumberScheduled"}, {"status", "numberReady"},
	{"status", "numberAvailable"}, {"status", "numberUnavailable"}, {"status", "numberMisscheduled"},
	{"status", "updatedNumberScheduled"},
	// events.k8s.io Events. Not `note`: free text a controller writes.
	{"type"}, {"reason"}, {"action"}, {"reportingController"}, {"eventTime"},
	{"deprecatedCount"}, {"deprecatedFirstTimestamp"}, {"deprecatedLastTimestamp"},
	{"series", "count"}, {"series", "lastObservedTime"},
	{"regarding", "apiVersion"}, {"regarding", "kind"}, {"regarding", "namespace"},
	{"regarding", "name"}, {"regarding", "uid"},
}

// inventoryLabels are the metadata.labels keys shipped: the ones naming
// what a component is and who installed it. Any other label is dropped.
var inventoryLabels = []string{
	"app.kubernetes.io/name", "app.kubernetes.io/instance", "app.kubernetes.io/version",
	"app.kubernetes.io/component", "app.kubernetes.io/part-of", "app.kubernetes.io/managed-by",
	"helm.sh/chart", "forge.dev/component-owner",
}

// inventoryLists are the lists shipped, each element projected onto its
// fields and the list cut at max elements (OTTL has no loop, so each
// element is copied by index).
var inventoryLists = []struct {
	path   []string
	fields []string
	max    int
}{
	{[]string{"spec", "template", "spec", "containers"}, []string{"name", "image"}, 8},
	{[]string{"status", "conditions"}, []string{"type", "status", "reason"}, 16},
}

// ottlPath renders root followed by each key as an OTTL map index.
func ottlPath(root string, keys ...string) string {
	var b strings.Builder
	b.WriteString(root)
	for _, k := range keys {
		b.WriteString("[")
		b.WriteString(strconv.Quote(k))
		b.WriteString("]")
	}
	return b.String()
}

// inventoryStatements are the OTTL log statements projecting a k8sobjects
// body onto inventoryFields, inventoryLabels and inventoryLists. They wipe
// the body before copying anything back, so whatever fails afterwards (a
// type a path does not expect, an index past a list's end) leaves a field
// out, never the original body in. A pull body is the object; a watch body
// is the envelope `{type, object}`, whose type is kept and object projected.
func inventoryStatements() []any {
	const raw, obj, out = `log.cache["raw"]`, `log.cache["obj"]`, `log.cache["out"]`
	watch := "IsMap(" + raw + `["object"])`
	st := []string{
		"set(" + raw + ", log.body)",
		"set(log.body, {})",
		"set(" + obj + ", " + raw + `["object"]) where ` + watch,
		"set(" + obj + ", " + raw + ") where not " + watch,
	}
	copyField := func(path ...string) {
		src := ottlPath(obj, path...)
		st = append(st, "set("+ottlPath(out, path...)+", "+src+") where "+src+" != nil")
	}
	for _, f := range inventoryFields {
		copyField(f...)
	}
	for _, l := range inventoryLabels {
		copyField("metadata", "labels", l)
	}
	for li, l := range inventoryLists {
		list := ottlPath(obj, l.path...)
		var items []string
		for i := range l.max {
			item := fmt.Sprintf(`log.cache["list%d_%d"]`, li, i)
			var kv []string
			for _, f := range l.fields {
				kv = append(kv, strconv.Quote(f)+": "+list+"["+strconv.Itoa(i)+"]"+ottlPath("", f))
			}
			st = append(st, "set("+item+", {"+strings.Join(kv, ", ")+"})")
			items = append(items, item)
			cmp := "=="
			if i == l.max-1 {
				cmp = ">="
			}
			st = append(st, "set("+ottlPath(out, l.path...)+", ["+strings.Join(items, ", ")+"]) where Len("+list+") "+cmp+" "+strconv.Itoa(i+1))
		}
	}
	st = append(st,
		`set(log.body["type"], `+raw+`["type"]) where `+watch,
		`set(log.body["object"], `+out+") where "+watch,
		"set(log.body, "+out+") where not "+watch,
	)
	return strs(st...)
}

// KubernetesInventory is what a cluster's ObservabilityAgent record asks
// for when the env opts in (kcl/schema.k KubernetesInventory). Nil renders
// none.
type KubernetesInventory struct {
	// Cluster is the agent's cluster (its kube context), stamped as
	// ClusterNameAttribute.
	Cluster string `json:"cluster"`
	// NodeInterval is how often Nodes are listed; WorkloadInterval how
	// often DaemonSets and Deployments are. Collector durations ("60s").
	NodeInterval     string `json:"nodeInterval"`
	WorkloadInterval string `json:"workloadInterval"`
	// Events watches events.k8s.io Events.
	Events bool `json:"events"`
}

var durationRE = regexp.MustCompile(`^[0-9]+(ms|s|m|h)$`)

func (c KubernetesInventory) validate() error {
	if c.Cluster == "" {
		return fmt.Errorf("kubernetes inventory: cluster is required")
	}
	for field, v := range map[string]string{"nodeInterval": c.NodeInterval, "workloadInterval": c.WorkloadInterval} {
		if !durationRE.MatchString(v) {
			return fmt.Errorf("kubernetes inventory: %s %q is not a duration like 60s", field, v)
		}
	}
	return nil
}

// ClusterCollectorConfig is the cluster collector's configuration, as YAML.
func (s ObservabilityAgentSpec) ClusterCollectorConfig(c KubernetesInventory) (string, error) {
	if err := s.validate(); err != nil {
		return "", err
	}
	if err := c.validate(); err != nil {
		return "", err
	}
	objects := []any{
		map[string]any{"name": "nodes", "mode": "pull", "interval": c.NodeInterval},
		map[string]any{"name": "daemonsets", "group": "apps", "mode": "pull", "interval": c.WorkloadInterval},
		map[string]any{"name": "deployments", "group": "apps", "mode": "pull", "interval": c.WorkloadInterval},
	}
	if c.Events {
		objects = append(objects, map[string]any{"name": "events", "group": "events.k8s.io", "mode": "watch"})
	}
	// No persistent queue: a Deployment has no host directory to keep one
	// in, and a pull re-reads the whole inventory on its next interval.
	exp := s.exporter()
	delete(exp["sending_queue"].(map[string]any), "storage")
	extensions := map[string]any{"health_check": map[string]any{"endpoint": "0.0.0.0:13133"}}
	extNames := []string{"health_check"}
	if s.TokenSecret != nil {
		extensions[bearerAuthName] = bearerExtension()
		extNames = append(extNames, bearerAuthName)
	}
	limit := clusterCollectorMemoryMiB
	cfg := map[string]any{
		"extensions": extensions,
		"receivers": map[string]any{
			"k8sobjects": map[string]any{"auth_type": "serviceAccount", "objects": objects},
		},
		"processors": map[string]any{
			"memory_limiter": map[string]any{"check_interval": "1s", "limit_mib": limit * 80 / 100, "spike_limit_mib": limit * 20 / 100},
			// error_mode silent: a statement that fails on one object (a
			// list shorter than an index it copies) only leaves that field
			// out, and the body was wiped first (inventoryStatements).
			inventoryProcessor: map[string]any{
				"error_mode":     "silent",
				"log_statements": []any{map[string]any{"context": "log", "statements": inventoryStatements()}},
			},
			"resource": map[string]any{"attributes": []any{
				map[string]any{"key": ClusterNameAttribute, "value": c.Cluster, "action": "upsert"},
			}},
			"batch": map[string]any{},
		},
		"exporters": map[string]any{s.exporterName(): exp},
		"service": map[string]any{
			"extensions": strs(extNames...),
			"pipelines": map[string]any{
				"logs": map[string]any{
					"receivers":  strs("k8sobjects"),
					"processors": strs("memory_limiter", inventoryProcessor, "resource", "batch"),
					"exporters":  strs(s.exporterName()),
				},
			},
		},
	}
	return marshalConfig(cfg)
}

// stampScrapeCluster adds ClusterNameAttribute and NodeNameAttribute to
// each of the node agent's declared-telemetry scrape pipelines
// (`metrics/telemetry_<service>`), before its batch processor, when the
// agent's cluster opts in to its Kubernetes inventory, so a scraped row
// joins the inventory's Nodes. Other pipelines are untouched.
func (s ObservabilityAgentSpec) stampScrapeCluster(cfg map[string]any) {
	if s.KubernetesInventory == nil {
		return
	}
	pipelines, _ := cfg["service"].(map[string]any)["pipelines"].(map[string]any)
	processors, _ := cfg["processors"].(map[string]any)
	stamped := false
	for _, t := range s.declaredTelemetry() {
		p, ok := pipelines["metrics/telemetry_"+t.ServiceName].(map[string]any)
		if !ok {
			continue
		}
		stamped = true
		var out []any
		inserted := false
		for _, v := range p["processors"].([]any) {
			if v == "batch" && !inserted {
				out = append(out, scrapeClusterProcessor)
				inserted = true
			}
			out = append(out, v)
		}
		if !inserted {
			out = append(out, scrapeClusterProcessor)
		}
		p["processors"] = out
	}
	if stamped {
		// The agent pulls only its own node's pods (DiscoverNodePods; an
		// address it would pull from every node is refused), so every
		// scraped row's pod runs on K8S_NODE_NAME. A scrape row otherwise
		// carries no k8s.node.name, and an exporter's own host label is
		// often not a node name.
		processors[scrapeClusterProcessor] = map[string]any{"attributes": []any{
			map[string]any{"key": ClusterNameAttribute, "value": s.KubernetesInventory.Cluster, "action": "upsert"},
			map[string]any{"key": NodeNameAttribute, "value": "${env:K8S_NODE_NAME}", "action": "upsert"},
		}}
	}
}

// renderClusterCollector renders the cluster collector's ServiceAccount,
// read-only ClusterRole + binding, ConfigMap and one-replica Deployment.
func renderClusterCollector(s ObservabilityAgentSpec, c KubernetesInventory, ctx ObservabilityContext) ([]runtime.Object, error) {
	cfg, err := s.ClusterCollectorConfig(c)
	if err != nil {
		return nil, fmt.Errorf("cluster collector: %w", err)
	}
	// The collector reads its config once, at startup, and a ConfigMap
	// edit restarts nothing: the digest in the pod template is what rolls
	// the pod when the config changes (events turned off, an interval).
	sum := sha256.Sum256([]byte(cfg))
	digest := hex.EncodeToString(sum[:])
	name, ns := ClusterCollectorName, ctx.Namespace
	labels := managedLabels(name, ctx.PartOf)
	selector := map[string]string{LabelName: name}
	read := []string{"get", "list", "watch"}
	sa := &corev1.ServiceAccount{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceAccount"}, ObjectMeta: objectMeta(name, ns, labels)}
	// Read-only, and only what the receiver lists: no Secrets, no
	// nodes/proxy, no write verb, and no Events unless it watches them.
	rules := []rbacv1.PolicyRule{
		{APIGroups: []string{""}, Resources: []string{"nodes"}, Verbs: read},
		{APIGroups: []string{"apps"}, Resources: []string{"daemonsets", "deployments"}, Verbs: read},
	}
	if c.Events {
		rules = append(rules, rbacv1.PolicyRule{APIGroups: []string{"events.k8s.io"}, Resources: []string{"events"}, Verbs: read})
	}
	role := &rbacv1.ClusterRole{
		TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRole"},
		ObjectMeta: objectMeta(ns+"-"+name, "", labels),
		Rules:      rules,
	}
	binding := &rbacv1.ClusterRoleBinding{
		TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRoleBinding"},
		ObjectMeta: objectMeta(ns+"-"+name, "", labels),
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: ns + "-" + name},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: name, Namespace: ns}},
	}
	cm := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: objectMeta(name, ns, labels),
		Data:       map[string]string{"config.yaml": cfg},
	}
	limit := clusterCollectorMemoryMiB
	env := []corev1.EnvVar{{Name: "GOMEMLIMIT", Value: strconv.Itoa(limit*80/100) + "MiB"}}
	if s.HeadersSecret != nil {
		env = append(env, corev1.EnvVar{Name: agentHeadersEnv, ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: s.HeadersSecret.Name}, Key: s.HeadersSecret.Key},
		}})
	}
	volumes := []corev1.Volume{{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: name}}}}}
	mounts := []corev1.VolumeMount{{Name: "config", MountPath: "/etc/otel", ReadOnly: true}}
	if s.TokenSecret != nil {
		// The node agent's token Secret, mounted the same way (a whole
		// directory, optional) so a renewal reaches it with no restart.
		optional := true
		volumes = append(volumes, corev1.Volume{Name: "otlp-token", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
			SecretName: s.TokenSecret.Name,
			Optional:   &optional,
			Items:      []corev1.KeyToPath{{Key: s.TokenSecret.Key, Path: agentTokenFile}},
		}}})
		mounts = append(mounts, corev1.VolumeMount{Name: "otlp-token", MountPath: agentTokenDir, ReadOnly: true})
	}
	replicas, nonRoot, noEscalate, readOnly, uid := int32(1), true, false, true, int64(10001)
	probe := corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/", Port: intstr.FromInt32(13133)}}
	dep := &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: objectMeta(name, ns, labels),
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: selector},
			// Never two at once: both would ship the same inventory.
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: copyLabels(labels), Annotations: map[string]string{clusterCollectorConfigAnnotation: digest}},
				Spec: corev1.PodSpec{
					ServiceAccountName: name,
					EnableServiceLinks: new(false),
					Containers: []corev1.Container{{
						Name:           "collector",
						Image:          s.Image,
						Args:           []string{"--config=/etc/otel/config.yaml"},
						Env:            env,
						Ports:          []corev1.ContainerPort{{Name: "health", ContainerPort: 13133, Protocol: corev1.ProtocolTCP}},
						ReadinessProbe: &corev1.Probe{ProbeHandler: probe},
						LivenessProbe:  &corev1.Probe{ProbeHandler: probe, InitialDelaySeconds: 10},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("20m"), corev1.ResourceMemory: resource.MustParse(strconv.Itoa(limit/2) + "Mi")},
							Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(strconv.Itoa(limit) + "Mi")},
						},
						SecurityContext: &corev1.SecurityContext{
							RunAsNonRoot:             &nonRoot,
							RunAsUser:                &uid,
							AllowPrivilegeEscalation: &noEscalate,
							ReadOnlyRootFilesystem:   &readOnly,
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
							SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
						},
						VolumeMounts: mounts,
					}},
					Volumes: volumes,
				},
			},
		},
	}
	return []runtime.Object{sa, role, binding, cm, dep}, nil
}
