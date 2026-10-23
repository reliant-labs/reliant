package cluster

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// A stateful FAKE CLUSTER behind a `kubectl` on PATH.
//
// The recorders elsewhere in this package prove WHAT forge sends. The chart
// prune is a statement about two applies in a row — an object present in
// release N and absent from N+1 is gone afterwards — and no recorder can say
// that, because the answer lives in what the first apply left behind. So this
// fake keeps objects: `apply` stores them, `get -l` filters them by label,
// `delete` removes them.
//
// It is the test binary itself. TestMain below turns the process into the fake
// when it is exec'd under the name `kubectl` with fakeClusterEnv set, and
// newFakeCluster symlinks the test binary to that name on PATH. No shell, no
// second toolchain, and the fake is ordinary Go that can be read and extended.
//
// What it models, on purpose: object identity (kind/namespace/name), labels and
// annotations with server-side-apply field ownership (a key forge applied and
// later stops applying is removed; a key another manager set is kept), the
// namespace defaulting `-n` gives a namespace-less document, the cluster's
// own `kubernetes.io/metadata.name` label, and resourceVersion with
// `create`/`replace` compare-and-swap (AlreadyExists, Conflict, NotFound, and a
// create into a namespace that does not exist). What it does not model is the
// apiserver: admission, finalizers, garbage collection. Those are the
// apiserver's, and the prune's guards are written so it does not need them.
//
// A CONCURRENT actor is modelled by fakeInjectEnv: a file of objects the fake
// upserts the first time it serves the named verb, as if another client's
// write had landed at exactly that moment.

const fakeClusterEnv = "FORGE_FAKE_CLUSTER_STATE"

// fakeInjectEnv names, per verb, a JSON file of objects to upsert when the fake
// first serves that verb (FORGE_FAKE_INJECT_ON_APPLY, …_ON_REPLACE). The file
// is consumed: it fires once.
const fakeInjectEnv = "FORGE_FAKE_INJECT_ON_"

