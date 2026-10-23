package v1alpha1

import (
	"fmt"
	"reflect"
	"strings"
)

// Profile is what a DESTINATION allows a Workload to say (ADR 0002 §3).
//
// It is a property of where the workload runs, never of the workload. The
// hosted control plane picks it server-side from the destination's
// isolation. The CLI runs the same check at render time only so the author
// sees the error before publishing. A client-chosen profile would be a
// hosted user choosing their own sandbox.
//
// Profiles are ORDERED from most to least permissive. A field classified
// at profile X is allowed under X and every more permissive profile, which
// is what Permits encodes.
type Profile int

const (
	// ProfileFull allows every field: a cluster the author operates, and
	// (later) a hosted user's own vcluster.
	ProfileFull Profile = iota
	// ProfileRestricted is the hosted runtime on shared nodes. It allows
	// what reaches only the hosted user's own pod, and refuses anything that
	// reaches the Kubernetes API, another object in the namespace, or
	// platform-owned identity.
	ProfileRestricted
)

func (p Profile) String() string {
	switch p {
	case ProfileFull:
		return "full"
	case ProfileRestricted:
		return "restricted"
	default:
		return fmt.Sprintf("Profile(%d)", int(p))
	}
}

// Permits reports whether a destination with profile p accepts something
// classified at fieldProfile.
func (p Profile) Permits(fieldProfile Profile) bool { return p <= fieldProfile }

// FieldProfiles classifies EVERY WorkloadSpec field, by JSON path, at the
// most restrictive profile that allows it. It is the whole profile mask, in
// one table.
//
// THE TABLE IS DEFAULT-DENY. Validate walks a spec by reflection and
// refuses, under EVERY profile, any set field whose path is missing from
// this table, and TestFieldProfilesClassifyEverySpecField fails the build when a
// field is added to the spec without an entry. So a new field can never
// silently become writable by a hosted user: someone has to write down which profile
// allows it.
//
// Paths are JSON names joined with '.', and list elements share their list's
// path ("env.value", "ports.expose"). A field classified ProfileFull is
// refused whole under ProfileRestricted, so its children need no entry.
// Every child of a Restricted-allowed struct needs one.
var FieldProfiles = map[string]Profile{
	"kind":     ProfileRestricted,
	"image":    ProfileRestricted,
	"command":  ProfileRestricted,
	"args":     ProfileRestricted,
	"replicas": ProfileRestricted,

	"resources":                      ProfileRestricted,
	"resources.cpuRequestMillicores": ProfileRestricted,
	"resources.cpuLimitMillicores":   ProfileRestricted,
	"resources.memoryRequestBytes":   ProfileRestricted,
	"resources.memoryLimitBytes":     ProfileRestricted,

	"env":               ProfileRestricted,
	"env.name":          ProfileRestricted,
	"env.value":         ProfileRestricted,
	"env.managedSecret": ProfileRestricted,
	// optional is Restricted too: it is the hosted spelling of an optional
	// credential, and it widens nothing. The name still resolves only
	// within the workload's own environment's store.
	"env.managedSecret.name":     ProfileRestricted,
	"env.managedSecret.optional": ProfileRestricted,
	"env.databaseRef":            ProfileRestricted,
	"env.databaseRef.name":       ProfileRestricted,
	"env.databaseRef.key":        ProfileRestricted,
	"env.workloadURL":            ProfileRestricted,
	"env.workloadURL.name":       ProfileRestricted,
	"env.secretRef":              ProfileFull,
	"env.configMapRef":           ProfileFull,
	"env.fieldRef":               ProfileFull,

	"ports":          ProfileRestricted,
	"ports.name":     ProfileRestricted,
	"ports.port":     ProfileRestricted,
	"ports.protocol": ProfileRestricted,
	"ports.expose":   ProfileRestricted,
	// appProtocol only sets the Service port's appProtocol, and domains are
	// custom hostnames that hosted verifies before serving. Neither reaches
	// past the hosted user's own workload.
	"ports.appProtocol": ProfileRestricted,
	"ports.domains":     ProfileRestricted,

	"probes":                     ProfileRestricted,
	"probes.port":                ProfileRestricted,
	"probes.readinessPath":       ProfileRestricted,
	"probes.livenessPath":        ProfileRestricted,
	"probes.startupPath":         ProfileRestricted,
	"probes.tcp":                 ProfileRestricted,
	"probes.initialDelaySeconds": ProfileRestricted,
	"probes.periodSeconds":       ProfileRestricted,
	"probes.timeoutSeconds":      ProfileRestricted,
	"probes.failureThreshold":    ProfileRestricted,

	"storageGiB": ProfileRestricted,

	// before and deployPhase order the hosted user's OWN workloads against each
	// other and reach nothing outside the env. A hosted migrate job that
	// could not be ordered is the defect ADR 0002 names ("silently
	// ignored"), so jobs are only useful on Restricted with these.
	"before":      ProfileRestricted,
	"deployPhase": ProfileRestricted,

	"schedule":                  ProfileFull,
	"namespacedRBAC":            ProfileFull,
	"clusterRBAC":               ProfileFull,
	"crds":                      ProfileFull,
	"group":                     ProfileFull,
	"version":                   ProfileFull,
	"leaderElection":            ProfileFull,
	"serviceAccountAnnotations": ProfileFull,

	// Pod-level escape hatches (pod_types.go).
	"sidecars":       ProfileFull,
	"volumes":        ProfileFull,
	"serviceAccount": ProfileFull,
	"nodeSelector":   ProfileFull,
	"tolerations":    ProfileFull,
	"podAnnotations": ProfileFull,
	// Pod priority ranks this pod against every other pod on the node,
	// including other hosted users' pods.
	"priorityClassName": ProfileFull,
	// Service links put every Service in the namespace into the pod's env.
	// A hosted namespace is shared platform plumbing, and the hosted user
	// has no use for it: they address their own workloads by name.
	"enableServiceLinks": ProfileFull,

	// A timeout only ever shortens what runs, so it is not an escalation.
	"activeDeadlineSeconds": ProfileRestricted,
	// The rollout strategy trades the author's OWN availability against
	// running one version at a time. It reaches nothing outside their
	// workload, so a hosted user decides it.
	"strategy": ProfileRestricted,
	// The grace period holds a node's capacity past SIGTERM, and the pod
	// identity is the platform's to set.
	"terminationGracePeriodSeconds": ProfileFull,
	"securityContext":               ProfileFull,
}

