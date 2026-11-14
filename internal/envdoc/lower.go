package envdoc

// THE GO-SIDE LOWERING of a hand-written env document.
//
// A KCL render is post-lowering: kcl/render.k `_lower` and `render` decide
// things the author never wrote. A document may omit what Go can derive; this
// file applies the same default, in the same place, so a document does not
// silently lose a guarantee a KCL env has. Each step names the KCL it mirrors,
// and the internal/cli tests hold the two together (stripping what this file
// derives from every render-contract golden and re-deriving it).
//
// Every step only FILLS what the document left unset, so lowering a document
// that already carries the lowered form — a render saved as a document — is
// the identity.
//
// What only KCL can compute is refused here, naming why (design §3).

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/hostedimage"
	deployv1alpha1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// Defaults of the KCL schema (kcl/schema.k), for a block the document
// declares but leaves fields of unset. TestEnvDocumentDefaults_KCLMatchesGo
// renders the empty KCL schemas and compares.
const (
	controlPlaneType     = "control_plane"
	controlPlaneTokenEnv = "FORGE_CONTROL_PLANE_TOKEN"
	// defaultGoMemoryLimitPercent is render.k DEFAULT_GO_MEMORY_LIMIT_PERCENT.
	defaultGoMemoryLimitPercent = 90
)

// observabilitySignalDefaults are kcl/schema.k ObservabilitySignals' defaults.
var observabilitySignalDefaults = map[string]bool{
	"traces": true, "metrics": true, "logs": true, "metadata": true, "browser": true, "replay": false,
}

// runsAProcess is render.k _RUNS_A_PROCESS; hasMemoryLimit is _HAS_MEMORY_LIMIT.
var (
	runsAProcess   = map[string]bool{"host": true, "cluster": true, "hosted": true, "external": true}
	hasMemoryLimit = map[string]bool{"cluster": true, "hosted": true}
)

