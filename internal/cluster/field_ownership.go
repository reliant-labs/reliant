package cluster

// Spec-driven reconciliation for forge's own apply.
//
// forge applies with `kubectl apply --server-side` (field manager "kubectl",
// operation Apply). Server-side apply removes a field only when THE APPLYING
// MANAGER owned it and stops declaring it; a field owned by anyone else, or by
// no one, survives every apply forever. --force-conflicts does not help: it
// settles a field the config declares, and the fields that drift are exactly
// the ones it does not. So without help, a `forge env deploy` converges only what it
// declares, and everything else that lands on its objects stays:
//
//   - a hand edit: `kubectl set env`, `kubectl edit`, `kubectl patch`, `kubectl
//     label` each write as their own manager (kubectl-set, kubectl-edit, ...),
//     with operation Update. Prod's admin-server kept an env var
//     a `kubectl` Update had added, through every later deploy.
//   - an OWNERLESS field: one no managedFields entry claims. They are left
//     behind when ownership history is lost (managedFields cleared or edited
//     by hand, an entry removed, an object handed between appliers), and
//     server-side apply has no way to remove them at all. Prod's
//     daemon-gateway, reliant-api-server and reliant-temporal-worker each kept
//     a `scratch` emptyDir an older forge render declared and later dropped,
//     and zitadel kept a serviceAccountName nobody declared.
//
// convergeFieldOwnership runs before every apply stream and makes forge the
// owner of both, so the apply that follows removes whatever forge no longer
// declares. It rewrites only managedFields, never a value; the apply does the
// rest, by server-side apply's own rules.
//
// WHAT IS TAKEN OVER. The managers of imperative clients — every manager whose
// name starts with "kubectl" (kubectl-edit, kubectl-patch, kubectl-set,
// kubectl-client-side-apply, kubectl-last-applied, kubectl-rollout, …, in
// either operation) and "before-first-apply" — on the main resource. That is
// the set Flux's kustomize-controller takes over on every apply (its
// `kubectl` prefix match), so an object reads the same whichever applier
// converges it. A controller's manager (kube-controller-manager, an HPA's
// scale subresource, an operator writing an annotation) is never touched:
// those writes are the cluster working, not drift.
//
// WHAT IS CLAIMED. Most ownerless fields are the server's own: defaults
// (dnsPolicy, terminationMessagePath), allocations (a Service's clusterIP),
// values it derives (the serviceAccount alias of a serviceAccountName). An
// ownerless field is forge's only when removing it sticks: it is absent from
// what the server makes of the object with every ownerless field removed
// (`kubectl replace --dry-run=server`, which re-runs defaulting, allocation
// and admission). Which fields an object has at all comes from the server too:
// a server-side dry-run apply of the live object under a probe manager owns
// exactly its fields. Neither needs a schema in forge.
//
// The cost on a converged cluster is three reads per apply stream (one get,
// one dry-run apply, one dry-run replace) and no writes.
//
// THE ESCAPE HATCH. An object annotated `forge.dev/field-ownership: shared`
// is left as server-side apply leaves it: forge converges only the fields it
// declares, and other writers' fields stay.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/structured-merge-diff/v6/fieldpath"
	sigsyaml "sigs.k8s.io/yaml"
)

const (
	// forgeFieldManager is the manager forge's apply writes as: kubectl's own
	// default for `apply --server-side`, since kubectlApplyArgs passes no
	// --field-manager.
	forgeFieldManager = "kubectl"
	// ownershipProbeManager is the manager of the dry-run apply that reads an
	// object's field set. Dry-run: it is never persisted.
	ownershipProbeManager = "forge-ownership-probe"
	// fieldOwnershipAnnotation opts one object out of the convergence.
	fieldOwnershipAnnotation = "forge.dev/field-ownership"
	// fieldOwnershipShared is the opt-out value: other writers' fields stay.
	fieldOwnershipShared = "shared"
	// ownershipClaimAttempts bounds the retries of a managedFields patch that
	// lost a race with another writer (its resourceVersion test failed).
	ownershipClaimAttempts = 3
	// maxReportedPaths bounds the per-object list of fields printed.
	maxReportedPaths = 8
)