func TestMain(m *testing.M) {
	if state := os.Getenv(fakeClusterEnv); state != "" && filepath.Base(os.Args[0]) == "kubectl" {
		os.Exit(runFakeKubectl(state, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

// fakeClusterState is the on-disk cluster.
type fakeClusterState struct {
	Objects []map[string]any `json:"objects"`
	// Deletes is every `kubectl delete` the fake served, as "resource name [ns]".
	Deletes []string `json:"deletes"`
	// Applies counts the apply calls, so a test can tell "nothing applied".
	Applies int `json:"applies"`
	// RV is the last resourceVersion handed out; every write takes the next.
	RV int `json:"rv"`
}

// stamp gives o the next resourceVersion, as every apiserver write does.
func (s *fakeClusterState) stamp(o map[string]any) {
	s.RV++
	md := strMap(o["metadata"])
	if md["uid"] == nil {
		for _, existing := range s.Objects {
			if objKey(existing) == objKey(o) {
				md["uid"] = strMap(existing["metadata"])["uid"]
				break
			}
		}
		if md["uid"] == nil {
			md["uid"] = fmt.Sprintf("uid-%d", s.RV)
		}
	}
	md["resourceVersion"] = strconv.Itoa(s.RV)
	o["metadata"] = md
}

// upsert replaces the object with o's identity, or adds o.
func (s *fakeClusterState) upsert(o map[string]any) {
	s.stamp(o)
	for i, existing := range s.Objects {
		if objKey(existing) == objKey(o) {
			s.Objects[i] = o
			return
		}
	}
	s.Objects = append(s.Objects, o)
}

// fakeResourceMatches reports whether a kubectl resource argument
// (`networkpolicies.networking.k8s.io`, `configmap`, `clusterrole.rbac…`)
// names kind: the singular, or the plural forms the fake's kinds take.
func fakeResourceMatches(resource, kind string) bool {
	r, _, _ := strings.Cut(strings.ToLower(resource), ".")
	k := strings.ToLower(kind)
	return r == k || r == k+"s" || r == k+"es" || (strings.HasSuffix(k, "y") && r == strings.TrimSuffix(k, "y")+"ies")
}

// injectOnce upserts the objects fakeInjectEnv names for verb, once.
func injectOnce(state *fakeClusterState, verb string) {
	path := os.Getenv(fakeInjectEnv + strings.ToUpper(verb))
	if path == "" {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return // already consumed
	}
	_ = os.Remove(path)
	var objs []map[string]any
	if err := json.Unmarshal(b, &objs); err != nil {
		return
	}
	for _, o := range objs {
		state.upsert(o)
	}
}

// fakeClusterScoped are the kinds the fake treats as cluster-scoped: no
// namespace is defaulted onto them and a delete of one takes no -n.
var fakeClusterScoped = map[string]bool{
	"Namespace": true, "ClusterRole": true, "ClusterRoleBinding": true,
	"CustomResourceDefinition": true, "PersistentVolume": true,
	"ValidatingWebhookConfiguration": true, "MutatingWebhookConfiguration": true,
	"PriorityClass": true, "StorageClass": true,
}

func fakeKey(kind, ns, name string) string { return kind + "|" + ns + "|" + name }

func objKey(o map[string]any) string {
	md, _ := o["metadata"].(map[string]any)
	ns, _ := md["namespace"].(string)
	kind, _ := o["kind"].(string)
	name, _ := md["name"].(string)
	return fakeKey(kind, ns, name)
}

func loadFakeState(path string) fakeClusterState {
	var s fakeClusterState
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

func saveFakeState(path string, s fakeClusterState) {
	b, _ := json.MarshalIndent(s, "", " ")
	_ = os.WriteFile(path, b, 0o644)
}

func strMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		m = map[string]any{}
	}
	return m
}

// mergeOwned is server-side-apply's rule for a map field written by one
// manager: the result is the applied keys, plus every existing key some OTHER
// manager owns. A key this manager applied last time and omits now is removed.
func mergeOwned(existing, applied map[string]any, prevOwned []any) (merged map[string]any, owned []any) {
	merged = map[string]any{}
	was := map[string]bool{}
	for _, k := range prevOwned {
		was[fmt.Sprint(k)] = true
	}
	for k, v := range existing {
		if !was[k] {
			merged[k] = v
		}
	}
	for k, v := range applied {
		merged[k] = v
		owned = append(owned, k)
	}
	sort.Slice(owned, func(i, j int) bool { return fmt.Sprint(owned[i]) < fmt.Sprint(owned[j]) })
	return merged, owned
}

func runFakeKubectl(statePath string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) >= 2 && args[0] == "--context" {
		args = args[2:]
	}
	if len(args) == 0 {
		return 0
	}
	state := loadFakeState(statePath)
	defer func() { saveFakeState(statePath, state) }()

	flag := func(name string) string {
		for i, a := range args {
			if a == name && i+1 < len(args) {
				return args[i+1]
			}
		}
		return ""
	}

	injectOnce(&state, args[0])
	switch args[0] {
	case "apply":
		if os.Getenv("FORGE_FAKE_FAIL_APPLY") != "" {
			fmt.Fprintln(stderr, "error: the server is currently unable to handle the request")
			return 1
		}
		state.Applies++
		var applied []map[string]any
		defaultNS := flag("-n")
		dec := yaml.NewDecoder(stdin)
		for {
			var doc map[string]any
			if err := dec.Decode(&doc); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				fmt.Fprintln(stderr, "error: parse:", err)
				return 1
			}
			if doc == nil {
				continue
			}
			kind, _ := doc["kind"].(string)
			md := strMap(doc["metadata"])
			if ns, _ := md["namespace"].(string); ns == "" && !fakeClusterScoped[kind] {
				if defaultNS == "" {
					defaultNS = "default"
				}
				md["namespace"] = defaultNS
			}
			doc["metadata"] = md
			applyFakeObject(&state, doc)
			name, _ := md["name"].(string)
			if flag("-o") == "json" {
				applied = append(applied, doc)
			} else {
				fmt.Fprintf(stdout, "%s/%s serverside-applied\n", strings.ToLower(kind), name)
			}
		}
		if flag("-o") == "json" {
			b, _ := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "List", "items": applied})
			fmt.Fprintln(stdout, string(b))
		}
	case "get":
		if raw := flag("--raw"); raw != "" {
			var resources []map[string]any
			seen := map[string]bool{}
			for _, o := range state.Objects {
				kind, _ := o["kind"].(string)
				if seen[kind] {
					continue
				}
				seen[kind] = true
				name := strings.ToLower(kind) + "s"
				if strings.HasSuffix(kind, "Policy") {
					name = strings.TrimSuffix(strings.ToLower(kind), "y") + "ies"
				}
				resources = append(resources, map[string]any{"name": name, "kind": kind, "namespaced": !fakeClusterScoped[kind]})
			}
			b, _ := json.Marshal(map[string]any{"resources": resources})
			fmt.Fprintln(stdout, string(b))
			return 0
		}
		// A kind whose CRD is gone: the server no longer serves it at all.
		if unserved := os.Getenv("FORGE_FAKE_UNSERVED_KIND"); unserved != "" && fakeResourceMatches(args[1], unserved) {
			fmt.Fprintf(stderr, "error: the server doesn't have a resource type %q\n", args[1])
			return 1
		}
		resources := strings.Split(args[1], ",")
		ofResource := func(o map[string]any) bool {
			kind, _ := o["kind"].(string)
			for _, r := range resources {
				if fakeResourceMatches(r, kind) {
					return true
				}
			}
			return false
		}
		// `get <resource> <name>`: one object, or nothing with --ignore-not-found.
		if len(args) > 2 && !strings.HasPrefix(args[2], "-") {
			name, ns := args[2], flag("-n")
			for _, o := range state.Objects {
				md := strMap(o["metadata"])
				kind, _ := o["kind"].(string)
				oNS, _ := md["namespace"].(string)
				if !ofResource(o) || md["name"] != name || (!fakeClusterScoped[kind] && oNS != ns) {
					continue
				}
				if flag("-o") == "name" {
					fmt.Fprintf(stdout, "%s/%s\n", strings.ToLower(kind), name)
					return 0
				}
				b, _ := json.Marshal(o)
				fmt.Fprintln(stdout, string(b))
				return 0
			}
			if !hasArg(args, "--ignore-not-found") {
				fmt.Fprintf(stderr, "Error from server (NotFound): %s %q not found\n", args[1], name)
				return 1
			}
			return 0
		}
		sel := map[string]string{}
		for _, kv := range strings.Split(flag("-l"), ",") {
			if k, v, ok := strings.Cut(kv, "="); ok {
				sel[k] = v
			}
		}
		items := []map[string]any{}
		for _, o := range state.Objects {
			labels := strMap(strMap(o["metadata"])["labels"])
			match := len(sel) > 0 && ofResource(o)
			for k, v := range sel {
				if labels[k] != v {
					match = false
				}
			}
			if match {
				items = append(items, o)
			}
		}
		b, _ := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "List", "items": items})
		fmt.Fprintln(stdout, string(b))
	case "create", "replace":
		var doc map[string]any
		if err := yaml.NewDecoder(stdin).Decode(&doc); err != nil {
			fmt.Fprintln(stderr, "error: parse:", err)
			return 1
		}
		doc = jsonRoundTrip(doc)
		md := strMap(doc["metadata"])
		kind, _ := doc["kind"].(string)
		ns, _ := md["namespace"].(string)
		var existing map[string]any
		for _, o := range state.Objects {
			if objKey(o) == objKey(doc) {
				existing = o
			}
		}
		if args[0] == "create" {
			if existing != nil {
				fmt.Fprintf(stderr, "Error from server (AlreadyExists): error when creating \"STDIN\": %s %q already exists\n", kind, md["name"])
				return 1
			}
			if !fakeClusterScoped[kind] && !state.hasNamespace(ns) {
				fmt.Fprintf(stderr, "Error from server (NotFound): error when creating \"STDIN\": namespaces %q not found\n", ns)
				return 1
			}
		} else {
			if existing == nil {
				fmt.Fprintf(stderr, "Error from server (NotFound): error when replacing \"STDIN\": %s %q not found\n", kind, md["name"])
				return 1
			}
			if md["resourceVersion"] != strMap(existing["metadata"])["resourceVersion"] {
				fmt.Fprintf(stderr, "Error from server (Conflict): error when replacing \"STDIN\": Operation cannot be fulfilled on %s %q: "+
					"the object has been modified; please apply your changes to the latest version and try again\n", kind, md["name"])
				return 1
			}
		}
		state.upsert(doc)
		if flag("-o") == "json" {
			b, _ := json.Marshal(doc)
			fmt.Fprintln(stdout, string(b))
		} else {
			fmt.Fprintf(stdout, "%s/%s %sd\n", strings.ToLower(kind), md["name"], args[0])
		}
	case "delete":
		if raw := flag("--raw"); raw != "" {
			var options struct {
				Preconditions struct {
					UID             string `json:"uid"`
					ResourceVersion string `json:"resourceVersion"`
				} `json:"preconditions"`
			}
			if err := json.NewDecoder(stdin).Decode(&options); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			parts := strings.Split(strings.Trim(raw, "/"), "/")
			res, name := parts[len(parts)-2], parts[len(parts)-1]
			ns := ""
			for i, p := range parts {
				if p == "namespaces" && i < len(parts)-2 {
					ns = parts[i+1]
				}
			}
			for i, o := range state.Objects {
				md := strMap(o["metadata"])
				kind, _ := o["kind"].(string)
				oNS, _ := md["namespace"].(string)
				if !fakeResourceMatches(res, kind) || md["name"] != name || oNS != ns {
					continue
				}
				if md["uid"] != options.Preconditions.UID || md["resourceVersion"] != options.Preconditions.ResourceVersion {
					fmt.Fprintln(stderr, "Error from server (Conflict): delete preconditions failed")
					return 1
				}
				// Keep the existing tests' resource/name/namespace delete log.
				group := apiGroup(fmt.Sprint(o["apiVersion"]))
				state.Deletes = append(state.Deletes, strings.TrimSpace(kubectlResource(kind, group)+" "+name+" "+ns))
				state.Objects = append(state.Objects[:i], state.Objects[i+1:]...)
				return 0
			}
			fmt.Fprintln(stderr, "Error from server (NotFound): object absent")
			return 1
		}
		res, name, ns := args[1], args[2], flag("-n")
		state.Deletes = append(state.Deletes, strings.TrimSpace(res+" "+name+" "+ns))
		want, _, _ := strings.Cut(res, ".")
		kept := state.Objects[:0]
		for _, o := range state.Objects {
			md := strMap(o["metadata"])
			kind, _ := o["kind"].(string)
			oNS, _ := md["namespace"].(string)
			if strings.ToLower(kind) == want && md["name"] == name && oNS == ns {
				continue
			}
			kept = append(kept, o)
		}
		state.Objects = kept
	}
	return 0
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want || strings.HasPrefix(a, want+"=") {
			return true
		}
	}
	return false
}

