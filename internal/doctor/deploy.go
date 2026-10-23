package doctor

// deploy.go — deployability checks.
//
// The rest of doctor answers "is my dev stack healthy". These checks
// answer the question an SRE asks on day one: "would I let this reach
// production". They render the project's own deploy/kcl/<env> packages
// through the embedded KCL runtime — the same seam `forge env up` and
// `forge ci validate-kcl` use — and assert properties of the manifests
// that will actually be applied, not of the .k source that produces
// them. Rendering is the point: every defect these catch is invisible
// in the source and only exists in the output.
//
// Each check SKIPs when the project declares no environments, so
// `--kind cli` / `--kind library` projects (no deploy/) stay quiet.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/internal/kclrender"
)

// k8sObject is the slice of a rendered manifest these checks reason
// about. Everything else stays as raw JSON so a schema addition
// upstream never breaks the parse.
type k8sObject struct {
	APIVersion string         `json:"apiVersion"`
	Kind       string         `json:"kind"`
	Metadata   objectMeta     `json:"metadata"`
	Spec       map[string]any `json:"spec"`
	// raw is the object's exact rendered bytes, kept because CONTENT is what
	// separates a benign cluster-scoped singleton (every env renders an
	// identical CRD / GatewayClass) from a real one (a ClusterRoleBinding
	// whose subject namespace differs per env, so the last env to deploy
	// steals it). The typed fields above cannot answer that — they are a
	// deliberate slice of the object, and the difference usually lives in the
	// part that was sliced off. See CheckObjectCollision.
	raw []byte
	// platform marks an object the CONTROL PLANE runs for a hosted env
	// (pkg/deploy.Render of a tier spec) rather than one forge applies. The
	// checks' evidence then names the tier field to change, since the
	// author never wrote this manifest and cannot edit it.
	platform bool
}

// UnmarshalJSON decodes the typed fields AND retains the original document.
// encoding/json has no "give me both" mode, so the alias dodges the infinite
// recursion this method would otherwise cause on itself.
func (o *k8sObject) UnmarshalJSON(b []byte) error {
	type alias k8sObject
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*o = k8sObject(a)
	o.raw = append([]byte(nil), b...)
	return nil
}

type objectMeta struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	// Labels carries the two routing labels the DEPLOY-TIME scoper reads
	// (cluster.AppNameLabel / cluster.ClusterRoutingLabel). They are what
	// decides which cluster an object actually lands on, so a check that
	// reasons about namespaces without them is reasoning about a namespace
	// with no address — see envRender.clustersOf.
	Labels map[string]string `json:"labels"`
}

// manifestRootKey names the applyable stream inside the deploy contract:
// `output.manifests`. forge expands it (cluster.ExtractManifests: every
// forge.dev Workload record rendered through pkg/deploy.RenderWorkloads)
// before applying it, and doctor judges exactly that expanded stream.
const manifestRootKey = "output.manifests"

// outputRootKey is the ONE top-level key a render exports: forge's JSON
// deploy contract (`output = forge.render(bundle)`), the same document
// `forge build` / `forge env deploy` consume.
const outputRootKey = "output"

// parseRenderedFrontends reads the frontend declarations out of the
// `output` contract. Same best-effort contract as parseHostServices: a
// render that predates the key, or whose shape has moved on, answers nil
// rather than failing the whole parse.
func parseRenderedFrontends(raw json.RawMessage) []renderedFrontend {
	if len(raw) == 0 {
		return nil
	}
	var contract struct {
		Frontends []renderedFrontend `json:"frontends"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		return nil
	}
	return contract.Frontends
}

// parseHostServices reads the HOST-bound workloads out of the `output`
// contract (`workloads[runtime.type == "host"]`). Anything unparseable
// answers nil: this is a best-effort enrichment of a check that must keep
// working on a render whose shape has moved on.
func parseHostServices(raw json.RawMessage) []renderedService {
	if len(raw) == 0 {
		return nil
	}
	var contract struct {
		Workloads []renderedWorkload `json:"workloads"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		return nil
	}
	var hosts []renderedService
	for _, w := range contract.Workloads {
		if w.Runtime.Type != "host" {
			continue
		}
		svc := renderedService{Name: w.Name, Command: append(append([]string{}, w.Spec.Command...), w.Spec.Args...)}
		svc.Deploy.Type = "host"
		for _, e := range w.Spec.Env {
			if e.Value != nil {
				svc.EnvVars = append(svc.EnvVars, renderedEnv{Name: e.Name, Value: *e.Value})
			}
		}
		hosts = append(hosts, svc)
	}
	return hosts
}

// renderedWorkload is the slice of one `output.workloads[]` entry doctor
// reads: its name, its runtime placement, and the spec fields a host
// workload's checks reason about.
type renderedWorkload struct {
	Name    string `json:"name"`
	Runtime struct {
		Type    string `json:"type"`
		Cluster string `json:"cluster"`
	} `json:"runtime"`
	Spec struct {
		Command []string `json:"command"`
		Args    []string `json:"args"`
		Env     []struct {
			Name  string  `json:"name"`
			Value *string `json:"value"`
		} `json:"env"`
	} `json:"spec"`
}