// takenOverManager reports whether a managedFields entry belongs to an
// imperative client whose fields forge takes over. forge's own entry is not
// "taken over": it is the destination.
func takenOverManager(e metav1.ManagedFieldsEntry) bool {
	if e.Subresource != "" {
		return false
	}
	if e.Manager == forgeFieldManager && e.Operation == metav1.ManagedFieldsOperationApply {
		return false
	}
	return strings.HasPrefix(e.Manager, "kubectl") || e.Manager == "before-first-apply"
}

// noOrphanCheckKinds are the kinds whose ownerless fields are never claimed.
// A Job's and a Pod's spec is immutable once created, and the server sets
// fields of its own on it at creation (a Job's selector and controller-uid
// labels) that no update may remove: a field forge does not declare there goes
// only with the object, which the immutable recovery recreates on the next
// spec change.
var noOrphanCheckKinds = map[string]bool{"Job": true, "Pod": true}

// kubectlRun runs one kubectl invocation against the apply's context. A seam so
// the orchestration is testable without a cluster.
type kubectlRun func(ctx context.Context, args []string, stdin []byte) (stdout, stderr []byte, err error)

func contextKubectl(kctx string) kubectlRun {
	return func(ctx context.Context, args []string, stdin []byte) ([]byte, []byte, error) {
		cmd := kubectlCmd(ctx, kctx, args...)
		cmd.Stdin = bytes.NewReader(stdin)
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		return out.Bytes(), errb.Bytes(), err
	}
}

// objectKey identifies an object across the declared stream and the cluster:
// API group, kind, namespace and name. The version is not part of it — the
// cluster serves one object at every version.
type objectKey struct{ Group, Kind, Namespace, Name string }

func (k objectKey) String() string {
	if k.Namespace == "" {
		return k.Kind + "/" + k.Name
	}
	return k.Kind + "/" + k.Namespace + "/" + k.Name
}

func keyOf(apiVersion, kind, namespace, name string) objectKey {
	return objectKey{Group: apiGroup(apiVersion), Kind: kind, Namespace: namespace, Name: name}
}

// liveObject is one object as `kubectl get -o json --show-managed-fields`
// returned it.
type liveObject struct {
	raw     map[string]any
	key     objectKey
	version string // apiVersion
	rv      string
	entries []metav1.ManagedFieldsEntry
}

func decodeLiveObject(raw map[string]any) (liveObject, error) {
	md, _ := raw["metadata"].(map[string]any)
	apiVersion, _ := raw["apiVersion"].(string)
	kind, _ := raw["kind"].(string)
	name, _ := md["name"].(string)
	ns, _ := md["namespace"].(string)
	rv, _ := md["resourceVersion"].(string)
	o := liveObject{raw: raw, key: keyOf(apiVersion, kind, ns, name), version: apiVersion, rv: rv}
	if mf, ok := md["managedFields"]; ok {
		b, err := json.Marshal(mf)
		if err != nil {
			return o, err
		}
		if err := json.Unmarshal(b, &o.entries); err != nil {
			return o, fmt.Errorf("%s: managedFields: %w", o.key, err)
		}
	}
	return o, nil
}

// decodeObjects reads every object in a kubectl JSON output: one object, a
// List, or several objects one after another (what `replace -o json` prints).
// Empty output is no objects.
func decodeObjects(b []byte) ([]map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var out []map[string]any
	for {
		var o map[string]any
		if err := dec.Decode(&o); err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return nil, err
		}
		if o == nil {
			continue
		}
		if kind, _ := o["kind"].(string); kind == "List" || (strings.HasSuffix(kind, "List") && o["items"] != nil) {
			items, _ := o["items"].([]any)
			for _, it := range items {
				if m, ok := it.(map[string]any); ok {
					out = append(out, m)
				}
			}
			continue
		}
		out = append(out, o)
	}
}

// fieldSet parses one managedFields entry's set.
func fieldSet(e metav1.ManagedFieldsEntry) (*fieldpath.Set, error) {
	s := &fieldpath.Set{}
	if e.FieldsV1 == nil || len(e.FieldsV1.GetRawBytes()) == 0 {
		return s, nil
	}
	if err := s.FromJSON(e.FieldsV1.GetRawReader()); err != nil {
		return nil, fmt.Errorf("manager %s: %w", e.Manager, err)
	}
	return s, nil
}

