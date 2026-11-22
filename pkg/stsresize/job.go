package stsresize

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// DefaultImage is the image the resize Job runs: kubectl and jq, pinned by
// digest so the Job runs the same bytes on every cluster and a registry
// retagging cannot change what writes to a production volume.
// Bundle.statefulset_resize.image replaces it, for a mirror or a newer
// kubectl.
const DefaultImage = "docker.io/alpine/k8s:1.34.12@sha256:d50424976a0a888d2531f13035dce45c004ec2b6f877f233005ba35c0d61be5d"

// Names and labels of what [Build] emits. They are exported because a project
// that runs a default-deny network policy must let the Job's pod reach DNS and
// the API server, and selects that pod by [PodLabel].
const (
	// JobBaseName is the Job's name before its spec hash, and the name of its
	// ServiceAccount, Role and RoleBinding, in the default delivery unit.
	JobBaseName = "sts-volume-resize"
	// PodLabel is the label key every pod of the Job carries, valued with the
	// Job's base name.
	PodLabel = "forge.dev/job-name"
	// ContainerName is the Job's one container.
	ContainerName = "resize"

	// exitRefused is the script's exit code for a permanent refusal. The Job's
	// podFailurePolicy fails the whole Job on it instead of retrying.
	exitRefused = 2
	// jobBackoffLimit retries a transient failure (an API server blip) with a
	// fresh pod; a refusal never retries.
	jobBackoffLimit = 3
	// jobDeadlineSeconds bounds one run: a few claims and a delete.
	jobDeadlineSeconds = 900
	runAsUser          = 65532
	maxBaseNameLen     = 40
)

// Labels and annotations the objects carry, spelled as pkg/deploy and
// pkg/release spell them; this package imports neither, so a consumer that
// wants only the Decide/Converge library does not pull the renderer.
const (
	labelName       = "app.kubernetes.io/name"
	labelManagedBy  = "app.kubernetes.io/managed-by"
	labelSpecHash   = "forge.dev/spec-hash"
	annotationPhase = "forge.dev/deploy-phase"
	phasePreRollout = "pre-rollout"
)

// JobSpec is one resize Job's input: the StatefulSets of one namespace (and,
// for a project that splits its delivery units, of one unit) whose volumes it
// converges.
type JobSpec struct {
	// Namespace is the StatefulSets' namespace. Required.
	Namespace string
	// Unit names the delivery unit, "" or "base" for the default one. It only
	// names the objects: a Job in a unit's pre tree gates that unit's rollout,
	// so each unit has its own, and two must not share a name.
	Unit string
	// Image is the Job's image; empty is [DefaultImage].
	Image string
	// StatefulSets are the DECLARED objects, all in Namespace, each with at
	// least one volumeClaimTemplate.
	StatefulSets []*appsv1.StatefulSet
	// Labels are stamped on the metadata of every object (never a pod): the
	// routing and ownership labels of the StatefulSets the Job serves, so the
	// objects deploy to the same cluster, in the same env and delivery unit.
	Labels map[string]string
	// ImagePullSecrets name existing pull Secrets in Namespace, put on the
	// Job's ServiceAccount like every forge-generated one.
	ImagePullSecrets []string
}

// Objects is what [Build] emits for one [JobSpec].
type Objects struct {
	ServiceAccount *corev1.ServiceAccount
	Role           *rbacv1.Role
	RoleBinding    *rbacv1.RoleBinding
	// NetworkPolicy lets the Job's pod, and only it, reach DNS and the API
	// server in a namespace that runs default-deny. Policies are additive, so
	// it changes nothing where there is none.
	NetworkPolicy *networkingv1.NetworkPolicy
	Job           *batchv1.Job
}

// Name is the base name for a delivery unit's objects.
func Name(unit string) string {
	if unit == "" || unit == "base" {
		return JobBaseName
	}
	name := JobBaseName + "-" + strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			return r
		}
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return '-'
	}, unit)
	if len(name) > maxBaseNameLen {
		sum := sha256.Sum256([]byte(unit))
		return JobBaseName + "-" + hex.EncodeToString(sum[:])[:8]
	}
	return strings.TrimRight(name, "-")
}