// parseClusterAttribution reads the per-workload cluster coordinates out of
// the `output` contract: which kubectl context each Cluster-bound workload
// declares (its runtime.cluster), plus the full set of contexts the env
// touches (those, and the env-wide cluster_target).
//
// Best-effort, like parseHostServices: a render whose shape has moved on
// answers empty, and the caller degrades to UNDETERMINED rather than guessing.
func parseClusterAttribution(raw json.RawMessage) (map[string]string, []string) {
	if len(raw) == 0 {
		return nil, nil
	}
	var contract struct {
		// The env-wide target: present for every env that deploys support
		// resources to a cluster, and the only cluster fact some envs have.
		ClusterTarget *struct {
			Cluster string `json:"cluster"`
		} `json:"cluster_target"`
		Workloads []renderedWorkload `json:"workloads"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		return nil, nil
	}

	byApp := map[string]string{}
	seen := map[string]bool{}
	if contract.ClusterTarget != nil && contract.ClusterTarget.Cluster != "" {
		seen[contract.ClusterTarget.Cluster] = true
	}
	for _, w := range contract.Workloads {
		if w.Runtime.Type != "cluster" || w.Runtime.Cluster == "" {
			continue // host / compose / hosted / build-only: never lands in a namespace forge applies to
		}
		if w.Name != "" {
			byApp[w.Name] = w.Runtime.Cluster
		}
		seen[w.Runtime.Cluster] = true
	}

	clusters := make([]string, 0, len(seen))
	for c := range seen {
		clusters = append(clusters, c)
	}
	sort.Strings(clusters)
	if len(byApp) == 0 {
		byApp = nil
	}
	return byApp, clusters
}

// clustersOf answers which cluster(s) a rendered object actually lands on.
//
// It mirrors internal/cluster.ScopeManifestsToGroup, the code that performs
// the routing at deploy time, and the mirroring is the whole point: a check
// that models routing differently from the router reports on a deploy that
// never happens. The three arms are that function's rules, in its order:
//
//   - the first-class `forge.dev/cluster` label wins outright — the scoper
//     keeps such a doc iff the label equals the group's cluster and drops it
//     everywhere else, with no app-label indirection.
//   - otherwise an `app.kubernetes.io/name` label naming a workload with a
//     declared cluster routes to THAT workload's cluster. This is the
//     ownership rule: a manifest lands on the cluster of its owning service.
//   - otherwise (an unlabeled Namespace / ConfigMap / RuntimeClass, or an app
//     label owned by no declared workload) it lands on EVERY cluster the env
//     deploys to. The deploy layer never picks a cluster for an unattributed
//     doc; it replicates it to every group, so every one of them is a real
//     landing site and a real place to collide.
//
// Empty only when the env declares no cluster whatsoever.
func (r envRender) clustersOf(o k8sObject) []string {
	if c := o.Metadata.Labels[cluster.ClusterRoutingLabel]; c != "" {
		return []string{c}
	}
	if app := cluster.ManifestGroup(o.Metadata.Labels); app != "" {
		if c := r.clusterOfApp[app]; c != "" {
			return []string{c}
		}
	}
	return r.clusters
}

// envRender is one environment's render outcome.
type envRender struct {
	env string
	// hasManifestRoot records whether the render carried `output.manifests`
	// (or was itself a bare list / single object, the hand-written shapes
	// that need no selection).
	hasManifestRoot bool
	// strayRoots are top-level keys OTHER than `output` that carry
	// k8s-object-shaped data. They are a silent trap: no deploy reads them,
	// so those objects render, review clean, and never reach a cluster.
	strayRoots []string
	// invalid names entries of the manifest list that `kubectl apply`
	// would reject — a document with no apiVersion or no kind.
	invalid []string
	objects []k8sObject
	// clusterOfApp maps an `app.kubernetes.io/name` label value to the
	// kubectl context of the cluster that workload declares, and clusters is
	// every context the env deploys to (sorted). Together they turn a
	// rendered object into the cluster it lands on; clusters is empty when
	// the render declares no cluster at all, which is UNDETERMINED rather
	// than "no cluster".
	clusterOfApp map[string]string
	clusters     []string
	// hostServices are the env's HOST-deployed services, read from the
	// `output` JSON contract rather than from `manifests`.
	//
	// A host-mode environment runs its app as a process on the developer's
	// machine and gives the cluster only what must be in it. Such a service
	// has real env vars and a real command, but never becomes a container —
	// so a check that reads only manifests cannot see it at all, and would
	// report an env that migrates on every boot as having no way to migrate.
	hostServices []renderedService
	// frontends are the env's declared frontends, read from the `output`
	// JSON contract. A frontend never becomes a k8s object unless it
	// deploys `cluster`-mode, and a Firebase-hosted one never does at
	// all, so `manifests` cannot answer "which frontends does this env
	// declare" — see CheckFrontendCode.
	frontends []renderedFrontend
	err       error

	// destinations is the deploy path's classification (DeployShape
	// .Destinations); nil when the render was not classified.
	destinations []string
	// hosted is true when the deploy path ships this env THROUGH a control
	// plane (see DeployShape). Its `manifests` stream is empty by design,
	// and what runs is platformObjects.
	hosted bool
	// hostedWorkloads is how many tier workloads the control plane would
	// admit, and hostedRefusal the deploy path's refusal when it would admit
	// none of them.
	hostedWorkloads int
	hostedRefusal   error
	// platformObjects are the Kubernetes objects the control plane runs for
	// a hosted env's tiers. Deliberately NOT in objects: objects is what
	// forge applies to a cluster it addresses, and the checks that reason
	// about that (object collisions, orphans, cluster routing) must never
	// see a workload that lands in a namespace the platform allocates.
	platformObjects []k8sObject
}

// workloadObjects is every object that will actually RUN for this env —
// what forge applies plus what the platform runs for its hosted tiers. The
// content checks (probes, resources, secrets, ServiceAccounts, migrations)
// read this, so a hosted env is judged by its real workloads rather than by
// an empty stream that says nothing either way.
func (r envRender) workloadObjects() []k8sObject {
	if len(r.platformObjects) == 0 {
		return r.objects
	}
	return append(append([]k8sObject{}, r.objects...), r.platformObjects...)
}

// applyShape folds the deploy path's classification of this env into the
// render.
func (r *envRender) applyShape(s DeployShape) {
	r.destinations = s.Destinations
	r.hosted = s.Hosted
	r.hostedWorkloads = s.Workloads
	r.hostedRefusal = s.Refusal
	if len(s.PlatformObjects) == 0 {
		return
	}
	var objs []k8sObject
	if err := json.Unmarshal(s.PlatformObjects, &objs); err != nil {
		// The CLI marshalled these from pkg/deploy.Render; an undecodable
		// list is a forge defect, and the env is reported unreadable rather
		// than judged on half its workloads.
		r.err = fmt.Errorf("decode the platform objects for hosted env %q: %w", r.env, err)
		return
	}
	for i := range objs {
		objs[i].platform = true
	}
	r.platformObjects = objs
}

// subject names an object in evidence. A platform object is named for the
// hosted workload the author declared, since that declaration is the only
// thing they can change.
func (o k8sObject) subject(env string) string {
	if o.platform {
		return fmt.Sprintf("%s/%s %s (hosted, rendered as the control plane runs it)", env, o.Kind, o.Metadata.Name)
	}
	return fmt.Sprintf("%s/%s %s", env, o.Kind, o.Metadata.Name)
}

// renderedFrontend is the slice of the `output.frontends` contract the
// frontend-code check reasons about: the NAME, whether its runtime ships a
// build somewhere, and where its code lives. The rest of the
// contract (dev_runner, env vars, the typed config block) belongs to the
// build and dev-loop paths, and reading it here would be one more thing
// that can break.
type renderedFrontend struct {
	Name string `json:"name"`
	// Path and Source answer whether THIS repository is where the
	// frontend's code is supposed to be — a contained path with no
	// cross-repo pin. That is the only shape whose absence from disk
	// means forge has nothing to build. See CheckFrontendCode.
	Path   string          `json:"path"`
	Source *renderedSource `json:"source"`
	// Runtime is where the frontend runs (always present: the render
	// refuses a frontend with none).
	Runtime renderedFrontendRuntime `json:"runtime"`
}

// HasSource reports whether the declaration pins its code to another
// repository, in which case there is no directory in this tree to
// generate into and the pin is materialized at build time instead.
func (f renderedFrontend) HasSource() bool { return f.Source != nil && f.Source.Repo != "" }

// renderedSource carries only the field that distinguishes a real
// cross-repo pin from KCL's projection of an absent one.
type renderedSource struct {
	Repo string `json:"repo"`
}

// renderedFrontendRuntime carries only the discriminator. The dev server
// ("host") and a frontend built only for a sibling's bundle ("build-only")
// ship nothing; every other runtime — "hosted", "bucket", "firebase", and
// any added later — reads as "this claims to ship", which is the question
// being asked, so the check needs no update when a runtime is added.
type renderedFrontendRuntime struct {
	Type string `json:"type"`
}

// Ships reports whether the runtime publishes the frontend's build.
func (r renderedFrontendRuntime) Ships() bool {
	return r.Type != "" && r.Type != "host" && r.Type != "build-only"
}

// renderedService is a HOST workload as the checks below read it (built by
// parseHostServices from `output.workloads`). The Deploy block is kept so a
// reader written against the old shape reads the same fields; it carries only
// the type. These
// checks reason about. Deliberately minimal: the contract is large and
// evolving, and every field read here is one more thing that can break.
type renderedService struct {
	Name    string        `json:"name"`
	Command []string      `json:"command"`
	EnvVars []renderedEnv `json:"env_vars"`
	Deploy  struct {
		Type string `json:"type"`
		// A host service's environment is composed onto the DEPLOY block,
		// not the service's own env_vars — the host adapter owns it, the
		// way a cluster service's env lands on the container. Reading only
		// the outer list finds an empty slice on every host service.
		EnvVars []renderedEnv `json:"env_vars"`
	} `json:"deploy"`
}

type renderedEnv struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// deployRenders renders every declared environment once and memoises
// the result on the Environment, so the four deploy checks that run in
// doctor's parallel phase share a single KCL evaluation.
func deployRenders(env *Environment) []envRender {
	env.deployOnce.Do(func() {
		env.deployCache = renderDeployEnvs(env.ProjectDir, env.DeployShaper)
	})
	return env.deployCache
}

// renderDeployEnvs discovers deploy/kcl/<env>/main.k and renders each. A
// non-nil shaper classifies every successful render the way the deploy path
// does, so a hosted env is judged as one.
func renderDeployEnvs(projectDir string, shaper DeployShaper) []envRender {
	kclDir := filepath.Join(projectDir, "deploy", "kcl")
	entries, err := os.ReadDir(kclDir)
	if err != nil {
		return nil
	}

	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, statErr := os.Stat(filepath.Join(kclDir, e.Name(), "main.k")); statErr != nil {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	renders := make([]envRender, 0, len(names))
	for _, name := range names {
		// The main.k files resolve their imports (`..components`,
		// `..ingress`) and the kcl.mod vendor path relative to the project
		// root, so the project root is the work dir — same contract
		// `forge ci validate-kcl` uses.
		rel := filepath.Join("deploy", "kcl", name, "main.k")
		// `-D env=<name>` is not optional. It is what `option("env")`
		// reads, and env-conditionals key off it — including forge's own
		// kcl/schema.k, whose RenderedSecretKey check allows an inlined
		// literal secret only when option("env") is dev or e2e. Rendering
		// without it leaves the option Undefined, so that check fails for
		// EVERY environment including the two it permits, and the deploy
		// checks report a project non-applyable over a conditional that
		// would have been satisfied. internal/cli's renderKCLRaw passes the
		// same argument; the two renderers disagreeing is the bug.
		raw, rerr := kclrender.Run(projectDir, filepath.Join(projectDir, rel), []string{"env=" + name})
		if rerr != nil {
			renders = append(renders, envRender{env: name, err: rerr})
			continue
		}
		r := parseRender(raw)
		r.env = name
		if shaper != nil && r.err == nil {
			shape, serr := shaper(name, raw)
			if serr != nil {
				r = envRender{env: name, err: serr}
			} else {
				r.applyShape(shape)
			}
		}
		renders = append(renders, r)
	}
	return renders
}

// parseRender resolves a rendered KCL result into the objects a deploy
// would actually apply, plus everything about the render's SHAPE that
// decides whether the apply works at all.
//
// A render exports ONE top-level key, `output = forge.render(bundle)`, and
// the applyable stream is `output.manifests`. forge does not apply that list
// as-is: cluster.ExtractManifests expands every forge.dev Workload record in
// it through pkg/deploy.RenderWorkloads (and every ManagedDatabase through
// pkg/deploy.Render). The objects judged here are that EXPANDED stream,
// because the probes, resources and ServiceAccounts a check reads exist only
// after expansion. An expansion error is the render's error: the deploy
// would refuse it the same way.
//
// So the shape questions are:
//
//   - is there an `output` contract at all? Without it there is nothing a
//     deploy consumes.
//   - does every applied object carry apiVersion + kind? That is literally
//     what kubectl validates.
//   - does any OTHER top-level key carry k8s objects? Those are invisible:
//     no deploy reads them, so they render, review clean, and never land.
//     A top-level `manifests` is one of them.
//
// A bare list at the root, or a single object, is the hand-written shape
// that needs no selection at all — accepted as-is.
func parseRender(raw []byte) envRender {
	var root map[string]json.RawMessage
	if uerr := json.Unmarshal(raw, &root); uerr != nil {
		// Not an object: a bare list of manifests is applyable as-is.
		var list []k8sObject
		if lerr := json.Unmarshal(raw, &list); lerr != nil {
			return envRender{err: fmt.Errorf("render is neither an object nor a list: %w", uerr)}
		}
		return envRender{hasManifestRoot: true, objects: list, invalid: invalidObjects(manifestRootKey, list)}
	}

	// A single k8s object at the root is applyable as-is.
	if _, hasKind := root["kind"]; hasKind {
		var single k8sObject
		if serr := json.Unmarshal(raw, &single); serr != nil {
			return envRender{err: serr}
		}
		list := []k8sObject{single}
		return envRender{hasManifestRoot: true, objects: list, invalid: invalidObjects(manifestRootKey, list)}
	}

	keys := make([]string, 0, len(root))
	for k := range root {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := envRender{}
	out.hostServices = parseHostServices(root[outputRootKey])
	out.frontends = parseRenderedFrontends(root[outputRootKey])
	out.clusterOfApp, out.clusters = parseClusterAttribution(root[outputRootKey])
	for _, k := range keys {
		if k == outputRootKey {
			continue
		}
		var list []k8sObject
		if lerr := json.Unmarshal(root[k], &list); lerr != nil {
			continue
		}
		// A root outside `output` that carries k8s objects never gets applied.
		if len(list) > 0 && (list[0].Kind != "" || list[0].APIVersion != "") {
			out.strayRoots = append(out.strayRoots, k)
		}
	}
	if _, ok := root[outputRootKey]; !ok {
		return out
	}
	var contract struct {
		Manifests json.RawMessage `json:"manifests"`
	}
	if err := json.Unmarshal(root[outputRootKey], &contract); err != nil || len(contract.Manifests) == 0 {
		return out
	}
	out.hasManifestRoot = true
	expanded, err := cluster.ExtractManifests([]byte(`{"output":{"manifests":` + string(contract.Manifests) + `}}`))
	if err != nil {
		out.err = fmt.Errorf("expand %s: %w", manifestRootKey, err)
		return out
	}
	for _, doc := range cluster.SplitManifestDocs(expanded) {
		var o k8sObject
		if err := yaml.Unmarshal([]byte(doc), &o); err != nil {
			out.err = fmt.Errorf("read an expanded object: %w", err)
			return out
		}
		out.objects = append(out.objects, o)
	}
	out.clusters = withStampedClusters(out.clusters, out.objects)
	out.invalid = invalidObjects(manifestRootKey, out.objects)
	return out
}

// withStampedClusters adds every context an object is stamped onto
// (`forge.dev/cluster`) to the env's cluster set. The deploy gives each such
// context a group of its own even when no workload runs there (a
// forge.Manifests group on an otherwise empty cluster), so it is a cluster
// the env deploys to — and one an unattributed object is replicated onto.
func withStampedClusters(clusters []string, objects []k8sObject) []string {
	seen := map[string]bool{}
	for _, c := range clusters {
		seen[c] = true
	}
	added := false
	for _, o := range objects {
		if c := o.Metadata.Labels[cluster.ClusterRoutingLabel]; c != "" && !seen[c] {
			seen[c] = true
			clusters = append(clusters, c)
			added = true
		}
	}
	if added {
		sort.Strings(clusters)
	}
	return clusters
}

// invalidObjects names the entries kubectl would reject outright.
func invalidObjects(root string, list []k8sObject) []string {
	var bad []string
	for i, o := range list {
		var missing []string
		if o.APIVersion == "" {
			missing = append(missing, "apiVersion")
		}
		if o.Kind == "" {
			missing = append(missing, "kind")
		}
		if len(missing) > 0 {
			name := o.Metadata.Name
			if name == "" {
				name = "<unnamed>"
			}
			bad = append(bad, fmt.Sprintf("%s[%d] (%s): no %s", root, i, name, strings.Join(missing, ", ")))
		}
	}
	return bad
}

// podTemplateKinds are the workload kinds whose pod template sits
// directly at spec.template.
//
// Job belongs here with the long-running kinds even though it runs to
// completion: its pod carries a ServiceAccount and a full environment,
// which is what the SA-binding and secret checks read. Omitting it made
// a migrate Job that correctly bound its SA report as unbound, and hid a
// plaintext credential on the one workload most likely to carry a
// database URL.
var podTemplateKinds = map[string]bool{
	"Deployment":  true,
	"StatefulSet": true,
	"DaemonSet":   true,
	"ReplicaSet":  true,
	"Job":         true,
}

// containersOf returns (podSpec, containers) for a workload object, or
// (nil, nil) when the object carries no pod template.
func containersOf(o k8sObject) (map[string]any, []map[string]any) {
	podSpec, ok := podSpecOf(o)
	if !ok {
		return nil, nil
	}
	return podSpec, containerList(podSpec, "containers")
}

// podSpecOf locates a workload's pod spec, which sits one level deeper
// for a CronJob: it templates a Job, which templates the pod
// (spec.jobTemplate.spec.template.spec).
func podSpecOf(o k8sObject) (map[string]any, bool) {
	spec := o.Spec
	if o.Kind == "CronJob" {
		jobTmpl, ok := mapAt(spec, "jobTemplate")
		if !ok {
			return nil, false
		}
		if spec, ok = mapAt(jobTmpl, "spec"); !ok {
			return nil, false
		}
	} else if !podTemplateKinds[o.Kind] {
		return nil, false
	}
	tmpl, _ := mapAt(spec, "template")
	return mapAt(tmpl, "spec")
}

// initContainersOf returns a workload's initContainers. Separate from
// [containersOf] on purpose: an init container runs to completion, so
// the probe and (pod-effective) resource checks must NOT descend into
// it — but the secret check must, because a credential leaks the same
// way whichever container carries it.
func initContainersOf(o k8sObject) []map[string]any {
	podSpec, _ := containersOf(o)
	return containerList(podSpec, "initContainers")
}

// containerList extracts a named container list off a pod spec.
func containerList(podSpec map[string]any, key string) []map[string]any {
	if podSpec == nil {
		return nil
	}
	raw, _ := podSpec[key].([]any)
	containers := make([]map[string]any, 0, len(raw))
	for _, c := range raw {
		if m, isMap := c.(map[string]any); isMap {
			containers = append(containers, m)
		}
	}
	return containers
}

func mapAt(m map[string]any, key string) (map[string]any, bool) {
	if m == nil {
		return nil, false
	}
	v, ok := m[key].(map[string]any)
	return v, ok
}

// ── the render-scope contract ───────────────────────────────────────
//
// Every deploy check answers a question ABOUT THE DECLARED
// ENVIRONMENTS, and there are two kinds of question. Six of them ask
// about the CONTENT of what an environment renders — probes, resource
// limits, secret handling, ServiceAccount binding, migrations, frontend
// drift. One, CheckDeployManifests, asks about the RENDER ITSELF. That
// difference is exactly what an unrenderable environment means:
//
//   - to a content check it is a HOLE in the evidence. An environment
//     that did not evaluate contributed no containers, so "every
//     container sets limits" becomes a claim about a subset. Reporting
//     that as Pass is the report lying by omission; the honest answer is
//     StatusUnknown — see doctor.go, where UNDETERMINED is defined as
//     "could not obtain the facts" and explicitly not a pass.
//   - to CheckDeployManifests it IS the finding. "This environment does
//     not render" is precisely what that check exists to say, so it
//     consumes the failures instead of being degraded by them.
//
// The helper this replaced (renderPreamble, now deleted) expressed just
// the two extremes — zero environments → Skip, EVERY environment broken
// → Fail — and handed anything in between straight through with the
// failures still mixed into the list, for each caller to remember to
// filter. Six of seven callers never acted on envRender.err at all, so
// four of five environments could fail to render and the check still
// said Pass: a confident green over facts it never read, which is worse
// than no answer. CheckDeployServiceAccount could even reach StatusSkip
// ("no ServiceAccount rendered") because the only environment declaring
// one was the environment that failed — asserting "not applicable" from
// missing facts.
//
// It is deleted rather than deprecated deliberately. A helper that can
// still be called is still a way to write the bug, and the one that
// reads as the shorter path gets copied into the next check.
//
// The fix is therefore not one more field to remember to consult.
// examineRendered owns BOTH ENDS of the check: it hands the body only
// the environments that actually rendered, and it folds the unread ones
// into whatever the body answers on the way back out. A check cannot
// reach the renders without going through it, and cannot return a
// verdict that bypasses the fold, because the folded result IS the
// return value. There is no carry-forward left to drop.

// unreadEnv is one environment doctor could not read, and why.
type unreadEnv struct {
	env string
	err error
}

// renderScope is the evidence base a deploy check is working from: the
// environments it was able to examine, and the ones it was not.
type renderScope struct {
	// what names the subject in the check's own words ("probes",
	// "resource limits"). Only surfaces when nothing was readable at all.
	what string
	// all is every declared environment in render order, failures
	// included — reserved for the one check whose subject IS the render.
	all []envRender
	// usable is every environment that rendered. A content-check body
	// sees ONLY these, which is why `if r.err != nil { continue }` is not
	// something such a check can forget: it is something it cannot say.
	usable []envRender
	// unread is every environment that did not, with its render error.
	unread []unreadEnv
}

// total is how many environments the project declares.
func (s renderScope) total() int { return len(s.all) }

// unreadNames lists the environments missing from the answer, in render
// order, so a message can name them rather than only count them.
func (s renderScope) unreadNames() []string {
	names := make([]string, 0, len(s.unread))
	for _, u := range s.unread {
		names = append(names, u.env)
	}
	return names
}

// unreadEvidence spells out each failure, so `-v` says WHY an
// environment is missing from the answer and not merely that it is.
func (s renderScope) unreadEvidence() string {
	lines := make([]string, 0, len(s.unread))
	for _, u := range s.unread {
		lines = append(lines, fmt.Sprintf("%s: %v", u.env, u.err))
	}
	return strings.Join(lines, "\n")
}

// scopePrefix states what the answer actually covers. It leads the
// message rather than trailing it: the scope of a claim has to arrive
// before the claim, or the reader has already believed it.
func (s renderScope) scopePrefix() string {
	return fmt.Sprintf("%d of %d env(s) read (%s did not render) — ",
		len(s.usable), s.total(), strings.Join(s.unreadNames(), ", "))
}

// fold applies the degradation contract to a verdict the check reached
// from s.usable alone.
//
// A COMPLETE scope returns the verdict untouched, byte for byte: a
// project whose environments all render sees exactly the report it saw
// before this contract existed.
//
// An INCOMPLETE one rewrites two things:
//
//   - PASS and SKIP become UNKNOWN. Both are assertions the missing
//     environments could have contradicted — "every container has a
//     limit" and "no ServiceAccount is rendered" are equally unsafe to
//     say about an environment that was never read. Skip is the worse of
//     the two, since it claims the question does not apply on the
//     strength of facts that are merely absent.
//   - FAIL and WARN keep their status. A plaintext credential in prod is
//     a plaintext credential whether or not staging rendered, and
//     downgrading it to "undetermined" would bury a finding that IS
//     determined. They still take the prefix, because "2 of 12
//     containers" has to say which containers were counted.
func (s renderScope) fold(r CheckResult) CheckResult {
	if len(s.unread) == 0 {
		return r
	}
	if r.Status == StatusPass || r.Status == StatusSkip {
		r.Status = StatusUnknown
	}
	r.Message = s.scopePrefix() + r.Message
	r.Evidence = joinEvidence(r.Evidence, fmt.Sprintf(
		"could not render %d of %d environment(s); this result covers only the other %d:\n%s",
		len(s.unread), s.total(), len(s.usable), s.unreadEvidence()))
	return r
}

// nothingReadable is the answer when NOT ONE environment rendered. The
// status is the caller's, because that is the one place the two kinds of
// check genuinely disagree: unrenderable IS CheckDeployManifests' defect
// (Fail), and is merely the absence of evidence for everyone else
// (Unknown).
func (s renderScope) nothingReadable(status Status) CheckResult {
	return CheckResult{
		Status:   status,
		Message:  fmt.Sprintf("could not render any of %d environment(s) to check %s", s.total(), s.what),
		Evidence: s.unreadEvidence(),
	}
}

// joinEvidence concatenates the non-empty evidence blocks.
func joinEvidence(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, "\n")
}

// renderScopeOf partitions the memoised renders and handles the two
// cases no check body can say anything useful about: a project that
// declares no environments, and one where not a single environment
// rendered. A non-nil result means the caller must return it as-is.
func renderScopeOf(env *Environment, what string, unreadableStatus Status) (renderScope, *CheckResult) {
	renders := deployRenders(env)
	if len(renders) == 0 {
		r := CheckResult{Status: StatusSkip, Message: "no deploy/kcl/<env>/main.k — nothing to render"}
		return renderScope{}, &r
	}
	s := renderScope{what: what, all: renders}
	for _, r := range renders {
		if r.err != nil {
			s.unread = append(s.unread, unreadEnv{env: r.env, err: r.err})
			continue
		}
		s.usable = append(s.usable, r)
	}
	if len(s.usable) == 0 {
		r := s.nothingReadable(unreadableStatus)
		return s, &r
	}
	return s, nil
}

// examineRendered is how a CONTENT check reaches the rendered
// environments — the only way, deliberately.
//
// `examine` is handed the environments that rendered and nothing else,
// so it cannot accidentally reason over a failed one; and its verdict
// goes through [renderScope.fold] before it reaches doctor, so it cannot
// accidentally report Pass about an environment nobody read. Both
// halves of the old bug are unrepresentable rather than merely fixed.
func examineRendered(env *Environment, what string, examine func(usable []envRender) CheckResult) CheckResult {
	s, early := renderScopeOf(env, what, StatusUnknown)
	if early != nil {
		return *early
	}
	return s.fold(examine(s.usable))
}

// examineRenderability is CheckDeployManifests' door: the render is its
// subject, so it gets EVERY environment including the broken ones, and
// its answer is not folded — a failed render is the finding it reports
// in its own words, not a hole in someone else's evidence.
func examineRenderability(env *Environment, what string, examine func(all []envRender) CheckResult) CheckResult {
	s, early := renderScopeOf(env, what, StatusFail)
	if early != nil {
		return *early
	}
	return examine(s.all)
}

// CheckDeployManifests verifies that each environment renders to
// something `kubectl apply -f -` will accept.
//
// This is the gate `forge ci validate-kcl` used to skip: that command
// only asserted the KCL EVALUATES, so it printed "✅ All KCL manifests
// valid" over a render kubectl would reject — a green CI and a dead CD
// path, the worst possible combination. `validate-kcl` now calls this
// check, so the two can no longer disagree.
//
// What "applyable" means concretely: forge applies `output.manifests`,
// EXPANDED (cluster.ExtractManifests). So the render must carry it and
// expand without error, every expanded object must carry apiVersion +
// kind, and no top-level key but `output` may carry k8s objects — nothing
// reads them, so they silently never deploy.
//
// This is the ONE deploy check that takes examineRenderability rather
// than examineRendered: an environment that does not render is its
// finding, not a hole in its evidence, so it receives the failures and
// reports them by name instead of being degraded to UNDETERMINED.
//
// Hosting is PER WORKLOAD. An env's hosted part (see DeployShape) is judged
// by its own deploy path: `forge env deploy` publishes those workloads to a
// control plane, and "applyable" means the control plane would admit every
// one of them. The deploy path's refusal is the finding, verbatim. The env's
// cluster part, if it has one, is judged from the expanded stream like any
// other env's. An env with NO cluster part applies nothing, so any object it
// does render is a defect of its own.
//
// "Produced no k8s objects" is a finding only for an env that deploys to a
// cluster. An env the deploy path places entirely on this machine, on
// compose or behind a static host applies nothing to Kubernetes, and
// failing it for that failed the correct render. An env whose shape was
// never classified (no contract in the render, or no shaper wired) keeps
// the strict reading, since nothing says it is exempt.
func CheckDeployManifests(_ context.Context, env *Environment) CheckResult {
	return examineRenderability(env, "manifest shape", func(renders []envRender) CheckResult {
		var problems []string
		total, hosted, tiers := 0, 0, 0
		for _, r := range renders {
			if r.err != nil {
				problems = append(problems, fmt.Sprintf("%s: render failed: %v", r.env, r.err))
				continue
			}
			if r.hosted {
				// The env's HOSTED part: judged by the deploy path's own
				// admission (Workload.Validate(ProfileRestricted) per
				// workload, and the set rendered as the platform renders it).
				hosted++
				tiers += r.hostedWorkloads
				if r.hostedRefusal != nil {
					problems = append(problems, fmt.Sprintf(
						"%s: `forge env deploy %s` would refuse its hosted workloads before publishing anything:\n    %s",
						r.env, r.env, strings.ReplaceAll(r.hostedRefusal.Error(), "\n", "\n    ")))
				}
			}
			if len(r.strayRoots) > 0 {
				problems = append(problems, fmt.Sprintf(
					"%s: top-level key(s) %s carry k8s objects that no deploy applies — "+
						"a render exports only `output = forge.render(bundle)`; put them in the Bundle (additional manifests or infra)",
					r.env, strings.Join(r.strayRoots, ", ")))
			}
			if r.hosted && !r.deploysToCluster() {
				// Nothing in this env is applied to a cluster, so any object
				// in its stream is one no deploy will ever apply.
				if len(r.objects) > 0 {
					problems = append(problems, fmt.Sprintf(
						"%s: renders %d k8s object(s) into `%s`, and this env applies nothing to a cluster — "+
							"its workloads are hosted; bind the owner to forge.OnCluster or drop the object",
						r.env, len(r.objects), manifestRootKey))
				}
				continue
			}
			total += len(r.objects)
			if !r.hasManifestRoot && r.deploysToCluster() {
				problems = append(problems, fmt.Sprintf(
					"%s: render carries no `%s` — main.k must end with `output = forge.render(bundle)`, "+
						"so there is nothing for `forge env deploy %s` to apply",
					r.env, manifestRootKey, r.env))
			}
			if len(r.invalid) > 0 {
				problems = append(problems, fmt.Sprintf(
					"%s: %d manifest(s) `kubectl apply` would reject:\n    %s",
					r.env, len(r.invalid), strings.Join(r.invalid, "\n    ")))
			}
			if len(r.objects) == 0 && r.hasManifestRoot && r.deploysToCluster() {
				problems = append(problems, fmt.Sprintf("%s: render produced no k8s objects", r.env))
			}
		}

		if len(problems) > 0 {
			return CheckResult{
				Status:   StatusFail,
				Message:  fmt.Sprintf("%d environment(s) render to non-applyable output", len(problems)),
				Evidence: strings.Join(problems, "\n"),
			}
		}
		msg := fmt.Sprintf("%d env(s), %d manifest(s) — all applyable", len(renders), total)
		if hosted > 0 {
			// Named separately, and never folded into the manifest count:
			// hosted workloads are not manifests anything applies.
			msg += fmt.Sprintf("; %d env(s) with hosted workloads, %d hosted item(s) the control plane would admit", hosted, tiers)
		}
		return CheckResult{Status: StatusPass, Message: msg}
	})
}

// deploysToCluster reports whether an env's `manifests` stream is expected to
// carry anything. True when the deploy path classified it as deploying to a
// cluster — and when it was not classified at all, because an unclassified
// env has not been shown to be exempt from the strict reading.
func (r envRender) deploysToCluster() bool {
	return r.destinations == nil || slices.Contains(r.destinations, "cluster")
}

// CheckDeployProbes verifies every rendered container that serves a
// port declares both a readinessProbe and a livenessProbe.
//
// Without a readinessProbe a rolling update has no gate: a pod that
// binds its port and then wedges is added to the Service endpoints
// immediately, and a bad image reaches every replica at line speed.
// Without a livenessProbe nothing ever restarts a deadlocked process.
//
// The port is the discriminator, not the workload kind. A container
// that declares a containerPort is serving something and can be probed;
// one that declares none (a job that runs to completion, a custom
// binary entrypoint) has no endpoint to probe, and an HTTP probe
// against it would not be a safety net but a guaranteed
// CrashLoopBackOff. Declaring exactly ONE of the two probes fails
// regardless of ports — it is always a half-finished thought.
func CheckDeployProbes(_ context.Context, env *Environment) CheckResult {
	return examineRendered(env, "probes", func(renders []envRender) CheckResult {
		var missing []string
		checked, exempt := 0, 0
		for _, r := range renders {
			for _, o := range r.workloadObjects() {
				_, containers := containersOf(o)
				for _, c := range containers {
					name, _ := c["name"].(string)
					_, hasReady := c["readinessProbe"]
					_, hasLive := c["livenessProbe"]
					ports, _ := c["ports"].([]any)

					if len(ports) == 0 && !hasReady && !hasLive {
						// Nothing to probe and nothing claimed — not a defect.
						exempt++
						continue
					}
					checked++
					var absent []string
					if !hasReady {
						absent = append(absent, "readinessProbe")
					}
					if !hasLive {
						absent = append(absent, "livenessProbe")
					}
					if len(absent) > 0 {
						msg := fmt.Sprintf("%s container %q: serves %d port(s) but declares no %s",
							o.subject(r.env), name, len(ports), strings.Join(absent, " or "))
						if o.platform {
							// One Probes declaration drives both probes
							// (readiness + derived liveness), so the fix is one
							// field, not two.
							msg += " — set `probes = tiers.Probes {...}` on the hosted workload"
						}
						missing = append(missing, msg)
					}
				}
			}
		}

		if checked == 0 && exempt == 0 {
			return CheckResult{Status: StatusSkip, Message: "no workload containers rendered"}
		}
		if len(missing) > 0 {
			return CheckResult{
				Status: StatusFail,
				Message: fmt.Sprintf("%d of %d serving container(s) ship no readiness/liveness probe "+
					"— rollouts have no gate and a wedged pod is never restarted", len(missing), checked),
				Evidence: strings.Join(missing, "\n"),
			}
		}
		msg := fmt.Sprintf("%d container(s) probe readiness + liveness", checked)
		if exempt > 0 {
			msg += fmt.Sprintf(" (%d serve no port — nothing to probe)", exempt)
		}
		return CheckResult{Status: StatusPass, Message: msg}
	})
}

// CheckDeployResources verifies every rendered container sets CPU and
// memory requests and limits. Without requests the scheduler cannot
// place the pod correctly and it lands in the BestEffort QoS class,
// first to be evicted under node pressure.
func CheckDeployResources(_ context.Context, env *Environment) CheckResult {
	return examineRendered(env, "resource limits", func(renders []envRender) CheckResult {
		var missing []string
		checked := 0
		for _, r := range renders {
			for _, o := range r.workloadObjects() {
				_, containers := containersOf(o)
				for _, c := range containers {
					checked++
					name, _ := c["name"].(string)
					res, _ := c["resources"].(map[string]any)
					var absent []string
					for _, section := range []string{"requests", "limits"} {
						sec, ok := mapAt(res, section)
						if !ok || len(sec) == 0 {
							absent = append(absent, section)
							continue
						}
						for _, dim := range []string{"cpu", "memory"} {
							if _, has := sec[dim]; !has {
								absent = append(absent, section+"."+dim)
							}
						}
					}
					if len(absent) > 0 {
						missing = append(missing, fmt.Sprintf("%s container %q: missing %s",
							o.subject(r.env), name, strings.Join(absent, ", ")))
					}
				}
			}
		}

		if checked == 0 {
			return CheckResult{Status: StatusSkip, Message: "no workload containers rendered"}
		}
		if len(missing) > 0 {
			return CheckResult{
				Status:   StatusFail,
				Message:  fmt.Sprintf("%d of %d rendered container(s) missing cpu/memory requests or limits", len(missing), checked),
				Evidence: strings.Join(missing, "\n"),
			}
		}
		return CheckResult{Status: StatusPass, Message: fmt.Sprintf("%d container(s) set cpu+memory requests and limits", checked)}
	})
}

// sensitiveEnvNames are env-var name fragments whose value must never
// be a literal in a manifest. Matching is on the rendered NAME, so it
// survives any renaming of the config field that produced it.
var sensitiveEnvNames = []string{
	"PASSWORD", "PASSWD", "SECRET", "TOKEN", "PRIVATE_KEY", "API_KEY",
	"ACCESS_KEY", "CREDENTIAL", "DATABASE_URL", "DB_URL", "DSN", "CONNECTION_STRING",
}

// secretReferenceSuffixes name a variable that POINTS AT a credential
// rather than carrying one. The name of a Secret is not secret — a
// manifest has to carry it in the clear for a pod to reference it at all
// — and neither is the path a credential is mounted at.
//
// This is a narrow exemption on the SUFFIX, not a substring: it exempts
// IMAGE_PULL_SECRET_NAME and TOKEN_PATH, and does not exempt
// SECRET_NAME_ENCRYPTION_KEY, whose name merely mentions a name. Without
// it these were 68 of 89 findings on a real project — enough noise to
// bury the genuine plaintext credentials in the same report, and a check
// whose output is mostly false is one people learn to skip.
//
// _NAMESPACE belongs here for the same reason as _NAME: the namespace a Secret
// lives in (ALERTING_SECRET_NAMESPACE) is half of its address.
var secretReferenceSuffixes = []string{"_NAME", "_NAMESPACE", "_PATH", "_FILE", "_KEY_REF", "_SECRET_REF"}

// credentialMetadataSuffixes name a fact ABOUT a credential that is not the
// credential: a token's issuer and audience are claims every token carries
// in the clear (REGISTRY_TOKEN_ISSUER), and which provider a credential comes
// from is a choice, not a key (DEPLOY_PIPELINE_CREDENTIAL_SOURCE=google). The
// same narrow, suffix-only exemption as secretReferenceSuffixes.
var credentialMetadataSuffixes = []string{"_ISSUER", "_AUDIENCE", "_SOURCE"}

// literalSecretEnvs are the environments allowed to carry a credential as
// a literal. It MIRRORS the allowance in forge's kcl/schema.k
// (RenderedSecretKey: `option("env") in ["dev", "e2e"]`) — the two must
// agree, or a project is failed here for what the schema permits.
//
// Matched on the exact environment name, like the schema's own check: a
// near-miss such as "dev-k8s" deploys to a real cluster and is NOT
// exempt.
var literalSecretEnvs = map[string]bool{"dev": true, "e2e": true}

func isSensitiveEnvName(name string) bool {
	upper := strings.ToUpper(name)
	matched := false
	for _, frag := range sensitiveEnvNames {
		if strings.Contains(upper, frag) {
			matched = true
			break
		}
	}
	if !matched {
		return false
	}
	for _, suffixes := range [][]string{secretReferenceSuffixes, credentialMetadataSuffixes} {
		for _, suffix := range suffixes {
			if strings.HasSuffix(upper, suffix) {
				return false
			}
		}
	}
	return true
}

// isNonSecretLiteral rules out the values a credential can never take.
// It exists so a name-fragment match on a policy toggle
// (CORS_ALLOW_CREDENTIALS=false) does not read as a leaked secret: a
// boolean or a bare number is a switch, not a credential, a duration
// (REGISTRY_TOKEN_TTL=15m) is a lifetime, and a URL that carries no
// credential (credentialFreeURL) is an address. An EMPTY value is
// deliberately NOT excluded — an empty literal is the same plaintext wiring,
// and it is precisely the slot an operator fills in with a real password.
func isNonSecretLiteral(v any) bool {
	s, ok := v.(string)
	if !ok {
		// A rendered non-string (bool, number) is a toggle by construction.
		return v != nil
	}
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "false", "yes", "no", "on", "off":
		return true
	}
	if s == "" {
		return false
	}
	if _, err := time.ParseDuration(s); err == nil {
		return true
	}
	if credentialFreeURL(s) {
		return true
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// databaseURLSchemes are the schemes whose userinfo USERNAME is a database
// role, not a credential. Anywhere else a bare username can be the token
// itself (https://<token>@github.com/org/repo.git), so it is not exempt.
var databaseURLSchemes = map[string]bool{
	"postgres": true, "postgresql": true, "mysql": true, "mariadb": true,
	"mongodb": true, "mongodb+srv": true, "redis": true, "rediss": true,
	"amqp": true, "amqps": true, "nats": true, "clickhouse": true,
	"sqlserver": true, "cockroachdb": true,
}

// credentialQueryParam matches a query parameter name that carries a
// credential: ?password=, ?sslpassword=, ?token=, ?access_token=, ?key= (an
// API key), ?sig= / X-Amz-Signature (a signed URL), ?auth=. Deliberately
// broad: a parameter it wrongly matches (?sslkey=, a path) costs a finding,
// one it missed would hide a leak.
func credentialQueryParam(name string) bool {
	upper := strings.ToUpper(name)
	for _, frag := range []string{"PASS", "TOKEN", "SECRET", "KEY", "SIG", "AUTH", "CREDENTIAL"} {
		if strings.Contains(upper, frag) {
			return true
		}
	}
	return false
}

// credentialFreeURL reports whether s is an absolute URL that carries no
// credential: no password in its userinfo, no username except a database
// role, and no credential-named query parameter. That is the shape of a DSN
// whose password comes from elsewhere (a Secret, IAM) and of a token issuer
// (https://admin.example.com/telemetry): the address of a service, not a key
// to it.
func credentialFreeURL(s string) bool {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	if u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword {
			return false
		}
		if !databaseURLSchemes[strings.ToLower(u.Scheme)] {
			return false
		}
	}
	for name := range u.Query() {
		if credentialQueryParam(name) {
			return false
		}
	}
	return true
}

// CheckDeploySecrets verifies no rendered container carries a
// credential-shaped env var as a literal `value:`.
//
// A literal means the credential lives in a git-tracked .k file and in
// `kubectl get deployment -o yaml` for anyone with read access to the
// namespace. The correct shape — valueFrom.secretKeyRef — is what a
// config field marked `sensitive: true` in proto/<project>/config/v1/config.proto
// projects to; a credential-bearing field left unmarked is what puts a
// password back in the manifest.
//
// initContainers are scanned too: the migration step runs with the same
// env as the app, so a leak there is the same leak.
//
// An empty literal still fails: it is the same wiring, and it is what
// an operator will fill in with a real password.
func CheckDeploySecrets(_ context.Context, env *Environment) CheckResult {
	return examineRendered(env, "secret handling", func(renders []envRender) CheckResult {
		var leaks []string
		checked := 0
		for _, r := range renders {
			// dev and e2e may inline a credential, because forge's own KCL
			// schema says so: RenderedSecretKey permits `from = "literal"`
			// exactly when option("env") is dev or e2e, and refuses it in every
			// other environment. Failing a project here for the thing the schema
			// allows put the two halves of forge in disagreement, and left a
			// project with permanently-red findings it could not fix — which is
			// how a check stops being read, taking the prod findings in the same
			// list with it.
			//
			// A dev credential is `postgres:postgres` against a database bound to
			// the developer's own machine; an e2e credential is a throwaway whose
			// other half lives in the test's assertions. Neither is a secret in
			// any sense a Secret would protect.
			if literalSecretEnvs[r.env] {
				continue
			}
			for _, o := range r.workloadObjects() {
				_, containers := containersOf(o)
				containers = append(containers, initContainersOf(o)...)
				for _, c := range containers {
					cname, _ := c["name"].(string)
					rawEnv, _ := c["env"].([]any)
					for _, e := range rawEnv {
						entry, ok := e.(map[string]any)
						if !ok {
							continue
						}
						name, _ := entry["name"].(string)
						if !isSensitiveEnvName(name) {
							continue
						}
						literalValue, literal := entry["value"]
						if literal && isNonSecretLiteral(literalValue) {
							continue
						}
						checked++
						if _, fromRef := entry["valueFrom"]; fromRef {
							continue
						}
						if !literal {
							continue
						}
						fix := "use valueFrom.secretKeyRef"
						if o.platform {
							fix = "declare it `managedSecret = \"" + name + "\"` (write-only via `forge secret set --env " +
								r.env + "`) or `databaseRef`, never `value`"
						}
						leaks = append(leaks, fmt.Sprintf("%s container %q: env %s is a literal value — %s",
							o.subject(r.env), cname, name, fix))
					}
				}
			}
		}

		if checked == 0 {
			return CheckResult{Status: StatusPass, Message: "no credential-shaped env vars in the rendered manifests"}
		}
		if len(leaks) > 0 {
			return CheckResult{
				Status: StatusFail,
				Message: fmt.Sprintf("%d of %d credential-shaped env var(s) are plaintext in the manifest, not a Secret ref",
					len(leaks), checked),
				Evidence: strings.Join(leaks, "\n"),
			}
		}
		return CheckResult{Status: StatusPass, Message: fmt.Sprintf("%d credential env var(s), all sourced from a Secret", checked)}
	})
}

