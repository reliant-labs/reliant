package v1alpha1

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

var (
	// cronFieldRE is one field of a 5-field cron expression: digits, *, and
	// the - , / operators, or a three-letter month/day name.
	cronFieldRE    = regexp.MustCompile(`^([0-9*,/\-]+|[A-Za-z]{3}([,\-][A-Za-z]{3})*)$`)
	namedSchedules = map[string]bool{"@hourly": true, "@daily": true, "@weekly": true, "@monthly": true, "@yearly": true, "@annually": true}
	// annotationNameRE / annotationPrefixRE are the Kubernetes annotation
	// key grammar: [prefix/]name.
	annotationNameRE   = regexp.MustCompile(`^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`)
	annotationPrefixRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
	labelValueRE       = regexp.MustCompile(`^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`)
	// dnsSubdomainRE is RFC-1123's subdomain grammar, which names a
	// cluster-scoped object (a PriorityClass, a ServiceAccount).
	dnsSubdomainRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
	// appProtocolRE is Kubernetes' appProtocol grammar: an IANA service
	// name, or a domain-prefixed name.
	appProtocolRE = regexp.MustCompile(`^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)
)

// MaxDomains bounds a port's custom domains.
const MaxDomains = 8

// kindCapabilities is which kinds may set each kind-specific field. It is
// the Go statement of kcl/workloads/capabilities.k's KIND_CAPABILITIES, for
// the fields that matrix gated. An absent kind is a deliberate claim that
// the field means nothing there, and Validate REFUSES it rather than drop
// it: a dropped declaration is a pod that runs without what its author
// wrote down, and the manifest looks fine because it is the thing that lost
// the information.
var kindCapabilities = map[string]map[WorkloadKind]bool{
	"ports":                         {KindService: true, KindWorker: true, KindOperator: true},
	"replicas":                      {KindService: true, KindWorker: true, KindOperator: true},
	"probes":                        {KindService: true, KindWorker: true, KindOperator: true},
	"storageGiB":                    {KindService: true, KindWorker: true, KindOperator: true},
	"namespacedRBAC":                scheduledKinds,
	"clusterRBAC":                   scheduledKinds,
	"crds":                          {KindOperator: true},
	"group":                         {KindOperator: true},
	"version":                       {KindOperator: true},
	"leaderElection":                {KindOperator: true},
	"schedule":                      {KindCron: true},
	"before":                        {KindJob: true},
	"deployPhase":                   {KindJob: true},
	"serviceAccountAnnotations":     {KindService: true, KindWorker: true, KindJob: true, KindCron: true, KindOperator: true},
	"sidecars":                      scheduledKinds,
	"volumes":                       scheduledKinds,
	"serviceAccount":                scheduledKinds,
	"nodeSelector":                  scheduledKinds,
	"tolerations":                   scheduledKinds,
	"podAnnotations":                scheduledKinds,
	"priorityClassName":             scheduledKinds,
	"enableServiceLinks":            scheduledKinds,
	"strategy":                      deploymentKinds,
	"terminationGracePeriodSeconds": scheduledKinds,
	"activeDeadlineSeconds":         {KindJob: true, KindCron: true},
	"securityContext":               scheduledKinds,
}

// scheduledKinds are the kinds that render a pod. A tool is never scheduled,
// so every pod-level field is refused for it.
var scheduledKinds = map[WorkloadKind]bool{KindService: true, KindWorker: true, KindJob: true, KindCron: true, KindOperator: true}

// deploymentKinds are the kinds that render a Deployment, which is the only
// object with a rollout strategy. A batch kind's pods are replaced by
// creating a new Job, not by a strategy.
var deploymentKinds = map[WorkloadKind]bool{KindService: true, KindWorker: true, KindOperator: true}

// kindCapabilityReasons explains each refusal, naming the alternative.
var kindCapabilityReasons = map[string]string{
	"ports":                         "a batch kind (job/cron) runs to completion and a tool is never scheduled, so there is no pod anything can dial; use kind service or worker",
	"replicas":                      "a batch pod's concurrency belongs to its Job or CronJob and a tool is never scheduled, so Kubernetes has nowhere to put this value",
	"probes":                        "probes gate traffic and restart long-running pods; a batch pod runs to completion and a tool is never scheduled",
	"storageGiB":                    "a ReadWriteOnce volume is mounted by a long-running pod; a batch pod or a tool has nothing to keep it for",
	"namespacedRBAC":                "a tool is never scheduled, so it has no pod to grant permission to",
	"clusterRBAC":                   "a tool is never scheduled, so it has no pod to grant permission to",
	"crds":                          "crds names the custom resources a controller-runtime manager owns; declare kind operator",
	"group":                         "group is the API group of an operator's CRDs; declare kind operator",
	"version":                       "version is the API version of an operator's CRDs; declare kind operator",
	"leaderElection":                "only an operator's manager elects a leader",
	"schedule":                      "only a cron runs on a schedule; a one-shot is kind job",
	"before":                        "before orders a one-shot job ahead of other workloads; it is only valid for kind job",
	"deployPhase":                   "deployPhase places a standalone job in the deploy; it is only valid for kind job",
	"serviceAccountAnnotations":     "a tool is never scheduled, so forge renders no ServiceAccount to annotate",
	"sidecars":                      "a tool is never scheduled, so it has no pod to add a container to",
	"volumes":                       "a tool is never scheduled, so it has no pod to mount a volume in",
	"serviceAccount":                "a tool is never scheduled, so it has no pod to run as a ServiceAccount",
	"nodeSelector":                  "a tool is never scheduled, so there is no pod to place",
	"tolerations":                   "a tool is never scheduled, so there is no pod to place",
	"podAnnotations":                "a tool is never scheduled, so there is no pod to annotate",
	"priorityClassName":             "a tool is never scheduled, so there is no pod to rank",
	"enableServiceLinks":            "a tool is never scheduled, so there is no pod to inject service links into",
	"strategy":                      "only a Deployment rolls its pods out; a batch kind (job/cron) replaces them by creating a new Job, and a tool is never scheduled",
	"terminationGracePeriodSeconds": "a tool is never scheduled, so there is no pod to stop",
	"activeDeadlineSeconds":         "a deadline bounds a run to completion; a long-running kind never completes, so use probes, and a tool is never scheduled",
	"securityContext":               "a tool is never scheduled, so there is no pod to run",
}

// Validate checks a Workload spec against the invariants every destination
// shares and then against profile p. Every violation is collected and
// returned together.
//
// The structural rules come first, and they hold under EVERY profile: a spec
// that contradicts itself (a schedule on a job, storage on three replicas)
// has no correct rendering anywhere. The profile rules come second, and say
// only what a destination refuses to run.
func (s WorkloadSpec) Validate(p Profile) error {
	var errs []error
	kind := s.EffectiveKind()
	validKind := false
	if _, ok := KindProfiles[kind]; ok {
		validKind = true
	} else {
		errs = append(errs, fmt.Errorf("kind %q must be one of service, worker, job, cron, operator, tool", s.Kind))
	}

	// --- image ---
	if s.Image == "" {
		errs = append(errs, errors.New("image is required"))
	} else if strings.ContainsAny(s.Image, " \t\n") {
		errs = append(errs, fmt.Errorf("image %q contains whitespace", s.Image))
	} else if p == ProfileRestricted {
		// Registry-qualified and pinned only where the platform pulls
		// from a registry the hosted user does not control. A cluster the
		// author operates may run a bare name imported into its nodes
		// (k3d image import), and refusing that would break the dev loop
		// for no safety gain.
		if err := ValidateImage(s.Image); err != nil {
			errs = append(errs, err)
		}
	}

	// --- kind capabilities ---
	if validKind {
		for _, field := range []string{
			"ports", "replicas", "probes", "storageGiB", "namespacedRBAC", "clusterRBAC", "crds", "group",
			"version", "leaderElection", "schedule", "before", "deployPhase", "serviceAccountAnnotations",
			"sidecars", "volumes", "serviceAccount", "nodeSelector", "tolerations", "podAnnotations",
			"priorityClassName", "enableServiceLinks", "strategy", "terminationGracePeriodSeconds", "activeDeadlineSeconds",
			"securityContext",
		} {
			if s.declares(field) && !kindCapabilities[field][kind] {
				errs = append(errs, fmt.Errorf("%s is not supported for kind %q: %s", field, kind, kindCapabilityReasons[field]))
			}
		}
	}

	// --- replicas and storage ---
	if s.Replicas < 0 {
		errs = append(errs, errors.New("replicas must not be negative; omit it for one replica"))
	}
	if s.StorageGiB < 0 {
		errs = append(errs, errors.New("storageGiB must not be negative; omit it for a stateless workload"))
	}
	switch s.Strategy {
	case "", StrategyRollingUpdate, StrategyRecreate:
	default:
		errs = append(errs, fmt.Errorf("strategy %q must be RollingUpdate or Recreate", s.Strategy))
	}
	if s.StorageGiB > 0 && s.Strategy == StrategyRollingUpdate {
		errs = append(errs, errors.New("strategy RollingUpdate is not allowed with storageGiB: the volume is ReadWriteOnce, so the surge pod can never mount it and the rollout wedges; declare Recreate, or drop the strategy (storage already renders Recreate)"))
	}
	if s.StorageGiB > 0 && s.Replicas > 1 {
		errs = append(errs, fmt.Errorf("storageGiB requires replicas 1 (got %d): the volume is ReadWriteOnce, so a second replica can never mount it, and the workload rolls out with Recreate", s.Replicas))
	}

	// --- job / cron ---
	if kind == KindJob && len(s.Command) == 0 && len(s.Args) == 0 {
		errs = append(errs, errors.New("kind job requires command or args: a one-shot with nothing to run is not a job"))
	}
	if kind == KindCron {
		if s.Schedule == "" {
			errs = append(errs, errors.New("kind cron requires a schedule; a one-shot is kind job"))
		}
		if len(s.Command) == 0 && len(s.Args) == 0 {
			errs = append(errs, errors.New("kind cron requires command or args: a schedule that runs nothing is not a cron"))
		}
	}
	if s.Schedule != "" && !validSchedule(s.Schedule) {
		errs = append(errs, fmt.Errorf("schedule %q must be a 5-field cron expression or one of @hourly, @daily, @weekly, @monthly, @yearly", s.Schedule))
	}
	errs = append(errs, validateBefore(s.Before)...)
	switch s.DeployPhase {
	case "", DeployPhasePreRollout, DeployPhasePostRollout:
	default:
		errs = append(errs, fmt.Errorf("deployPhase %q must be pre-rollout or post-rollout", s.DeployPhase))
	}
	if s.DeployPhase != "" && len(s.Before) > 0 {
		errs = append(errs, errors.New("deployPhase is only for a standalone job: a job with before runs as an initContainer of the workloads it gates, so pod ordering already is its phase"))
	}

	// --- gating job ---
	if kind == KindJob && len(s.Before) > 0 {
		errs = append(errs, s.gatingJobPodFieldErrors()...)
	}

	// --- operator ---
	// No minimum on CRDs: an operator may own none yet (scaffolded before its
	// first CRD) or reconcile built-in kinds only, granted by ClusterRBAC.
	// CRDs is what its derived ClusterRole covers; empty derives nothing.

	// --- ports (the main container's, then pod-wide uniqueness) ---
	errs = append(errs, validatePorts(kind, s.Ports)...)
	errs = append(errs, validatePodPorts(s)...)

	// --- probes ---
	if s.Probes != nil {
		errs = append(errs, s.Probes.validate()...)
		if s.ProbePort() == 0 {
			errs = append(errs, errors.New("probes need a port: declare one in ports, or set probes.port"))
		}
	}

	// --- env ---
	seenEnv := map[string]bool{}
	for _, e := range s.Env {
		if err := e.Validate(); err != nil {
			errs = append(errs, err)
		}
		if seenEnv[e.Name] {
			errs = append(errs, fmt.Errorf("env var %s is declared twice", e.Name))
		}
		seenEnv[e.Name] = true
	}

	// --- resources ---
	if err := s.Resources.Validate(); err != nil {
		errs = append(errs, err)
	}

	// --- RBAC ---
	for field, rules := range map[string][]PolicyRule{"namespacedRBAC": s.NamespacedRBAC, "clusterRBAC": s.ClusterRBAC} {
		for i, r := range rules {
			if err := r.validate(fmt.Sprintf("%s[%d]", field, i)); err != nil {
				errs = append(errs, err)
			}
		}
	}

	// --- service account annotations ---
	for _, k := range sortedKeys(s.ServiceAccountAnnotations) {
		if !validAnnotationKey(k) {
			errs = append(errs, fmt.Errorf("serviceAccountAnnotations key %q must be [prefix/]name: name at most 63 characters of [A-Za-z0-9-_.] starting and ending alphanumeric, prefix a DNS subdomain (e.g. iam.gke.io/gcp-service-account)", k))
		}
	}

	// --- pod-level: sidecars, volumes, identity, placement ---
	errs = append(errs, validateSidecars(s.Sidecars)...)
	errs = append(errs, validateVolumes(s)...)
	if s.ServiceAccount != "" {
		for field, set := range map[string]bool{
			"namespacedRBAC": len(s.NamespacedRBAC) > 0, "clusterRBAC": len(s.ClusterRBAC) > 0,
			"serviceAccountAnnotations": len(s.ServiceAccountAnnotations) > 0,
		} {
			if set {
				errs = append(errs, fmt.Errorf("serviceAccount %q and %s are mutually exclusive: naming an existing ServiceAccount means forge renders none, so there is no generated ServiceAccount for %s to describe; grant it where that ServiceAccount is owned", s.ServiceAccount, field, field))
			}
		}
	}
	for _, k := range sortedKeys(s.NodeSelector) {
		if !validAnnotationKey(k) {
			errs = append(errs, fmt.Errorf("nodeSelector key %q is not a valid label key ([prefix/]name)", k))
		}
		if v := s.NodeSelector[k]; len(v) > 63 || (v != "" && !labelValueRE.MatchString(v)) {
			errs = append(errs, fmt.Errorf("nodeSelector %s: value %q is not a valid label value (at most 63 characters of [A-Za-z0-9-_.], starting and ending alphanumeric)", k, v))
		}
	}
	for i, t := range s.Tolerations {
		errs = append(errs, t.validate(fmt.Sprintf("tolerations[%d]", i))...)
	}
	for _, k := range sortedKeys(s.PodAnnotations) {
		if !validAnnotationKey(k) {
			errs = append(errs, fmt.Errorf("podAnnotations key %q must be [prefix/]name: name at most 63 characters of [A-Za-z0-9-_.] starting and ending alphanumeric, prefix a DNS subdomain", k))
		}
	}

	// --- priority, grace, deadline, security ---
	if n := s.PriorityClassName; n != "" && (len(n) > 253 || !dnsSubdomainRE.MatchString(n)) {
		errs = append(errs, fmt.Errorf("priorityClassName %q must be a DNS subdomain of at most 253 characters (lowercase alphanumerics, '-' and '.', starting and ending alphanumeric): it names a cluster-scoped PriorityClass", n))
	}
	if g := s.TerminationGracePeriodSeconds; g != nil && (*g < 0 || *g > 3600) {
		errs = append(errs, fmt.Errorf("terminationGracePeriodSeconds must be 0-3600 (got %d)", *g))
	}
	if d := s.ActiveDeadlineSeconds; d != nil && *d < 1 {
		errs = append(errs, fmt.Errorf("activeDeadlineSeconds must be positive (got %d): omit it for no deadline", *d))
	}
	if sc := s.SecurityContext; sc != nil {
		for _, id := range []struct {
			field string
			v     *int64
		}{{"runAsUser", sc.RunAsUser}, {"runAsGroup", sc.RunAsGroup}, {"fsGroup", sc.FSGroup}} {
			if id.v != nil && *id.v < 1 {
				errs = append(errs, fmt.Errorf("securityContext.%s must be at least 1 (got %d): every forge pod runs as non-root", id.field, *id.v))
			}
		}
	}

	// --- profile ---
	errs = append(errs, profileViolations(s, p)...)
	return errors.Join(errs...)
}

// gatingPodFields are the fields that describe a pod of the workload's OWN.
// A job with `before` has none: it runs as an initContainer INSIDE the pods
// it gates, under their ServiceAccount, on their nodes, with their volumes.
// So each of these on a gating job describes nothing, and expand.k's old
// behaviour (an orphan ServiceAccount and Role that nothing bound) made a
// migrate job's namespacedRBAC look granted when it was not. Ordered for
// deterministic errors.
var gatingPodFields = []string{
	"namespacedRBAC", "clusterRBAC", "serviceAccount", "serviceAccountAnnotations", "sidecars",
	"volumes", "nodeSelector", "tolerations", "podAnnotations", "priorityClassName", "enableServiceLinks",
	"terminationGracePeriodSeconds", "activeDeadlineSeconds", "securityContext",
}

// gatingJobPodFieldErrors refuses each pod-level field set on a gating job.
// pkg/deploy's renderer carries the same check as defence in depth. This is
// the gate: the control plane admits with Validate before rendering.
func (s WorkloadSpec) gatingJobPodFieldErrors() []error {
	var errs []error
	for _, field := range gatingPodFields {
		if s.declares(field) {
			errs = append(errs, fmt.Errorf("%s is not allowed on a job with before: it runs as an initContainer in the pods it gates, under their identity and placement, so it has no pod of its own for %s to apply to (drop before to run it as a standalone Job)", field, field))
		}
	}
	return errs
}

// declares reports whether the spec sets a kind-specific field. Replicas 1
// is the default and says nothing, so only more than one counts.
func (s WorkloadSpec) declares(field string) bool {
	switch field {
	case "ports":
		return len(s.Ports) > 0
	case "replicas":
		return s.Replicas > 1
	case "probes":
		return s.Probes != nil
	case "storageGiB":
		return s.StorageGiB > 0
	case "namespacedRBAC":
		return len(s.NamespacedRBAC) > 0
	case "clusterRBAC":
		return len(s.ClusterRBAC) > 0
	case "crds":
		return len(s.CRDs) > 0
	case "group":
		return s.Group != ""
	case "version":
		return s.Version != ""
	case "leaderElection":
		return s.LeaderElection != nil
	case "schedule":
		return s.Schedule != ""
	case "before":
		return len(s.Before) > 0
	case "deployPhase":
		return s.DeployPhase != ""
	case "serviceAccountAnnotations":
		return len(s.ServiceAccountAnnotations) > 0
	case "sidecars":
		return len(s.Sidecars) > 0
	case "volumes":
		return len(s.Volumes) > 0
	case "serviceAccount":
		return s.ServiceAccount != ""
	case "nodeSelector":
		return len(s.NodeSelector) > 0
	case "tolerations":
		return len(s.Tolerations) > 0
	case "podAnnotations":
		return len(s.PodAnnotations) > 0
	case "priorityClassName":
		return s.PriorityClassName != ""
	case "enableServiceLinks":
		return s.EnableServiceLinks
	case "strategy":
		return s.Strategy != ""
	case "terminationGracePeriodSeconds":
		return s.TerminationGracePeriodSeconds != nil
	case "activeDeadlineSeconds":
		return s.ActiveDeadlineSeconds != nil
	case "securityContext":
		return s.SecurityContext != nil
	}
	return false
}

func validSchedule(s string) bool {
	if namedSchedules[s] {
		return true
	}
	fields := strings.Fields(s)
	if len(fields) != 5 {
		return false
	}
	for _, f := range fields {
		if !cronFieldRE.MatchString(f) {
			return false
		}
	}
	return true
}

func validateBefore(before []string) []error {
	var errs []error
	seen := map[string]bool{}
	for _, b := range before {
		if b != BeforeAll && !dnsLabelRE.MatchString(b) {
			errs = append(errs, fmt.Errorf("before entry %q must be a workload name (an RFC-1123 label) or %q", b, BeforeAll))
		}
		if seen[b] {
			errs = append(errs, fmt.Errorf("before entry %q is listed twice", b))
		}
		seen[b] = true
	}
	// The wildcard already means every workload. Mixing it with names is
	// a contradiction: the names are redundant, or the author believed the
	// list NARROWED the broadcast, which it does not.
	if seen[BeforeAll] && len(before) > 1 {
		errs = append(errs, fmt.Errorf("before = [%q] already gates every workload; remove the other names or drop the wildcard and enumerate", BeforeAll))
	}
	return errs
}

func validatePorts(kind WorkloadKind, ports []Port) []error {
	var errs []error
	names, numbers := map[string]bool{}, map[string]bool{}
	exposed := 0
	for _, p := range ports {
		if !dnsLabelRE.MatchString(p.Name) || len(p.Name) > 15 {
			errs = append(errs, fmt.Errorf("port name %q must be at most 15 lowercase alphanumerics or '-', starting and ending alphanumeric", p.Name))
		}
		if names[p.Name] {
			errs = append(errs, fmt.Errorf("port name %q is declared twice: Services and probes refer to a port by name", p.Name))
		}
		names[p.Name] = true
		if p.Port < 1 || p.Port > 65535 {
			errs = append(errs, fmt.Errorf("port %s: number %d must be 1-65535", p.Name, p.Port))
		}
		switch p.Protocol {
		case "", ProtocolTCP, ProtocolUDP:
		default:
			errs = append(errs, fmt.Errorf("port %s: protocol %q must be tcp or udp", p.Name, p.Protocol))
		}
		proto := p.Protocol
		if proto == "" {
			proto = DefaultPortProtocol
		}
		num := fmt.Sprintf("%d/%s", p.Port, proto)
		if numbers[num] {
			errs = append(errs, fmt.Errorf("port %s: %s is declared twice", p.Name, num))
		}
		numbers[num] = true
		if p.AppProtocol != "" && !appProtocolRE.MatchString(p.AppProtocol) {
			errs = append(errs, fmt.Errorf("port %s: appProtocol %q must be a protocol name (h2c, grpc, http) or a prefixed name (example.com/proto)", p.Name, p.AppProtocol))
		}
		if p.Expose {
			exposed++
			if kind != KindService {
				errs = append(errs, fmt.Errorf("port %s: expose is only for kind service, which is the only kind with a Service object to route a hostname to", p.Name))
			}
		}
		if len(p.Domains) > 0 {
			if !p.Expose {
				errs = append(errs, fmt.Errorf("port %s: domains are only meaningful on the exposed port (expose: true): nothing routes a hostname to a port that is not exposed", p.Name))
			}
			if len(p.Domains) > MaxDomains {
				errs = append(errs, fmt.Errorf("port %s: %d domains; at most %d are allowed", p.Name, len(p.Domains), MaxDomains))
			}
			if err := validateDomains(fmt.Sprintf("port %s: domains", p.Name), p.Domains); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if exposed > 1 {
		errs = append(errs, fmt.Errorf("%d ports set expose; at most one may, because a workload reports one hostname", exposed))
	}
	return errs
}

func (p Probes) validate() []error {
	var errs []error
	if p.Port < 0 || p.Port > 65535 {
		errs = append(errs, fmt.Errorf("probes.port %d must be 1-65535", p.Port))
	}
	if p.TCP && (p.ReadinessPath != "" || p.LivenessPath != "") {
		errs = append(errs, errors.New("probes.tcp probes with a TCP connect, so readinessPath and livenessPath would be ignored; set one or the other"))
	}
	if p.TCP && p.StartupPath != "" {
		errs = append(errs, errors.New("probes.tcp probes with a TCP connect, so startupPath would be ignored; set one or the other"))
	}
	for field, path := range map[string]string{"readinessPath": p.ReadinessPath, "livenessPath": p.LivenessPath, "startupPath": p.StartupPath} {
		if path != "" && !strings.HasPrefix(path, "/") {
			errs = append(errs, fmt.Errorf("probes.%s %q must start with '/'", field, path))
		}
	}
	if p.InitialDelaySeconds < 0 || p.PeriodSeconds < 0 || p.TimeoutSeconds < 0 || p.FailureThreshold < 0 {
		errs = append(errs, errors.New("probes timings must not be negative"))
	}
	return errs
}

func (r PolicyRule) validate(field string) error {
	var errs []error
	if len(r.APIGroups) == 0 {
		errs = append(errs, fmt.Errorf(`%s.apiGroups must list at least one group ("" is the core group)`, field))
	}
	if len(r.Resources) == 0 {
		errs = append(errs, fmt.Errorf("%s.resources must list at least one resource", field))
	}
	if len(r.Verbs) == 0 {
		errs = append(errs, fmt.Errorf("%s.verbs must list at least one verb", field))
	}
	return errors.Join(errs...)
}

func validAnnotationKey(k string) bool {
	prefix, name, hasPrefix := strings.Cut(k, "/")
	if !hasPrefix {
		name, prefix = prefix, ""
	}
	if len(name) == 0 || len(name) > 63 || !annotationNameRE.MatchString(name) {
		return false
	}
	return !hasPrefix || (len(prefix) <= 253 && annotationPrefixRE.MatchString(prefix))
}

// validatePodPorts checks that port names and numbers are unique across the
// whole POD. A sidecar shares the main container's network namespace, so two
// containers binding one port is a crash, and Kubernetes refuses a duplicate
// container port name in one pod.
func validatePodPorts(s WorkloadSpec) []error {
	var errs []error
	owner := map[string]string{} // port name / number+proto -> container
	claim := func(key, container string) {
		if prev, ok := owner[key]; ok && prev != container {
			errs = append(errs, fmt.Errorf("port %s is declared by both %s and %s: containers in one pod share a network namespace", key, prev, container))
		}
		owner[key] = container
	}
	for _, c := range append([]Container{{Name: "the main container", Ports: s.Ports}}, s.Sidecars...) {
		for _, p := range c.Ports {
			proto := p.Protocol
			if proto == "" {
				proto = DefaultPortProtocol
			}
			claim(fmt.Sprintf("name %q", p.Name), c.Name)
			claim(fmt.Sprintf("%d/%s", p.Port, proto), c.Name)
		}
	}
	return errs
}

func validateSidecars(sidecars []Container) []error {
	var errs []error
	seen := map[string]bool{}
	for _, c := range sidecars {
		label := fmt.Sprintf("sidecars[%s]", c.Name)
		if !dnsLabelRE.MatchString(c.Name) || len(c.Name) > 63 {
			errs = append(errs, fmt.Errorf("%s: name must be an RFC-1123 label of at most 63 characters", label))
		}
		if seen[c.Name] {
			errs = append(errs, fmt.Errorf("%s: sidecar name is declared twice", label))
		}
		seen[c.Name] = true
		if c.Image == "" {
			errs = append(errs, fmt.Errorf("%s: image is required", label))
		} else if strings.ContainsAny(c.Image, " \t\n") {
			errs = append(errs, fmt.Errorf("%s: image %q contains whitespace", label, c.Image))
		}
		for _, err := range validatePorts(KindWorker, c.Ports) {
			errs = append(errs, fmt.Errorf("%s: %w", label, err))
		}
		for _, p := range c.Ports {
			if p.Expose || len(p.Domains) > 0 {
				errs = append(errs, fmt.Errorf("%s: port %s: a sidecar's ports are container ports only; expose and domains belong on the workload's own port", label, p.Name))
			}
		}
		if c.Probes != nil {
			for _, err := range c.Probes.validate() {
				errs = append(errs, fmt.Errorf("%s: %w", label, err))
			}
			if c.ProbePort() == 0 {
				errs = append(errs, fmt.Errorf("%s: probes need a port: declare one in the sidecar's ports, or set probes.port", label))
			}
		}
		envSeen := map[string]bool{}
		for _, e := range c.Env {
			if err := e.Validate(); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", label, err))
			}
			if envSeen[e.Name] {
				errs = append(errs, fmt.Errorf("%s: env var %s is declared twice", label, e.Name))
			}
			envSeen[e.Name] = true
		}
		if err := c.Resources.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", label, err))
		}
	}
	return errs
}

func validateVolumes(s WorkloadSpec) []error {
	var errs []error
	names, paths := map[string]bool{TmpVolumeName: true}, map[string]string{TmpMountPath: TmpVolumeName}
	if s.StorageGiB > 0 {
		names[DataVolumeName] = true
		paths[DataMountPath] = DataVolumeName
	}
	for _, v := range s.Volumes {
		label := fmt.Sprintf("volumes[%s]", v.Name)
		if !dnsLabelRE.MatchString(v.Name) || len(v.Name) > 63 {
			errs = append(errs, fmt.Errorf("%s: name must be an RFC-1123 label of at most 63 characters", label))
		}
		if names[v.Name] {
			if v.Name == TmpVolumeName || (v.Name == DataVolumeName && s.StorageGiB > 0) {
				errs = append(errs, fmt.Errorf("%s: the name %q is reserved for forge's own %s volume", label, v.Name, v.Name))
			} else {
				errs = append(errs, fmt.Errorf("%s: volume name is declared twice", label))
			}
		}
		names[v.Name] = true
		switch {
		case !strings.HasPrefix(v.MountPath, "/"):
			errs = append(errs, fmt.Errorf("%s: mountPath %q must be absolute", label, v.MountPath))
		case paths[v.MountPath] != "":
			errs = append(errs, fmt.Errorf("%s: mountPath %s is already mounted by volume %s", label, v.MountPath, paths[v.MountPath]))
		}
		paths[v.MountPath] = v.Name

		src := v.Source
		set := 0
		if src.Secret != nil {
			set++
			if src.Secret.Name == "" {
				errs = append(errs, fmt.Errorf("%s: source.secret.name is required", label))
			}
			errs = append(errs, validateKeyToPaths(label+".source.secret", src.Secret.Items)...)
		}
		if src.ConfigMap != nil {
			set++
			if src.ConfigMap.Name == "" {
				errs = append(errs, fmt.Errorf("%s: source.configMap.name is required", label))
			}
			errs = append(errs, validateKeyToPaths(label+".source.configMap", src.ConfigMap.Items)...)
		}
		if src.EmptyDir != nil {
			set++
			if src.EmptyDir.SizeLimitBytes < 0 {
				errs = append(errs, fmt.Errorf("%s: source.emptyDir.sizeLimitBytes must not be negative", label))
			}
		}
		if src.PVC != nil {
			set++
			if src.PVC.ClaimName == "" {
				errs = append(errs, fmt.Errorf("%s: source.pvc.claimName is required", label))
			}
		}
		if set != 1 {
			errs = append(errs, fmt.Errorf("%s: source must set exactly one of secret, configMap, emptyDir, pvc (got %d): a volume with no source has nothing to mount, and one with two has no correct reading", label, set))
		}
	}
	return errs
}

func validateKeyToPaths(label string, items []KeyToPath) []error {
	var errs []error
	seen := map[string]bool{}
	for _, it := range items {
		if it.Key == "" {
			errs = append(errs, fmt.Errorf("%s.items: key is required", label))
		}
		switch {
		case it.Path == "":
			errs = append(errs, fmt.Errorf("%s.items[%s]: path is required", label, it.Key))
		case strings.HasPrefix(it.Path, "/") || slices.Contains(strings.Split(it.Path, "/"), ".."):
			errs = append(errs, fmt.Errorf("%s.items[%s]: path %q must be relative and must not contain '..': a key is never written outside its volume", label, it.Key, it.Path))
		}
		if seen[it.Path] {
			errs = append(errs, fmt.Errorf("%s.items: path %q is projected twice", label, it.Path))
		}
		seen[it.Path] = true
	}
	return errs
}

func (t Toleration) validate(label string) []error {
	var errs []error
	switch t.Operator {
	case "", TolerationOpEqual:
		if t.Key == "" {
			errs = append(errs, fmt.Errorf("%s: an empty key tolerates every taint, which requires operator Exists", label))
		}
	case TolerationOpExists:
		if t.Value != "" {
			errs = append(errs, fmt.Errorf("%s: operator Exists matches any value, so value must be empty", label))
		}
	default:
		errs = append(errs, fmt.Errorf("%s: operator %q must be Exists or Equal", label, t.Operator))
	}
	switch t.Effect {
	case "", TaintNoSchedule, TaintPreferNoSchedule, TaintNoExecute:
	default:
		errs = append(errs, fmt.Errorf("%s: effect %q must be NoSchedule, PreferNoSchedule or NoExecute", label, t.Effect))
	}
	if t.TolerationSeconds != nil {
		if t.Effect != TaintNoExecute {
			errs = append(errs, fmt.Errorf("%s: tolerationSeconds only applies to effect NoExecute (it bounds time before eviction)", label))
		}
		if *t.TolerationSeconds < 0 {
			errs = append(errs, fmt.Errorf("%s: tolerationSeconds must not be negative", label))
		}
	}
	if t.Key != "" && !validAnnotationKey(t.Key) {
		errs = append(errs, fmt.Errorf("%s: key %q is not a valid taint key ([prefix/]name)", label, t.Key))
	}
	return errs
}