// Build assembles the pre-rollout Job that converges spec's StatefulSets, and
// the identity it runs as.
//
// THE JOB'S NAME CARRIES A HASH OF ITS POD SPEC, like every forge one-shot: a
// Job's spec is immutable, so a changed spec must be a new object, and an
// unchanged one is a true no-op under both a re-apply and a Flux reconcile. The
// spec embeds the declared volume fields, so the Job runs again exactly when a
// declared volume changes, and not when only a pod template does.
//
// THE IDENTITY IS NAMESPACE-SCOPED AND NARROW: get and delete on the named
// StatefulSets only, and get, list and patch on claims. It holds nothing
// cluster-scoped, which is why the script learns whether a claim may grow from
// the API server's own refusal of a dry-run patch rather than by reading
// StorageClasses.
func Build(spec JobSpec) (Objects, error) {
	if spec.Namespace == "" {
		return Objects{}, fmt.Errorf("stsresize: the resize Job needs a namespace")
	}
	if len(spec.StatefulSets) == 0 {
		return Objects{}, fmt.Errorf("stsresize: the resize Job in %s has no StatefulSet", spec.Namespace)
	}
	sets := append([]*appsv1.StatefulSet(nil), spec.StatefulSets...)
	sort.Slice(sets, func(i, j int) bool { return sets[i].Name < sets[j].Name })
	names := make([]string, len(sets))
	for i, s := range sets {
		if s.Name == "" || len(s.Spec.VolumeClaimTemplates) == 0 {
			return Objects{}, fmt.Errorf("stsresize: StatefulSet %q in %s declares no volumeClaimTemplates", s.Name, spec.Namespace)
		}
		if s.Namespace != "" && s.Namespace != spec.Namespace {
			return Objects{}, fmt.Errorf("stsresize: StatefulSet %s/%s is not in the Job's namespace %s", s.Namespace, s.Name, spec.Namespace)
		}
		names[i] = s.Name
	}
	declared, err := DeclaredJSON(sets...)
	if err != nil {
		return Objects{}, err
	}
	image := spec.Image
	if image == "" {
		image = DefaultImage
	}
	base := Name(spec.Unit)
	meta := func(name string, extra map[string]string) metav1.ObjectMeta {
		labels := map[string]string{labelName: base, labelManagedBy: "forge"}
		for k, v := range spec.Labels {
			labels[k] = v
		}
		for k, v := range extra {
			labels[k] = v
		}
		return metav1.ObjectMeta{Name: name, Namespace: spec.Namespace, Labels: labels}
	}

	sa := &corev1.ServiceAccount{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceAccount"},
		ObjectMeta: meta(base, nil),
	}
	for _, name := range spec.ImagePullSecrets {
		sa.ImagePullSecrets = append(sa.ImagePullSecrets, corev1.LocalObjectReference{Name: name})
	}
	role := &rbacv1.Role{
		TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "Role"},
		ObjectMeta: meta(base, nil),
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{"apps"}, Resources: []string{"statefulsets"}, ResourceNames: names, Verbs: []string{"get", "delete"}},
			{APIGroups: []string{""}, Resources: []string{"persistentvolumeclaims"}, Verbs: []string{"get", "list", "patch"}},
		},
	}
	binding := &rbacv1.RoleBinding{
		TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "RoleBinding"},
		ObjectMeta: meta(base, nil),
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: base, Namespace: spec.Namespace}},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: base},
	}

	tcp, udp := corev1.ProtocolTCP, corev1.ProtocolUDP
	port := func(p int32, proto *corev1.Protocol) networkingv1.NetworkPolicyPort {
		v := intstr.FromInt32(p)
		return networkingv1.NetworkPolicyPort{Protocol: proto, Port: &v}
	}
	policy := &networkingv1.NetworkPolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy"},
		ObjectMeta: meta(base, nil),
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{PodLabel: base}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress: []networkingv1.NetworkPolicyEgressRule{
				{
					To:    []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"}}}},
					Ports: []networkingv1.NetworkPolicyPort{port(53, &udp), port(53, &tcp)},
				},
				// The API server is reached through the kubernetes Service,
				// which a policy sees AFTER translation to the control
				// plane's own address: 443 on most clusters, 6443 on others.
				{
					To:    []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "0.0.0.0/0"}}},
					Ports: []networkingv1.NetworkPolicyPort{port(443, &tcp), port(6443, &tcp)},
				},
			},
		},
	}

	uid := int64(runAsUser)
	pod := corev1.PodSpec{
		ServiceAccountName:           base,
		AutomountServiceAccountToken: new(true),
		// No <SERVICE>_PORT=tcp://… per Service in the namespace: those names
		// can shadow the script's own env. kubectl still finds the apiserver,
		// because KUBERNETES_SERVICE_HOST/PORT are injected regardless.
		EnableServiceLinks: new(false),
		RestartPolicy:      corev1.RestartPolicyNever,
		SecurityContext: &corev1.PodSecurityContext{
			RunAsNonRoot:   new(true),
			RunAsUser:      &uid,
			RunAsGroup:     &uid,
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		Containers: []corev1.Container{{
			Name:            ContainerName,
			Image:           image,
			ImagePullPolicy: corev1.PullIfNotPresent,
			Command:         []string{"sh", "-c"},
			Args:            []string{Script},
			Env: []corev1.EnvVar{
				{Name: "RESIZE_NAMESPACE", Value: spec.Namespace},
				{Name: "RESIZE_DECLARED", Value: declared},
			},
			SecurityContext: &corev1.SecurityContext{
				RunAsNonRoot:             new(true),
				RunAsUser:                &uid,
				RunAsGroup:               &uid,
				ReadOnlyRootFilesystem:   new(true),
				AllowPrivilegeEscalation: new(false),
				Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
				Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m"), corev1.ResourceMemory: resource.MustParse("256Mi")},
			},
			VolumeMounts: []corev1.VolumeMount{{Name: "tmp", MountPath: "/tmp"}},
		}},
		Volumes: []corev1.Volume{{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
	}
	// The hash covers the whole pod spec, as forge's other one-shots do.
	raw, err := json.Marshal(pod)
	if err != nil {
		return Objects{}, fmt.Errorf("stsresize: hash the resize Job: %w", err)
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])[:10]

	podLabels := map[string]string{labelName: base, labelManagedBy: "forge", PodLabel: base}
	job := &batchv1.Job{
		TypeMeta: metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"},
		ObjectMeta: func() metav1.ObjectMeta {
			m := meta(base+"-"+hash, map[string]string{PodLabel: base, labelSpecHash: hash})
			m.Annotations = map[string]string{annotationPhase: phasePreRollout}
			return m
		}(),
		Spec: batchv1.JobSpec{
			BackoffLimit:          new(int32(jobBackoffLimit)),
			ActiveDeadlineSeconds: new(int64(jobDeadlineSeconds)),
			// A refusal is a verdict, not a flake: it fails the Job at once,
			// so the pre-rollout gate stops the deploy with the reason in the
			// pod's log instead of retrying it three times.
			PodFailurePolicy: &batchv1.PodFailurePolicy{Rules: []batchv1.PodFailurePolicyRule{{
				Action: batchv1.PodFailurePolicyActionFailJob,
				OnExitCodes: &batchv1.PodFailurePolicyOnExitCodesRequirement{
					ContainerName: new(ContainerName),
					Operator:      batchv1.PodFailurePolicyOnExitCodesOpIn,
					Values:        []int32{exitRefused},
				},
			}}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: podLabels},
				Spec:       pod,
			},
		},
	}
	return Objects{ServiceAccount: sa, Role: role, RoleBinding: binding, NetworkPolicy: policy, Job: job}, nil
}

// Manifests encodes the objects as the plain documents a render stream
// carries, in apply order, without the null creationTimestamp and empty status
// a typed object serializes.
func (o Objects) Manifests() ([]map[string]any, error) {
	var out []map[string]any
	for _, obj := range []any{o.ServiceAccount, o.Role, o.RoleBinding, o.NetworkPolicy, o.Job} {
		raw, err := json.Marshal(obj)
		if err != nil {
			return nil, fmt.Errorf("stsresize: encode the resize objects: %w", err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("stsresize: encode the resize objects: %w", err)
		}
		delete(doc, "status")
		stripNulls(doc)
		out = append(out, doc)
	}
	return out, nil
}

// stripNulls removes the null a typed object writes for an unset struct
// (creationTimestamp, an empty resources block), at every depth. An empty map
// or list is kept: in Kubernetes it is frequently the meaning (`emptyDir: {}`).
func stripNulls(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if val == nil {
				delete(t, k)
				continue
			}
			stripNulls(val)
		}
	case []any:
		for _, val := range t {
			stripNulls(val)
		}
	}
}