// ownershipClaim is what convergence does to one object: the managedFields it
// writes, and an account of what changed hands.
type ownershipClaim struct {
	key        objectKey
	rv         string
	version    string
	kind       string
	name       string
	namespace  string
	entries    []metav1.ManagedFieldsEntry
	takenFrom  []string         // managers whose entries forge took over
	claimed    *fieldpath.Set   // every field forge gains
	reportable []fieldpath.Path // shallowest of claimed, minus what forge already owned
	ownerless  int              // how many of claimed nobody owned
}

// ownershipPicture is who owns what on one live object.
type ownershipPicture struct {
	owned   *fieldpath.Set   // every entry's fields, subresources included
	sets    []*fieldpath.Set // per entry, in entry order
	forge   int              // index of forge's own entry, or -1
	version string           // the API version forge's entry (or the object) is at
	// sameVersion is false when a main-resource entry was recorded at another
	// API version. Its set names that version's fields: merging it into
	// forge's would mis-attribute renamed ones, and a picture mixing versions
	// cannot say what nobody owns.
	sameVersion bool
}

func ownershipOf(live liveObject) (ownershipPicture, error) {
	pic := ownershipPicture{owned: &fieldpath.Set{}, forge: -1, version: live.version, sameVersion: true}
	for i, e := range live.entries {
		if e.Manager == forgeFieldManager && e.Operation == metav1.ManagedFieldsOperationApply && e.Subresource == "" {
			pic.forge = i
		}
	}
	if pic.forge >= 0 && live.entries[pic.forge].APIVersion != "" {
		pic.version = live.entries[pic.forge].APIVersion
	}
	// The probe names the fields of the version the object was read at.
	// Entries all recorded at another version (forge's included) agree with
	// one another, not with the probe.
	if live.version != "" && pic.version != live.version {
		pic.sameVersion = false
	}
	for _, e := range live.entries {
		s, err := fieldSet(e)
		if err != nil {
			return pic, fmt.Errorf("%s: %w", live.key, err)
		}
		pic.sets = append(pic.sets, s)
		pic.owned = pic.owned.Union(s)
		if e.Subresource == "" && e.APIVersion != "" && e.APIVersion != pic.version {
			pic.sameVersion = false
		}
	}
	return pic, nil
}

// ownerlessFields is the part of the object's field set (probe) that no entry
// owns, or nil when that cannot be told (no probe, or entries at mixed
// versions). It holds server defaults as well as orphans; trimmedDefaults
// tells them apart.
func ownerlessFields(pic ownershipPicture, probe *fieldpath.Set) *fieldpath.Set {
	if probe == nil || !pic.sameVersion {
		return nil
	}
	return probe.Difference(pic.owned)
}