// migrationJobHint matches the name of an object, or the command of a
// container, that plausibly applies schema migrations. Deliberately
// loose: this check asks "is there ANY migration step", so a false
// negative (failing a project that does migrate, by some name this
// misses) is the only expensive outcome.
func looksLikeMigration(s string) bool {
	return strings.Contains(strings.ToLower(s), "migrat")
}

// containerLooksLikeMigration reports whether a container is a
// migration step — by NAME or by any word of its command.
//
// Name OR command, deliberately: forge's own render names the init
// container "migrate" AND runs `<binary> db migrate up`, but a project
// that hand-writes the step may only match one.
func containerLooksLikeMigration(c map[string]any) bool {
	if name, _ := c["name"].(string); looksLikeMigration(name) {
		return true
	}
	cmd, _ := c["command"].([]any)
	for _, part := range cmd {
		if s, ok := part.(string); ok && looksLikeMigration(s) {
			return true
		}
	}
	return false
}

// hasMigrationStep reports whether a render carries anything that
// applies schema migrations before or alongside the rollout: a
// migration Job, a migration initContainer, or a container whose
// command says so.
//
// An initContainer only counts when it LOOKS like a migration. Any
// init container used to satisfy this — a wait-for-dependency sleep, a
// config-templating step — which made the check pass for projects with
// no migration path at all.
func hasMigrationStep(objects []k8sObject) bool {
	for _, o := range objects {
		if (o.Kind == "Job" || o.Kind == "CronJob") && looksLikeMigration(o.Metadata.Name) {
			return true
		}
		_, containers := containersOf(o)
		for _, c := range append(containers, initContainersOf(o)...) {
			if containerLooksLikeMigration(c) {
				return true
			}
		}
	}
	return false
}

