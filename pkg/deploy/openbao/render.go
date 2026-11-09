package openbao

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/reliant-labs/forge/pkg/stsresize"
)

// ConvergeScript is the script the converge Jobs run. It is embedded so the
// Job's content address covers it: a change to the script re-runs the converge.
//
//go:embed converge.sh
var ConvergeScript string

// Names of what the component expects from, and hands to, its neighbours.
const (
	// StaticKeyKey is the key, in the Secret [Spec.UnsealSecret], holding the
	// static seal key (hex or base64 of 32 bytes).
	StaticKeyKey = "static-key"
	// ConnectionURLKey is the key, in [Spec.StorageSecret], holding the
	// Postgres DSN.
	ConnectionURLKey = "connection-url"
	// RunAsUser/RunAsGroup are the openbao image's own identity.
	runAsUser  int64 = 100
	runAsGroup int64 = 1000
	// convergeUser is the converge Jobs' non-root identity.
	convergeUser int64 = 65532
	// exitRefused is converge.sh's exit status for a verdict a retry cannot
	// change; the Job's podFailurePolicy fails the Job on it at once.
	exitRefused = 64
)

// DefaultCloudSQLProxyImage is the Cloud SQL Auth Proxy sidecar.
const DefaultCloudSQLProxyImage = "gcr.io/cloud-sql-connectors/cloud-sql-proxy:2.14.1"

// DefaultKubectlImage runs the converge Jobs: kubectl, curl and jq, pinned by
// digest (the image forge's StatefulSet resize Job already runs).
const DefaultKubectlImage = stsresize.DefaultImage

// WithDefaults fills the images a record may leave empty.
func (s Spec) WithDefaults() Spec {
	if s.KubectlImage == "" {
		s.KubectlImage = DefaultKubectlImage
	}
	if s.CloudSQLProxyImage == "" && s.Storage.Postgres != nil && s.Storage.Postgres.CloudSQLInstance != "" {
		s.CloudSQLProxyImage = DefaultCloudSQLProxyImage
	}
	if s.Bootstrap != nil && s.Hooks.Escrow == "" {
		s.Hooks.Escrow = EscrowConfigMap(s.Name)
	}
	return s
}

// UnsealSecret is the in-cluster Secret the static seal key is read from.
func (s Spec) UnsealSecret() string { return s.Name + "-unseal" }

// StorageSecret is the in-cluster Secret the Postgres DSN is read from.
func (s Spec) StorageSecret() string { return s.Name + "-storage" }

// TLSSecret is the Secret holding the server certificate.
func (s Spec) TLSSecret() string {
	if s.TLS.Secret != "" {
		return s.TLS.Secret
	}
	return s.Name + "-tls"
}

func (s Spec) internalName() string { return s.Name + "-internal" }
func (s Spec) raft() bool           { return s.Storage.Type == "raft" }
func (s Spec) apiPort() int         { return s.Listeners[0].Port }

// Host is the in-cluster DNS name of the active-node Service.
func (s Spec) Host() string { return s.Name + "." + s.Namespace + ".svc" }

// ServerURL is the base URL clients (ESO's SecretStores) use in-cluster.
func (s Spec) ServerURL() string { return "https://" + s.Host() + ":" + strconv.Itoa(s.apiPort()) }

func (s Spec) labels(extra ...string) map[string]string {
	l := map[string]string{
		LabelName: s.Name, LabelManagedBy: "forge", LabelComponent: "openbao", LabelOwner: s.Owner,
	}
	if i := strings.Index(s.Owner, "."); i > 0 {
		l[LabelPartOf] = s.Owner[:i]
	}
	for i := 0; i+1 < len(extra); i += 2 {
		l[extra[i]] = extra[i+1]
	}
	return l
}

func (s Spec) selector() map[string]string { return map[string]string{LabelName: s.Name} }