// jsonRoundTrip normalizes a decoded document to the JSON shapes the state file
// holds (map[string]any all the way down).
func jsonRoundTrip(doc map[string]any) map[string]any {
	b, _ := json.Marshal(doc)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func (s *fakeClusterState) hasNamespace(ns string) bool {
	for _, o := range s.Objects {
		if objKey(o) == fakeKey("Namespace", "", ns) {
			return true
		}
	}
	return false
}

// applyFakeObject upserts doc with server-side-apply label/annotation
// ownership.
func applyFakeObject(state *fakeClusterState, doc map[string]any) {
	state.stamp(doc)
	key := objKey(doc)
	md := strMap(doc["metadata"])
	appliedLabels, appliedAnns := strMap(md["labels"]), strMap(md["annotations"])
	for i, existing := range state.Objects {
		if objKey(existing) != key {
			continue
		}
		emd := strMap(existing["metadata"])
		managed := strMap(emd["_managed"])
		labels, ownedL := mergeOwned(strMap(emd["labels"]), appliedLabels, asSlice(managed["labels"]))
		anns, ownedA := mergeOwned(strMap(emd["annotations"]), appliedAnns, asSlice(managed["annotations"]))
		md["labels"], md["annotations"] = labels, anns
		md["_managed"] = map[string]any{"labels": ownedL, "annotations": ownedA}
		pruneEmpty(md)
		state.Objects[i] = doc
		return
	}
	_, ownedL := mergeOwned(nil, appliedLabels, nil)
	_, ownedA := mergeOwned(nil, appliedAnns, nil)
	md["_managed"] = map[string]any{"labels": ownedL, "annotations": ownedA}
	if kind, _ := doc["kind"].(string); kind == "Namespace" {
		labels := strMap(md["labels"])
		labels["kubernetes.io/metadata.name"] = md["name"]
		md["labels"] = labels
	}
	state.Objects = append(state.Objects, doc)
}

func asSlice(v any) []any { s, _ := v.([]any); return s }

func pruneEmpty(md map[string]any) {
	for _, k := range []string{"labels", "annotations"} {
		if m, ok := md[k].(map[string]any); ok && len(m) == 0 {
			delete(md, k)
		}
	}
}

// fakeCluster is the test's handle on the fake.
type fakeCluster struct {
	t    *testing.T
	path string
}

// newFakeCluster installs the fake `kubectl` on PATH and returns a handle. The
// fake kubectl is this very test binary, symlinked under the right name.
func newFakeCluster(t *testing.T) *fakeCluster {
	t.Helper()
	requirePOSIXFake(t, "kubectl")
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	dir := t.TempDir()
	if err := os.Symlink(self, filepath.Join(dir, "kubectl")); err != nil {
		t.Fatalf("symlink fake kubectl: %v", err)
	}
	path := filepath.Join(dir, "cluster.json")
	saveFakeState(path, fakeClusterState{Objects: []map[string]any{{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": "kube-system"}}}})
	t.Setenv(fakeClusterEnv, path)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &fakeCluster{t: t, path: path}
}

func (c *fakeCluster) state() fakeClusterState { return loadFakeState(c.path) }

// find returns the live object, or nil.
func (c *fakeCluster) find(kind, ns, name string) map[string]any {
	for _, o := range c.state().Objects {
		if objKey(o) == fakeKey(kind, ns, name) {
			return o
		}
	}
	return nil
}

func (c *fakeCluster) has(kind, ns, name string) bool { return c.find(kind, ns, name) != nil }

// seed puts an object straight into the cluster, as if another actor created it.
func (c *fakeCluster) seed(doc map[string]any) {
	c.t.Helper()
	s := c.state()
	s.Objects = append(s.Objects, doc)
	saveFakeState(c.path, s)
}

// setForeignLabel adds a label to a live object as a DIFFERENT field manager
// would: forge does not own it.
func (c *fakeCluster) setForeignLabel(kind, ns, name, key, value string) {
	c.t.Helper()
	s := c.state()
	for _, o := range s.Objects {
		if objKey(o) == fakeKey(kind, ns, name) {
			md := strMap(o["metadata"])
			labels := strMap(md["labels"])
			labels[key] = value
			md["labels"] = labels
			saveFakeState(c.path, s)
			return
		}
	}
	c.t.Fatalf("no live %s %s/%s to label", kind, ns, name)
}

func (c *fakeCluster) labels(kind, ns, name string) map[string]any {
	o := c.find(kind, ns, name)
	if o == nil {
		c.t.Fatalf("no live %s %s/%s", kind, ns, name)
	}
	return strMap(strMap(o["metadata"])["labels"])
}

func (c *fakeCluster) deletes() []string {
	var objects []string
	for _, d := range c.state().Deletes {
		if strings.HasPrefix(d, "configmap forge-chart-lock-") {
			continue
		}
		objects = append(objects, d)
	}
	return objects
}

// injectOn makes the fake upsert objs the first time it serves verb: another
// client's write, landing mid-run.
func (c *fakeCluster) injectOn(verb string, objs ...map[string]any) {
	c.t.Helper()
	b, err := json.Marshal(objs)
	if err != nil {
		c.t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(c.path), "inject-"+verb+".json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		c.t.Fatal(err)
	}
	c.t.Setenv(fakeInjectEnv+strings.ToUpper(verb), path)
}

// inventories returns every chart inventory ConfigMap in the cluster, keyed
// "<namespace>/<name>", each as its sorted entries.
func (c *fakeCluster) inventories() map[string][]string {
	c.t.Helper()
	out := map[string][]string{}
	for _, o := range c.state().Objects {
		md := strMap(o["metadata"])
		if o["kind"] != "ConfigMap" || strMap(md["labels"])[ChartInventoryLabel] == nil {
			continue
		}
		inv, err := decodeChartInventory(fmt.Sprint(strMap(o["data"])[chartInventoryDataKey]))
		if err != nil {
			c.t.Fatalf("inventory %v is unreadable: %v", md["name"], err)
		}
		var entries []string
		for e := range inv {
			entries = append(entries, e.String())
		}
		sort.Strings(entries)
		out[fmt.Sprintf("%v/%v", md["namespace"], md["name"])] = entries
	}
	return out
}

// inventory returns the entries of the one chart inventory in ns.
func (c *fakeCluster) inventory(ns string) []string {
	c.t.Helper()
	for k, entries := range c.inventories() {
		if strings.HasPrefix(k, chartStateNamespace+"/") {
			return entries
		}
	}
	c.t.Fatalf("no chart inventory in namespace %s; inventories: %v", ns, c.inventories())
	return nil
}