// hostMigrationStep reports whether any HOST-deployed service applies
// migrations — either by running a migrate command, or by carrying
// AUTO_MIGRATE=true.
//
// Same two mechanisms the manifest path accepts, read off the `output`
// contract instead of a pod spec, so a host env and a cluster env are
// judged by the same standard.
func hostMigrationStep(services []renderedService) bool {
	for _, s := range services {
		if looksLikeMigration(s.Name) {
			return true
		}
		for _, word := range s.Command {
			if looksLikeMigration(word) {
				return true
			}
		}
		// Both lists: a host service's env is composed onto the deploy
		// block, while the service's own env_vars carry anything declared
		// directly on it.
		for _, e := range append(append([]renderedEnv{}, s.EnvVars...), s.Deploy.EnvVars...) {
			if e.Name == "AUTO_MIGRATE" && strings.EqualFold(strings.TrimSpace(e.Value), "true") {
				return true
			}
		}
	}
	return false
}

// autoMigrateEnabled reports whether every workload in the render runs
// its migrations at startup (AUTO_MIGRATE=true).
func autoMigrateEnabled(objects []k8sObject) bool {
	seen := false
	for _, o := range objects {
		_, containers := containersOf(o)
		for _, c := range containers {
			rawEnv, _ := c["env"].([]any)
			for _, e := range rawEnv {
				entry, ok := e.(map[string]any)
				if !ok {
					continue
				}
				if name, _ := entry["name"].(string); name != "AUTO_MIGRATE" {
					continue
				}
				value, _ := entry["value"].(string)
				if !strings.EqualFold(strings.TrimSpace(value), "true") {
					return false
				}
				seen = true
			}
		}
	}
	return seen
}