// Render expands the spec into the objects that run it. The objects carry no
// forge.dev/cluster or forge.dev/env stamp: the caller copies the record's.
func Render(s Spec) ([]*unstructured.Unstructured, error) {
	s = s.WithDefaults()
	if err := s.Validate(); err != nil {
		return nil, fmt.Errorf("openbao %s/%s: %w", s.Namespace, s.Name, err)
	}
	doc, err := BuildAccess(s)
	if err != nil {
		return nil, fmt.Errorf("openbao %s/%s: access document: %w", s.Namespace, s.Name, err)
	}

	var objs []any
	add := func(o ...any) { objs = append(objs, o...) }

	add(s.serviceAccount(s.Name, nil))
	if s.Storage.Type == "postgres" && s.Storage.Postgres.GCPServiceAccount != "" {
		sa := s.serviceAccount(s.Name, map[string]string{"iam.gke.io/gcp-service-account": s.Storage.Postgres.GCPServiceAccount})
		objs[len(objs)-1] = sa
	}
	if s.Writer.Type == "service_account" {
		add(s.serviceAccount(s.Writer.ServiceAccount, nil))
	}
	cfg, err := s.serverConfig()
	if err != nil {
		return nil, err
	}
	add(s.configMap(s.Name+"-config", map[string]string{"config.hcl": cfg}))
	add(s.service(s.Name, false), s.service(s.internalName(), true))
	if s.TLS.Secret == "" {
		add(s.certManagerObjects()...)
	}
	if s.raft() {
		add(s.statefulSet(cfg))
	} else {
		add(s.deployment(cfg))
	}
	if s.Replicas > 1 && s.HA.PDB {
		add(s.pdb())
	}
	add(s.networkPolicy())
	add(s.publicObjects()...)
	if s.Bootstrap != nil {
		add(s.bootstrapObjects()...)
	}

	conv, err := s.converge(doc)
	if err != nil {
		return nil, err
	}
	add(conv...)

	out := make([]*unstructured.Unstructured, 0, len(objs))
	for _, o := range objs {
		m, err := toMap(o)
		if err != nil {
			return nil, err
		}
		u := &unstructured.Unstructured{Object: m}
		if s.ReleaseOwner != "" {
			owners, _ := json.Marshal([]string{s.ReleaseOwner})
			annotations := u.GetAnnotations()
			if annotations == nil {
				annotations = map[string]string{}
			}
			annotations["forge.dev/release-owners"] = string(owners)
			u.SetAnnotations(annotations)
		}
		out = append(out, u)
	}
	return out, nil
}

func toMap(o any) (map[string]any, error) {
	if m, ok := o.(map[string]any); ok {
		return m, nil
	}
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(o)
	if err != nil {
		return nil, fmt.Errorf("convert %T: %w", o, err)
	}
	dropNils(m)
	return m, nil
}

func dropNils(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if val == nil {
				delete(t, k)
				continue
			}
			if k == "creationTimestamp" {
				delete(t, k)
				continue
			}
			dropNils(val)
		}
		if st, ok := t["status"].(map[string]any); ok && len(st) == 0 {
			delete(t, "status")
		}
	case []any:
		for _, val := range t {
			dropNils(val)
		}
	}
}

func (s Spec) meta(name string, extra ...string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: s.Namespace, Labels: s.labels(extra...)}
}

func (s Spec) serviceAccount(name string, annotations map[string]string) *corev1.ServiceAccount {
	m := s.meta(name)
	m.Annotations = annotations
	return &corev1.ServiceAccount{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceAccount"}, ObjectMeta: m}
}

func (s Spec) configMap(name string, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"}, ObjectMeta: s.meta(name), Data: data}
}

func (s Spec) service(name string, headless bool) *corev1.Service {
	var ports []corev1.ServicePort
	for i, l := range s.Listeners {
		n := "api"
		if i > 0 {
			n = "api-" + strconv.Itoa(l.Port)
		}
		ports = append(ports, corev1.ServicePort{Name: n, Port: int32(l.Port), TargetPort: intstr.FromInt32(int32(l.Port))})
	}
	if s.Public != nil {
		ports = append(ports, corev1.ServicePort{Name: publicPortName, Port: int32(s.Public.Port), TargetPort: intstr.FromInt32(int32(s.Public.Port))})
	}
	svc := &corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: s.meta(name),
		Spec:       corev1.ServiceSpec{Selector: s.selector(), Ports: ports},
	}
	if headless {
		svc.Spec.ClusterIP = corev1.ClusterIPNone
		svc.Spec.PublishNotReadyAddresses = true
		svc.Spec.Ports = append(svc.Spec.Ports, corev1.ServicePort{Name: "cluster", Port: ClusterPort, TargetPort: intstr.FromInt32(ClusterPort)})
	}
	return svc
}

func (s Spec) pdb() *policyv1.PodDisruptionBudget {
	one := intstr.FromInt32(1)
	return &policyv1.PodDisruptionBudget{
		TypeMeta:   metav1.TypeMeta{APIVersion: "policy/v1", Kind: "PodDisruptionBudget"},
		ObjectMeta: s.meta(s.Name),
		Spec:       policyv1.PodDisruptionBudgetSpec{MaxUnavailable: &one, Selector: &metav1.LabelSelector{MatchLabels: s.selector()}},
	}
}

// dnsNames are the names the server certificate answers to: the active
// Service, every per-pod name behind the headless Service, and what a caller
// brought (the public host).
func (s Spec) dnsNames() []string {
	svc, hl := s.Name, s.internalName()
	n := []string{
		svc, svc + "." + s.Namespace, svc + "." + s.Namespace + ".svc", svc + "." + s.Namespace + ".svc.cluster.local",
		hl, hl + "." + s.Namespace + ".svc", "*." + hl + "." + s.Namespace + ".svc",
		hl + "." + s.Namespace + ".svc.cluster.local", "*." + hl + "." + s.Namespace + ".svc.cluster.local",
		"localhost",
	}
	n = append(n, s.ExtraDNSNames...)
	slices.Sort(n)
	return slices.Compact(n)
}

