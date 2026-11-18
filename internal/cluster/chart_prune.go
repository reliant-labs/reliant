package cluster

// Chart pruning uses a durable inventory and a non-expiring, cluster-side
// mutex. The mutex spans inventory read -> apply -> inventory commit -> prune.
// A second applier fails closed, never steals a lock based on wall-clock time.
// After a crash, an operator must terminate the old writer (including kubectl
// children) before removing its lock; see deploy/flux. A suspended writer must
// not be allowed to resume after manual recovery.
//
// Both records live in kube-system, so changing a chart's target namespace
// cannot fork its lock or lose its prior inventory. They are keyed by the
// entire owner + chart, not an app-label selector. The inventory records UIDs
// from successful server-side-apply RESPONSES, not a later GET of a name that
// another actor could have replaced. A failed apply leaves the inventory
// unchanged and prunes nothing. Partial writes are conservatively untracked
// until a successful retry; this prefers retention over deleting a foreign UID.
//
// Before deletion we GET the inventoried object, check its UID and ownership,
// apply the shared stateful and Flux prune-exemption guards, then DELETE with
// BOTH UID and resourceVersion preconditions. A replacement or an intervening
// annotation/ownership change cannot be deleted with the stale decision.
// Inventory commit precedes deletion and retains failed/guarded candidates, so
// a crash or failed DELETE can be retried without forgetting the old objects.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// Chart ownership and inventory keys. PruneAnnotation/PruneDisabled use the
// Flux spelling so the direct and reconciler delivery paths share one guard.
const (
	ChartOwnerLabel          = "forge.dev/chart-owner"
	PruneAnnotation          = "kustomize.toolkit.fluxcd.io/prune"
	PruneDisabled            = "disabled"
	ChartInventoryLabel      = "forge.dev/chart-inventory"
	ChartInventoryOwnerLabel = "forge.dev/chart-inventory-owner"
	chartInventoryDataKey    = "inventory"
	chartStateNamespace      = "kube-system"
)

// ChartOwner names the project/env that owns the chart, or disables ownership
// when either component is unknown, using the same spelling as CRDOwner.
func ChartOwner(project, env string) string { return CRDOwner(project, env) }

type chartObjectRef struct{ Kind, Namespace, Name string }

func (r chartObjectRef) String() string {
	if r.Namespace == "" {
		return r.Kind + "/" + r.Name
	}
	return r.Kind + "/" + r.Namespace + "/" + r.Name
}
func apiGroup(version string) string {
	if g, _, ok := strings.Cut(version, "/"); ok {
		return g
	}
	return ""
}
func kubectlResource(kind, group string) string {
	if group != "" {
		return strings.ToLower(kind) + "." + group
	}
	return strings.ToLower(kind)
}