// CheckDeployMigrations verifies that an environment which ships SQL
// migrations has SOME way of applying them.
//
// The generated cloud envs set AUTO_MIGRATE=false — correct, because
// three replicas racing to migrate on startup is not a migration
// strategy. What replaces it is the scaffolded `migrate` workload: a
// one-shot job (`kind = "job"`, `before = [fw.BEFORE_ALL]`) running
// `<binary> db migrate up` over the migrations EMBEDDED in the image,
// serialized across replicas by a postgres advisory lock, and lowered to
// an initContainer so it is ordered before the app container by
// Kubernetes itself.
//
// The check does not care WHICH mechanism an env uses — a migration
// Job, an initContainer, a migrate command, or AUTO_MIGRATE=true all
// satisfy it. It cares that a project shipping .sql has SOMETHING, so a
// schema-changing release cannot deploy new code against the old schema
// and let the first request that touches a new column be the discovery
// mechanism.
func CheckDeployMigrations(_ context.Context, env *Environment) CheckResult {
	migrationsDir := filepath.Join(env.ProjectDir, "db", "migrations")
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		return CheckResult{Status: StatusSkip, Message: "no db/migrations — nothing to apply"}
	}
	sqlCount := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			sqlCount++
		}
	}
	if sqlCount == 0 {
		return CheckResult{Status: StatusSkip, Message: "db/migrations is empty — nothing to apply"}
	}

	return examineRendered(env, "migrations", func(renders []envRender) CheckResult {
		var unmigrated []string
		for _, r := range renders {
			// No err arm: examineRendered hands this body only the
			// environments that rendered, so there is no failed render left
			// to skip past — and no way to skip one silently. An env that
			// rendered ZERO objects is a different matter and stays exempt
			// here: "this env produced no manifests" is CheckDeployManifests'
			// finding, and repeating it as a migration failure would report
			// one defect twice under two names.
			//
			// A hosted env is read through its platform objects: the
			// backend the control plane runs carries the same env and
			// command a cluster Deployment would, so the same two
			// mechanisms answer for it.
			objects := r.workloadObjects()
			if len(objects) == 0 {
				continue
			}
			if hasMigrationStep(objects) || autoMigrateEnabled(objects) {
				continue
			}
			// A HOST-deployed service migrates just as effectively as a
			// container does, and it is the normal shape for a dev env: the app
			// runs on the developer's machine with AUTO_MIGRATE=true while the
			// cluster holds only the pieces that must be in it. That service
			// never becomes a container, so reading manifests alone reports an
			// env that migrates on every boot as one with no way to migrate.
			if hostMigrationStep(r.hostServices) {
				continue
			}
			unmigrated = append(unmigrated, fmt.Sprintf(
				"%s: AUTO_MIGRATE is off and the render carries no migration Job, initContainer or migrate command "+
					"— a schema-changing release deploys new code against the old schema", r.env))
		}

		if len(unmigrated) > 0 {
			return CheckResult{
				Status: StatusFail,
				Message: fmt.Sprintf("%d of %d environment(s) ship %d SQL migration(s) with no way to apply them",
					len(unmigrated), len(renders), sqlCount),
				Evidence: strings.Join(unmigrated, "\n") +
					"\nfix: declare the migration as a one-shot workload in deploy/kcl/workloads.k —\n" +
					"      migrate = fw.Workload {\n" +
					"          name = \"migrate\"\n" +
					"          kind = \"job\"\n" +
					"          command = [\"/app/<project>\", \"db\", \"migrate\", \"up\"]\n" +
					"          before = [fw.BEFORE_ALL]\n" +
					"      }\n" +
					"    and add it to this environment's workload list. `before = [fw.BEFORE_ALL]` gates " +
					"EVERY workload without naming any, so it keeps holding when a workload is added later; " +
					"it renders an initContainer running the image's `db migrate up` (embedded migrations, " +
					"advisory-locked) before the app container starts. If this environment applies migrations " +
					"out of band instead, that is a legitimate answer — this check reads the render, so wire " +
					"the step you actually use.",
			}
		}
		return CheckResult{
			Status:  StatusPass,
			Message: fmt.Sprintf("%d SQL migration(s), applied by all %d environment(s)", sqlCount, len(renders)),
		}
	})
}