// certManagerObjects renders the namespaced self-signed CA: a self-signed
// Issuer signs the CA Certificate, a CA Issuer backed by it signs the server
// Certificate. cert-manager (cert-manager.io/v1) is a prerequisite.
func (s Spec) certManagerObjects() []any {
	m := func(kind, name string, spec map[string]any) map[string]any {
		return map[string]any{
			"apiVersion": "cert-manager.io/v1", "kind": kind,
			"metadata": map[string]any{"name": name, "namespace": s.Namespace, "labels": toAnyMap(s.labels())},
			"spec":     spec,
		}
	}
	ref := func(n string) map[string]any {
		return map[string]any{"name": n, "kind": "Issuer", "group": "cert-manager.io"}
	}
	return []any{
		m("Issuer", s.Name+"-selfsigned", map[string]any{"selfSigned": map[string]any{}}),
		m("Certificate", s.Name+"-ca", map[string]any{
			"isCA": true, "commonName": s.Name + "-ca", "secretName": s.Name + "-ca", "issuerRef": ref(s.Name + "-selfsigned"),
			"privateKey": map[string]any{"algorithm": "ECDSA", "size": 256},
			"duration":   "87600h",
		}),
		m("Issuer", s.Name+"-ca", map[string]any{"ca": map[string]any{"secretName": s.Name + "-ca"}}),
		m("Certificate", s.Name+"-tls", map[string]any{
			"commonName": s.Name, "dnsNames": toAnySlice(s.dnsNames()), "ipAddresses": []any{"127.0.0.1"},
			"secretName": s.TLSSecret(), "issuerRef": ref(s.Name + "-ca"),
			"duration": s.TLS.Duration, "privateKey": map[string]any{"algorithm": "ECDSA", "size": 256, "rotationPolicy": "Always"},
			"usages": []any{"server auth", "client auth", "digital signature", "key encipherment"},
		}),
	}
}

func toAnyMap(in map[string]string) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func toAnySlice(in []string) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}

// ---- server config ---------------------------------------------------------

const (
	tlsDir   = "/openbao/tls"
	dataDir  = "/openbao/data"
	auditDir = "/openbao/audit"
)

func (s Spec) serverConfig() (string, error) {
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f, a...) }
	w("ui = false\ncluster_name = %q\n\n", s.Name)

	if s.raft() {
		w("storage \"raft\" {\n  path = %q\n", dataDir)
		// A single node has nobody to join. With more, every node lists every
		// peer (itself included, which OpenBao skips): they start together
		// under Parallel pod management and the joiners retry until the
		// initialised node is up. Auto-unseal lets a joiner open itself.
		for i := 0; s.Replicas > 1 && i < s.Replicas; i++ {
			peer := fmt.Sprintf("https://%s-%d.%s.%s.svc:%d", s.Name, i, s.internalName(), s.Namespace, s.apiPort())
			w("  retry_join {\n    leader_api_addr = %q\n    leader_ca_cert_file = \"%s/ca.crt\"\n  }\n", peer, tlsDir)
		}
		w("}\n\n")
	} else {
		w("storage \"postgresql\" {\n  ha_enabled = \"true\"\n  max_parallel = \"%d\"\n}\n\n", s.Storage.Postgres.MaxParallel)
	}

	for i, l := range s.Listeners {
		w("listener \"tcp\" {\n  address = \"0.0.0.0:%d\"\n", l.Port)
		if i == 0 {
			w("  cluster_address = \"0.0.0.0:%d\"\n", ClusterPort)
		}
		if i == 0 {
			w("  telemetry {\n    unauthenticated_metrics_access = true\n  }\n")
		}
		if l.TLS {
			w("  tls_cert_file = \"%s/tls.crt\"\n  tls_key_file = \"%s/tls.key\"\n  tls_min_version = \"tls12\"\n", tlsDir, tlsDir)
		} else {
			w("  tls_disable = true\n")
		}
		w("}\n\n")
	}

	w("%s", s.publicListener())
	w("seal \"static\" {\n  current_key_id = %q\n  current_key = \"env://BAO_STATIC_KEY\"\n}\n\n", s.Seal.KeyID)
	w("telemetry {\n  disable_hostname = true\n  prometheus_retention_time = \"24h\"\n}\n")
	if s.Audit.Stdout {
		w("\naudit \"file\" \"to-stdout\" {\n  options {\n    file_path = \"stdout\"\n  }\n}\n")
	}
	if s.Audit.File {
		w("\naudit \"file\" \"to-disk\" {\n  options {\n    file_path = %q\n  }\n}\n", s.Audit.FilePath)
	}
	return b.String(), nil
}

// ---- pods ------------------------------------------------------------------