// planClaim decides one object's claim. probe is the object's full field set,
// and trimmed what the server makes of the object with its ownerless fields
// removed (trimmedDefaults); either nil means it could not be read, and then
// ownerless fields are left alone.
//
// An ownerless field is an orphan, and forge's, when it is absent from
// trimmed: removing it sticks. One the server puts back — a default, an
// allocated value, the serviceAccount alias the server derives from a
// serviceAccountName another manager owns — is the cluster's, and stays.
//
// An object with no managedFields at all (history cleared) is the same
// question with every field ownerless; trimmed is then the server's answer
// for the declaration itself (see trimmedDefaults).
//
// It returns nil when there is nothing to claim: the steady state.
func planClaim(live liveObject, probe *fieldpath.Set, trimmed map[string]any, now time.Time) (*ownershipClaim, error) {
	pic, err := ownershipOf(live)
	if err != nil {
		return nil, err
	}
	claimed := &fieldpath.Set{}
	var takenFrom []string
	takenIdx := map[int]bool{}
	for i, e := range live.entries {
		if !takenOverManager(e) || (e.APIVersion != "" && e.APIVersion != pic.version) {
			continue
		}
		takenIdx[i] = true
		if !pic.sets[i].Empty() {
			claimed = claimed.Union(pic.sets[i])
			takenFrom = append(takenFrom, e.Manager)
		}
	}

	ownerless := 0
	if candidates := ownerlessFields(pic, probe); candidates != nil && trimmed != nil {
		var orphans []fieldpath.Path
		candidates.Iterate(func(p fieldpath.Path) {
			if !pathPresent(trimmed, p) {
				orphans = append(orphans, p.Copy())
			}
		})
		if len(orphans) > 0 {
			claimed = claimed.Union(fieldpath.NewSet(orphans...))
			ownerless = len(orphans)
		}
	}
	if claimed.Empty() && len(takenIdx) == 0 {
		return nil, nil
	}

	forgeIdx, version := pic.forge, pic.version
	forgeSet := &fieldpath.Set{}
	if forgeIdx >= 0 {
		forgeSet = pic.sets[forgeIdx]
	}
	merged := forgeSet.Union(claimed)
	raw, err := merged.ToJSON()
	if err != nil {
		return nil, fmt.Errorf("%s: encode claimed fields: %w", live.key, err)
	}
	stamp := metav1.NewTime(now.UTC().Truncate(time.Second))
	forgeEntry := metav1.ManagedFieldsEntry{
		Manager:    forgeFieldManager,
		Operation:  metav1.ManagedFieldsOperationApply,
		APIVersion: version,
		Time:       &stamp,
		FieldsType: "FieldsV1",
		FieldsV1:   metav1.NewFieldsV1(string(raw)),
	}
	var entries []metav1.ManagedFieldsEntry
	for i, e := range live.entries {
		switch {
		case i == forgeIdx:
			entries = append(entries, forgeEntry)
		case takenIdx[i]:
			// Merged into forge's entry above.
		default:
			entries = append(entries, e)
		}
	}
	if forgeIdx < 0 {
		entries = append(entries, forgeEntry)
	}

	md, _ := live.raw["metadata"].(map[string]any)
	name, _ := md["name"].(string)
	ns, _ := md["namespace"].(string)
	c := &ownershipClaim{
		key: live.key, rv: live.rv, version: live.version, kind: live.key.Kind, name: name, namespace: ns,
		entries: entries, takenFrom: dedupe(takenFrom), claimed: claimed, ownerless: ownerless,
		reportable: shallowest(claimed.Difference(forgeSet)),
	}
	return c, nil
}