// unboundReason explains why the rendered ServiceAccount `name` has no
// pods, distinguishing the two states this check used to conflate.
//
// It tested whether any pod named THAT SA, and then attributed every miss
// to "no pod spec sets serviceAccountName" — a cause it never checked.
// When a pod sets the field to a DIFFERENT SA the old wording was simply
// false, and it sent the reader hunting for a missing field that was not
// missing. Measured against control-plane's prod render: three pods each
// set serviceAccountName to `reliant-cloudsql` while the report claimed
// none set it at all.
//
// `others` is every OTHER identity pods in this namespace bind. Empty
// means the original claim holds and is worth keeping — an unbound SA
// with no competing binder really does leave its workload on `default`.
// Non-empty is the strictly more useful form: it names the identity that
// actually runs, which localizes the fault instead of describing it.
func unboundReason(name string, others []string) string {
	if len(others) == 0 {
		return fmt.Sprintf("ServiceAccount %s is rendered but no pod spec sets serviceAccountName"+
			" — the workload runs as the namespace `default` SA", name)
	}
	return fmt.Sprintf("ServiceAccount %s is rendered but no pod binds it; pods in this namespace run as %s"+
		" — any Role/RoleBinding granted to %s is inert, and the grant should follow the identity that runs",
		name, strings.Join(others, ", "), name)
}