func (s Spec) podTemplate(cfg string) corev1.PodTemplateSpec {
	restricted := &corev1.SecurityContext{
		RunAsNonRoot: new(true), RunAsUser: new(runAsUser), RunAsGroup: new(runAsGroup),
		ReadOnlyRootFilesystem: new(true), AllowPrivilegeEscalation: new(false),
		Capabilities:   &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
	apiURL := "https://$(POD_NAME)." + s.internalName() + "." + s.Namespace + ".svc:" + strconv.Itoa(s.apiPort())
	if !s.raft() {
		apiURL = s.ServerURL()
	}
	env := []corev1.EnvVar{
		{Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}},
		{Name: "POD_IP", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.podIP"}}},
		{Name: "BAO_API_ADDR", Value: apiURL},
		{Name: "BAO_CLUSTER_ADDR", Value: "https://$(POD_IP):" + strconv.Itoa(ClusterPort)},
		{Name: "BAO_STATIC_KEY", ValueFrom: secretKey(s.UnsealSecret(), StaticKeyKey)},
	}
	if s.raft() {
		env = append(env, corev1.EnvVar{Name: "BAO_RAFT_NODE_ID", Value: "$(POD_NAME)"})
	} else {
		env = append(env, corev1.EnvVar{Name: "BAO_PG_CONNECTION_URL", ValueFrom: secretKey(s.StorageSecret(), ConnectionURLKey)})
	}

	var ports []corev1.ContainerPort
	for i, l := range s.Listeners {
		n := "api"
		if i > 0 {
			n = "api-" + strconv.Itoa(l.Port)
		}
		ports = append(ports, corev1.ContainerPort{Name: n, ContainerPort: int32(l.Port)})
	}
	ports = append(ports, corev1.ContainerPort{Name: "cluster", ContainerPort: ClusterPort})
	if s.Public != nil {
		ports = append(ports, corev1.ContainerPort{Name: publicPortName, ContainerPort: int32(s.Public.Port)})
	}

	mounts := []corev1.VolumeMount{
		{Name: "config", MountPath: "/etc/bao", ReadOnly: true},
		{Name: "tls", MountPath: tlsDir, ReadOnly: true},
		{Name: "audit", MountPath: auditDir},
		{Name: "tmp", MountPath: "/tmp"},
	}
	vols := []corev1.Volume{
		{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: s.Name + "-config"}}}},
		{Name: "tls", VolumeSource: s.tlsVolumeSource()},
		{Name: "audit", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}
	if s.raft() {
		mounts = append(mounts, corev1.VolumeMount{Name: "data", MountPath: dataDir})
	}

	// A sealed or uninitialised node must not be routed to, and an
	// uninitialised one must not block a first rollout: uninitcode=200 keeps a
	// fresh store Ready so the converge Job can reach it and initialise it.
	// sealed stays 503: nothing routes to a sealed pod.
	probe := &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
			Path: "/v1/sys/health?standbyok=true&uninitcode=200", Port: intstr.FromInt32(int32(s.apiPort())), Scheme: corev1.URISchemeHTTPS,
		}},
		PeriodSeconds: 5, FailureThreshold: 3,
	}

	main := corev1.Container{
		Name: "openbao", Image: s.Image, Args: []string{"server", "-config=/etc/bao/config.hcl"},
		Env: env, Ports: ports, VolumeMounts: mounts, SecurityContext: restricted,
		ReadinessProbe: probe,
		StartupProbe: func() *corev1.Probe {
			p := probe.DeepCopy()
			p.FailureThreshold = 60
			return p
		}(),
		Lifecycle: &corev1.Lifecycle{PreStop: &corev1.LifecycleHandler{Exec: &corev1.ExecAction{Command: []string{"sleep", "5"}}}},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("128Mi")},
			Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")},
		},
	}

	pod := corev1.PodSpec{
		ServiceAccountName: s.Name, EnableServiceLinks: new(false),
		AutomountServiceAccountToken: new(false),
		PriorityClassName:            s.HA.PriorityClass,
		SecurityContext: &corev1.PodSecurityContext{
			RunAsNonRoot: new(true), RunAsUser: new(runAsUser), RunAsGroup: new(runAsGroup), FSGroup: new(runAsGroup),
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		Containers: []corev1.Container{main},
		Volumes:    vols,
	}
	if s.Replicas > 1 {
		sel := &metav1.LabelSelector{MatchLabels: s.selector()}
		if s.HA.ZoneSpread {
			pod.TopologySpreadConstraints = append(pod.TopologySpreadConstraints, corev1.TopologySpreadConstraint{
				MaxSkew: 1, TopologyKey: "topology.kubernetes.io/zone", WhenUnsatisfiable: corev1.ScheduleAnyway, LabelSelector: sel,
			})
		}
		// An HA store must not put two replicas on one node: a node loss would
		// take a Raft quorum, or the active node and its standby, with it. The
		// default is therefore a hard constraint (the SC7 prod adoption landed
		// both replicas on one node under a preference). With maxSkew 1 it still
		// lets 3 replicas share 2 nodes, and a cluster with a single node still
		// schedules (one domain, no skew). NodeTaintsPolicy Honor keeps a
		// cordoned node from counting as an empty domain, which would otherwise
		// leave the replacement of a drained pod Pending for good.
		node := corev1.TopologySpreadConstraint{
			MaxSkew: 1, TopologyKey: "kubernetes.io/hostname", WhenUnsatisfiable: corev1.ScheduleAnyway, LabelSelector: sel,
		}
		if s.HA.NodeSpread != NodeSpreadPreferred {
			honor := corev1.NodeInclusionPolicyHonor
			node.WhenUnsatisfiable = corev1.DoNotSchedule
			node.NodeTaintsPolicy = &honor
		}
		pod.TopologySpreadConstraints = append(pod.TopologySpreadConstraints, node)
	}
	if p := s.Storage.Postgres; p != nil && p.CloudSQLInstance != "" {
		always := corev1.ContainerRestartPolicyAlways
		args := []string{"--address=127.0.0.1", "--port=5432"}
		if p.PrivateIP {
			args = append(args, "--private-ip")
		}
		args = append(args, "--exit-zero-on-sigterm", "--max-sigterm-delay=20s", "--health-check", "--http-address=0.0.0.0", "--http-port=9801", p.CloudSQLInstance)
		hp := &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/startup", Port: intstr.FromInt32(9801)}}, PeriodSeconds: 2, FailureThreshold: 60}
		pod.InitContainers = []corev1.Container{{
			Name: "cloud-sql-proxy", Image: s.CloudSQLProxyImage, Args: args, RestartPolicy: &always,
			StartupProbe: hp,
			SecurityContext: &corev1.SecurityContext{
				RunAsNonRoot: new(true), RunAsUser: new(runAsUser), AllowPrivilegeEscalation: new(false),
				ReadOnlyRootFilesystem: new(true), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("20m"), corev1.ResourceMemory: resource.MustParse("32Mi")},
				Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")},
			},
		}}
	}
	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels: s.labels(),
			// OpenBao reads config.hcl once, at start.
			Annotations: map[string]string{"checksum/config": sum(cfg)},
		},
		Spec: pod,
	}
}