// Lower applies the Go-side lowering to a checked document, in place.
func Lower(doc map[string]any, opts Options) error {
	project, _ := doc["project"].(string)
	env, _ := doc["env"].(string)
	lowerControlPlane(doc)
	lowerObservability(doc)
	workloads, _ := doc["workloads"].([]any)
	names := map[string]bool{}
	for _, raw := range workloads {
		if w, ok := raw.(map[string]any); ok {
			name, _ := w["name"].(string)
			names[name] = true
		}
	}
	var problems []string
	var records []any
	for _, raw := range workloads {
		w, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _ := w["name"].(string)
		rt := runtimeType(w)
		spec := specOf(w)
		kind := kindOf(w, spec)
		problems = append(problems, maskViolations(w, spec, rt, kind)...)
		problems = append(problems, danglingRefs(name, spec, w, names)...)
		if rt == "cluster" && hasBuild(w) {
			problems = append(problems, fmt.Sprintf("workload %q: a cluster workload that forge builds needs KCL in env schema v1 — its image is resolved per release "+
				"(the tag and the pushed digest) by the KCL render; declare it in deploy/kcl/<env>/main.k, or deploy a prebuilt image: drop `build` and set spec.image to the full reference", name))
		}
		// The hosted mask is Go's already: the deploy path runs
		// Workload.Validate(ProfileRestricted) on the PINNED spec
		// (deploytarget.PreflightHosted), for a document as for KCL. Running
		// it here, before the publish pins the artifact name to repo@digest,
		// would refuse every forge-built hosted workload.
		if spec == nil {
			spec = map[string]any{}
			w["spec"] = spec
		}
		// The workload's kind is stated once; the spec carries it (the
		// parser refuses the two disagreeing).
		if k, _ := w["kind"].(string); k != "" && k != string(deployv1alpha1.DefaultWorkloadKind) {
			setDefault(spec, "kind", k)
		}
		deriveEnv(w, spec, rt, kind, project, env, opts.Release)
		defaultProbes(w, spec, kind)
		if rt == "cluster" && kind != "tool" {
			records = append(records, workloadRecord(w, spec, project, env, opts.Release))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("the document declares what this runtime cannot honour:\n  - %s", strings.Join(problems, "\n  - "))
	}
	addWorkloadRecords(doc, records)
	return nil
}

// lowerControlPlane fills a declared control_plane's unset fields with
// kcl/schema.k ControlPlane's defaults: Reliant's endpoint, its token env and
// its registry.
func lowerControlPlane(doc map[string]any) {
	cp, ok := doc["control_plane"].(map[string]any)
	if !ok {
		return
	}
	setDefault(cp, "type", controlPlaneType)
	setDefault(cp, "endpoint", cloud.DefaultEndpoint)
	setDefault(cp, "token_env", controlPlaneTokenEnv)
	setDefault(cp, "registry_host", hostedimage.DefaultRegistryHost)
}

// lowerObservability fills a declared observability block's switch, signals
// and sampling ratio with kcl/schema.k Observability's defaults: a block that
// says only where telemetry goes is on, with every signal the schema turns on
// by default.
func lowerObservability(doc map[string]any) {
	o, ok := doc["observability"].(map[string]any)
	if !ok {
		return
	}
	setDefault(o, "enabled", true)
	sig, _ := o["signals"].(map[string]any)
	if sig == nil {
		sig = map[string]any{}
		o["signals"] = sig
	}
	for k, v := range observabilitySignalDefaults {
		setDefault(sig, k, v)
	}
	sm, _ := o["sampling"].(map[string]any)
	if sm == nil {
		sm = map[string]any{}
		o["sampling"] = sm
	}
	setDefault(sm, "traces_ratio", json.Number("1"))
}

func setDefault(m map[string]any, k string, v any) {
	if cur, ok := m[k]; !ok || cur == nil {
		m[k] = v
	}
}

func runtimeType(w map[string]any) string {
	rt, _ := w["runtime"].(map[string]any)
	t, _ := rt["type"].(string)
	return t
}

func specOf(w map[string]any) map[string]any {
	s, _ := w["spec"].(map[string]any)
	return s
}

func hasBuild(w map[string]any) bool {
	b, ok := w["build"].(map[string]any)
	return ok && b != nil
}

func buildType(w map[string]any) string {
	b, _ := w["build"].(map[string]any)
	t, _ := b["type"].(string)
	return strings.ToLower(strings.TrimSpace(t))
}

// kindOf is the workload's kind: its own `kind`, else spec.kind, else the
// default (decodeWorkload refuses the two disagreeing).
func kindOf(w, spec map[string]any) string {
	if k, _ := w["kind"].(string); k != "" {
		return k
	}
	if k, _ := spec["kind"].(string); k != "" {
		return k
	}
	return string(deployv1alpha1.DefaultWorkloadKind)
}

// decodeSpec reads the spec into its Go type, for checks that live there.
// The parser decodes it strictly later; a spec this cannot read fails there,
// with that error.
func decodeSpec(spec map[string]any) deployv1alpha1.WorkloadSpec {
	var s deployv1alpha1.WorkloadSpec
	if b, err := json.Marshal(spec); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

// --- env: render.k _derived_env -------------------------------------------

// envEntry finds the spec.env entry named k: its index, or -1.
func envEntry(entries []any, k string) int {
	for i, e := range entries {
		if m, ok := e.(map[string]any); ok && m["name"] == k {
			return i
		}
	}
	return -1
}

// envSet is render.k _env_set: the key is present and is not a blank literal.
func envSet(entries []any, k string) bool {
	i := envEntry(entries, k)
	if i < 0 {
		return false
	}
	m := entries[i].(map[string]any)
	for key, v := range m {
		if key == "name" || v == nil {
			continue
		}
		if s, ok := v.(string); ok && s == "" && key == "value" {
			continue
		}
		return true
	}
	return false
}

// envString is a literal env value, "" for a reference or an unset key
// (render.k _env_str).
func envString(entries []any, k string) string {
	i := envEntry(entries, k)
	if i < 0 {
		return ""
	}
	s, _ := entries[i].(map[string]any)["value"].(string)
	return s
}

// otelEncode is render.k _otel_encode.
func otelEncode(v string) string {
	return strings.NewReplacer("%", "%25", ",", "%2C", "=", "%3D", " ", "%20").Replace(v)
}

// resourceAttributes is render.k _otel_resource_attributes.
func resourceAttributes(project, env, release string) string {
	var parts []string
	if env != "" {
		parts = append(parts, "deployment.environment.name="+otelEncode(env))
	}
	parts = append(parts, "service.namespace="+otelEncode(project))
	if release != "" {
		parts = append(parts, "service.version="+otelEncode(release))
	}
	return strings.Join(parts, ",")
}

// deriveEnv fills the env keys render.k _derived_env derives from what the
// document holds: the telemetry identity every process carries, and
// GOMEMLIMIT for a Go process under a memory limit. A key the author set
// wins; a blank one keeps its position and takes the derived value.
func deriveEnv(w, spec map[string]any, rt, kind, project, env, release string) {
	if !runsAProcess[rt] || kind == "tool" {
		return
	}
	entries, _ := spec["env"].([]any)
	set := func(k, v string) {
		if v == "" || envSet(entries, k) {
			return
		}
		if i := envEntry(entries, k); i >= 0 {
			entries[i] = map[string]any{"name": k, "value": v}
			return
		}
		entries = append(entries, map[string]any{"name": k, "value": v})
	}
	name, _ := w["name"].(string)
	set("OTEL_SERVICE_NAME", name)
	set("OTEL_RESOURCE_ATTRIBUTES", resourceAttributes(project, env, release))
	if hasMemoryLimit[rt] && !envSet(entries, "GOGC") {
		set("GOMEMLIMIT", goMemLimit(w, spec))
	}
	if len(entries) > 0 {
		spec["env"] = entries
	}
}

// goMemLimit is render.k _gomemlimit for a workload whose env leaves
// GOMEMLIMIT and GOGC unset: the default percent of a Go build's memory
// limit (the limit, else the request, else the default request).
func goMemLimit(w, spec map[string]any) string {
	if buildType(w) != "go" {
		return ""
	}
	// An unset block decodes to the zero Resources, whose defaults are the
	// default request: the same answer as KCL's `_TResources {}`.
	bytes := decodeSpec(map[string]any{"resources": spec["resources"]}).Resources.WithDefaults().MemoryLimitBytes
	mib := bytes * defaultGoMemoryLimitPercent / 100 / (1 << 20)
	if mib <= 0 {
		return ""
	}
	return strconv.FormatInt(mib, 10) + "MiB"
}

// --- probes: render.k _default_probes ---------------------------------------

// defaultProbes gives a forge-built service (or an operator with ports) the
// readiness/liveness/startup probes ADR 0002 §5 promises, when the document
// declares none: the paths from READINESS_PATHS / LIVENESS_PATHS, on PORT or
// the `http` port.
func defaultProbes(w, spec map[string]any, kind string) {
	if p, ok := spec["probes"]; ok && p != nil {
		return
	}
	ports, _ := spec["ports"].([]any)
	if !hasBuild(w) || !(kind == "service" || (kind == "operator" && len(ports) > 0)) {
		return
	}
	entries, _ := spec["env"].([]any)
	ready := probePath(entries, "READINESS_PATHS", deployv1alpha1.DefaultReadinessPath)
	p := deployv1alpha1.Probes{
		StartupPath:   ready,
		ReadinessPath: ready,
		LivenessPath:  probePath(entries, "LIVENESS_PATHS", deployv1alpha1.DefaultLivenessPath),
		Port:          int32(probePort(entries, ports)),
	}.WithDefaults()
	b, _ := json.Marshal(p)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	spec["probes"] = m
}

// probePath is render.k _probe_path: the first path of a comma-separated
// list in env name, else the default.
func probePath(entries []any, name, def string) string {
	for _, p := range strings.Split(envString(entries, name), ",") {
		if p = strings.TrimSpace(p); p != "" {
			return p
		}
	}
	return def
}

// probePort is render.k _probe_port / _http_port: PORT when it is a literal
// number, else the port named http, else the first, else 8080.
func probePort(entries []any, ports []any) int {
	if p := envString(entries, "PORT"); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 {
			return n
		}
	}
	first := 0
	for i, raw := range ports {
		m, _ := raw.(map[string]any)
		n := intOf(m["port"])
		if i == 0 {
			first = n
		}
		if m["name"] == "http" {
			return n
		}
	}
	if first > 0 {
		return first
	}
	return 8080
}

func intOf(v any) int {
	switch n := v.(type) {
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

// --- manifests: render.k _workload_record -----------------------------------

// workloadRecord is the forge.dev/v1alpha1 Workload record a cluster-bound
// workload becomes in the applyable stream: what pkg/deploy.RenderWorkloads
// expands, carrying the same spec.
func workloadRecord(w, spec map[string]any, project, env, release string) map[string]any {
	name, _ := w["name"].(string)
	rt, _ := w["runtime"].(map[string]any)
	cluster, _ := rt["cluster"].(string)
	ns, _ := rt["namespace"].(string)
	partOf := project
	if partOf == "" {
		partOf = name
	}
	labels := map[string]any{
		"app.kubernetes.io/name":       name,
		"app.kubernetes.io/managed-by": "forge",
		"app.kubernetes.io/part-of":    partOf,
		"forge.dev/cluster":            cluster,
	}
	if env != "" {
		labels["forge.dev/env"] = env
	}
	if u, _ := w["delivery_unit"].(string); u != "" {
		labels["forge.dev/delivery-unit"] = u
	}
	if release != "" {
		labels["app.kubernetes.io/version"] = release
	}
	return map[string]any{
		"apiVersion": "forge.dev/v1alpha1",
		"kind":       "Workload",
		"metadata":   map[string]any{"name": name, "namespace": ns, "labels": labels},
		"spec":       spec,
	}
}

// addWorkloadRecords puts each record in the stream unless the document
// already carries a Workload record of that name (a render saved as a
// document does).
func addWorkloadRecords(doc map[string]any, records []any) {
	if len(records) == 0 {
		return
	}
	manifests, _ := doc["manifests"].([]any)
	have := map[string]bool{}
	for _, raw := range manifests {
		m, _ := raw.(map[string]any)
		if m["kind"] == "Workload" {
			md, _ := m["metadata"].(map[string]any)
			n, _ := md["name"].(string)
			have[n] = true
		}
	}
	for _, r := range records {
		md := r.(map[string]any)["metadata"].(map[string]any)
		if !have[md["name"].(string)] {
			manifests = append(manifests, r)
		}
	}
	doc["manifests"] = manifests
}

// --- checks: render.k _check_requires / _check_before ------------------------

func danglingRefs(name string, spec, w map[string]any, names map[string]bool) []string {
	var out []string
	check := func(field string, list []any, allow string) {
		for _, raw := range list {
			ref, _ := raw.(string)
			if ref == "" || ref == allow || names[ref] {
				continue
			}
			out = append(out, fmt.Sprintf("workload %q: %s names %q, which this env does not declare", name, field, ref))
		}
	}
	reqs, _ := w["requires"].([]any)
	check("requires", reqs, "")
	before, _ := spec["before"].([]any)
	check("spec.before", before, "*")
	return out
}

// --- the runtime mask: render.k _mask_violations ------------------------------

// localRefused is render.k _LOCAL_REFUSED: spec fields a local process cannot
// honour, and why. composeRefused and buildOnlyRefused are _COMPOSE_REFUSED
// and _BUILD_ONLY_REFUSED. TestEnvDocumentMaskMatchesKCL reads the three
// tables out of kcl/render.k and fails when the key sets differ.
var localRefused = map[string]string{
	"storageGiB":                    "a local process has no volume to provision; use a host path in its config",
	"namespacedRBAC":                "a local process has no Kubernetes identity to grant it to",
	"clusterRBAC":                   "a local process has no Kubernetes identity to grant it to",
	"serviceAccount":                "a local process has no Kubernetes identity",
	"serviceAccountAnnotations":     "a local process has no Kubernetes identity to annotate",
	"sidecars":                      "a local process has no pod to add a container to",
	"volumes":                       "a local process has no pod to mount into; it reads the host filesystem",
	"nodeSelector":                  "a local process is not scheduled onto nodes",
	"tolerations":                   "a local process is not scheduled onto nodes",
	"podAnnotations":                "a local process has no pod to annotate",
	"priorityClassName":             "a local process is not scheduled, so there is no pod to rank",
	"enableServiceLinks":            "a local process has no pod, so Kubernetes injects no service links",
	"strategy":                      "a local process has no Deployment to roll out; forge env up starts it once",
	"securityContext":               "a local process runs as the user who started it",
	"terminationGracePeriodSeconds": "a local process is stopped by forge env down, not by a kubelet",
}

var composeRefused = func() map[string]string {
	m := map[string]string{
		"preStopSeconds": "the compose file owns how its service stops; set stop_grace_period there",
		"build":          "compose builds its own image from the compose file; drop `build`, or bind a runtime forge builds for",
		"ports":          "the compose file owns the published ports; declare them there",
		"probes":         "the compose file owns the healthcheck; declare it there",
		"replicas":       "the compose file owns the scale",
		"command":        "the compose file owns the container's command",
		"args":           "the compose file owns the container's command",
		"resources":      "the compose file owns the container's resources",
		"schedule":       "compose has no cron",
		"crds":           "compose has no Kubernetes API",
	}
	for k, v := range localRefused {
		m[k] = v
	}
	return m
}()

var buildOnlyRefused = map[string]string{
	"ports":      "a build-only workload is never run, so nothing listens",
	"probes":     "a build-only workload is never run, so there is nothing to probe",
	"replicas":   "a build-only workload is never run",
	"storageGiB": "a build-only workload is never run",
}

// specSet is render.k _set: a field is set when it is truthy, except
// replicas, which is set when it is not 1. `build` lives on the workload, not
// its spec.
func specSet(w, spec map[string]any, f string) bool {
	var v any
	if f == "build" {
		v = w["build"]
	} else {
		v = spec[f]
	}
	if f == "replicas" {
		return v != nil && intOf(v) != 1
	}
	// The render emits every workload's resources, defaulted; only a block
	// that differs from the defaults is something the author set.
	if f == "resources" && v != nil {
		r := decodeSpec(map[string]any{"resources": v}).Resources
		return r.WithDefaults() != (deployv1alpha1.Resources{}).WithDefaults()
	}
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case json.Number:
		f, _ := x.Float64()
		return f != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

func maskViolations(w, spec map[string]any, rt, kind string) []string {
	name, _ := w["name"].(string)
	refuse := func(table map[string]string) []string {
		var out []string
		keys := make([]string, 0, len(table))
		for k := range table {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, f := range keys {
			if specSet(w, spec, f) {
				out = append(out, fmt.Sprintf("workload %q on the %s runtime: %s: %s", name, rt, f, table[f]))
			}
		}
		return out
	}
	switch rt {
	case "host":
		out := refuse(localRefused)
		if specSet(w, spec, "replicas") {
			out = append(out, fmt.Sprintf("workload %q on the host runtime: replicas: a host workload is one local process; forge env up launches it once", name))
		}
		r, _ := w["runtime"].(map[string]any)
		runner, _ := r["runner"].(string)
		if (runner == "go-run" || runner == "binary" || runner == "delve") && buildType(w) != "go" && !specSet(w, spec, "command") {
			out = append(out, fmt.Sprintf("workload %q on the host runtime: build: the %s runner derives its command from a go build; add one, or set spec.command to run a binary verbatim", name, runner))
		}
		if kind == "cron" || kind == "operator" {
			out = append(out, fmt.Sprintf("workload %q on the host runtime: kind %q cannot run as a local process; bind it to the cluster runtime", name, kind))
		}
		return out
	case "compose":
		return refuse(composeRefused)
	case "build-only":
		out := refuse(buildOnlyRefused)
		if !hasBuild(w) {
			out = append(out, fmt.Sprintf("workload %q on the build-only runtime: build: a build-only workload with no build has nothing to produce", name))
		}
		return out
	}
	return nil
}
