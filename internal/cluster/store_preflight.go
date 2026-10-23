package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/pkg/deploy/openbao"
)

// store_preflight.go is the live half of SF9: what must hold on a cluster
// BEFORE a Bundle with store-backed Secrets (forge.StoreSource) is applied.
//
// A store-backed Secret carries no value and forge applies no Secret for it:
// the External Secrets Operator (ESO) fetches it from a store and writes it
// into the namespace. So the existence check an out-of-band Secret gets does
// not fit an Orphan one (it is created AFTER the deploy; see
// ProvisionedByDeploy), and these questions take its place:
//
//  1. Is the CRD surface there, at a version this Bundle can use? The kinds
//     ExternalSecret and SecretStore must be served at external-secrets.io/v1,
//     which is what the render emits. The operator may be this Bundle's, another
//     env's, or a hand-run chart.
//  2. Who owns the operator? One per cluster; a second owner fights over the
//     cluster-scoped CRDs. cluster.Apply refuses this at apply time
//     (checkComponentOwner); here it is refused before ANY write.
//  3. Is the store ready? An OpenBao this Bundle runs must have finished
//     converging, or no SecretStore can log in and ESO syncs nothing.
//  4. Does the store HOLD the names the ExternalSecrets read? The preflight
//     used to verify that a key exists, not that it resolves (the 2026-09-02
//     incident): the Secret was present, the value behind it was not. This asks
//     the store, value-free (names and versions only).
//
// A creationPolicy Merge Secret only updates one that already exists, so it
// stays in RequiredSecrets and gets the ordinary existence check. Nothing here
// repeats it.
//
// Every finding names the field to change or the command to run.

// StoreExpectation is what the Bundle declares about ONE cluster that holds
// store-backed Secrets, projected from the render by the CLI (this package
// never sees the cli entity types).
type StoreExpectation struct {
	// Context is the kubectl context the objects land on.
	Context string
	// Owner is the component-owner stamp of this Bundle (CRDOwner(project, env)).
	Owner string
	// InstallsESO is true when this deploy installs the External Secrets
	// Operator on Context (a forge.ExternalSecretsOperator for the cluster that
	// the deploy's --target selects). The CRDs are then applied before anything
	// that uses them, so their absence is not a gap, and a different live owner
	// is a conflict.
	InstallsESO bool
	// OpenBao, when non-nil, is the forge.OpenBao this Bundle runs on Context
	// and that a store-backed Secret reads from.
	OpenBao *OpenBaoExpectation
	// Reads are the stores the Secrets on Context read from, by kind.
	Reads []StoreRead
}

// OpenBaoExpectation is the in-cluster store a Bundle runs.
type OpenBaoExpectation struct {
	Name      string
	Namespace string
	// Installs is true when this deploy applies the component, so its converge
	// Job is about to be created and has not run yet.
	Installs bool
}

// StoreRead is the set of names the ExternalSecrets on a cluster read from one
// store.
type StoreRead struct {
	// Kind is "hosted", "openbao" or "gcpsm"; Project names the GSM project.
	Kind    string
	Project string
	// Names are the store names that must hold a value: every name the
	// ExternalSecrets read, less the ones the source generates itself.
	Names []string
	// Pending is true when the store is being installed by this deploy, so it
	// holds nothing yet and its names cannot be checked.
	Pending bool
}

// Label is the store as a human reads it.
func (r StoreRead) Label() string {
	if r.Project != "" {
		return r.Kind + ":" + r.Project
	}
	return r.Kind
}

// CRDState is what a live CustomResourceDefinition looks like.
type CRDState struct {
	Present bool
	// Owner is the forge.dev/component-owner label ("" when forge did not
	// install it).
	Owner string
	// Versions are the versions the CRD SERVES.
	Versions []string
}

// StoreFindings are the gaps a store preflight found. Each entry is a full
// sentence naming what to do. Empty (see Empty) means nothing blocks.
type StoreFindings struct {
	// MissingCRDs: kinds the cluster does not serve and nothing in this deploy
	// installs.
	MissingCRDs []string
	// IncompatibleCRDs: the operator is there but does not serve the version
	// the render emits.
	IncompatibleCRDs []string
	// OwnerConflicts: another env owns the cluster's External Secrets Operator.
	OwnerConflicts []string
	// StoreNotReady: the store this Bundle runs has not finished converging.
	StoreNotReady []string
	// MissingStoreNames: names the ExternalSecrets read that the store does not
	// hold.
	MissingStoreNames []string
	// Warnings are advisory: a store that could not be asked. They do not block.
	Warnings []string
}