// fullOnlyReasons is WHY each ProfileFull field is refused under
// ProfileRestricted. The error names the field and this reason, so an author
// learns what to do instead of only that it is not allowed.
// TestFieldProfilesClassifyEverySpecField requires a reason for every
// Full-only entry.
var fullOnlyReasons = map[string]string{
	"env.secretRef":                 "a raw Secret name in a shared hosted namespace can address platform-owned Secrets; use managedSecret, which names a value in your environment's secret store",
	"env.configMapRef":              "a ConfigMap reference reads a namespace object you did not write; use value, or managedSecret for a credential",
	"env.fieldRef":                  "the Downward API exposes pod and node placement, which the hosted platform owns",
	"schedule":                      "cron is not available on hosted until metering covers it",
	"namespacedRBAC":                "hosted workloads have no Kubernetes API access until each hosted user runs in an isolated cluster (vcluster)",
	"clusterRBAC":                   "hosted workloads have no Kubernetes API access until each hosted user runs in an isolated cluster (vcluster)",
	"crds":                          "operators need cluster-scoped Kubernetes API access, which shared hosted nodes cannot grant until each hosted user runs in an isolated cluster (vcluster)",
	"group":                         "operators need cluster-scoped Kubernetes API access, which shared hosted nodes cannot grant until each hosted user runs in an isolated cluster (vcluster)",
	"version":                       "operators need cluster-scoped Kubernetes API access, which shared hosted nodes cannot grant until each hosted user runs in an isolated cluster (vcluster)",
	"leaderElection":                "operators need cluster-scoped Kubernetes API access, which shared hosted nodes cannot grant until each hosted user runs in an isolated cluster (vcluster)",
	"serviceAccountAnnotations":     "hosted workloads run under a platform-owned ServiceAccount; cloud workload identity is bound by the platform, never by the workload's author",
	"sidecars":                      "the hosted platform composes the pod itself (runtime class, isolation, egress), and a second container you author would run outside the single-container policy it enforces",
	"volumes":                       "a Secret, ConfigMap or PVC volume reads namespace objects you did not write; use storageGiB for a persistent disk, and env for configuration",
	"serviceAccount":                "hosted workloads run under a platform-owned ServiceAccount; naming another one would borrow its identity",
	"nodeSelector":                  "hosted placement (the isolation pool) is decided by the platform, never by the workload's author",
	"tolerations":                   "hosted placement (the isolation pool) is decided by the platform, never by the workload's author; a toleration would let a pod onto nodes reserved for something else",
	"priorityClassName":             "the hosted platform ranks hosted pods itself, on its own priority ladder; a class you chose could preempt another hosted user's pods, or the platform's own",
	"enableServiceLinks":            "service links inject an env var per Service in the namespace; a hosted namespace is platform-owned, and its Services are not the author's to read. Address your own workloads by DNS name",
	"podAnnotations":                "pod annotations drive platform integrations (mesh injection, runtime class, autoscalers) that the hosted platform owns",
	"terminationGracePeriodSeconds": "the hosted platform sets the grace period, because it holds shared node capacity past SIGTERM; tune PRE_STOP_DELAY and SHUTDOWN_TIMEOUT, which it derives from",
	"securityContext":               "the hosted platform owns the pod identity (uid, gid, filesystem) its isolation depends on",
}