// shallowest lists a set's members that have no ancestor in the set — the
// fields a reader recognizes ("the scratch volume"), not every leaf under them.
func shallowest(s *fieldpath.Set) []fieldpath.Path {
	var all []fieldpath.Path
	s.Iterate(func(p fieldpath.Path) { all = append(all, p.Copy()) })
	sort.Slice(all, func(i, j int) bool { return len(all[i]) < len(all[j]) })
	var out []fieldpath.Path
	for _, p := range all {
		covered := false
		for _, q := range out {
			if len(q) < len(p) && q.Equals(p[:len(q)]) {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// child selects one path element's child of node: a map field, or the list
// element server-side apply keys it by — its key fields, its value (a set), or
// its index (an atomic list). idx is the element's index in a list, else -1.
func child(node any, pe fieldpath.PathElement) (v any, idx int, ok bool) {
	switch {
	case pe.FieldName != nil:
		m, isMap := node.(map[string]any)
		if !isMap {
			return nil, -1, false
		}
		v, ok = m[*pe.FieldName]
		return v, -1, ok
	case pe.Index != nil:
		l, isList := node.([]any)
		if !isList || *pe.Index < 0 || *pe.Index >= len(l) {
			return nil, -1, false
		}
		return l[*pe.Index], *pe.Index, true
	}
	l, isList := node.([]any)
	if !isList {
		return nil, -1, false
	}
	for i, it := range l {
		switch {
		case pe.Key != nil:
			m, isMap := it.(map[string]any)
			if !isMap {
				continue
			}
			match := true
			for _, f := range *pe.Key {
				if !jsonEqual(m[f.Name], f.Value.Unstructured()) {
					match = false
					break
				}
			}
			if match {
				return it, i, true
			}
		case pe.Value != nil:
			if jsonEqual(it, (*pe.Value).Unstructured()) {
				return it, i, true
			}
		}
	}
	return nil, -1, false
}

// pathPresent reports whether obj (an unstructured object) holds the field p
// names.
func pathPresent(obj any, p fieldpath.Path) bool {
	cur := obj
	for _, pe := range p {
		v, _, ok := child(cur, pe)
		if !ok {
			return false
		}
		cur = v
	}
	return true
}

// deletePath removes the field p names from node and returns the result (a
// list shrinks, so the caller stores it back). An absent field is a no-op.
func deletePath(node any, p fieldpath.Path) any {
	if len(p) == 0 {
		return node
	}
	v, idx, ok := child(node, p[0])
	if !ok {
		return node
	}
	if m, isMap := node.(map[string]any); isMap {
		if len(p) == 1 {
			delete(m, *p[0].FieldName)
		} else {
			m[*p[0].FieldName] = deletePath(v, p[1:])
		}
		return m
	}
	l := node.([]any)
	if len(p) == 1 {
		return append(l[:idx:idx], l[idx+1:]...)
	}
	l[idx] = deletePath(v, p[1:])
	return l
}

// jsonEqual compares two decoded JSON values by their encoding, so a
// json.Number from kubectl's output and an int64 from a fieldpath key agree.
func jsonEqual(a, b any) bool {
	ja, err1 := json.Marshal(a)
	jb, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && bytes.Equal(ja, jb)
}

// probeBody is the live object as an apply configuration: everything it holds
// that a client can write, so a dry-run apply of it owns every field.
func probeBody(raw map[string]any) map[string]any {
	body := make(map[string]any, len(raw))
	for k, v := range raw {
		if k != "status" {
			body[k] = v
		}
	}
	md, _ := raw["metadata"].(map[string]any)
	clean := map[string]any{}
	for k, v := range md {
		switch k {
		case "managedFields", "resourceVersion", "uid", "creationTimestamp", "generation",
			"selfLink", "deletionTimestamp", "deletionGracePeriodSeconds":
		default:
			clean[k] = v
		}
	}
	body["metadata"] = clean
	return body
}

// probeSets returns each object's full field set, read by a dry-run apply of
// the live objects under ownershipProbeManager. One batched call; when the
// batch fails (kubectl prints nothing for any object of a -o json batch that
// failed anywhere), each object is probed alone so one bad object costs only
// its own answer.
func probeSets(ctx context.Context, run kubectlRun, objs []liveObject) map[objectKey]*fieldpath.Set {
	out := map[objectKey]*fieldpath.Set{}
	if len(objs) == 0 {
		return out
	}
	args := []string{"apply", "--server-side", "--dry-run=server", "--force-conflicts",
		"--field-manager=" + ownershipProbeManager, "--show-managed-fields", "-o", "json", "-f", "-"}
	collect := func(b []byte) {
		results, err := decodeObjects(b)
		if err != nil {
			return
		}
		for _, r := range results {
			o, err := decodeLiveObject(r)
			if err != nil {
				continue
			}
			for _, e := range o.entries {
				if e.Manager == ownershipProbeManager && e.Subresource == "" {
					if s, err := fieldSet(e); err == nil {
						out[o.key] = s
					}
				}
			}
		}
	}
	list := func(objs []liveObject) []byte {
		items := make([]any, 0, len(objs))
		for _, o := range objs {
			items = append(items, probeBody(o.raw))
		}
		b, _ := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "List", "items": items})
		return b
	}
	if stdout, _, err := run(ctx, args, list(objs)); err == nil {
		collect(stdout)
		return out
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, o := range objs {
		wg.Add(1)
		sem <- struct{}{}
		go func(o liveObject) {
			defer wg.Done()
			defer func() { <-sem }()
			stdout, _, err := run(ctx, args, list([]liveObject{o}))
			if err != nil {
				return
			}
			mu.Lock()
			collect(stdout)
			mu.Unlock()
		}(o)
	}
	wg.Wait()
	return out
}

// trimmedDefaults returns, per object with ownerless fields, what the server
// makes of it with those fields removed: `kubectl replace --dry-run=server` of
// the live object minus every ownerless field. A field the server puts back is
// not drift (see planClaim). replace prints each object it wrote, so an object
// it refuses costs only its own answer, and that refusal is reported on out:
// its ownerless fields stay as they are.
//
// An object with no managedFields has nothing but ownerless fields, and the
// live object minus all of them is no object at all (its required fields
// gone). Its replacement is the declaration instead (declared[key]): what the
// server makes of it is what forge's apply leaves, so a live field absent from
// it is one only that apply could remove.
//
// Every replacement carries the live resourceVersion: an update of a custom
// resource must name one, and it makes the answer describe exactly the
// revision the claim is planned on (a write in between fails it, and the
// claim's own resourceVersion test then re-reads and re-judges the object).
func trimmedDefaults(ctx context.Context, run kubectlRun, objs []liveObject, probes map[objectKey]*fieldpath.Set, declared map[objectKey]map[string]any, out io.Writer) map[objectKey]map[string]any {
	answers := map[objectKey]map[string]any{}
	var items []any
	for _, o := range objs {
		pic, err := ownershipOf(o)
		if err != nil {
			continue
		}
		ownerless := ownerlessFields(pic, probes[o.key])
		if ownerless == nil || ownerless.Empty() {
			continue
		}
		var body map[string]any
		if len(o.entries) == 0 {
			decl, ok := declared[o.key]
			if !ok {
				continue
			}
			body = jsonCopy(decl)
		} else {
			body = jsonCopy(probeBody(o.raw))
			var trimmed any = body
			for _, p := range shallowest(ownerless) {
				trimmed = deletePath(trimmed, p)
			}
			body = trimmed.(map[string]any)
		}
		md, _ := body["metadata"].(map[string]any)
		if md == nil {
			md = map[string]any{}
			body["metadata"] = md
		}
		md["resourceVersion"] = o.rv
		if o.key.Namespace != "" {
			md["namespace"] = o.key.Namespace
		}
		items = append(items, body)
	}
	if len(items) == 0 {
		return answers
	}
	list, err := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "List", "items": items})
	if err != nil {
		return answers
	}
	stdout, stderr, rerr := run(ctx, []string{"replace", "--dry-run=server", "-o", "json", "-f", "-"}, list)
	if rerr != nil && out != nil {
		fmt.Fprintf(out, "[deploy]   field ownership: the server refused a dry-run replace, so the ownerless fields of the objects it names stay as they are: %s\n",
			strings.TrimSpace(string(stderr)))
	}
	results, err := decodeObjects(stdout)
	if err != nil {
		return answers
	}
	for _, r := range results {
		apiVersion, _ := r["apiVersion"].(string)
		kind, _ := r["kind"].(string)
		md, _ := r["metadata"].(map[string]any)
		name, _ := md["name"].(string)
		ns, _ := md["namespace"].(string)
		answers[keyOf(apiVersion, kind, ns, name)] = r
	}
	return answers
}

// jsonCopy deep-copies a decoded JSON value, so trimming never touches the
// live object it was read from.
func jsonCopy(v map[string]any) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil {
		return v
	}
	return out
}