type inventoryEntry struct {
	Group     string `json:"group,omitempty"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

func (e inventoryEntry) ref() chartObjectRef { return chartObjectRef{e.Kind, e.Namespace, e.Name} }
func (e inventoryEntry) resource() string    { return kubectlResource(e.Kind, e.Group) }
func (e inventoryEntry) String() string      { return e.ref().String() }

// Values are Kubernetes UIDs, never apply/run ids.
type chartInventory map[inventoryEntry]string
type inventoryRecord struct {
	inventoryEntry
	UID string `json:"uid"`
}

func (inv chartInventory) encode() (string, error) {
	records := make([]inventoryRecord, 0, len(inv))
	for e, uid := range inv {
		records = append(records, inventoryRecord{e, uid})
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].String() != records[j].String() {
			return records[i].String() < records[j].String()
		}
		return records[i].Group < records[j].Group
	})
	b, err := json.Marshal(records)
	return string(b), err
}
func decodeChartInventory(data string) (chartInventory, error) {
	var records []inventoryRecord
	if err := json.Unmarshal([]byte(data), &records); err != nil {
		return nil, err
	}
	if records == nil {
		return nil, errors.New("inventory must be a JSON array")
	}
	inv := chartInventory{}
	for _, r := range records {
		if r.Kind == "" || r.Name == "" || r.UID == "" {
			return nil, errors.New("inventory entry needs kind, name and UID")
		}
		if _, ok := inv[r.inventoryEntry]; ok {
			return nil, errors.New("duplicate inventory entry")
		}
		inv[r.inventoryEntry] = r.UID
	}
	return inv, nil
}
func chartInventoryName(chart, owner string) string {
	sum := sha256.Sum256([]byte(owner + "/" + chart))
	return "forge-chart-inventory-" + hex.EncodeToString(sum[:])[:32]
}
func chartLockName(spec HelmChartSpec) string {
	return strings.Replace(chartInventoryName(spec.Name, spec.Owner), "inventory", "lock", 1)
}
func newChartApplyID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

type liveChartObject struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name              string            `json:"name"`
		Namespace         string            `json:"namespace"`
		UID               string            `json:"uid"`
		ResourceVersion   string            `json:"resourceVersion"`
		Labels            map[string]string `json:"labels"`
		Annotations       map[string]string `json:"annotations"`
		OwnerReferences   []json.RawMessage `json:"ownerReferences"`
		DeletionTimestamp string            `json:"deletionTimestamp"`
	} `json:"metadata"`
	raw map[string]any
}

func (o *liveChartObject) UnmarshalJSON(b []byte) error {
	type plain liveChartObject
	if err := json.Unmarshal(b, (*plain)(o)); err != nil {
		return err
	}
	return json.Unmarshal(b, &o.raw)
}
func (o liveChartObject) ref() chartObjectRef {
	return chartObjectRef{o.Kind, o.Metadata.Namespace, o.Metadata.Name}
}
func (o liveChartObject) resource() string { return kubectlResource(o.Kind, apiGroup(o.APIVersion)) }
func (o liveChartObject) entry() inventoryEntry {
	return inventoryEntry{apiGroup(o.APIVersion), o.Kind, o.Metadata.Namespace, o.Metadata.Name}
}

type chartDesiredSet struct {
	namespaced   map[chartObjectRef]bool
	anyNamespace map[chartObjectRef]bool
	entries      []inventoryEntry
}

func desiredChartObjects(stream, namespace string) (s chartDesiredSet, ok bool) {
	s = chartDesiredSet{namespaced: map[chartObjectRef]bool{}, anyNamespace: map[chartObjectRef]bool{}}
	for _, doc := range splitDocs(stream) {
		m, parsed := parseDoc(doc)
		if !parsed {
			return s, false
		}
		if m.Kind == "" || m.Metadata.Name == "" {
			continue
		}
		ns := m.Metadata.Namespace
		if ns == "" {
			ns = namespace
		}
		ref := chartObjectRef{m.Kind, ns, m.Metadata.Name}
		if !s.namespaced[ref] {
			s.entries = append(s.entries, inventoryEntry{apiGroup(m.APIVersion), m.Kind, ns, m.Metadata.Name})
		}
		s.namespaced[ref] = true
		s.anyNamespace[chartObjectRef{m.Kind, "", m.Metadata.Name}] = true
	}
	return s, len(s.entries) > 0
}
func (s chartDesiredSet) contains(ref chartObjectRef) bool {
	if ref.Namespace == "" {
		return s.anyNamespace[ref]
	}
	return s.namespaced[ref]
}

type chartPruneVerdict int

const (
	pruneDelete chartPruneVerdict = iota
	pruneKeep
	pruneForget
)

func decideChartPrune(o liveChartObject, desired chartDesiredSet, spec HelmChartSpec) (chartPruneVerdict, string) {
	switch {
	case desired.contains(o.ref()):
		return pruneForget, ""
	case o.Metadata.Labels[ChartOwnerLabel] != spec.Owner || o.Metadata.Labels[WorkloadLabel] != spec.Name:
		return pruneForget, "ownership stamp changed"
	case release.StatefulKind(o.Kind):
		return pruneKeep, "stateful kind; delete by hand only"
	case o.Metadata.Annotations[PruneAnnotation] == PruneDisabled:
		return pruneKeep, "prune: disabled"
	case len(o.Metadata.OwnerReferences) > 0:
		return pruneKeep, "owned by a controller"
	case o.Metadata.DeletionTimestamp != "":
		return pruneForget, ""
	default:
		return pruneDelete, ""
	}
}

func runChartJSON(ctx context.Context, kctx string, args []string, body any) ([]byte, error) {
	cmd := kubectlCmd(ctx, kctx, args...)
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		cmd.Stdin = bytes.NewReader(b)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("kubectl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}
func getChartObject(ctx context.Context, kctx string, e inventoryEntry) (*liveChartObject, error) {
	args := []string{"get", e.resource(), e.Name, "-o", "json", "--ignore-not-found"}
	if e.Namespace != "" {
		args = append(args, "-n", e.Namespace)
	}
	b, err := runChartJSON(ctx, kctx, args, nil)
	if err != nil {
		if kindNotServed(err.Error()) {
			return nil, nil
		}
		return nil, err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, nil
	}
	var o liveChartObject
	if err := json.Unmarshal(b, &o); err != nil {
		return nil, err
	}
	if o.Kind == "" || o.Metadata.Name == "" {
		return nil, errors.New("read returned no object identity")
	}
	return &o, nil
}
func kindNotServed(s string) bool {
	return strings.Contains(s, "the server doesn't have a resource type")
}

// DELETE --raw -f - sends the DeleteOptions body verbatim through kubectl's
// kubeconfig transport. Unlike ordinary kubectl delete, it DOES enforce both
// preconditions. Discovery resolves the plural (never guess kind + "s").
func deleteChartObject(ctx context.Context, kctx string, o liveChartObject) error {
	if o.Metadata.UID == "" || o.Metadata.ResourceVersion == "" {
		return errors.New("refusing delete without UID and resourceVersion")
	}
	prefix := "/api/" + o.APIVersion
	if apiGroup(o.APIVersion) != "" {
		prefix = "/apis/" + o.APIVersion
	}
	b, err := runChartJSON(ctx, kctx, []string{"get", "--raw", prefix}, nil)
	if err != nil {
		return err
	}
	var discovery struct {
		Resources []struct {
			Name, Kind string
			Namespaced bool
		} `json:"resources"`
	}
	if err := json.Unmarshal(b, &discovery); err != nil {
		return err
	}
	resource := ""
	for _, r := range discovery.Resources {
		if r.Kind == o.Kind && !strings.Contains(r.Name, "/") && r.Namespaced == (o.Metadata.Namespace != "") {
			resource = r.Name
			break
		}
	}
	if resource == "" {
		return fmt.Errorf("no API resource for %s %s", o.APIVersion, o.Kind)
	}
	path := prefix
	if o.Metadata.Namespace != "" {
		path += "/namespaces/" + url.PathEscape(o.Metadata.Namespace)
	}
	path += "/" + resource + "/" + url.PathEscape(o.Metadata.Name)
	_, err = runChartJSON(ctx, kctx, []string{"delete", "--raw", path, "-f", "-"}, map[string]any{
		"apiVersion": "v1", "kind": "DeleteOptions", "preconditions": map[string]string{"uid": o.Metadata.UID, "resourceVersion": o.Metadata.ResourceVersion},
	})
	return err
}

type chartPrune struct {
	kctx      string
	rc        renderedChart
	applyID   string
	lock      *liveChartObject
	inventory *liveChartObject
	prior     chartInventory
	applied   chartInventory
}
type chartPruneContextKey struct{}

func chartStateEntry(name string) inventoryEntry {
	return inventoryEntry{Kind: "ConfigMap", Namespace: chartStateNamespace, Name: name}
}
func stateManifest(spec HelmChartSpec, name string, data map[string]string) map[string]any {
	return map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{
		"name": name, "namespace": chartStateNamespace, "labels": map[string]string{ChartInventoryLabel: spec.Name, ChartInventoryOwnerLabel: spec.Owner},
	}, "data": data}
}

// beginChartPrune must succeed before ANY chart write, including Namespace
// creation. kube-system is the stable coordination namespace across chart moves.
func beginChartPrune(ctx context.Context, kctx string, rc renderedChart, id string) (*chartPrune, error) {
	if rc.spec.Owner == "" {
		return nil, nil
	}
	if strings.TrimSpace(kctx) == "" {
		return nil, errors.New("chart apply requires an explicit context")
	}
	p := &chartPrune{kctx: kctx, rc: rc, applyID: id, prior: chartInventory{}, applied: chartInventory{}}
	name := chartLockName(rc.spec)
	body := stateManifest(rc.spec, name, map[string]string{"holder": id, "started": time.Now().UTC().Format(time.RFC3339)})
	body["metadata"].(map[string]any)["labels"] = map[string]string{"forge.dev/chart-lock": rc.spec.Name, ChartInventoryOwnerLabel: rc.spec.Owner}
	b, err := runChartJSON(ctx, kctx, []string{"create", "-f", "-", "-o", "json"}, body)
	if err != nil {
		return nil, fmt.Errorf("chart %q lock %s/%s: %w; no timeout takeover is allowed; after a crash terminate the old forge process AND its kubectl children before explicitly removing the lock (see deploy/flux)", rc.spec.Name, chartStateNamespace, name, err)
	}
	var lock liveChartObject
	if err := json.Unmarshal(b, &lock); err != nil {
		return nil, fmt.Errorf("read created chart lock (leave it for explicit recovery): %w", err)
	}
	p.lock = &lock
	if lock.Metadata.UID == "" || lock.Metadata.ResourceVersion == "" {
		return nil, errors.New("created chart lock has no UID/resourceVersion; explicit recovery required")
	}
	inv, err := getChartObject(ctx, kctx, chartStateEntry(chartInventoryName(rc.spec.Name, rc.spec.Owner)))
	if err != nil {
		return p, err
	}
	p.inventory = inv
	if inv != nil {
		data, _ := inv.raw["data"].(map[string]any)
		text, _ := data[chartInventoryDataKey].(string)
		p.prior, err = decodeChartInventory(text)
		if err != nil {
			return p, fmt.Errorf("unreadable chart inventory; refusing apply: %w", err)
		}
	}
	return p, nil
}
func (p *chartPrune) checkLock(ctx context.Context) error {
	if p == nil {
		return nil
	}
	o, err := getChartObject(ctx, p.kctx, chartStateEntry(chartLockName(p.rc.spec)))
	if err != nil {
		return err
	}
	if o == nil || o.Metadata.UID != p.lock.Metadata.UID || o.Metadata.ResourceVersion != p.lock.Metadata.ResourceVersion {
		return errors.New("chart lock lost or modified; refusing further writes; stop this writer before recovery")
	}
	return nil
}
func (p *chartPrune) release(ctx context.Context) error {
	if p == nil {
		return nil
	}
	if err := p.checkLock(ctx); err != nil {
		return err
	}
	return deleteChartObject(ctx, p.kctx, *p.lock)
}

// RecoverChartLock removes exactly the lock an operator inspected. Confirmation
// must name its holder after the operator has terminated/fenced that writer and
// its children on every host. It never checks an age or automatically recovers.
// UID and resourceVersion prevent this approval being reused for a newer lock.
func RecoverChartLock(ctx context.Context, kctx, owner, chart, uid, rv, confirmation string) error {
	if kctx == "" || owner == "" || chart == "" || uid == "" || rv == "" || !strings.HasPrefix(confirmation, "terminated:") {
		return errors.New("recovery requires explicit context, owner, chart, inspected lock UID/resourceVersion and --confirm-terminated=terminated:<holder>")
	}
	spec := HelmChartSpec{Name: chart, Owner: owner}
	o, err := getChartObject(ctx, kctx, chartStateEntry(chartLockName(spec)))
	if err != nil {
		return err
	}
	if o == nil {
		return errors.New("lock no longer exists; nothing recovered")
	}
	data, _ := o.raw["data"].(map[string]any)
	holder, _ := data["holder"].(string)
	if holder == "" || confirmation != "terminated:"+holder || o.Metadata.UID != uid || o.Metadata.ResourceVersion != rv || o.Metadata.Labels["forge.dev/chart-lock"] != chart || o.Metadata.Labels[ChartInventoryOwnerLabel] != owner {
		return errors.New("lock identity/holder changed or confirmation does not match; inspect again, never clear a live writer's lock")
	}
	return deleteChartObject(ctx, kctx, *o)
}

// collectApply reads only the SSA response, including its actual UID. Returning
// status lines preserves the existing completeness/recovery checks. Secret
// payloads are NOT printed or persisted in the inventory.
func (p *chartPrune) collectApply(data []byte) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	var out strings.Builder
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return "", err
		}
		var list struct {
			Kind  string            `json:"kind"`
			Items []json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(raw, &list); err != nil {
			return "", err
		}
		objects := []json.RawMessage{raw}
		if list.Kind == "List" {
			objects = list.Items
		}
		for _, b := range objects {
			var o liveChartObject
			if err := json.Unmarshal(b, &o); err != nil {
				return "", err
			}
			if o.Kind == "" || o.Metadata.Name == "" || o.Metadata.UID == "" {
				return "", errors.New("apply response needs kind, name and UID")
			}
			p.applied[o.entry()] = o.Metadata.UID
			fmt.Fprintf(&out, "%s/%s serverside-applied\n", o.resource(), o.Metadata.Name)
		}
	}
	return out.String(), nil
}
func (p *chartPrune) writeInventory(ctx context.Context, inv chartInventory) error {
	if err := p.checkLock(ctx); err != nil {
		return err
	}
	data, err := inv.encode()
	if err != nil {
		return err
	}
	body := stateManifest(p.rc.spec, chartInventoryName(p.rc.spec.Name, p.rc.spec.Owner), map[string]string{chartInventoryDataKey: data})
	verb := "create"
	if p.inventory != nil {
		verb = "replace"
		body["metadata"].(map[string]any)["resourceVersion"] = p.inventory.Metadata.ResourceVersion
	}
	b, err := runChartJSON(ctx, p.kctx, []string{verb, "-f", "-", "-o", "json"}, body)
	if err != nil {
		return err
	}
	var o liveChartObject
	if err := json.Unmarshal(b, &o); err != nil {
		return err
	}
	p.inventory = &o
	return nil
}
func (p *chartPrune) afterApply(ctx context.Context) ([]string, error) {
	if p == nil {
		return nil, nil
	}
	if err := p.checkLock(ctx); err != nil {
		return nil, err
	}
	desired, ok := desiredChartObjects(joinNonEmpty(p.rc.manifests, p.rc.extra), p.rc.spec.Namespace)
	if !ok {
		return nil, nil
	}
	// Commit confirmed UIDs before deleting. Retain absent old entries until
	// their delete succeeds, so crashes/failures never make grants untrackable.
	next := chartInventory{}
	for e, uid := range p.prior {
		next[e] = uid
	}
	for e, uid := range p.applied {
		next[e] = uid
	}
	if err := p.writeInventory(ctx, next); err != nil {
		return nil, err
	}
	if !p.rc.spec.Prune {
		return nil, nil
	}
	var pruned []string
	var errs []error
	for e, uid := range p.prior {
		if desired.contains(e.ref()) || release.StatefulKind(e.Kind) {
			continue
		}
		o, err := getChartObject(ctx, p.kctx, e)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if o == nil || o.Metadata.UID != uid {
			delete(next, e)
			continue
		}
		verdict, _ := decideChartPrune(*o, desired, p.rc.spec)
		if verdict == pruneKeep {
			continue
		}
		if verdict == pruneForget {
			delete(next, e)
			continue
		}
		if err := p.checkLock(ctx); err != nil {
			return pruned, err
		}
		if err := deleteChartObject(ctx, p.kctx, *o); err != nil {
			errs = append(errs, err)
			continue
		}
		delete(next, e)
		pruned = append(pruned, o.ref().String())
	}
	if err := p.writeInventory(ctx, next); err != nil {
		errs = append(errs, err)
	}
	sort.Strings(pruned)
	return pruned, errors.Join(errs...)
}

type chartPruneKeep struct {
	Object chartObjectRef
	Why    string
}

// Plan and apply use the same inventory membership, UID and policy guards.
// A preview never acquires a lock or changes cluster state.
func chartPruneCandidates(ctx context.Context, kctx string, rc renderedChart) (del []liveChartObject, kept []chartPruneKeep, readable bool, err error) {
	desired, ok := desiredChartObjects(joinNonEmpty(rc.manifests, rc.extra), rc.spec.Namespace)
	if !ok {
		return nil, nil, false, nil
	}
	inv, err := getChartObject(ctx, kctx, chartStateEntry(chartInventoryName(rc.spec.Name, rc.spec.Owner)))
	if err != nil {
		return nil, nil, true, err
	}
	if inv == nil {
		return nil, nil, true, nil
	}
	data, _ := inv.raw["data"].(map[string]any)
	text, _ := data[chartInventoryDataKey].(string)
	entries, err := decodeChartInventory(text)
	if err != nil {
		return nil, nil, true, err
	}
	for e, uid := range entries {
		if desired.contains(e.ref()) {
			continue
		}
		if release.StatefulKind(e.Kind) {
			kept = append(kept, chartPruneKeep{e.ref(), "stateful kind"})
			continue
		}
		o, err := getChartObject(ctx, kctx, e)
		if err != nil {
			return nil, nil, true, err
		}
		if o == nil || o.Metadata.UID != uid {
			continue
		}
		verdict, why := decideChartPrune(*o, desired, rc.spec)
		if verdict == pruneDelete {
			del = append(del, *o)
		} else if verdict == pruneKeep {
			kept = append(kept, chartPruneKeep{o.ref(), why})
		}
	}
	sort.Slice(del, func(i, j int) bool { return del[i].ref().String() < del[j].ref().String() })
	return del, kept, true, nil
}