// KindProfiles is the kind mask: the most restrictive profile each kind is
// allowed under. Restricted runs service, worker and job.
var KindProfiles = map[WorkloadKind]Profile{
	KindService:  ProfileRestricted,
	KindWorker:   ProfileRestricted,
	KindJob:      ProfileRestricted,
	KindCron:     ProfileFull,
	KindOperator: ProfileFull,
	KindTool:     ProfileFull,
}

// fullOnlyKindReasons is WHY each Full-only kind is refused under
// ProfileRestricted.
var fullOnlyKindReasons = map[WorkloadKind]string{
	KindCron:     "cron is not available on hosted until metering covers it",
	KindOperator: "an operator needs cluster-scoped Kubernetes API access, which shared hosted nodes cannot grant until each hosted user runs in an isolated cluster (vcluster)",
	KindTool:     "a tool is never scheduled, so there is nothing for the hosted runtime to run",
}

// FullOnlyReason is why a ProfileFull field path (a FieldProfiles key) is
// refused under ProfileRestricted, or "" when the path is not Full-only.
// Exported so author-time checks (the generated KCL runtime mask) state the
// same reason admission does, from the one table.
func FullOnlyReason(path string) string { return fullOnlyReasons[path] }

// FullOnlyKindReason is why a Full-only kind is refused under
// ProfileRestricted, or "" when the kind is allowed there.
func FullOnlyKindReason(k WorkloadKind) string { return fullOnlyKindReasons[k] }

// profileViolations walks a spec and returns one error per set field that
// profile p does not permit, including fields FieldProfiles does not
// classify at all (default-deny).
func profileViolations(s WorkloadSpec, p Profile) []error {
	var errs []error
	if kp, ok := KindProfiles[s.EffectiveKind()]; ok && !p.Permits(kp) {
		errs = append(errs, fmt.Errorf("kind %q is not allowed under the %s profile: %s", s.EffectiveKind(), p, fullOnlyKindReasons[s.EffectiveKind()]))
	}
	walkSpecFields(reflect.ValueOf(s), "", "", func(key, display string, _ reflect.Value) bool {
		fp, ok := FieldProfiles[key]
		if !ok {
			errs = append(errs, fmt.Errorf("%s: this field has no profile classification, so no profile may set it (add it to FieldProfiles)", display))
			return false
		}
		if !p.Permits(fp) {
			errs = append(errs, fmt.Errorf("%s: not allowed under the %s profile: %s", display, p, fullOnlyReasons[key]))
			return false
		}
		return classifiesChildren(fp)
	})
	return errs
}

// classifiesChildren reports whether the children of a field classified at
// fp need classifications of their own. A field only ProfileFull allows is
// allowed or refused WHOLE, because the most permissive profile has nothing
// narrower to say about its parts. Only the children of a field some
// restricted profile allows are classified one by one.
func classifiesChildren(fp Profile) bool { return fp != ProfileFull }

// walkSpecFields visits every SET field of a struct value, depth-first,
// calling visit with its table key ("env.secretRef"), a display path that
// names list elements ("env[DB_URL].secretRef"), and the value. visit
// returns whether to descend into the field. Zero values are skipped: an
// unset field says nothing, so no profile can refuse it.
func walkSpecFields(v reflect.Value, key, display string, visit func(key, display string, v reflect.Value) bool) {
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return
	}
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		name := jsonName(t.Field(i))
		if name == "" {
			continue
		}
		fv := v.Field(i)
		if fv.IsZero() {
			continue
		}
		fk, fd := joinPath(key, name), joinPath(display, name)
		if !visit(fk, fd, fv) {
			continue
		}
		switch fv.Kind() {
		case reflect.Slice:
			for j := 0; j < fv.Len(); j++ {
				walkSpecFields(fv.Index(j), fk, fd+elemLabel(fv.Index(j), j), visit)
			}
		case reflect.Struct, reflect.Pointer:
			walkSpecFields(fv, fk, fd, visit)
		}
	}
}

// specFieldPaths lists every JSON path a struct type can carry, with the
// same keys walkSpecFields produces, stopping below a path stop reports
// true. It is the type-level twin of walkSpecFields, used by the
// default-deny test.
func specFieldPaths(t reflect.Type, key string, stop func(string) bool) []string {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	var out []string
	for i := 0; i < t.NumField(); i++ {
		name := jsonName(t.Field(i))
		if name == "" {
			continue
		}
		fk := joinPath(key, name)
		out = append(out, fk)
		if !stop(fk) {
			out = append(out, specFieldPaths(t.Field(i).Type, fk, stop)...)
		}
	}
	return out
}

func jsonName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	if name == "-" {
		return ""
	}
	return name
}

func joinPath(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

// elemLabel names a list element in an error: by its Name when it has one
// (env vars, ports), else by index.
func elemLabel(v reflect.Value, i int) string {
	if v.Kind() == reflect.Struct {
		if f := v.FieldByName("Name"); f.IsValid() && f.Kind() == reflect.String && f.String() != "" {
			return "[" + f.String() + "]"
		}
	}
	return fmt.Sprintf("[%d]", i)
}
