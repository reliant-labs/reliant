package cli

// `forge env deploy <env> [<version>]` for an env that declares
// `applier = "flux"`: record the release, then POINT the env's own Flux at it.
// forge never applies the bundle (flux_delivery.go); the Flux the bootstrap
// installed in the env's cluster does.
//
// THE ORDER, and why each step sits where it does:
//
//  1. PREFLIGHT, before anything is written (preflightFluxDelivery). Read the
//     release's recorded bundle, build the pointer input it would carry and
//     check it (fluxdelivery.ValidateSelfHosted), and run the grant preflight
//     (flux_delivery_grants.go). A refusal here records, pushes and points
//     nothing. A grant this deploy will make itself becomes a finding on the
//     deploy plan, so the approval covers it.
//  2. AFTER THE APPROVAL, BEFORE THE RECORD (beforeRecordFluxDelivery). Re-run
//     the bootstrap when the preflight decided to re-grant, then push the
//     bundle to the delivery registry. Both are safe to repeat and neither moves
//     what the env runs: the bundle is content-addressed, and a grant is not a
//     release. A failure here still leaves the ledger untouched.
//  3. THE RECORD (the ordinary promotion write).
//  4. THE POINTER, after the record (followFluxDelivery). The pointer is a
//     projection of the ledger: written first, a cluster could converge to a
//     release the ledger never recorded, and nothing would know to re-point
//     it. Written conditionally on the pointer just read (create-only, or
//     an update at its resourceVersion) with the next fence in the
//     CLUSTER's sequence (pointFluxDelivery), refused by
//     fluxdelivery.CheckTransition when it would move the fence backwards,
//     drop an un-retired applier or move one's path. A tree the release no
//     longer carries is retired, and that retire observed, first.
//  5. THE WAIT (waitFluxDelivery): every Kustomization has applied the
//     digest and is Ready (0), Flux reports a failure in its own words (1;
//     a Forbidden for an apply identity is the grant refusal's message), or
//     the budget ran out (8).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/pkg/fluxdelivery"
	"github.com/reliant-labs/forge/pkg/release"
)

// fluxDeliveryRelease is one release a flux deploy ships, as its preflight
// resolved it: carried on the promote plan to the steps that write.
type fluxDeliveryRelease struct {
	fd       FluxDeliveryEntity
	entities *KCLEntities
	project  string
	version  string
	// digest and doc are the release's recorded bundle; objects its
	// manifests, for the grant derivation.
	digest  string
	doc     release.BundleDoc
	objects []bundle.RenderedObject
	// bundleRef is where the bundle is recorded (this machine's OCI layout,
	// or a registry); pushTo is the repository it is pushed to, as this
	// machine reaches it; sourceURL is the same repository as the env's
	// cluster reaches it, `oci://` included.
	bundleRef string
	pushTo    string
	sourceURL string
	grants    fluxDeliveryGrantPlan
}

// The interval of the env's OCIRepository and of every Kustomization: how
// often Flux re-reads a digest that cannot change, and re-corrects drift.
const fluxDeliveryInterval = 10 * time.Minute

// preflightFluxDelivery is step 1. projectDir is the project the release is
// recorded for; deployPlan is the plan the approval will name (nil: no plan to
// add the grant findings to).
func preflightFluxDelivery(ctx context.Context, env, version, projectDir string, ledger envLedger, deployPlan *release.Plan, notice func(string, ...any)) (*fluxDeliveryRelease, error) {
	entities := ledger.FluxEntities
	fd := *entities.FluxDelivery
	rel := &fluxDeliveryRelease{fd: fd, entities: entities, project: fluxDeliveryProject(projectDir), version: version}
	if rel.project == "" {
		return nil, fmt.Errorf("env %q: this project has no name in forge.yaml, which names its delivery objects", env)
	}

	found, ok, err := findReleaseBundle(ctx, projectDir, env, version, ledger, bundleBuildInputs{})
	if err != nil {
		return nil, fmt.Errorf("read release %s's bundle for env %s: %w", version, env, err)
	}
	if !ok {
		return nil, fmt.Errorf("env %s is delivered by its in-cluster Flux, which applies a release's BUNDLE, and release %s has none recorded for it.\n"+
			"  Build one: forge env build %s, then re-run this deploy", env, version, env)
	}
	rel.digest, rel.bundleRef = found.Digest, found.Reference
	fetched, err := fetchBundleRef(ctx, projectDir, env, found.Reference)
	if err != nil {
		return nil, fmt.Errorf("read release %s's bundle %s: %w", version, shortDigest(found.Digest), err)
	}
	rel.doc = fetched.Doc
	if rel.objects, err = fluxDeliveryBundleObjects(fetched, fd.Cluster); err != nil {
		return nil, fmt.Errorf("read release %s's bundle %s: %w", version, shortDigest(found.Digest), err)
	}

	base := fluxRegistryBase(entities)
	if base == "" {
		return nil, fmt.Errorf("env %s is delivered by its in-cluster Flux, which pulls each release's bundle from a registry, and none is known.\n"+
			"  Declare one: flux_delivery = forge.FluxDelivery {registry = \"<host>/<path>\"}\n"+
			"  (or give a cluster workload an image that names a registry host: the bundle is then pushed beside it)", env)
	}
	rel.pushTo = bundle.Repository(base, env)
	rel.sourceURL = "oci://" + clusterReachableRegistry(rel.pushTo)

	// The input this release will be pointed with, checked now: a structural
	// refusal must write nothing. The fence and promotion are the record's,
	// known only after it, and refused by nothing here.
	in, err := rel.input("1", "preflight")
	if err != nil {
		return nil, err
	}
	if err := checkFluxDeliveryInput(in, fd); err != nil {
		return nil, fmt.Errorf("env %s: release %s cannot be pointed at: %w", env, version, err)
	}

	if rel.grants, err = planFluxDeliveryGrants(ctx, env, version, fd, rel.objects); err != nil {
		return nil, err
	}
	if rel.grants.Regrant {
		rows := fluxDeliveryGrantRows(rel.grants.Missing)
		notice("\nRelease %s needs permissions env %s's delivery identities were not granted; this credential can grant them,\n"+
			"so once the plan is approved this deploy re-runs the bootstrap (--target %s) to add:\n", version, env, fd.Chart)
		var b bytes.Buffer
		writeFluxDeliveryGrantRows(&b, "  + ", rows)
		notice("%s", b.String())
		if deployPlan != nil {
			findings := make([]release.PlanFinding, 0, len(rows))
			for _, r := range rows {
				findings = append(findings, release.PlanFinding{
					Code: release.FindingDeliveryGrant, Class: release.ClassWarn, Section: release.SectionDeliveryGrants,
					Subject: r.Identity + " " + r.Resource + " " + r.Scope,
					Detail:  "granted by re-running the bootstrap with your credential: " + strings.Join(r.Verbs, ","),
				})
			}
			updated, err := deployPlan.WithFindings(findings...)
			if err != nil {
				return nil, err
			}
			*deployPlan = updated
		}
	}
	return rel, nil
}