// Empty reports whether nothing BLOCKS.
func (f StoreFindings) Empty() bool {
	return len(f.MissingCRDs)+len(f.IncompatibleCRDs)+len(f.OwnerConflicts)+len(f.StoreNotReady)+len(f.MissingStoreNames) == 0
}

// StoreProbe is the reads a store preflight makes. A fake in tests; the live
// one is KubectlStoreProbe plus whatever answers Presence.
type StoreProbe interface {
	// CRD reads one CustomResourceDefinition on kctx.
	CRD(ctx context.Context, kctx, name string) (CRDState, error)
	// ConvergeSucceeded reports whether the NEWEST OpenBao converge Job of the
	// component has completed. found=false when none exists.
	ConvergeSucceeded(ctx context.Context, kctx, namespace, name string) (succeeded, found bool, err error)
	// Presence returns the names of read.Names the store holds a usable value
	// for. It never sees or returns a value.
	Presence(ctx context.Context, kctx string, read StoreRead) (present map[string]bool, err error)
}

const (
	// esoAPIGroup is the API group of the kinds a store-backed Secret renders.
	esoAPIGroup       = "external-secrets.io"
	esoSecretStoreCRD = "secretstores.external-secrets.io"
	// esoRenderedVersion is the apiVersion the render emits for ExternalSecret
	// and SecretStore (kcl/lib/external_secret.k).
	esoRenderedVersion = "v1"
)

// KubectlStoreProbe reads the cluster with kubectl. Presence is not a cluster
// read, so it is injected: the CLI supplies the one that talks to the stores.
type KubectlStoreProbe struct {
	// PresenceFn answers Presence. Nil answers every name as present, so a probe
	// built without a store client checks nothing it cannot check.
	PresenceFn func(ctx context.Context, kctx string, read StoreRead) (map[string]bool, error)
}

// CRD implements StoreProbe.
func (KubectlStoreProbe) CRD(ctx context.Context, kctx, name string) (CRDState, error) {
	o, err := getChartObject(ctx, kctx, inventoryEntry{Group: "apiextensions.k8s.io", Kind: "CustomResourceDefinition", Name: name})
	if err != nil {
		return CRDState{}, err
	}
	if o == nil {
		return CRDState{}, nil
	}
	st := CRDState{Present: true, Owner: o.Metadata.Labels[componentOwnerLabel]}
	spec, _ := o.raw["spec"].(map[string]any)
	versions, _ := spec["versions"].([]any)
	for _, v := range versions {
		m, _ := v.(map[string]any)
		if served, _ := m["served"].(bool); served {
			if n, _ := m["name"].(string); n != "" {
				st.Versions = append(st.Versions, n)
			}
		}
	}
	return st, nil
}

// ConvergeSucceeded implements StoreProbe.
func (KubectlStoreProbe) ConvergeSucceeded(ctx context.Context, kctx, namespace, name string) (bool, bool, error) {
	b, err := runChartJSON(ctx, kctx, []string{"get", "jobs", "-n", namespace, "-l", openbao.LabelJobName + "=" + name + "-converge", "-o", "json"}, nil)
	if err != nil {
		return false, false, err
	}
	return convergeOutcome(b)
}

// Presence implements StoreProbe.
func (p KubectlStoreProbe) Presence(ctx context.Context, kctx string, read StoreRead) (map[string]bool, error) {
	if p.PresenceFn == nil {
		all := make(map[string]bool, len(read.Names))
		for _, n := range read.Names {
			all[n] = true
		}
		return all, nil
	}
	return p.PresenceFn(ctx, kctx, read)
}