// CheckDeployServiceAccount verifies that a rendered ServiceAccount is
// actually bound to the workloads in its namespace.
//
// Emitting a ServiceAccount + Role + RoleBinding trio and then leaving
// serviceAccountName off the pod spec is worse than emitting nothing:
// the manifests read as if RBAC is scoped, while every pod runs as the
// namespace `default` SA with whatever that account can do.
//
// The `declared == 0` arm below is why this check needed the scope
// contract most: it answers StatusSkip, "not applicable", and it used to
// reach that answer when the ONLY environment declaring a ServiceAccount
// was the one that failed to render. [renderScope.fold] turns that into
// UNDETERMINED — a security-shaped check must never say "does not apply"
// on the strength of facts it could not obtain.
func CheckDeployServiceAccount(_ context.Context, env *Environment) CheckResult {
	return examineRendered(env, "service accounts", func(renders []envRender) CheckResult {
		var unbound []string
		declared := 0
		for _, r := range renders {
			objects := r.workloadObjects()
			accounts := map[string]bool{}
			for _, o := range objects {
				if o.Kind == "ServiceAccount" {
					accounts[o.Metadata.Namespace+"/"+o.Metadata.Name] = false
					declared++
				}
			}
			if len(accounts) == 0 {
				continue
			}
			// Every OTHER identity a pod in this namespace binds. Without
			// it the check can say an SA is unbound but not why, which is
			// how it came to report a cause it never tested.
			boundElsewhere := map[string][]string{}
			for _, o := range objects {
				podSpec, containers := containersOf(o)
				if len(containers) == 0 {
					continue
				}
				sa, _ := podSpec["serviceAccountName"].(string)
				if sa == "" {
					continue
				}
				key := o.Metadata.Namespace + "/" + sa
				if _, known := accounts[key]; known {
					accounts[key] = true
					continue
				}
				ns := o.Metadata.Namespace
				if !slices.Contains(boundElsewhere[ns], sa) {
					boundElsewhere[ns] = append(boundElsewhere[ns], sa)
				}
			}
			names := make([]string, 0, len(accounts))
			for key, bound := range accounts {
				if !bound {
					names = append(names, key)
				}
			}
			sort.Strings(names)
			for _, n := range names {
				ns, _, _ := strings.Cut(n, "/")
				others := boundElsewhere[ns]
				sort.Strings(others)
				unbound = append(unbound, fmt.Sprintf("%s: %s", r.env, unboundReason(n, others)))
			}
		}

		if declared == 0 {
			return CheckResult{Status: StatusSkip, Message: "no ServiceAccount rendered"}
		}
		if len(unbound) > 0 {
			return CheckResult{
				Status:   StatusFail,
				Message:  fmt.Sprintf("%d of %d rendered ServiceAccount(s) are never bound to a pod", len(unbound), declared),
				Evidence: strings.Join(unbound, "\n"),
			}
		}
		return CheckResult{Status: StatusPass, Message: fmt.Sprintf("%d ServiceAccount(s), all bound to a workload", declared)}
	})
}