// checkFluxDeliveryInput is every check the pointer write repeats, run where a
// refusal still writes nothing: the input, and the objects it renders through
// the bootstrap's ResourceSet.
func checkFluxDeliveryInput(in fluxdelivery.BindingInput, fd FluxDeliveryEntity) error {
	mode := fluxdelivery.SelfHosted{ApplyIdentities: fd.ApplyIdentities}
	if err := fluxdelivery.ValidateSelfHosted(in, mode); err != nil {
		return err
	}
	objs, err := renderFluxDeliveryInput(in, fd)
	if err != nil {
		return err
	}
	return fluxdelivery.CheckRenderedSelfHosted(in, objs, mode)
}

// renderFluxDeliveryInput is what the bootstrap's ResourceSet renders for in,
// read through the pointer exactly as flux-operator reads it.
func renderFluxDeliveryInput(in fluxdelivery.BindingInput, fd FluxDeliveryEntity) ([]*unstructured.Unstructured, error) {
	pointer, err := fluxdelivery.InputProvider(in)
	if err != nil {
		return nil, err
	}
	values, _, _ := unstructured.NestedMap(pointer, "spec", "defaultValues")
	return fluxdelivery.Render(fluxDeliveryResourceSet(fd), fluxdelivery.Provider{
		Name: fluxdelivery.InputProviderName(in.Env, in.Binding), Inputs: []map[string]any{values},
	})
}

// fluxDeliveryResourceSet is the ResourceSet the bootstrap installs (kcl's
// flux_delivery_resourceset with inputs_from), as the pre-render needs it.
func fluxDeliveryResourceSet(fd FluxDeliveryEntity) map[string]any {
	return map[string]any{
		"apiVersion": fluxdelivery.APIVersion,
		"kind":       fluxdelivery.KindResourceSet,
		"metadata":   map[string]any{"name": fd.ResourceSet, "namespace": fd.Namespace},
		"spec": map[string]any{
			"serviceAccountName": fluxdelivery.ServiceAccount,
			"inputsFrom": []any{map[string]any{
				"apiVersion": fluxdelivery.APIVersion, "kind": fluxdelivery.KindInputProvider,
				"selector": map[string]any{"matchLabels": map[string]any{fluxdelivery.InputLabel: "true"}},
			}},
			"resourcesTemplate": fluxdelivery.Template,
		},
	}
}

// input is the pointer's BindingInput for this release at fence/promotion: one
// in-cluster applier per tree the bundle carries for the env's cluster.
func (r *fluxDeliveryRelease) input(fence, promotion string) (fluxdelivery.BindingInput, error) {
	in := fluxdelivery.BindingInput{
		Schema:    fluxdelivery.Schema,
		Org:       r.project,
		Env:       r.fd.Env,
		Namespace: r.fd.Namespace,
		Fence:     fence,
		Promotion: promotion,
		Labels: map[string]string{
			"app.kubernetes.io/managed-by": "forge",
			fluxdelivery.EnvLabel:          r.fd.Env,
		},
		Source: fluxdelivery.Source{
			Name:       fluxdelivery.SourceName(r.project, r.fd.Env, ""),
			URL:        r.sourceURL,
			Digest:     r.digest,
			Interval:   fluxDeliveryInterval.String(),
			Insecure:   insecureRegistry(r.sourceURL),
			PullSecret: r.fd.PullSecret,
			// At most audit (the render capped it), which renders no
			// spec.verify: nothing signs a self-hosted env's bundles yet.
			Verify: fluxdelivery.Verify{Mode: emptyAs(r.fd.FluxVerify.Effective, fluxdelivery.VerifyOff)},
		},
	}
	byPath := map[string]string{}
	var trees []release.BundleTree
	for _, t := range r.doc.Trees {
		if t.Cluster != r.fd.Cluster {
			continue
		}
		trees = append(trees, t)
		byPath[t.Path] = fluxdelivery.TreeKustomizationName(r.project, r.fd.Env, "", t.Cluster, t.Unit, t.Phase)
	}
	if len(trees) == 0 {
		return fluxdelivery.BindingInput{}, fmt.Errorf("release %s's bundle carries no tree for cluster %q (it holds %s): "+
			"a pointer with nothing to apply would report Ready over an env that never deployed", r.version, r.fd.Cluster, describeTrees(r.doc))
	}
	for _, t := range trees {
		a := fluxdelivery.Applier{
			Name:    byPath[t.Path],
			Source:  in.Source.Name,
			Cluster: t.Cluster,
			Unit:    t.Unit,
			Phase:   t.Phase,
			Path:    t.Path,
			Labels: map[string]string{
				"forge.dev/delivery-unit": fluxdelivery.LabelValue(t.Unit),
				"forge.dev/phase":         string(t.Phase),
			},
			InCluster:     true,
			Timeout:       (time.Duration(t.TimeoutSeconds) * time.Second).String(),
			RetryInterval: (time.Duration(t.RetryIntervalSeconds) * time.Second).String(),
		}
		if t.RetryIntervalSeconds <= 0 {
			a.RetryInterval = ""
		}
		if r.fd.ApplyIdentities {
			a.ServiceAccountName = fluxdelivery.SelfHostedServiceAccount(t.Phase)
		}
		for _, dep := range t.DependsOn {
			name, ok := byPath[dep]
			if !ok {
				return fluxdelivery.BindingInput{}, fmt.Errorf("release %s's tree %s depends on %s, which the bundle does not carry for cluster %s",
					r.version, t.Path, dep, r.fd.Cluster)
			}
			a.DependsOn = append(a.DependsOn, name)
		}
		exprs := map[string]bool{}
		for _, h := range t.Health {
			apiVersion, ok := fluxdelivery.HealthAPIVersion(h.Kind)
			if !ok {
				return fluxdelivery.BindingInput{}, fmt.Errorf("release %s's tree %s is judged on a %s, which Flux cannot health-check", r.version, t.Path, h.Kind)
			}
			a.Health = append(a.Health, fluxdelivery.HealthCheck{APIVersion: apiVersion, Kind: h.Kind, Name: h.Name, Namespace: h.Namespace})
			if h.Kind == "ExternalSecret" && !exprs[h.Kind] {
				exprs[h.Kind] = true
				a.HealthExprs = append(a.HealthExprs, fluxdelivery.HealthCheckExpr{APIVersion: apiVersion, Kind: h.Kind, Current: fluxdelivery.ExternalSecretCurrent})
			}
		}
		in.Appliers = append(in.Appliers, a)
	}
	return in, nil
}