// tlsVolumeSource projects the server certificate and the CA that signed it
// into one directory. The CA comes from the issued Secret, or from the
// ConfigMap that publishes a BYO certificate's CA, so raft peers and the
// converge Job verify against the same file either way.
func (s Spec) tlsVolumeSource() corev1.VolumeSource {
	sources := []corev1.VolumeProjection{{Secret: &corev1.SecretProjection{
		LocalObjectReference: corev1.LocalObjectReference{Name: s.TLSSecret()},
		Items:                []corev1.KeyToPath{{Key: "tls.crt", Path: "tls.crt"}, {Key: "tls.key", Path: "tls.key"}},
	}}}
	sources = append(sources, s.caProjection())
	return corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: sources}}
}

func (s Spec) caProjection() corev1.VolumeProjection {
	items := []corev1.KeyToPath{{Key: "ca.crt", Path: "ca.crt"}}
	if s.TLS.Secret == "" {
		return corev1.VolumeProjection{Secret: &corev1.SecretProjection{LocalObjectReference: corev1.LocalObjectReference{Name: s.TLSSecret()}, Items: items}}
	}
	return corev1.VolumeProjection{ConfigMap: &corev1.ConfigMapProjection{LocalObjectReference: corev1.LocalObjectReference{Name: s.TLS.CAConfigMap}, Items: items}}
}

func secretKey(name, key string) *corev1.EnvVarSource {
	return &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: name}, Key: key}}
}

func (s Spec) statefulSet(cfg string) *appsv1.StatefulSet {
	size := s.Storage.Raft.SizeGiB
	pvc := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data", Labels: s.labels()},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(strconv.Itoa(size) + "Gi")}},
		},
	}
	if sc := s.Storage.Raft.StorageClass; sc != "" {
		pvc.Spec.StorageClassName = &sc
	}
	return &appsv1.StatefulSet{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "StatefulSet"},
		ObjectMeta: s.meta(s.Name),
		Spec: appsv1.StatefulSetSpec{
			Replicas: new(int32(s.Replicas)), ServiceName: s.internalName(),
			// Joiners start together and find the leader by retry_join; a
			// one-at-a-time start would hold pod-1 until pod-0 is Ready.
			PodManagementPolicy:  appsv1.ParallelPodManagement,
			UpdateStrategy:       appsv1.StatefulSetUpdateStrategy{Type: appsv1.RollingUpdateStatefulSetStrategyType},
			Selector:             &metav1.LabelSelector{MatchLabels: s.selector()},
			Template:             s.podTemplate(cfg),
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{pvc},
		},
	}
}

func (s Spec) deployment(cfg string) *appsv1.Deployment {
	one := intstr.FromInt32(1)
	zero := intstr.FromInt32(0)
	return &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: s.meta(s.Name),
		Spec: appsv1.DeploymentSpec{
			Replicas: new(int32(s.Replicas)), Selector: &metav1.LabelSelector{MatchLabels: s.selector()},
			Strategy:                appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType, RollingUpdate: &appsv1.RollingUpdateDeployment{MaxUnavailable: &zero, MaxSurge: &one}},
			ProgressDeadlineSeconds: new(int32(600)),
			Template:                s.podTemplate(cfg),
		},
	}
}

// ---- network ---------------------------------------------------------------