// benignGetError reports whether a `kubectl get -f -` error line is one that
// only means "this object does not exist": its kind is not served (yet — a CRD
// in the same stream registers it), or the object is absent.
func benignGetError(line string) bool {
	return strings.Contains(line, "no matches for kind") ||
		strings.Contains(line, "the server doesn't have a resource type") ||
		strings.Contains(line, "could not find the requested resource") ||
		strings.Contains(line, "(NotFound)")
}

// readLive reads the stream's objects as they are on the cluster, with their
// managedFields. Objects that do not exist are simply absent.
func readLive(ctx context.Context, run kubectlRun, namespace string, docs []string) ([]liveObject, error) {
	args := []string{"get", "-f", "-", "-o", "json", "--ignore-not-found", "--show-managed-fields"}
	if strings.TrimSpace(namespace) != "" {
		args = append(args, "-n", namespace)
	}
	stdout, stderr, err := run(ctx, args, []byte(strings.Join(docs, docDelimiter)))
	if err != nil {
		for _, line := range strings.Split(strings.TrimSpace(string(stderr)), "\n") {
			if line = strings.TrimSpace(line); line != "" && !benignGetError(line) {
				return nil, fmt.Errorf("kubectl get: %w: %s", err, strings.TrimSpace(string(stderr)))
			}
		}
	}
	raws, derr := decodeObjects(stdout)
	if derr != nil {
		return nil, fmt.Errorf("kubectl get: decode: %w", derr)
	}
	objs := make([]liveObject, 0, len(raws))
	for _, r := range raws {
		o, err := decodeLiveObject(r)
		if err != nil {
			return nil, err
		}
		objs = append(objs, o)
	}
	return objs, nil
}