// convergeOutcome reads a `kubectl get jobs -o json` list and answers for the
// newest Job: a newer Job that failed is not rescued by an older one that
// succeeded, because the newer one is the spec the Bundle now declares.
func convergeOutcome(list []byte) (succeeded, found bool, err error) {
	var doc struct {
		Items []struct {
			Metadata struct {
				CreationTimestamp time.Time `json:"creationTimestamp"`
			} `json:"metadata"`
			Status struct {
				Succeeded int `json:"succeeded"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(list, &doc); err != nil {
		return false, false, fmt.Errorf("parse the converge Job list: %w", err)
	}
	if len(doc.Items) == 0 {
		return false, false, nil
	}
	newest := 0
	for i, it := range doc.Items {
		if it.Metadata.CreationTimestamp.After(doc.Items[newest].Metadata.CreationTimestamp) {
			newest = i
		}
	}
	return doc.Items[newest].Status.Succeeded > 0, true, nil
}

// CheckStore runs the store preflight for every expectation, one goroutine per
// cluster. A read that FAILS (as opposed to finding a gap) is an error: the
// cluster's state is unknown, and a gate must not read "could not look" as
// "nothing wrong". A store that cannot be ASKED (credentials, an unreachable
// control plane) is different: that is a warning, the same discipline as an
// image check whose registry is down.
func CheckStore(ctx context.Context, probe StoreProbe, expect []StoreExpectation) (StoreFindings, error) {
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		out      StoreFindings
		firstErr error
	)
	fail := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
	}
	add := func(into *[]string, msg string) {
		mu.Lock()
		*into = append(*into, msg)
		mu.Unlock()
	}
	for _, e := range expect {
		e := e
		// A cluster this deploy installs the operator on is owner-checked even
		// when nothing store-backed lands there yet.
		if strings.TrimSpace(e.Context) == "" || (len(e.Reads) == 0 && e.OpenBao == nil && !e.InstallsESO) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			checkStoreCluster(ctx, probe, e, add, fail, &out)
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return StoreFindings{}, firstErr
	}
	for _, s := range []*[]string{&out.MissingCRDs, &out.IncompatibleCRDs, &out.OwnerConflicts, &out.StoreNotReady, &out.MissingStoreNames, &out.Warnings} {
		sort.Strings(*s)
	}
	return out, nil
}

func checkStoreCluster(ctx context.Context, probe StoreProbe, e StoreExpectation, add func(*[]string, string), fail func(error), out *StoreFindings) {
	es, err := probe.CRD(ctx, e.Context, esoOwnerProbeCRD)
	if err != nil {
		fail(fmt.Errorf("preflight: read the ExternalSecret CRD on %q: %w", e.Context, err))
		return
	}
	ss, err := probe.CRD(ctx, e.Context, esoSecretStoreCRD)
	if err != nil {
		fail(fmt.Errorf("preflight: read the SecretStore CRD on %q: %w", e.Context, err))
		return
	}

	// The operator's owner: refused before any write when this deploy would
	// install over another env's. An unlabelled CRD was not forge's and is
	// adopted, exactly as the apply-time check does.
	if e.InstallsESO && es.Present && es.Owner != "" && e.Owner != "" && es.Owner != e.Owner {
		add(&out.OwnerConflicts, fmt.Sprintf(
			"cluster %q's External Secrets Operator is owned by %q; this env (%q) may not install a second one. Remove its `external_secrets_operators` entry for this cluster and let %q's bundle own it, or move the owner's declaration here after removing it there",
			e.Context, es.Owner, e.Owner, es.Owner))
	}

	// The kinds the render emits. A deploy that installs the operator applies
	// the CRDs first; otherwise they must already be served.
	if !e.InstallsESO && len(e.Reads) > 0 {
		var missing []string
		if !es.Present {
			missing = append(missing, "ExternalSecret")
		}
		if !ss.Present {
			missing = append(missing, "SecretStore")
		}
		if len(missing) > 0 {
			add(&out.MissingCRDs, fmt.Sprintf(
				"cluster %q does not serve %s (external-secrets.io), and this deploy does not install the External Secrets Operator there. Declare `external_secrets_operators = [forge.ExternalSecretsOperator {cluster = ...}]` on this Bundle and deploy `--target external-secrets` first, or deploy the env that owns that cluster's operator",
				e.Context, strings.Join(missing, " and ")))
		}
		for _, c := range []struct {
			kind string
			st   CRDState
		}{{"ExternalSecret", es}, {"SecretStore", ss}} {
			if c.st.Present && !containsString(c.st.Versions, esoRenderedVersion) {
				add(&out.IncompatibleCRDs, fmt.Sprintf(
					"cluster %q's %s CRD serves external-secrets.io/%s, and this Bundle renders external-secrets.io/%s. Upgrade that operator (forge pins chart %s), or have this Bundle install its own",
					e.Context, c.kind, strings.Join(c.st.Versions, ","), esoRenderedVersion, "2.x"))
			}
		}
	}

	if ob := e.OpenBao; ob != nil && !ob.Installs {
		done, found, err := probe.ConvergeSucceeded(ctx, e.Context, ob.Namespace, ob.Name)
		if err != nil {
			fail(fmt.Errorf("preflight: read the OpenBao converge Job on %q: %w", e.Context, err))
			return
		}
		switch {
		case !found:
			add(&out.StoreNotReady, fmt.Sprintf(
				"OpenBao %s/%s on %q has no converge Job: the store has not been deployed there. Deploy it first (`forge env deploy <env> --target %s`) so its access document is applied before any SecretStore logs in",
				ob.Namespace, ob.Name, e.Context, ob.Name))
		case !done:
			add(&out.StoreNotReady, fmt.Sprintf(
				"OpenBao %s/%s on %q has not finished converging (its newest converge Job has not succeeded), so no SecretStore can log in. Inspect it: kubectl --context %s -n %s get jobs -l %s=%s-converge",
				ob.Namespace, ob.Name, e.Context, e.Context, ob.Namespace, openbao.LabelJobName, ob.Name))
		}
	}

	for _, r := range e.Reads {
		if r.Pending || len(r.Names) == 0 {
			continue
		}
		present, err := probe.Presence(ctx, e.Context, r)
		if err != nil {
			add(&out.Warnings, fmt.Sprintf("could not ask the %s store whether it holds %s (%v); the deploy proceeds unverified. `forge secret list` asks it directly",
				r.Label(), strings.Join(r.Names, ", "), err))
			continue
		}
		var missing []string
		for _, n := range r.Names {
			if !present[n] {
				missing = append(missing, n)
			}
		}
		if len(missing) > 0 {
			add(&out.MissingStoreNames, fmt.Sprintf(
				"the %s store holds no value for %s: an ExternalSecret reads each, so the Secret would never fill. Set each with `forge secret set <NAME>` (value on stdin), import an existing Secret with `forge secret import`, or declare it under `source.generate` to have forge mint it",
				r.Label(), strings.Join(missing, ", ")))
		}
	}
}

func containsString(in []string, want string) bool {
	for _, s := range in {
		if s == want {
			return true
		}
	}
	return false
}

// FormatStoreFindings renders the BLOCKING findings as one block, or "" when
// none block.
func FormatStoreFindings(f StoreFindings) string {
	if f.Empty() {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n  Store-backed Secrets that cannot be delivered:\n")
	block := func(title string, items []string) {
		for _, it := range items {
			fmt.Fprintf(&b, "    - %s: %s\n", title, it)
		}
	}
	block("CRDs not served", f.MissingCRDs)
	block("operator version", f.IncompatibleCRDs)
	block("operator owner", f.OwnerConflicts)
	block("store not ready", f.StoreNotReady)
	block("store has no value", f.MissingStoreNames)
	return strings.TrimRight(b.String(), "\n")
}

// ExternalSecretsIn lists the ExternalSecrets a manifest stream applies, as
// "<namespace>/<name>". It is how the store gate knows which declared Secrets
// THIS deploy sends, after a --target filter.
func ExternalSecretsIn(manifests string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, doc := range splitDocs(manifests) {
		var head struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name      string `yaml:"name"`
				Namespace string `yaml:"namespace"`
			} `yaml:"metadata"`
		}
		if err := yaml.Unmarshal([]byte(doc), &head); err != nil || strings.TrimSpace(head.Kind) != "ExternalSecret" {
			continue
		}
		out[head.Metadata.Namespace+"/"+head.Metadata.Name] = struct{}{}
	}
	return out
}

// JobsWithPrefix lists the Jobs a manifest stream applies whose name starts
// with prefix, as "<namespace>/<name>". The OpenBao converge Job is
// content-addressed (<name>-converge-<hash>), so a stream that carries one is a
// deploy that is about to (re)converge the store.
func JobsWithPrefix(manifests, prefix string) []string {
	var out []string
	for _, doc := range splitDocs(manifests) {
		var head struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name      string `yaml:"name"`
				Namespace string `yaml:"namespace"`
			} `yaml:"metadata"`
		}
		if err := yaml.Unmarshal([]byte(doc), &head); err != nil || strings.TrimSpace(head.Kind) != "Job" {
			continue
		}
		if strings.HasPrefix(head.Metadata.Name, prefix) {
			out = append(out, head.Metadata.Namespace+"/"+head.Metadata.Name)
		}
	}
	sort.Strings(out)
	return out
}

// withoutGroup drops the manifests of one API group from a GVK list.
func withoutGroup(gvks []ManifestGVK, group string) []ManifestGVK {
	out := gvks[:0:0]
	for _, g := range gvks {
		if !strings.EqualFold(g.group(), group) {
			out = append(out, g)
		}
	}
	return out
}