func (s Spec) networkPolicy() *networkingv1.NetworkPolicy {
	tcp := corev1.ProtocolTCP
	port := func(p int) networkingv1.NetworkPolicyPort {
		v := intstr.FromInt32(int32(p))
		return networkingv1.NetworkPolicyPort{Protocol: &tcp, Port: &v}
	}
	var apiPorts []networkingv1.NetworkPolicyPort
	for _, l := range s.Listeners {
		apiPorts = append(apiPorts, port(l.Port))
	}
	var from []networkingv1.NetworkPolicyPeer
	// The converge Jobs, in this namespace.
	from = append(from, networkingv1.NetworkPolicyPeer{PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{LabelName: s.Name + "-converge"}}})
	// The writer's tokens are minted for forge-secret-writer; callers reach
	// the store from wherever `forge secret set` port-forwards, which is a
	// pod in this namespace's network path, so no peer is added for it.
	for _, c := range s.Consumers {
		ns := c.Namespace
		if ns == "" {
			ns = s.Namespace
		}
		from = append(from, networkingv1.NetworkPolicyPeer{
			NamespaceSelector: nsSelector(ns),
			PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{LabelName: c.Workload}},
		})
	}
	ingNS := slices.Clone(s.IngressNamespaces)
	slices.Sort(ingNS)
	for _, ns := range slices.Compact(ingNS) {
		from = append(from, networkingv1.NetworkPolicyPeer{NamespaceSelector: nsSelector(ns)})
	}
	for _, p := range s.IngressFrom {
		peer := networkingv1.NetworkPolicyPeer{}
		if len(p.NamespaceLabels) > 0 {
			peer.NamespaceSelector = &metav1.LabelSelector{MatchLabels: p.NamespaceLabels}
		}
		if len(p.PodLabels) > 0 {
			peer.PodSelector = &metav1.LabelSelector{MatchLabels: p.PodLabels}
		}
		from = append(from, peer)
	}
	rules := []networkingv1.NetworkPolicyIngressRule{
		{From: from, Ports: apiPorts},
		{
			From:  []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{MatchLabels: s.selector()}}},
			Ports: append(slices.Clone(apiPorts), port(ClusterPort)),
		},
	}
	if r, ok := s.publicIngressRule(); ok {
		rules = append(rules, r)
	}
	return &networkingv1.NetworkPolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy"},
		ObjectMeta: s.meta(s.Name),
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: s.selector()},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress:     rules,
		},
	}
}

func nsSelector(ns string) *metav1.LabelSelector {
	return &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": ns}}
}

// ---- converge --------------------------------------------------------------

// Phases of converge.sh.
const (
	PhaseWiden    = "widen"
	PhaseConverge = "converge"
)

func (s Spec) converge(doc AccessDoc) ([]any, error) {
	raw, err := doc.JSON()
	if err != nil {
		return nil, err
	}
	cmName := s.Name + "-access-" + sum(ConvergeScript + string(raw))[:10]
	cm := s.configMap(cmName, map[string]string{"converge.sh": ConvergeScript, "access.json": string(raw) + "\n"})
	cm.Labels[LabelName] = s.Name + "-converge"

	base := s.Name + "-converge"
	var saAnnotations map[string]string
	if s.Bootstrap != nil {
		saAnnotations = map[string]string{AnnotationGCPServiceAccount: s.Bootstrap.EscrowGSA}
	}
	sa := s.serviceAccount(ConvergeServiceAccount(s), saAnnotations)
	sa.Labels[LabelName] = base

	role := &rbacv1.Role{
		TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "Role"},
		ObjectMeta: s.meta(base, LabelName, base),
		Rules: []rbacv1.PolicyRule{
			// The recovery Secret is written once, by create, and read on a
			// rerun. There is no update or delete: a Job that could delete it
			// could destroy the only in-cluster copy of the recovery keys.
			{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"create"}},
			{APIGroups: []string{""}, Resources: []string{"secrets"}, ResourceNames: []string{s.RecoverySecret}, Verbs: []string{"get"}},
			// The applied access document, the widen/narrow ledger; and the
			// published CA.
			{APIGroups: []string{""}, Resources: []string{"configmaps"}, Verbs: []string{"create"}},
			{APIGroups: []string{""}, Resources: []string{"configmaps"}, ResourceNames: s.ownedConfigMaps(), Verbs: []string{"get", "update", "patch"}},
		},
	}
	binding := &rbacv1.RoleBinding{
		TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "RoleBinding"},
		ObjectMeta: s.meta(base, LabelName, base),
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: sa.Name, Namespace: s.Namespace}},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: base},
	}
	objs := []any{cm, sa, role, binding}

	// The CA is published beside every namespace that holds a SecretStore for
	// this store, so each store's caProvider can name a ConfigMap of its own
	// namespace.
	caNS := s.caNamespaces()
	for _, ns := range caNS {
		if ns == s.Namespace {
			continue
		}
		objs = append(objs,
			&rbacv1.Role{
				TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "Role"},
				ObjectMeta: metav1.ObjectMeta{Name: base + "-ca", Namespace: ns, Labels: s.labels(LabelName, base)},
				Rules: []rbacv1.PolicyRule{
					{APIGroups: []string{""}, Resources: []string{"configmaps"}, Verbs: []string{"create"}},
					{APIGroups: []string{""}, Resources: []string{"configmaps"}, ResourceNames: []string{s.TLS.CAConfigMap}, Verbs: []string{"get", "update", "patch"}},
				},
			},
			&rbacv1.RoleBinding{
				TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "RoleBinding"},
				ObjectMeta: metav1.ObjectMeta{Name: base + "-ca", Namespace: ns, Labels: s.labels(LabelName, base)},
				Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: sa.Name, Namespace: s.Namespace}},
				RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: base + "-ca"},
			})
	}

	tcp, udp := corev1.ProtocolTCP, corev1.ProtocolUDP
	np := func(p int, proto *corev1.Protocol) networkingv1.NetworkPolicyPort {
		v := intstr.FromInt32(int32(p))
		return networkingv1.NetworkPolicyPort{Protocol: proto, Port: &v}
	}
	egressRules := []networkingv1.NetworkPolicyEgressRule{
		{To: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: nsSelector("kube-system")}}, Ports: []networkingv1.NetworkPolicyPort{np(53, &udp), np(53, &tcp)}},
		{To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "0.0.0.0/0"}}}, Ports: []networkingv1.NetworkPolicyPort{np(443, &tcp), np(6443, &tcp)}},
		{To: []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{MatchLabels: s.selector()}}}, Ports: []networkingv1.NetworkPolicyPort{np(s.apiPort(), &tcp)}},
	}
	if s.Bootstrap != nil {
		// The escrow sink gets its access token from the GKE metadata server
		// (Workload Identity): 169.254.169.252:988 on GKE 1.21+, and
		// 169.254.169.254:80 on older nodes. Without these a default-deny
		// policy leaves the Job unable to authenticate.
		egressRules = append(egressRules,
			networkingv1.NetworkPolicyEgressRule{To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "169.254.169.252/32"}}}, Ports: []networkingv1.NetworkPolicyPort{np(988, &tcp)}},
			networkingv1.NetworkPolicyEgressRule{To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "169.254.169.254/32"}}}, Ports: []networkingv1.NetworkPolicyPort{np(80, &tcp)}},
		)
	}
	egress := &networkingv1.NetworkPolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy"},
		ObjectMeta: s.meta(base, LabelName, base),
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{LabelName: base}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress:      egressRules,
		},
	}
	objs = append(objs, egress)

	for _, phase := range []string{PhaseWiden, PhaseConverge} {
		j, err := s.convergeJob(phase, cmName)
		if err != nil {
			return nil, err
		}
		objs = append(objs, j)
	}
	return objs, nil
}