// patchClaim writes a claim's managedFields, guarded by the resourceVersion it
// was planned against so it never overwrites a write it did not see.
func patchClaim(ctx context.Context, run kubectlRun, c *ownershipClaim) (conflict bool, err error) {
	ops := []map[string]any{
		{"op": "test", "path": "/metadata/resourceVersion", "value": c.rv},
		// `add`, not `replace`: an object whose history was cleared has no
		// metadata.managedFields member at all, and a JSON-patch `replace`
		// of a missing member is refused. `add` sets an object member
		// whether or not it exists.
		{"op": "add", "path": "/metadata/managedFields", "value": c.entries},
	}
	patch, err := json.Marshal(ops)
	if err != nil {
		return false, err
	}
	md := map[string]any{"name": c.name}
	if c.namespace != "" {
		md["namespace"] = c.namespace
	}
	ident, _ := json.Marshal(map[string]any{"apiVersion": c.version, "kind": c.kind, "metadata": md})
	_, stderr, err := run(ctx, []string{"patch", "-f", "-", "--type=json", "-p", string(patch)}, ident)
	if err != nil {
		msg := strings.TrimSpace(string(stderr))
		// A failed JSON-patch `test` is how a lost race surfaces: the API
		// server refuses the whole patch with this message.
		if strings.Contains(msg, "the server rejected our request due to an error in our request") || strings.Contains(msg, "(Conflict)") {
			return true, fmt.Errorf("%s: %s", c.key, msg)
		}
		return false, fmt.Errorf("claim the fields of %s: %w: %s", c.key, err, msg)
	}
	return false, nil
}