func describeTrees(doc release.BundleDoc) string {
	if len(doc.Trees) == 0 {
		return "no trees"
	}
	var parts []string
	for _, t := range doc.Trees {
		parts = append(parts, t.Path)
	}
	return strings.Join(parts, ", ")
}

// fluxDeliveryProject is the project name the delivery's object names start
// with (the hub's org segment).
var fluxDeliveryProject = func(projectDir string) string { return ledgerProjectName(projectDir) }

// fluxDeliveryBundleObjects reads a fetched bundle's manifests for one cluster
// as rendered objects, each attributed to that cluster, the way a render
// stream attributes them.
func fluxDeliveryBundleObjects(f bundle.Fetched, kubeContext string) ([]bundle.RenderedObject, error) {
	dir, err := os.MkdirTemp("", "forge-flux-bundle-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	paths, err := bundle.Unpack(bytes.NewReader(f.Manifests), dir)
	if err != nil {
		return nil, err
	}
	prefix := release.BundleClusterPath(kubeContext) + "/"
	var out []bundle.RenderedObject
	for _, p := range paths {
		if !strings.HasPrefix(p, prefix) || !(strings.HasSuffix(p, ".yaml") || strings.HasSuffix(p, ".yml")) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
		if err != nil {
			return nil, err
		}
		for i, doc := range bytes.Split(raw, []byte("\n---")) {
			var body map[string]any
			if err := yaml.Unmarshal(doc, &body); err != nil {
				return nil, fmt.Errorf("%s document %d: %w", p, i+1, err)
			}
			if body == nil {
				continue
			}
			u := unstructured.Unstructured{Object: body}
			if u.GetKind() == "" || u.GetName() == "" || u.GetKind() == "Kustomization" && u.GetAPIVersion() == "kustomize.config.k8s.io/v1beta1" {
				continue
			}
			out = append(out, bundle.RenderedObject{
				Clusters: []string{kubeContext}, APIVersion: u.GetAPIVersion(), Kind: u.GetKind(),
				Namespace: u.GetNamespace(), Name: u.GetName(), Body: body,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Kind+out[i].Namespace+out[i].Name < out[j].Kind+out[j].Namespace+out[j].Name
	})
	return out, nil
}

// beforeRecordFluxDelivery is step 2: re-grant if the preflight decided to,
// then push the bundle where the env's Flux pulls it.
func beforeRecordFluxDelivery(ctx context.Context, env string, rel *fluxDeliveryRelease, notice func(string, ...any)) error {
	if rel.grants.Regrant {
		notice("\nGranting env %s's delivery identities what release %s needs (re-running the bootstrap with your credential)\n", env, rel.version)
		if err := regrantFluxDelivery(ctx, env, rel); err != nil {
			return fmt.Errorf("%w\n\nNOTHING WAS RECORDED: env %q's binding did not move", err, env)
		}
	}
	notice("\nPushing release %s's bundle %s to %s\n", rel.version, shortDigest(rel.digest), rel.pushTo)
	if err := pushFluxDeliveryBundle(ctx, rel); err != nil {
		return fmt.Errorf("%w\n\nNOTHING WAS RECORDED: env %q's binding did not move", err, env)
	}
	return nil
}

// regrantFluxDelivery re-runs the bootstrap chart's install, its grants
// derived from the render AND the release's bundle, then proves the record now
// covers the release.
func regrantFluxDelivery(ctx context.Context, env string, rel *fluxDeliveryRelease) error {
	fd := rel.fd
	fd.releaseObjects = rel.objects
	installs := platformChartInstalls(rel.entities, []string{fd.Chart}, nil)
	if len(installs) == 0 {
		return fmt.Errorf("env %q: the bootstrap chart %s resolves to no cluster", env, fd.Chart)
	}
	for i := range installs {
		installs[i].Chart.FluxDelivery = &fd
	}
	if err := installFluxDeliveryChartsFn(ctx, rel.entities, installs, cluster.ChartOwner(rel.project, env)); err != nil {
		return fmt.Errorf("re-run env %s's bootstrap to grant its delivery identities: %w", env, err)
	}
	needs, err := fluxDeliveryNeeds(rel.objects, rel.fd)
	if err != nil {
		return err
	}
	granted, err := fluxDeliveryReadGrants(ctx, rel.fd)
	if err != nil {
		return err
	}
	if missing := fluxDeliveryShortfall(needs, granted); len(missing) > 0 {
		return newFluxDeliveryGrantRefusal(env, rel.version, missing)
	}
	return nil
}

// pushFluxDeliveryBundle copies the recorded bundle, verified by digest, to
// the env's bundle repository. A var so the deploy is testable without a
// registry.
var pushFluxDeliveryBundle = func(ctx context.Context, rel *fluxDeliveryRelease) error {
	var src bundle.Resolver
	if rest, ok := strings.CutPrefix(rel.bundleRef, "oci-layout:"); ok {
		root, _, _ := strings.Cut(rest, "@")
		layout, err := bundle.NewLocalLayout(root)
		if err != nil {
			return err
		}
		src = layout
	} else {
		repo, _, _ := strings.Cut(rel.bundleRef, "@")
		r, err := bundle.NewRepository(repo)
		if err != nil {
			return err
		}
		src = r
	}
	dst, err := bundle.NewRepository(rel.pushTo)
	if err != nil {
		return err
	}
	pushed, err := bundle.Republish(ctx, src, dst, rel.digest)
	if err != nil {
		return fmt.Errorf("push release %s's bundle to %s: %w", rel.version, rel.pushTo, err)
	}
	if pushed != rel.digest {
		return fmt.Errorf("push release %s's bundle to %s: wrote %s, not the recorded %s", rel.version, rel.pushTo, pushed, rel.digest)
	}
	return nil
}

// followFluxDelivery is steps 4 and 5, after the record.
func followFluxDelivery(ctx context.Context, env string, plan promotePlan, ledger envLedger, o promoteFollowOptions) error {
	rel := plan.flux
	if rel == nil || plan.Recorded == nil {
		return fmt.Errorf("env %s: the deploy recorded no promotion to point its in-cluster Flux at", env)
	}
	o.notice("\nPointing env %s's in-cluster Flux at release %s (bundle %s, %s/%s)\n",
		env, rel.version, shortDigest(rel.digest), rel.fd.Namespace, fluxdelivery.InputProviderName(env, ""))
	in, err := pointFluxDelivery(ctx, rel, plan.Recorded.ID, o.Timeout, func(f string, a ...any) { o.notice(f, a...) })
	if err != nil {
		return err
	}
	if o.NoWait {
		o.notice("\nRecorded and pointed. %s is converged by the Flux in its cluster; gate on it with: forge env status %s --wait\n", env, env)
		return nil
	}
	return waitFluxDelivery(ctx, env, rel.version, rel.fd, in, o.Timeout, fluxWaitPace{failFast: o.FailFast}, func(f string, a ...any) { o.notice(f, a...) })
}

// fluxDeliveryPointAttempts bounds how often a pointer write re-reads after
// another writer changed the pointer under it.
const fluxDeliveryPointAttempts = 5

// pointFluxDelivery writes the pointer for rel at promotion and returns the
// input it wrote.
//
// THE FENCE IS THE CLUSTER'S. Every deployer — another administrator, a fresh
// CI runner — has its own ledger, so no ledger position orders their writes.
// The pointer itself does: each write carries the live pointer's fence plus
// one and is conditional on the object it read (create-only when there was
// none, a resourceVersion-conditional update otherwise). A writer that lost
// the race gets a conflict, re-reads, and re-checks the transition against
// what actually won, so nothing is ever overwritten unchecked.
//
// A TREE THE RELEASE NO LONGER CARRIES is retired before the source moves:
// first a write that keeps the live source and marks the dropped appliers
// `retire: orphan`, then — once the renderer has applied that — the release
// itself, which drops them. Without it the dropped Kustomization would keep
// reading the shared source and prune everything it applied the moment the
// source moved to a digest without its path.
//
// The OBSERVATION is not tied to the write that marked the retire. Every
// write that drops an applier first waits for the renderer to have applied
// that applier's retire, whoever marked it: this deploy, an earlier one that
// timed out or crashed after marking it, or a concurrent one. And the drop is
// conditional on the pointer read BEFORE that wait, so a pointer that changed
// while this one waited (a writer that un-retired the applier, or moved the
// source) fails the write, which re-reads and observes again.
func pointFluxDelivery(ctx context.Context, rel *fluxDeliveryRelease, promotion string, budget time.Duration, notice func(string, ...any)) (fluxdelivery.BindingInput, error) {
	fd := rel.fd
	for attempt := 1; ; attempt++ {
		live, err := fluxDeliveryPointerIO.read(ctx, fd)
		if err != nil {
			return fluxdelivery.BindingInput{}, err
		}
		var liveIn *fluxdelivery.BindingInput
		rv, liveRelease := "", ""
		if live != nil {
			l, err := fluxdelivery.LiveInput(live)
			if err != nil {
				return fluxdelivery.BindingInput{}, fmt.Errorf("env %s: the live pointer cannot be read, so this write cannot prove it is safe: %w", fd.Env, err)
			}
			liveIn, rv = &l, live.GetResourceVersion()
			liveRelease = live.GetAnnotations()[fluxDeliveryReleaseAnnotation]
		}
		fence, err := nextFluxDeliveryFence(liveIn)
		if err != nil {
			return fluxdelivery.BindingInput{}, fmt.Errorf("env %s: refusing to point its Flux: %w", fd.Env, err)
		}
		next, err := rel.input(fence, promotion)
		if err != nil {
			return fluxdelivery.BindingInput{}, err
		}
		dropping, unmarked := fluxDeliveryDropped(liveIn, next)
		write, writeRelease := next, rel.version
		switch {
		case len(unmarked) > 0:
			// Mark first; the next pass observes the mark and drops.
			write, writeRelease = retireFluxDeliveryAppliers(*liveIn, unmarked, fence), liveRelease
			notice("  release %s no longer carries %s: retiring %s first (orphaned, not deleted)\n",
				rel.version, plural(len(unmarked), "a tree", "trees"), strings.Join(unmarked, ", "))
		case len(dropping) > 0:
			// Every drop is observed, whoever marked it.
			if err := waitFluxDeliveryRetired(ctx, fd, dropping, budget); err != nil {
				return fluxdelivery.BindingInput{}, err
			}
		}
		err = writeFluxDeliveryPointer(ctx, fd, liveIn, rv, write, writeRelease)
		if errors.Is(err, errFluxPointerConflict) && attempt < fluxDeliveryPointAttempts {
			notice("  env %s's pointer changed under this write; re-reading it\n", fd.Env)
			continue
		}
		if err != nil {
			return fluxdelivery.BindingInput{}, err
		}
		if len(unmarked) == 0 {
			return next, nil
		}
		// The retire is marked: the next pass re-reads it, observes it
		// applied, and drops them.
	}
}

// nextFluxDeliveryFence is the fence a write over live carries: one past
// live's, or 1 for the first write.
func nextFluxDeliveryFence(live *fluxdelivery.BindingInput) (string, error) {
	if live == nil {
		return "1", nil
	}
	n, err := strconv.ParseUint(live.Fence, 10, 64)
	if err != nil {
		return "", fmt.Errorf("%w: the live pointer's fence %q is not a sequence number, so no write can be ordered after it",
			fluxdelivery.ErrStaleFence, live.Fence)
	}
	return strconv.FormatUint(n+1, 10), nil
}

// fluxDeliveryDropped names the appliers live holds that next drops (sorted),
// and of those the ones live never marked retired: the ones a write must mark
// before anything drops them. Every dropped one must be OBSERVED retired
// before the drop, marked or not. A binding-wide retire is the ledger's intent
// for the whole binding, which CheckTransition owns; nothing is dropped here.
func fluxDeliveryDropped(live *fluxdelivery.BindingInput, next fluxdelivery.BindingInput) (dropping, unmarked []string) {
	if live == nil || live.Retire != "" {
		return nil, nil
	}
	kept := map[string]bool{}
	for _, a := range next.Appliers {
		kept[a.Name] = true
	}
	for _, a := range live.Appliers {
		if kept[a.Name] {
			continue
		}
		dropping = append(dropping, a.Name)
		if a.Retire == "" {
			unmarked = append(unmarked, a.Name)
		}
	}
	sort.Strings(dropping)
	sort.Strings(unmarked)
	return dropping, unmarked
}

// retireFluxDeliveryAppliers is live, at fence, with the named appliers
// retired (orphan): the same source and the same promotion, so it moves
// nothing but those appliers' lifecycle.
func retireFluxDeliveryAppliers(live fluxdelivery.BindingInput, names []string, fence string) fluxdelivery.BindingInput {
	retire := map[string]bool{}
	for _, n := range names {
		retire[n] = true
	}
	out := live
	out.Fence = fence
	out.Appliers = append([]fluxdelivery.Applier(nil), live.Appliers...)
	for i := range out.Appliers {
		if retire[out.Appliers[i].Name] {
			out.Appliers[i].Retire = fluxdelivery.RetireOrphan
		}
	}
	return out
}

// waitFluxDeliveryRetired polls until the renderer has applied a retire to
// every named Kustomization (or it is gone), within budget.
func waitFluxDeliveryRetired(ctx context.Context, fd FluxDeliveryEntity, names []string, budget time.Duration) error {
	if budget <= 0 {
		budget = fluxDeliveryWaitDefault
	}
	deadline := time.Now().Add(budget)
	for {
		done, err := fluxDeliveryRetireApplied(ctx, fd, names)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		if !time.Now().Before(deadline) {
			return exitCodeError{code: exitTimedOut, msg: fmt.Sprintf(
				"env %s: its Flux did not apply the retire of %s within %s, so the release was not pointed at (the retire is recorded on the pointer; re-run the deploy to continue).\n"+
					"  `kubectl --context %s -n %s get kustomizations` shows Flux's own view.",
				fd.Env, strings.Join(names, ", "), budget, fd.Cluster, fd.Namespace)}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(fluxDeliveryWaitInterval):
		}
	}
}

// fluxDeliveryRetireApplied reports whether every named Kustomization is gone
// or carries the retire the renderer renders (suspended, orphaning). A var so
// the writer is testable without a cluster.
var fluxDeliveryRetireApplied = func(ctx context.Context, fd FluxDeliveryEntity, names []string) (bool, error) {
	out, err := exec.CommandContext(ctx, "kubectl", "--context", fd.Cluster, "-n", fd.Namespace,
		"get", "kustomizations.kustomize.toolkit.fluxcd.io", "-o", "json").Output()
	if err != nil {
		return false, fmt.Errorf("read env %s's Kustomizations on %s: %w", fd.Env, fd.Cluster, kubectlErr(err))
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return false, fmt.Errorf("decode env %s's Kustomizations: %w", fd.Env, err)
	}
	byName := map[string]map[string]any{}
	for _, item := range list.Items {
		name, _ := nested(item, "metadata", "name").(string)
		byName[name] = item
	}
	for _, n := range names {
		k, ok := byName[n]
		if !ok {
			continue
		}
		suspended, _ := nested(k, "spec", "suspend").(bool)
		policy, _ := nested(k, "spec", "deletionPolicy").(string)
		if !suspended || policy != "Orphan" {
			return false, nil
		}
	}
	return true, nil
}

// fluxDeliveryReleaseAnnotation names, on the pointer, the release version its
// promotion binds: what `forge env status --wait --release` checks, read from
// the cluster rather than any one deployer's ledger.
const fluxDeliveryReleaseAnnotation = "forge.dev/delivery-release"

// errFluxPointerConflict is a pointer write the API server refused because
// the pointer is not the one it was conditional on: another writer created,
// updated or deleted it since the read.
var errFluxPointerConflict = errors.New("the pointer changed since it was read")

// writeFluxDeliveryPointer checks in against live (nil: no pointer exists) and
// writes it conditionally on that read: create-only when there was none, an
// update at liveRV otherwise, annotated with the release version in's
// promotion binds. A conflict is errFluxPointerConflict.
func writeFluxDeliveryPointer(ctx context.Context, fd FluxDeliveryEntity, live *fluxdelivery.BindingInput, liveRV string, in fluxdelivery.BindingInput, version string) error {
	if err := checkFluxDeliveryInput(in, fd); err != nil {
		return fmt.Errorf("env %s: refusing to point its Flux: %w", fd.Env, err)
	}
	pointer, err := fluxdelivery.InputProvider(in)
	if err != nil {
		return err
	}
	if version != "" {
		md := pointer["metadata"].(map[string]any)
		md["annotations"].(map[string]any)[fluxDeliveryReleaseAnnotation] = version
	}
	if live == nil {
		return fluxDeliveryPointerIO.create(ctx, fd, pointer)
	}
	if err := fluxdelivery.CheckTransition(live, in); err != nil {
		return fmt.Errorf("env %s: refusing to point its Flux: %w", fd.Env, err)
	}
	// A fence names ONE promotion: an equal fence is this write repeated,
	// never another promotion's.
	if in.Fence == live.Fence && in.Promotion != live.Promotion {
		return fmt.Errorf("env %s: refusing to point its Flux: %w: fence %s is promotion %s's, not %s",
			fd.Env, fluxdelivery.ErrStaleFence, live.Fence, live.Promotion, in.Promotion)
	}
	md := pointer["metadata"].(map[string]any)
	md["resourceVersion"] = liveRV
	return fluxDeliveryPointerIO.update(ctx, fd, pointer)
}

// fluxDeliveryPointerIO is the pointer's cluster I/O. A seam so the writer's
// refusals are testable without a cluster. create is create-only; update is
// conditional on the pointer's metadata.resourceVersion. Both return
// errFluxPointerConflict when that precondition fails.
var fluxDeliveryPointerIO = struct {
	read   func(ctx context.Context, fd FluxDeliveryEntity) (*unstructured.Unstructured, error)
	create func(ctx context.Context, fd FluxDeliveryEntity, pointer map[string]any) error
	update func(ctx context.Context, fd FluxDeliveryEntity, pointer map[string]any) error
}{
	read: func(ctx context.Context, fd FluxDeliveryEntity) (*unstructured.Unstructured, error) {
		out, err := exec.CommandContext(ctx, "kubectl", "--context", fd.Cluster, "-n", fd.Namespace, "get",
			"resourcesetinputproviders.fluxcd.controlplane.io", fluxdelivery.InputProviderName(fd.Env, ""),
			"-o", "json", "--ignore-not-found").Output()
		if err != nil {
			return nil, fmt.Errorf("read env %s's pointer on %s: %w", fd.Env, fd.Cluster, kubectlErr(err))
		}
		if strings.TrimSpace(string(out)) == "" {
			return nil, nil
		}
		var obj map[string]any
		if err := json.Unmarshal(out, &obj); err != nil {
			return nil, fmt.Errorf("decode env %s's pointer: %w", fd.Env, err)
		}
		return &unstructured.Unstructured{Object: obj}, nil
	},
	create: func(ctx context.Context, fd FluxDeliveryEntity, pointer map[string]any) error {
		return kubectlWritePointer(ctx, fd, "create", pointer)
	},
	update: func(ctx context.Context, fd FluxDeliveryEntity, pointer map[string]any) error {
		return kubectlWritePointer(ctx, fd, "replace", pointer)
	},
}

// kubectlWritePointer runs `kubectl create|replace` with the pointer. Neither
// verb forces anything: create fails when the pointer exists, and replace
// fails when its resourceVersion is not the live one (or it was deleted) —
// each errFluxPointerConflict.
func kubectlWritePointer(ctx context.Context, fd FluxDeliveryEntity, verb string, pointer map[string]any) error {
	body, err := json.Marshal(pointer)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "kubectl", "--context", fd.Cluster, verb,
		"--field-manager", fluxDeliveryFieldManager, "-f", "-")
	cmd.Stdin = bytes.NewReader(body)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if fluxPointerConflictRE.MatchString(msg) {
			return fmt.Errorf("write env %s's pointer: %w: %s", fd.Env, errFluxPointerConflict, msg)
		}
		return fmt.Errorf("write env %s's pointer %s/%s on %s: %w: %s", fd.Env, fd.Namespace,
			fluxdelivery.InputProviderName(fd.Env, ""), fd.Cluster, err, msg)
	}
	return nil
}

// fluxPointerConflictRE is the API server's refusal of a conditional write, as
// kubectl prints it: AlreadyExists (create), Conflict (a stale
// resourceVersion), NotFound (deleted since the read).
var fluxPointerConflictRE = regexp.MustCompile(`Error from server \((AlreadyExists|Conflict|NotFound)\)`)

// fluxDeliveryFieldManager owns the pointer's fields.
const fluxDeliveryFieldManager = "forge"

// The wait's cadence and default budget.
const (
	fluxDeliveryWaitDefault  = 10 * time.Minute
	fluxDeliveryWaitInterval = 3 * time.Second
)

// fluxWaitPace is how a flux wait polls: its cadence (0:
// fluxDeliveryWaitInterval) and whether the first failure Flux reports ends
// it (--fail-fast).
type fluxWaitPace struct {
	interval time.Duration
	failFast bool
}

// waitFluxDelivery polls until every applier of in has applied its digest and
// is Ready (nil), Flux reports a failure for it (exit 1), or the budget runs
// out (exit 8).
//
// NOT FAIL-FAST BY DEFAULT, like every other `--wait` (env_wait.go, owner
// ruling Q4): a Kustomization whose health check fails once on a cold start
// and then settles is ordinary, and Flux retries it on its own. A failure is
// tolerated until the deadline, which turns one still reported then into
// exit 1; pace.failFast returns it at once. A renderer Stalled on this input
// is terminal either way: flux-operator does not retry an input it cannot
// render, so only a new pointer changes it.
func waitFluxDelivery(ctx context.Context, env, rel string, fd FluxDeliveryEntity, in fluxdelivery.BindingInput, budget time.Duration, pace fluxWaitPace, notice func(string, ...any)) error {
	_, err := waitFluxDeliveryObserved(ctx, env, rel, fd, in, budget, pace, notice)
	return err
}

// waitFluxDeliveryObserved is waitFluxDelivery, also returning the last
// observation (zero when none was made) for a caller that reports it.
func waitFluxDeliveryObserved(ctx context.Context, env, rel string, fd FluxDeliveryEntity, in fluxdelivery.BindingInput, budget time.Duration, pace fluxWaitPace, notice func(string, ...any)) (fluxDeliveryObservation, error) {
	interval := pace.interval
	if budget <= 0 {
		budget = fluxDeliveryWaitDefault
	}
	if interval <= 0 {
		interval = fluxDeliveryWaitInterval
	}
	started := time.Now()
	deadline := started.Add(budget)
	notice("\nWaiting for env %s's Flux to apply %s (budget %s)\n", env, shortDigest(in.Source.Digest), budget)
	var last string
	for {
		obs, err := fluxDeliveryObserve(ctx, fd, in)
		if err != nil {
			return obs, exitCodeError{code: exitUndetermined, msg: err.Error()}
		}
		if err := obs.superseded(env, in); err != nil {
			return obs, err
		}
		if obs.converged(in) {
			notice("  converged: %s in %s\n", obs.summary(in), time.Since(started).Round(time.Second))
			return obs, nil
		}
		failed := obs.failure(env, rel, in)
		if failed != nil && (pace.failFast || obs.rendererStalledOnInput()) {
			return obs, failed
		}
		line := obs.summary(in)
		if failed != nil {
			// Tolerated: Flux retries it, and the deadline decides.
			line = "degraded: " + strings.ReplaceAll(failed.Error(), "\n", "; ")
		}
		if line != last {
			notice("  %s\n", line)
			last = line
		}
		if !time.Now().Before(deadline) {
			if failed != nil {
				notice("  still failing when the %s budget ran out\n", budget)
				return obs, failed
			}
			return obs, exitCodeError{code: exitTimedOut, msg: fmt.Sprintf(
				"env %s did not converge to %s within %s: %s.\n"+
					"  Its Flux keeps reconciling; `forge env status %s --wait` keeps waiting, and\n"+
					"  `kubectl --context %s -n %s get kustomizations` shows Flux's own view.",
				env, shortDigest(in.Source.Digest), budget, obs.summary(in), env, fd.Cluster, fd.Namespace)}
		}
		select {
		case <-ctx.Done():
			return obs, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// fluxDeliveryOfEnv renders env and returns its self-hosted delivery, or nil
// when it declares none (or has no KCL to render).
func fluxDeliveryOfEnv(ctx context.Context, projectDir, env string) (*FluxDeliveryEntity, error) {
	if _, err := os.Stat(filepath.Join(projectDir, "deploy", "kcl", env, "main.k")); err != nil {
		return nil, nil
	}
	entities, err := RenderKCL(ctx, projectDir, env)
	if err != nil {
		return nil, err
	}
	if entities.Applier != applierFlux {
		return nil, nil
	}
	return entities.FluxDelivery, nil
}

// checkFluxDeliveryWaitOptions refuses the `--wait` options a flux env's wait
// cannot honour, before it waits: silently dropping one would report success
// on terms the caller did not ask for. --fail-fast and --interval are honoured
// (fluxWaitPace).
func checkFluxDeliveryWaitOptions(env string, opts envWaitOptions) error {
	var unsupported []string
	if opts.StableFor > 0 {
		unsupported = append(unsupported, "--stable-for")
	}
	if opts.WatchJSON {
		unsupported = append(unsupported, "--watch-json")
	}
	if opts.IncludeUnpinned {
		unsupported = append(unsupported, "--include-unpinned")
	}
	if len(unsupported) > 0 {
		return exitCodeError{code: exitWrong, msg: fmt.Sprintf(
			"env %s is delivered by its in-cluster Flux, whose wait does not support %s: it reports each Kustomization's own Ready, "+
				"with no stability hold, phase stream or unpinned workloads. Drop the flag", env, strings.Join(unsupported, ", "))}
	}
	if opts.Release != "" && opts.PromotionID != "" {
		return exitCodeError{code: exitWrong, msg: "--promotion and --release name the promotion two different ways; pass one"}
	}
	return nil
}

// checkFluxDeliveryWaitTarget enforces --promotion and --release against what
// the live pointer projects, before the wait: a pointer at another promotion
// was overtaken (6), and one that does not record its release cannot prove
// it binds the one asked about (2). Without it, `--release A` would wait on
// whatever the pointer names and report its success.
func checkFluxDeliveryWaitTarget(env string, opts envWaitOptions, got envWaitPromotionJSON) error {
	switch {
	case opts.PromotionID != "" && got.ID != opts.PromotionID:
		return exitCodeError{code: exitSuperseded, msg: fmt.Sprintf(
			"env %s's in-cluster Flux is pointed at promotion %s, not the %s this wait was asked about — it was overtaken",
			env, emptyAs(got.ID, "(none)"), opts.PromotionID)}
	case opts.Release != "" && got.ID != "" && got.Release == "":
		return exitCodeError{code: exitUndetermined, msg: fmt.Sprintf(
			"env %s's pointer (promotion %s) does not record which release it binds, so it cannot be proven to be %s; re-run: forge env deploy %s",
			env, got.ID, opts.Release, env)}
	case opts.Release != "":
		return checkWaitRelease(env, opts.Release, got)
	}
	return nil
}

// waitFluxDeliveryCurrent is `forge env status <env> --wait` for a flux env:
// the wait on whatever the live pointer names, refused first when that is not
// the promotion or release opts names. With opts.JSON it writes the same
// document every other `--wait` writes (envWaitReport) to out, with one row
// per Kustomization, and its progress to progress; otherwise both go to out
// as text.
func waitFluxDeliveryCurrent(ctx context.Context, env string, fd FluxDeliveryEntity, budget time.Duration, opts envWaitOptions, out, progress io.Writer) error {
	jsonOut := opts.JSON
	if !jsonOut {
		progress = out
	}
	notice := func(f string, a ...any) { fmt.Fprintf(progress, f, a...) }
	report := envWaitReport{Env: env, Workloads: []envWaitWorkloadJSON{}}
	started := time.Now().UTC()
	report.StartedAt = &started
	err := func() error {
		if err := checkFluxDeliveryWaitOptions(env, opts); err != nil {
			return err
		}
		live, err := fluxDeliveryPointerIO.read(ctx, fd)
		if err != nil {
			return exitCodeError{code: exitUndetermined, msg: err.Error()}
		}
		if live == nil {
			if opts.Release != "" || opts.PromotionID != "" {
				return exitCodeError{code: exitConflict, msg: fmt.Sprintf(
					"env %s's in-cluster Flux has never been pointed at a release, so %s was never delivered to it",
					env, emptyAs(opts.Release, "promotion "+opts.PromotionID))}
			}
			return exitCodeError{code: exitUndetermined, msg: fmt.Sprintf(
				"env %s's in-cluster Flux has never been pointed at a release (no %s/%s). Deploy one: forge env deploy %s",
				env, fd.Namespace, fluxdelivery.InputProviderName(env, ""), env)}
		}
		in, err := fluxdelivery.LiveInput(live)
		if err != nil {
			return exitCodeError{code: exitUndetermined, msg: err.Error()}
		}
		report.Promotion = &envWaitPromotionJSON{ID: in.Promotion, Release: live.GetAnnotations()[fluxDeliveryReleaseAnnotation]}
		if err := checkFluxDeliveryWaitTarget(env, opts, *report.Promotion); err != nil {
			return err
		}
		obs, err := waitFluxDeliveryObserved(ctx, env, report.Promotion.Release, fd, in, budget, fluxWaitPace{interval: opts.Interval, failFast: opts.FailFast}, notice)
		report.Workloads = fluxDeliveryWaitRows(obs, in.Source.Digest)
		if len(obs.Appliers) > 0 {
			report.Reason = obs.summary(in)
		}
		return err
	}()
	finished := time.Now().UTC()
	report.FinishedAt = &finished
	report.WaitedMS = finished.Sub(started).Milliseconds()
	report.Phase = fluxDeliveryWaitPhase(err)
	if !jsonOut {
		return err
	}
	report.stamp(err)
	if werr := writeJSONDocument(out, report); werr != nil && err == nil {
		return werr
	}
	return err
}

// fluxDeliveryWaitPhase is the report's phase for the wait's outcome, in the
// rollout phases' own words.
func fluxDeliveryWaitPhase(err error) string {
	switch {
	case err == nil:
		return "succeeded"
	case exitCodeForError(err) == exitTimedOut:
		return "progressing"
	case exitCodeForError(err) == exitUndetermined:
		return "unknown"
	}
	return "degraded"
}

// fluxDeliveryWaitRows is one report row per Kustomization: pinned to the
// pointer's digest, observed at what it last applied.
func fluxDeliveryWaitRows(obs fluxDeliveryObservation, digest string) []envWaitWorkloadJSON {
	rows := []envWaitWorkloadJSON{}
	for _, a := range obs.Appliers {
		row := envWaitWorkloadJSON{
			Name: a.Name, PinnedDigest: digest, ObservedDigest: revisionDigestOf(a.LastApplied),
			Reason: a.Reason, Phase: "progressing",
		}
		switch {
		case a.Found && a.Ready == "True" && a.Current && a.SpecMatches && row.ObservedDigest == digest:
			row.Phase = "succeeded"
		case a.Found && a.Ready == "False" && !fluxProgressing(a.Reason):
			row.Phase, row.LastError = "degraded", strings.TrimSpace(a.Message)
		}
		rows = append(rows, row)
	}
	return rows
}

// fluxRegistryBase is the registry subtree a flux env's bundles are pushed to,
// as this machine reaches it: FluxDelivery.registry when declared, else the
// registry of the env's first cluster workload image whose reference names a
// host, minus the repository's own name (`localhost:5050/acme/api:1` ->
// `localhost:5050/acme`), so the bundle sits beside the images it pins. Empty
// when neither names one; the caller refuses with what to declare rather than
// inventing a registry.
func fluxRegistryBase(entities *KCLEntities) string {
	if entities == nil {
		return ""
	}
	if fd := entities.FluxDelivery; fd != nil && strings.TrimSpace(fd.Registry) != "" {
		return strings.TrimSuffix(strings.TrimSpace(fd.Registry), "/")
	}
	for _, w := range entities.WorkloadsOn(RuntimeCluster) {
		repo := imageRepository(strings.TrimSpace(w.Spec.Image))
		if registryHost(repo) == "" {
			continue
		}
		if slash := strings.LastIndex(repo, "/"); slash > 0 {
			return repo[:slash]
		}
	}
	return ""
}

// clusterReachableRegistry rewrites a registry address the HOST pushes to into
// the one a POD pulls from. `localhost:<port>` and `127.0.0.1:<port>` resolve,
// inside a pod, to the pod itself; `host.k3d.internal` is the Docker host
// gateway alias forge keeps in every managed k3d cluster's CoreDNS, so it
// reaches the machine publishing the registry. (`registry.localhost` is the
// kubelet's mirror alias, resolved through the node's /etc/hosts, which a pod
// such as source-controller does not read.) Any other host is returned
// unchanged: a real registry has one name everywhere.
func clusterReachableRegistry(ref string) string {
	host, rest, hasPath := strings.Cut(strings.TrimSpace(ref), "/")
	hostOnly, port, hasPort := strings.Cut(host, ":")
	switch hostOnly {
	case "localhost", "127.0.0.1", "registry.localhost":
		rewritten := k3dHostGatewayAlias
		if hasPort {
			rewritten += ":" + port
		}
		if hasPath {
			return rewritten + "/" + rest
		}
		return rewritten
	}
	return ref
}

// insecureRegistry reports whether a bundle repository is served over plain
// HTTP. A PREDICATE OVER THE ADDRESS, never a flag: `insecure: true` downgrades
// the pull's transport, so only addresses no certificate could cover produce
// it — loopback, `*.localhost` (reserved, RFC 6761) and the k3d host gateway
// alias. A name's prefix is not such evidence: `k3d-registry.example.com` is
// an ordinary public host that can hold a certificate, and keeps TLS.
func insecureRegistry(ref string) bool {
	host, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimSpace(ref), "oci://"), "/")
	hostOnly, _, _ := strings.Cut(host, ":")
	switch {
	case hostOnly == "localhost", hostOnly == "127.0.0.1", hostOnly == k3dHostGatewayAlias:
		return true
	case strings.HasSuffix(hostOnly, ".localhost"):
		return true
	}
	return false
}