// ownedConfigMaps are the ConfigMaps the converge Job writes: the applied
// document, and the CA it publishes (only when forge issued the certificate).
func (s Spec) ownedConfigMaps() []string {
	owned := []string{s.appliedConfigMap()}
	if s.TLS.Secret == "" {
		owned = append(owned, s.TLS.CAConfigMap)
	}
	if s.Bootstrap != nil {
		owned = append(owned, s.EscrowLedger())
	}
	return owned
}

func (s Spec) appliedConfigMap() string { return s.Name + "-access-applied" }

func (s Spec) caNamespaces() []string {
	set := map[string]bool{s.Namespace: true}
	for _, ns := range s.CANamespaces {
		set[ns] = true
	}
	out := make([]string, 0, len(set))
	for ns := range set {
		out = append(out, ns)
	}
	sort.Strings(out)
	return out
}

func (s Spec) convergeJob(phase, cmName string) (*batchv1.Job, error) {
	base := s.Name + "-converge"
	var initAddr string
	if s.raft() {
		// Init goes to pod 0 and only pod 0: a second node initialised on its
		// own would be a second cluster.
		initAddr = fmt.Sprintf("https://%s-0.%s.%s.svc:%d", s.Name, s.internalName(), s.Namespace, s.apiPort())
	} else {
		initAddr = fmt.Sprintf("https://%s.%s.svc:%d", s.internalName(), s.Namespace, s.apiPort())
	}
	caFile := "/openbao-tls/ca.crt"
	caVol := corev1.Volume{Name: "ca", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{s.caProjection()}}}}
	exp := int64(900)
	selfAud := ConvergeAudience
	env := []corev1.EnvVar{
		{Name: "OPENBAO_PHASE", Value: phase},
		{Name: "OPENBAO_INIT_ADDR", Value: initAddr},
		{Name: "OPENBAO_ADDR", Value: s.ServerURL()},
		{Name: "OPENBAO_CA_FILE", Value: caFile},
		{Name: "OPENBAO_NAMESPACE", Value: s.Namespace},
		{Name: "OPENBAO_RECOVERY_SECRET", Value: s.RecoverySecret},
		{Name: "OPENBAO_APPLIED_CONFIGMAP", Value: s.appliedConfigMap()},
		{Name: "OPENBAO_CA_CONFIGMAP", Value: s.TLS.CAConfigMap},
		{Name: "OPENBAO_CA_NAMESPACES", Value: strings.Join(s.caNamespaces(), " ")},
		{Name: "OPENBAO_PUBLISH_CA", Value: strconv.FormatBool(s.TLS.Secret == "")},
		{Name: "OPENBAO_SELF_MOUNT", Value: "jwt-" + s.Access.SelfCluster},
		{Name: "OPENBAO_CONVERGE_ROLE", Value: RoleConverge},
		{Name: "OPENBAO_KV_MOUNT", Value: s.KV.Mount},
		{Name: "OPENBAO_KV_MAX_VERSIONS", Value: strconv.Itoa(s.KV.MaxVersions)},
		{Name: "OPENBAO_EXIT_REFUSED", Value: strconv.Itoa(exitRefused)},
		// Break-glass only, absent on every ordinary deploy: a root token
		// recovered from the recovery keys, for a store whose self mount lost
		// its config.
		{Name: "OPENBAO_BREAK_GLASS_TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: s.Name + "-break-glass"}, Key: "token", Optional: new(true)}}},
	}
	if s.Bootstrap != nil {
		for _, kv := range s.escrowEnv() {
			env = append(env, corev1.EnvVar{Name: kv[0], Value: kv[1]})
		}
	}
	pod := corev1.PodSpec{
		ServiceAccountName: ConvergeServiceAccount(s), RestartPolicy: corev1.RestartPolicyNever, EnableServiceLinks: new(false),
		AutomountServiceAccountToken: new(true),
		PriorityClassName:            s.HA.PriorityClass,
		SecurityContext: &corev1.PodSecurityContext{
			RunAsNonRoot: new(true), RunAsUser: new(convergeUser), RunAsGroup: new(convergeUser),
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		Containers: []corev1.Container{{
			Name: "converge", Image: s.KubectlImage, ImagePullPolicy: corev1.PullIfNotPresent,
			Command: []string{"bash", "/converge/converge.sh"}, Env: env,
			VolumeMounts: []corev1.VolumeMount{
				{Name: "converge", MountPath: "/converge", ReadOnly: true},
				{Name: "ca", MountPath: "/openbao-tls", ReadOnly: true},
				{Name: "token", MountPath: "/var/run/openbao", ReadOnly: true},
				{Name: "tmp", MountPath: "/tmp"},
			},
			SecurityContext: &corev1.SecurityContext{
				RunAsNonRoot: new(true), RunAsUser: new(convergeUser), RunAsGroup: new(convergeUser),
				ReadOnlyRootFilesystem: new(true), AllowPrivilegeEscalation: new(false),
				Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
				Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")},
			},
		}},
		Volumes: []corev1.Volume{
			{Name: "converge", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: cmName}, DefaultMode: new(int32(0o444)),
			}}},
			caVol,
			// The Job logs in with its own projected token, audience-pinned
			// to the store: nothing stored, nothing shared.
			{Name: "token", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{{
				ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Audience: selfAud, ExpirationSeconds: &exp, Path: "token"},
			}}}}},
			{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		},
	}
	for _, h := range []struct{ vol, mount, cm string }{
		{"escrow", "/escrow", s.Hooks.Escrow},
		{"generate", "/generate", s.Hooks.Generate},
	} {
		if h.cm == "" {
			continue
		}
		pod.Volumes = append(pod.Volumes, corev1.Volume{Name: h.vol, VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: h.cm}, DefaultMode: new(int32(0o444))}}})
		pod.Containers[0].VolumeMounts = append(pod.Containers[0].VolumeMounts, corev1.VolumeMount{Name: h.vol, MountPath: h.mount, ReadOnly: true})
	}
	raw, err := json.Marshal(pod)
	if err != nil {
		return nil, fmt.Errorf("hash the %s Job: %w", phase, err)
	}
	hash := sum(string(raw))[:10]
	jobBase := base
	if phase == PhaseWiden {
		jobBase = s.Name + "-widen"
	}
	phaseAnn := "post-rollout"
	if phase == PhaseWiden {
		// Before the workloads roll out, so a name a promotion adds is already
		// granted when its ExternalSecret first syncs.
		phaseAnn = "pre-rollout"
	}
	podLabels := s.labels(LabelName, base)
	m := s.meta(jobBase+"-"+hash, LabelName, base, LabelJobName, jobBase, LabelSpecHash, hash)
	m.Annotations = map[string]string{AnnotationDeployPhase: phaseAnn}
	return &batchv1.Job{
		TypeMeta:   metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"},
		ObjectMeta: m,
		Spec: batchv1.JobSpec{
			BackoffLimit:          new(int32(3)),
			ActiveDeadlineSeconds: new(int64(1800)),
			// No ttlSecondsAfterFinished: a reconciler would re-create a
			// finished Job the TTL controller deleted, re-running it forever.
			PodFailurePolicy: &batchv1.PodFailurePolicy{Rules: []batchv1.PodFailurePolicyRule{{
				Action: batchv1.PodFailurePolicyActionFailJob,
				OnExitCodes: &batchv1.PodFailurePolicyOnExitCodesRequirement{
					ContainerName: new("converge"), Operator: batchv1.PodFailurePolicyOnExitCodesOpIn, Values: []int32{exitRefused},
				},
			}}},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: podLabels}, Spec: pod},
		},
	}, nil
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