// convergeFieldOwnership makes forge the owner of every field on the stream's
// live objects that drifted from the declaration (see the file comment), so the
// apply that follows removes what forge does not declare. out receives one line
// per object whose ownership changed.
func convergeFieldOwnership(ctx context.Context, run kubectlRun, namespace, manifests string, out io.Writer) error {
	var docs []string
	declared := map[objectKey]map[string]any{}
	for _, doc := range splitDocs(manifests) {
		m, ok := parseDoc(doc)
		if !ok || m.Kind == "" || m.Metadata.Name == "" {
			continue
		}
		if m.Metadata.Annotations[fieldOwnershipAnnotation] == fieldOwnershipShared {
			continue
		}
		docs = append(docs, doc)
		if body, err := docJSON(doc); err == nil {
			declared[keyOf(m.APIVersion, m.Kind, m.Metadata.Namespace, m.Metadata.Name)] = body
		}
	}
	if len(docs) == 0 {
		return nil
	}
	live, err := readLive(ctx, run, namespace, docs)
	if err != nil {
		return fmt.Errorf("reading the live objects to converge their field ownership: %w", err)
	}
	if len(live) == 0 {
		return nil
	}
	// A declaration that names no namespace takes the apply's; key it by the
	// live object it resolved to.
	byLive := map[objectKey]map[string]any{}
	for _, o := range live {
		bare := o.key
		bare.Namespace = ""
		if d, ok := declared[o.key]; ok {
			byLive[o.key] = d
		} else if d, ok := declared[bare]; ok {
			byLive[o.key] = d
		}
	}
	j := ownerlessJudge{run: run, declared: byLive, out: out}
	probes, trimmed := j.judge(ctx, live)

	var errs []error
	for _, o := range live {
		if err := claimOne(ctx, run, j, o, probes[o.key], trimmed[o.key], out); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// docJSON decodes one YAML manifest into an unstructured object.
func docJSON(doc string) (map[string]any, error) {
	b, err := sigsyaml.YAMLToJSON([]byte(doc))
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}

// ownerlessJudge reads what the server says about objects' ownerless fields:
// each object's full field set (probeSets) and what it makes of the object
// without them (trimmedDefaults). Kinds in noOrphanCheckKinds are not asked.
type ownerlessJudge struct {
	run      kubectlRun
	declared map[objectKey]map[string]any
	out      io.Writer
}

func (j ownerlessJudge) judge(ctx context.Context, objs []liveObject) (map[objectKey]*fieldpath.Set, map[objectKey]map[string]any) {
	var check []liveObject
	for _, o := range objs {
		if !noOrphanCheckKinds[o.key.Kind] {
			check = append(check, o)
		}
	}
	probes := probeSets(ctx, j.run, check)
	trimmed := trimmedDefaults(ctx, j.run, check, probes, j.declared, j.out)
	for key, t := range trimmed {
		putBackStringData(t, key, j.declared[key])
	}
	return probes, trimmed
}

// putBackStringData marks, in a Secret's trimmed answer, the data keys its
// declaration writes through stringData. The server folds stringData into data
// and records the applier as owning only stringData, so every such data key is
// ownerless on the live object; and stringData is never stored, so removing
// the key in the dry-run replace sticks. Judged by the server alone, each one
// is an orphan, and forge claimed it on every apply, which rewrote an unchanged
// Secret on every deploy. The declaration puts the key back, so it is not
// drift: the key goes back with the value the server stores for it, the
// declared string base64-encoded, so the answer is the object forge's apply
// leaves (planClaim reads only presence, but the answer stays true to it).
func putBackStringData(trimmed map[string]any, key objectKey, declared map[string]any) {
	if key.Kind != "Secret" || trimmed == nil || declared == nil {
		return
	}
	sd, _ := declared["stringData"].(map[string]any)
	if len(sd) == 0 {
		return
	}
	data, _ := trimmed["data"].(map[string]any)
	if data == nil {
		data = map[string]any{}
		trimmed["data"] = data
	}
	for k, v := range sd {
		if _, ok := data[k]; !ok {
			s, isString := v.(string)
			if !isString {
				s = fmt.Sprint(v)
			}
			data[k] = base64.StdEncoding.EncodeToString([]byte(s))
		}
	}
}

// claimOne plans and writes one object's claim. When another writer got in
// between, it re-reads the object and re-judges its ownerless fields on that
// revision: a claim never mixes one revision's ownership with another's
// defaulting.
func claimOne(ctx context.Context, run kubectlRun, j ownerlessJudge, o liveObject, probe *fieldpath.Set, trimmed map[string]any, out io.Writer) error {
	for attempt := 1; ; attempt++ {
		c, err := planClaim(o, probe, trimmed, time.Now())
		if err != nil || c == nil {
			return err
		}
		conflict, err := patchClaim(ctx, run, c)
		if err == nil {
			reportClaim(out, c)
			return nil
		}
		if !conflict || attempt >= ownershipClaimAttempts {
			return err
		}
		md := map[string]any{"name": c.name}
		if c.namespace != "" {
			md["namespace"] = c.namespace
		}
		ident, _ := json.Marshal(map[string]any{"apiVersion": c.version, "kind": c.kind, "metadata": md})
		fresh, rerr := readLive(ctx, run, "", []string{string(ident)})
		if rerr != nil {
			return rerr
		}
		if len(fresh) != 1 {
			return nil // deleted meanwhile: nothing left to claim
		}
		o = fresh[0]
		probes, answers := j.judge(ctx, fresh)
		probe, trimmed = probes[o.key], answers[o.key]
	}
}

func reportClaim(out io.Writer, c *ownershipClaim) {
	if out == nil {
		return
	}
	var from []string
	from = append(from, c.takenFrom...)
	if c.ownerless > 0 {
		from = append(from, "no owner")
	}
	if len(c.reportable) == 0 {
		// Only empty entries of imperative managers were dropped.
		return
	}
	fmt.Fprintf(out, "[deploy]   %s: forge takes back %d field(s) set outside it (%s); this apply resets them to the declared spec and removes those it does not declare:\n",
		c.key, len(c.reportable), strings.Join(from, ", "))
	for i, p := range c.reportable {
		if i == maxReportedPaths {
			fmt.Fprintf(out, "[deploy]       … and %d more\n", len(c.reportable)-maxReportedPaths)
			break
		}
		fmt.Fprintf(out, "[deploy]       %s\n", p.String())
	}
}

// convergeBeforeApply is the apply path's call: the real kubectl for kctx, the
// report on stdout beside kubectl's own lines.
func convergeBeforeApply(ctx context.Context, kctx, namespace, manifests string) error {
	return convergeFieldOwnership(ctx, contextKubectl(kctx), namespace, manifests, os.Stdout)
}
