// File: internal/cli/env_shape.go
//
// `forge env shape <env>` — the env's rendered DECLARATION, as a value.
//
// This is the projection half of `forge env render`. Where render prints the
// manifests themselves — two megabytes of YAML for control-plane's prod —
// this prints what they ARE: the env's kind, what runs where, which secrets
// it needs, which domains it binds, and one hash per object. Small enough to
// store per environment, read per view, and diff as a set.
//
// WHY IT IS A COMMAND AND NOT ONLY AN INTERNAL STEP. Three callers need the
// same answer, and all three must get the same bytes:
//
//   - `forge env build` and `forge env deploy` RECORD it on the control
//     plane, which is what lets the Live view answer "what kind, which
//     secrets, which provider" with no checkout and no daemon running;
//   - the reliant daemon exposes this command as `forge.env_shape`, which is
//     what Preview's "Register" sends to EnsureEnvironment to bootstrap an
//     env the control plane has never heard of;
//   - F7's `forge env diff` compares this projection against the recorded
//     one.
//
// They share internal/bundle.ProjectShape, so a shape a user read, a shape
// the control plane stored, and a shape a bundle sealed cannot disagree about
// the same render.
//
// READ-ONLY, AND WATCHED RATHER THAN PROMISED. Like `forge env render`, this
// cannot guarantee purity — KCL evaluates `file.write` during evaluation and
// forge has no hook to suppress a project's own writes. So it does what
// render does and goes one step further: it scans the tree before and after,
// and a render that wrote anything is an ERROR naming the paths, not a
// warning. A caller registering an environment from this output is making a
// durable record, and a record derived from an impure render is a record
// nobody can reproduce.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/pkg/release"
)

// envShapeDoc is what the command prints, and what the daemon's
// `forge.env_shape` hook returns.
//
// Project and Env are carried alongside the shape rather than left implicit,
// because the consumer is addressing a control-plane environment, whose
// identity is (org, project, name) — a shape with no project is a shape
// nobody can record. Kind is duplicated out of the shape for the same
// reason a header exists: it is the one field a reader branches on.
type envShapeDoc struct {
	Project string `json:"project"`
	Env     string `json:"env"`
	Kind    string `json:"kind"`
	// Lifecycle is WHO APPLIES this env, as the env itself declares it
	// (Bundle.lifecycle): "local", "ephemeral", or absent for a real
	// environment reconciled from a bundle.
	//
	// It is NOT Kind, and the two answer different questions: Kind is
	// DERIVED from what the env hosts (persistent / preview /
	// self_managed / local), while this is DECLARED and is about the
	// apply path. An env can be Kind local and still be a real
	// environment, which is exactly why the derived field could not be
	// reused here. Omitted when unset, so the absence is the default.
	Lifecycle  string             `json:"lifecycle,omitempty"`
	Shape      release.Shape      `json:"shape"`
	Provenance release.Provenance `json:"provenance"`

	// manifests and images are the render the shape was projected FROM:
	// the annotated stream (`# cluster:` headers included) and the digests
	// it pinned. Unexported, so they are not part of the `--json` contract
	// — a reader of `forge env shape` wants the projection, not two
	// megabytes of YAML.
	//
	// THEY ARE CARRIED because `forge env build` writes a BUNDLE from the
	// same render, and bundle.Build re-projects the objects from the stream
	// it packages rather than accepting a finished shape. Without them the
	// build would have to render a second time, and a bundle whose shape
	// came from render A while its manifest layer came from render B is
	// precisely the "what was applied is not what was recorded" failure
	// the bundle exists to close. One render, both halves.
	manifests string
	// syncedSecrets names the Secrets forge supplies from the env's secret
	// store rather than the render; names only, and they ride the bundle
	// document so a deploy knows what to sync.
	syncedSecrets []release.BundleSecretRef
	images        map[string]string
	// units are the env's declared delivery units, for the bundle's trees.
	units []release.DeliveryUnit
	// hostedPlaceholder is true when the hosted records were planned over
	// PLACEHOLDER digests because no release pins them yet. Fine for a shape
	// a human reads; a bundle must never seal it.
	hostedPlaceholder bool
}

// reconciled reports whether a reconciler may apply the WHOLE of this env's
// bundle: it declares no local or ephemeral lifecycle and no applier, and it
// does not split its delivery units between the hub and forge. Such a bundle is
// applied repeatedly and pruned, so it must be safe to (see
// release.CheckFluxSafety).
//
// An env that DOES split its units is not wholly reconciled, but its hub units
// are: reconciledUnits names them, and the build refuses unsafe objects in
// exactly those, so the first unit to move onto Flux is neither held up by an
// unsafe object in one that has not, nor allowed to carry its own.
func (d envShapeDoc) reconciled() bool {
	return d.Lifecycle == "" && d.Shape.Applier == "" && len(d.Shape.Units) == 0
}

// reconciledUnits are the delivery units the hub converges, from the shape's
// recorded split. Empty for an env that declares none.
func (d envShapeDoc) reconciledUnits() []string {
	if d.Lifecycle != "" || d.Shape.Applier != "" {
		return nil
	}
	var out []string
	for _, u := range d.Shape.Units {
		if u.Applier == release.UnitApplierHub {
			out = append(out, u.Name)
		}
	}
	return out
}

func newEnvShapeCmd() *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "shape <environment>",
		Short: "Print what an environment DECLARES — kind, workloads, secrets, domains, and one hash per object",
		Args:  cobra.ExactArgs(1),
		Long: `Print the projection of deploy/kcl/<env>/'s render: what the environment is,
rather than the manifests it produces.

` + "`" + Name() + ` env render` + "`" + ` prints the objects themselves — megabytes of YAML for a
real environment. This prints the SHAPE: the environment's kind (persistent,
self-managed or local, derived from what it binds), each workload with its
runtime and cluster, each declared secret by NAME with its provider, the
domains it binds, the clusters it deploys to, and one hash per rendered
object. It is small enough to store per environment and compare as a set,
which is what makes "what changed" answerable without re-rendering anything.

This is the SAME projection ` + "`" + Name() + ` env build` + "`" + ` records on the control plane, so
what you read here is what a Live view shows and what a deploy plan is
computed against.

IT CARRIES NO SECRET VALUE, ever. Declared secrets appear as names and
providers. A rendered ` + "`" + `kind: Secret` + "`" + ` has every value replaced by the hash of
that value before the object is hashed, so a changed secret is still visible
while the value itself is not recorded.

READ-ONLY, AND CHECKED. No cluster is contacted, no image is built, nothing
is pushed. forge cannot promise the render is side-effect-free, because KCL
evaluates ` + "`" + `file.write` + "`" + ` — so this scans the project before and after, and a
render that wrote anything FAILS, naming the paths. A declaration derived
from an impure render is one nobody can reproduce.

Examples:
  ` + Name() + ` env shape prod               # the human summary
  ` + Name() + ` env shape prod --json        # {project, env, kind, shape, provenance}`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEnvShape(cmd, args[0], asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print {project, env, kind, shape, provenance} as JSON (the form the reliant daemon's forge.env_shape hook returns)")
	return cmd
}

// runEnvShape renders the env and prints its declaration. STDOUT CARRIES ONLY
// THE DOCUMENT in --json mode, for the same reason `forge env render` guards
// its stream: the render path prints notes with fmt.Printf in dozens of
// places, and one prose line on stdout makes the JSON unparseable. Diverting
// os.Stdout makes that structural rather than a property of every callee.
func runEnvShape(cmd *cobra.Command, envName string, asJSON bool) error {
	out := cmd.OutOrStdout()
	realStdout := os.Stdout
	os.Stdout = os.Stderr
	defer func() { os.Stdout = realStdout }()

	doc, err := projectEnvShapeFn(cmd.Context(), cmd.ErrOrStderr(), envName)
	if err != nil {
		return err
	}
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(doc)
	}
	writeShapeSummary(out, doc)
	return nil
}

// projectEnvShapeFn is the projection every caller goes through, as a var so
// the tests that pin the WIRE — which call carries which fields, in which
// order — can supply a fixed shape instead of evaluating KCL. The projection
// itself is tested in internal/bundle, from a literal fixture.
var projectEnvShapeFn = projectEnvShape

// projectEnvShape is the shared projection: render the env read-only, refuse
// an impure render, and project it.
//
// `forge env shape`, `forge env build` and `forge env deploy` all come
// through here, which is the point — the declaration a build records and the
// declaration a user reads are produced by one function, from one render.
func projectEnvShape(ctx context.Context, errOut io.Writer, envName string) (envShapeDoc, error) {
	projectDir := projectDirForKCL()

	// Pin FORGE'S OWN derived state before scanning, so the purity verdict
	// below is about the PROJECT's render and not about forge's tooling.
	// pinFile reverts a path only if the render actually moved it, so an
	// untouched file keeps even its mtime and stays out of the report as a
	// phantom write.
	//
	// Two paths qualify. The resolve_port store is the one `forge env
	// render` already pins. kcl.mod.lock is written by kpm — which forge
	// invokes to resolve the KCL module, and which writes the lock on
	// every resolve — so it appears on the FIRST render of any project and
	// would make this command refuse every env it had not already
	// rendered once. It is derived state by forge's own account
	// (kcl/embed.go), and a declaration's reproducibility does not turn on
	// it.
	//
	// The scan is armed AFTER the pins and BEFORE the dev-stack
	// activation, because deciding the render's purpose is itself a
	// render, and any file.write it fires belongs in the report.
	unpin := pinPaths(
		filepath.Join(projectDir, ".forge", "ports-"+envName+".json"),
		filepath.Join(projectDir, "deploy", "kcl", "kcl.mod.lock"),
	)
	scan := newRenderWriteScan(projectDir, false)
	activateDevStack(ctx, projectDir, envName, renderDeclaration, inspectBlocks)
	defer func() {
		unpin()
		scan.report(errOut, envName)
		reportDeclinedWrites(errOut, kclplugin.SuppressedWrites())
	}()

	doc, err := renderEnvShape(ctx, errOut, projectDir, envName)
	if err != nil {
		return envShapeDoc{}, err
	}

	// The write verdict, taken after the render and before the caller can
	// act on the document. `forge env render` makes this opt-in
	// (--fail-on-write) because its job is to show you the objects even on
	// a project that writes files. A DECLARATION is different: it is
	// recorded, and a record derived from a render nobody can reproduce is
	// worse than no record.
	unpin()
	if changes := scan.changes(); len(changes) > 0 {
		return envShapeDoc{}, fmt.Errorf("refusing to project env %q: the render wrote %d file(s) (see the report on stderr), so this declaration is not reproducible.\n"+
			"  KCL evaluates file.write during evaluation and forge cannot suppress a project's own writes.\n"+
			"  fix: move the generated file out of the render (forge's fp.write_file is materialized by `forge env up`, not by a render)",
			envName, len(changes))
	}
	return doc, nil
}

// renderEnvShape is the render itself: resolve the env's tag and digests the
// way a deploy would, render the manifests, attribute each document to its
// cluster(s) through the deploy path's own router, and project the result.
func renderEnvShape(ctx context.Context, errOut io.Writer, projectDir, envName string) (envShapeDoc, error) {
	// The project config supplies two facts, and BOTH have a fallback, so a
	// config that does not load is not fatal here. That is deliberate:
	// `forge env build` has always worked against an incomplete forge.yaml,
	// and making the declaration the thing that newly refuses it would
	// break a working command for a reason unrelated to what the user
	// asked for. The project NAME is resolved separately, through
	// hostedProjectName, which tolerates the same failure.
	kclDir, fallbackNamespace := "deploy/kcl", envName
	if store, err := loadProjectStore(); err == nil {
		if declared := store.K8s().KCLDir; declared != "" {
			kclDir = declared
		}
		fallbackNamespace = store.Meta().Name + "-" + envName
	}
	mainK := filepath.Join(kclDir, envName, "main.k")
	if _, serr := os.Stat(mainK); serr != nil {
		envs, _ := ListEnvs(projectDir)
		return envShapeDoc{}, fmt.Errorf("environment %q not found: %s does not exist (declared environments: %s)",
			envName, mainK, strings.Join(envs, ", "))
	}

	entities, kerr := RenderKCL(ctx, projectDir, envName)
	if kerr != nil {
		return envShapeDoc{}, fmt.Errorf("render deploy/kcl/%s/: %w", envName, kerr)
	}

	namespace := k8sClusterFieldFromEntities(entities, "namespace")
	if namespace == "" {
		namespace = fallbackNamespace
	}
	imageTag, _ := renderImageTag(ctx, projectDir, envName, "")

	// The digests a deploy would pin. Unresolvable digests are NOT fatal
	// here, for the reason `forge env render` gives: pinning is an
	// optimisation of which bytes ship, not of what the env declares, and a
	// shape on the mutable tag is more use to a reader than no shape. The
	// per-object ConfigHash is unaffected either way — it normalizes
	// release-bound digests away by construction.
	var digests map[string]string
	if compositionSnapshotFrom(ctx) == nil {
		if ledger, lerr := ledgerFor(ctx, projectDir, envName); lerr == nil {
			if d, _, derr := resolveDeployDigests(ctx, projectDir, envName, false, ledger.Bindings, ledger.Releases); derr == nil {
				digests = d
			} else {
				fmt.Fprintf(errOut, "warning: image digests unresolved (%v) — projecting on tag %q instead\n", derr, imageTag)
			}
		}

	}
	manifests, rerr := cluster.RenderManifests(ctx, mainK, imageTag, namespace, envName, loadDeployEnvConfigKV(projectDir, envName), digests)
	if rerr != nil {
		return envShapeDoc{}, fmt.Errorf("render %s: %w", mainK, rerr)
	}
	groups, gerr := buildDeployGroupsForEnv(envName, entities, namespace, imageTag, false)
	if gerr != nil {
		return envShapeDoc{}, gerr
	}
	objects, clusters := attributeRenderedObjects(manifests, groups, entities)

	// THE HOSTED TIERS RIDE THE BUNDLE. A hosted workload renders no object
	// in the env's own manifests (the control plane renders it), so without
	// this a hosted env's bundle would be an empty tree that Flux applies as
	// "nothing" and reports Ready. They are added as the forge.dev records
	// the platform admits, under one well-known cluster path.
	hosted, placeholder, herr := hostedBundleObjects(ctx, projectDir, envName, entities)
	if herr != nil {
		return envShapeDoc{}, herr
	}
	if len(hosted) > 0 {
		objects = append(objects, hosted...)
		clusters = append(clusters, release.BundleHostedCluster)
		sort.Strings(clusters)
	}

	// Helm charts are deliberately NOT templated. A declaration must be
	// derivable with no network and no `helm` on PATH — Preview's Register
	// runs on whatever machine the daemon is on — and a chart's objects are
	// the platform dependency's, not this project's declaration. `forge env
	// render --charts` is where they belong.
	stream := renderShapeStream(objects)
	syncedSecrets, serr := envSyncedSecretRefs(entities, groups, namespace, stream)
	if serr != nil {
		return envShapeDoc{}, fmt.Errorf("project env %q: %w", envName, serr)
	}
	alerts, alertRoutes, aerr := resolveShapeAlerts(projectDir, envName, entities)
	if aerr != nil {
		return envShapeDoc{}, fmt.Errorf("project env %q: %w", envName, aerr)
	}
	store, storeErr := shapeStoreSecretsOf(entities)
	if storeErr != nil {
		return envShapeDoc{}, fmt.Errorf("project env %q: %w", envName, storeErr)
	}
	split, splitErr := unitSplitOf(entities)
	if splitErr != nil {
		return envShapeDoc{}, fmt.Errorf("project env %q: %w", envName, splitErr)
	}
	shape, err := bundle.ProjectShape(bundle.ShapeInput{
		Kind:              shapeEnvKindOf(entities),
		Workloads:         shapeWorkloadsOf(entities),
		Secrets:           shapeSecretsOf(entities),
		Domains:           shapeDomainsOf(entities),
		Clusters:          clusters,
		Manifests:         stream,
		Images:            digests,
		StatefulWorkloads: shapeStatefulWorkloadsOf(entities),

		ControlPlaneSecrets: shapeControlPlaneSecretsOf(entities),
		Alerts:              alerts,
		AlertRoutes:         alertRoutes,
		ObservabilityPolicy: shapeObservabilityPolicyOf(entities),
		FrontendTelemetry:   shapeFrontendTelemetryOf(entities),
		DeliveryPolicy:      shapeDeliveryPolicyOf(entities),
		DataAccess:          shapeDataAccessOf(entities),
		ReleaseHealth:       shapeReleaseHealthOf(entities),
		Identity:            shapeIdentityOf(entities),
		Applier:             shapeApplierOf(entities),
		Units:               split.ShapeUnits(),
		Store:               store,
	})
	if err != nil {
		return envShapeDoc{}, fmt.Errorf("project env %q: %w", envName, err)
	}
	return envShapeDoc{
		Project:           hostedProjectName(),
		Env:               envName,
		Kind:              string(shape.Kind),
		Lifecycle:         entities.Lifecycle,
		Shape:             shape,
		Provenance:        captureBuildProvenance(ctx, projectDir).ForHosted(),
		manifests:         stream,
		syncedSecrets:     syncedSecrets,
		units:             entities.DeliveryUnits,
		images:            digests,
		hostedPlaceholder: placeholder,
	}, nil
}

// pinPaths pins several paths at once and returns one revert for all of
// them, idempotent so the caller can take the write verdict early and still
// revert in a defer.
func pinPaths(paths ...string) func() {
	unpins := make([]func(), 0, len(paths))
	for _, path := range paths {
		unpins = append(unpins, pinFile(path))
	}
	var done bool
	return func() {
		if done {
			return
		}
		done = true
		for _, unpin := range unpins {
			unpin()
		}
	}
}

// renderShapeStream is the annotated stream internal/bundle parses: the same
// bytes `forge env render` prints, header included.
//
// Handing the PRINTED form across the package boundary rather than a parsed
// structure is what keeps the routing in one place. internal/bundle has no
// opinion about which cluster a document lands on; it reads the answer that
// internal/cluster's own router computed. A second model of those rules in
// the projection would eventually disagree with the deploy, and a shape that
// disagrees with the deploy describes a deploy that never happens.
func renderShapeStream(objects []renderedObject) string {
	var b strings.Builder
	if err := writeRenderedStream(&b, objects); err != nil {
		// writeRenderedStream only fails on its writer, and a
		// strings.Builder cannot fail.
		panic("render the shape stream: " + err.Error())
	}
	return b.String()
}

// ─── The declaration half of the projection ─────────────────────────────────

// shapeEnvKindOf is the env's kind in pkg/release vocabulary. An env that
// declares no control plane is still LOCAL to a shape: the file ledger (F3)
// records it, and a shape has to say how the env runs whoever stores it.
func shapeEnvKindOf(e *KCLEntities) release.EnvKind {
	if name := hostedControlPlaneKindName(hostedEnvKindOf(e)); name != "" {
		return release.EnvKind(name)
	}
	if runsOnOwnCluster(e) {
		return release.EnvSelfManaged
	}
	return release.EnvLocal
}

// shapeWorkloadsOf projects every workload: its name, the runtime it resolved
// to, the cluster it lands on, the release artifact its image comes from, and
// its replica count.
//
// EVERY workload, of every runtime — host and build-only included. "which
// workloads does this env declare" is the question, and a projection that
// listed only the cluster ones would describe control-plane's dev, where most
// services run on the host, as nearly empty.
func shapeWorkloadsOf(e *KCLEntities) []release.ShapeWorkload {
	if e == nil {
		return nil
	}
	out := make([]release.ShapeWorkload, 0, len(e.Workloads)+len(e.Frontends)+len(e.Databases))
	for i := range e.Workloads {
		w := &e.Workloads[i]
		item := release.ShapeWorkload{Name: w.Name, Runtime: w.Runtime.Type, Artifact: w.Image}
		if c := w.Runtime.Cluster; c != nil {
			item.Cluster = c.Cluster
		}
		if r := w.Spec.Replicas; r > 0 {
			replicas := int(r)
			item.Replicas = &replicas
		}
		out = append(out, item)
	}
	for _, f := range e.Frontends {
		out = append(out, release.ShapeWorkload{Name: f.Name, Runtime: f.Runtime.Type, Artifact: f.Name})
	}
	// A managed database is a runnable thing the env declares, so it is a
	// workload in the shape even though it is not a WorkloadEntity: it
	// occupies a cluster, it holds data, and a reader asking "what does
	// this env run" must be told about it.
	for _, d := range e.Databases {
		out = append(out, release.ShapeWorkload{Name: d.Name, Runtime: d.Runtime, Cluster: d.Cluster})
	}
	return out
}

// shapeSecretsOf projects the env's declared secrets: NAMES, their provider,
// and which workloads read each one.
//
// This is what #353's set-secret form reads, which is why declared_by matters
// — "DATABASE_URL, needed by admin-server and the workers" is the sentence a
// user needs when the form asks them for a value, and it is a fact only the
// render knows.
//
// Keyed on the SECRET name (ev.SecretRef), not the env-var name: one Secret
// typically carries several keys, and the provider stores a Secret.
func shapeSecretsOf(e *KCLEntities) []release.ShapeSecret {
	if e == nil {
		return nil
	}
	provider := shapeSecretProviderOf(e)
	readers := map[string]map[string]bool{}
	for i := range e.Workloads {
		w := &e.Workloads[i]
		for _, ref := range secretRefsForService(w) {
			if readers[ref.SecretName] == nil {
				readers[ref.SecretName] = map[string]bool{}
			}
			readers[ref.SecretName][w.Name] = true
		}
	}
	// A declared external prerequisite is part of the env's secret surface
	// even when no workload in this render reads it: forge does not create
	// it, and "this env needs a Secret nobody has set" is exactly what a
	// declaration exists to say.
	for _, req := range e.RequiredSecrets {
		if readers[req.Name] == nil {
			readers[req.Name] = map[string]bool{}
		}
	}
	names := make([]string, 0, len(readers))
	for name := range readers {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]release.ShapeSecret, 0, len(names))
	for _, name := range names {
		declaredBy := make([]string, 0, len(readers[name]))
		for workload := range readers[name] {
			declaredBy = append(declaredBy, workload)
		}
		sort.Strings(declaredBy)
		out = append(out, release.ShapeSecret{Name: name, Provider: provider, DeclaredBy: declaredBy})
	}
	return out
}

// shapeControlPlaneSecretsOf projects the Secrets the env's control plane
// supplies and renews: a destination and a kind per Secret, and the keys the
// control plane writes. No value exists on forge's side to project.
//
// They are NOT part of shapeSecretsOf, deliberately. That list is "what a
// person must provide", and drives the set-secret form and the plan's presence
// check; a Secret the control plane delivers would show up there as one nobody
// has set. Its own list says what it is.
func shapeControlPlaneSecretsOf(e *KCLEntities) []release.ShapeControlPlaneSecret {
	if e == nil || len(e.ControlPlaneSecrets) == 0 {
		return nil
	}
	out := make([]release.ShapeControlPlaneSecret, 0, len(e.ControlPlaneSecrets))
	for _, c := range e.ControlPlaneSecrets {
		out = append(out, release.ShapeControlPlaneSecret{
			Name:             c.Name,
			Namespace:        c.Namespace,
			Cluster:          c.Cluster,
			ConnectedCluster: c.ConnectedCluster,
			Kind:             c.Kind,
			Keys:             c.Keys,
			Delivery:         c.Delivery,
			Required:         c.Required,
		})
	}
	return out
}

// shapeObservabilityPolicyOf projects what the env opted OUT of. Nil when the
// env declares no observability or opts out of nothing: the shape then encodes
// as it did before the field, and absence means "everything on".
func shapeObservabilityPolicyOf(e *KCLEntities) *release.ShapeObservabilityPolicy {
	if e == nil || e.Observability == nil || e.Observability.Policy == nil {
		return nil
	}
	p := e.Observability.Policy
	retention := shapeRetentionOf(e)
	if p.Enabled && len(p.Disabled) == 0 && len(p.OptIn) == 0 && retention == nil {
		return nil
	}
	return &release.ShapeObservabilityPolicy{Enabled: p.Enabled, Disabled: p.Disabled, OptIn: p.OptIn, Retention: retention}
}

// shapeRetentionOf is the browser-telemetry retention the env declared, or nil
// when it keeps every default (the shape then encodes as it did before the
// field).
func shapeRetentionOf(e *KCLEntities) *release.ShapeRetention {
	if e == nil || e.Observability == nil {
		return nil
	}
	r := e.Observability.Retention
	// The schema never renders 0 (each is checked >= 1), so a 0 is a render
	// that predates the field, and means the default.
	orDefault := func(days, def int) int {
		if days == 0 {
			return def
		}
		return days
	}
	ret := &release.ShapeRetention{
		ReplayDays:        orDefault(r.ReplayDays, release.DefaultReplayDays),
		ErrorsDays:        orDefault(r.ErrorsDays, release.DefaultErrorsDays),
		ProductEventsDays: orDefault(r.ProductEventsDays, release.DefaultProductEventsDays),
	}
	if ret.IsDefault() {
		return nil
	}
	return ret
}

// shapeFrontendTelemetryOf projects what each frontend declares about its
// browser telemetry. A frontend that declares nothing beyond the defaults
// contributes nothing.
func shapeFrontendTelemetryOf(e *KCLEntities) []release.ShapeFrontendTelemetry {
	if e == nil {
		return nil
	}
	var out []release.ShapeFrontendTelemetry
	for _, f := range e.Frontends {
		t := f.Telemetry
		if t == nil {
			continue
		}
		item := release.ShapeFrontendTelemetry{
			Frontend:     f.Name,
			Sourcemaps:   t.Sourcemaps,
			ErrorsIgnore: t.Errors.Ignore,
		}
		for _, fn := range t.Funnels {
			item.Funnels = append(item.Funnels, release.ShapeFunnel{ID: fn.ID, Title: fn.Title, Steps: fn.Steps, Window: fn.Window, By: fn.By})
		}
		if !item.IsDefault() {
			out = append(out, item)
		}
	}
	return out
}

// shapeDeliveryPolicyOf projects the env's delivery policy. Nil when the env
// declares none or declares only defaults: the shape then encodes as it did
// before the field, and absence means "every control at its default".
func shapeDeliveryPolicyOf(e *KCLEntities) *release.ShapeDeliveryPolicy {
	if e == nil || e.DeliveryPolicy == nil || e.DeliveryPolicy.IsDefault() {
		return nil
	}
	return e.DeliveryPolicy
}

// shapeDataAccessOf projects the env's data access. Nil when it is the default
// (audited): the shape then encodes as it did before the field, and absence
// means "audited", never "open". A local or ephemeral env resolves to open in
// the render, so it records that.
func shapeDataAccessOf(e *KCLEntities) *release.ShapeDataAccess {
	if e == nil || e.DataAccess.IsDefault() {
		return nil
	}
	return e.DataAccess
}

// shapeReleaseHealthOf projects the env's release-health policy. Nil when the
// env declares none or only the defaults, so the shape encodes as it did
// before the field.
func shapeReleaseHealthOf(e *KCLEntities) *release.ShapeReleaseHealth {
	if e == nil || e.ReleaseHealth == nil || e.ReleaseHealth.IsDefault() {
		return nil
	}
	return e.ReleaseHealth
}

// shapeIdentityOf projects the env's managed identity. The render already
// resolved it into the shape's own vocabulary (derived applications and
// consumers, the deliveries, the reserved managed-secret names), so this is a
// pass-through that exists to keep the one place a nil means "none declared".
func shapeIdentityOf(e *KCLEntities) *release.ShapeIdentity {
	if e == nil {
		return nil
	}
	return e.Identity
}

// shapeApplierOf projects the env's declared applier. Only a DECLARATION is
// recorded: an env that declares none stays empty, so "undeclared" and "forge
// applies it on purpose" remain different facts a reader can tell apart.
func shapeApplierOf(e *KCLEntities) string {
	if e != nil && e.Applier == applierForge {
		return release.ApplierForge
	}
	return ""
}

// shapeSecretProviderOf is WHERE this env's secret values come from, in
// forge's provider vocabulary. "hosted" is normalized to the control plane's
// own word for it so a reader does not have to know forge's spelling.
func shapeSecretProviderOf(e *KCLEntities) string {
	if e == nil || e.SecretProvider == nil {
		return ""
	}
	return strings.ToLower(e.SecretProvider.Type)
}

// shapeDomainsOf is every hostname the env binds: a Gateway's own host, each
// of its listeners' hostname overrides, and every route's override. Sorted
// and de-duplicated by Shape.Canonical.
func shapeDomainsOf(e *KCLEntities) []string {
	if e == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(host string) {
		if host != "" && !seen[host] {
			seen[host] = true
			out = append(out, host)
		}
	}
	for _, g := range e.Gateways {
		add(g.Host)
		for _, l := range g.Listeners {
			add(l.Hostname)
		}
	}
	for _, r := range e.HTTPRoutes {
		add(r.Host)
	}
	for _, r := range e.GRPCRoutes {
		add(r.Host)
	}
	return out
}

// shapeStatefulWorkloadsOf names the workloads whose objects hold data their
// KIND does not announce — the env's declared ManagedDatabases, each of which
// expands into a Postgres cluster plus its Services and volumes, all stamped
// with the database's name as their workload.
//
// The well-known stateful kinds (PVC, StatefulSet, Secret, Namespace, CRD)
// are recognised by pkg/release without being listed. This covers the case
// that would otherwise be invisible: a CloudNativePG `Cluster` is a custom
// resource like any other, and deleting it takes the volumes with it.
func shapeStatefulWorkloadsOf(e *KCLEntities) []string {
	if e == nil {
		return nil
	}
	out := make([]string, 0, len(e.Databases))
	for _, d := range e.Databases {
		out = append(out, d.Name)
	}
	return out
}

// ─── Recording ──────────────────────────────────────────────────────────────

// recordEnvDeclaration ensures the env on its control plane WITH its rendered
// shape and the provenance of that render.
//
// THIS IS THE WRITE THE WHOLE SLICE EXISTS FOR. After it, the control plane
// knows what the project declares for this env — so the Live view answers
// "what kind, which secrets, which provider" with no checkout, no daemon and
// no render, and a never-deployed env is "declared, not built yet" rather
// than absent.
//
// It runs BEFORE anything is built, pushed or cut, deliberately: a build that
// fails half-way has still told the control plane what the env is, which is
// the fact every other surface needs, and it costs one idempotent RPC.
//
// An env that declares no control plane records nothing and returns no error.
// That is not a failure — its ledger is the file ledger (F3), and refusing
// here would make `forge env build` depend on a control plane that a purely
// local env has no reason to have.
func recordEnvDeclaration(ctx context.Context, envName string, entities *KCLEntities) error {
	if entities == nil || entities.ControlPlane == nil {
		return nil
	}
	client, endpoint, err := envDeclarationClient(envName, entities)
	if err != nil {
		return err
	}
	doc, err := projectEnvShapeFn(ctx, os.Stderr, envName)
	if err != nil {
		// A PROJECTION failure is loud but not fatal, and the asymmetry
		// with the recording below is the point. The caller asked to
		// BUILD; refusing that because the env could not be projected —
		// a project whose KCL writes a file during render, an env this
		// forge cannot render for an unrelated reason — would break a
		// working command over a declaration nobody requested. A
		// REFUSED shape is different: that means the server read it and
		// said no, and continuing would leave every Live surface on a
		// stale declaration behind a green build.
		fmt.Printf("[declare] Warning: env %s's declaration was not recorded (%v).\n"+
			"[declare]   The build continues; `forge env shape %s` reproduces this.\n", envName, err, envName)
		return nil
	}
	ref := hostedEnvRefFor(envName, entities)
	ref.Shape, ref.DeclaredBy = &doc.Shape, &doc.Provenance
	// The bindings ride the SAME ensure as the shape, for the same reason:
	// this is the one write that happens after a render, so it is the only
	// one that knows which clusters the bundle rendered for.
	bindings, err := resolveClusterBindings(ctx, client, entities)
	if err != nil {
		return err
	}
	ref.ClusterBindings = bindings
	// WHO APPLIES each delivery unit, by the same split ledgerForEntities
	// uses to choose between applying from here and following the hub. The
	// control plane converges a unit's trees only when this says so; without
	// it, a mixed env (control-plane prod: bound clusters plus a Firebase
	// frontend) got hub Kustomizations over the very objects this machine
	// applies — two appliers, one of which would revert the other once it
	// gained the permissions to try.
	declared, serr := unitSplitOf(entities)
	if serr != nil {
		return fmt.Errorf("env %q: %w", envName, serr)
	}
	ref.HubConverged, ref.HubConvergedUnits = hubConvergedWire(declared)
	if err := declareHostedEnv(ctx, client, ref); err != nil {
		// F-15, stated loudly. A control plane that predates the shape
		// IGNORES the field (connect's JSON codec discards unknown
		// fields), so there is nothing to special-case for an old
		// server. An InvalidArgument, though, means the server read the
		// shape and REFUSED it — and silently continuing would leave
		// every Live surface reading a stale declaration while the
		// build reported success.
		return fmt.Errorf("record env %q's declaration on the control plane at %s: %w", envName, endpoint, err)
	}
	fmt.Printf("[declare] env %s: %s, %d workload(s), %d secret(s), %d object(s) recorded at %s\n",
		envName, doc.Kind, len(doc.Shape.Workloads), len(doc.Shape.Secrets), len(doc.Shape.Objects), endpoint)
	return nil
}

// envDeclarationClient resolves the control-plane client for an env that
// declares one, from the env's own declaration — the same resolution the
// hosted ledger and the hosted deploy perform.
//
// A var so a test can point the declaration path at a fake caller. It
// resolves its own client rather than reusing the ledger's, because the
// declaration is recorded for an env that may have no ledger yet: a
// never-built env's whole purpose here is to become known BEFORE anything
// else exists for it.
var envDeclarationClient = func(envName string, entities *KCLEntities) (cloudCaller, string, error) {
	decl := declarationFromEntities(entities)
	ep, err := cloud.ResolveEndpoint(envName, decl)
	if err != nil {
		return nil, "", err
	}
	cred, err := cloud.ResolveCredential("", ep)
	if err != nil {
		return nil, "", fmt.Errorf("env %q records its declaration on the control plane at %s: %w", envName, ep.URL, err)
	}
	return cloud.NewClient(ep, cred), ep.URL, nil
}

// ─── Text rendering ─────────────────────────────────────────────────────────

// writeShapeSummary is the human form: the few lines someone reads to answer
// "what is this environment" before deciding anything.
func writeShapeSummary(w io.Writer, doc envShapeDoc) {
	fmt.Fprintf(w, "env %s (project %s): %s\n", doc.Env, emptyAs(doc.Project, "(unnamed)"), doc.Kind)
	fmt.Fprintf(w, "  clusters:  %s\n", describeClusters(doc.Shape.Clusters))
	fmt.Fprintf(w, "  workloads: %d\n", len(doc.Shape.Workloads))
	for _, workload := range doc.Shape.Workloads {
		where := workload.Runtime
		if workload.Cluster != "" {
			where += " on " + workload.Cluster
		}
		fmt.Fprintf(w, "    %s (%s)\n", workload.Name, where)
	}
	if len(doc.Shape.Secrets) > 0 {
		fmt.Fprintf(w, "  secrets:   %d (names only; no value is ever recorded)\n", len(doc.Shape.Secrets))
		for _, secret := range doc.Shape.Secrets {
			line := secret.Name
			if secret.Provider != "" {
				line += " [" + secret.Provider + "]"
			}
			if len(secret.DeclaredBy) > 0 {
				line += " ← " + strings.Join(secret.DeclaredBy, ", ")
			}
			fmt.Fprintf(w, "    %s\n", line)
		}
	}
	if doc.Shape.Applier != "" {
		fmt.Fprintf(w, "  applied by: %s (declared: Bundle.applier)\n", doc.Shape.Applier)
	}
	if len(doc.Shape.ControlPlaneSecrets) > 0 {
		fmt.Fprintf(w, "  delivered by the control plane: %d (names only; the control plane mints and renews them)\n", len(doc.Shape.ControlPlaneSecrets))
		for _, c := range doc.Shape.ControlPlaneSecrets {
			fmt.Fprintf(w, "    %s/%s [%s] on %s via %s\n", c.Namespace, c.Name, c.Kind, c.Cluster, c.ConnectedCluster)
		}
	}
	if id := doc.Shape.Identity; id != nil {
		writeIdentitySummary(w, id)
	}
	if len(doc.Shape.Alerts) > 0 {
		user := 0
		for _, a := range doc.Shape.Alerts {
			if a.Source == release.AlertSourceUser {
				user++
			}
		}
		fmt.Fprintf(w, "  alerts:    %d (%d yours, %d generated defaults), %d route(s) (rules only; receivers' secrets live in the control plane)\n", len(doc.Shape.Alerts), user, len(doc.Shape.Alerts)-user, len(doc.Shape.AlertRoutes))
	}
	if len(doc.Shape.Domains) > 0 {
		fmt.Fprintf(w, "  domains:   %s\n", strings.Join(doc.Shape.Domains, ", "))
	}
	fmt.Fprintf(w, "  objects:   %d\n", len(doc.Shape.Objects))
	fmt.Fprintf(w, "  rendered from: %s\n", describeShapeProvenance(doc.Provenance))
}

// writeIdentitySummary prints the managed identity the env declares: where it
// runs, who it serves, and what is delivered. Names only, like the rest of the
// summary; the issuer is assigned by the control plane when the instance is
// first provisioned, so `forge env status` is where it appears.
func writeIdentitySummary(w io.Writer, id *release.ShapeIdentity) {
	where := id.Runtime
	switch {
	case id.ShareWith != "":
		where += ", sharing " + id.ShareWith + "'s users"
	case id.Domain != "":
		where += ", issuer on " + id.Domain
	}
	exposure := "private"
	if id.Public || id.Login.Mode == release.IdentityLoginHosted {
		exposure = "public"
	}
	fmt.Fprintf(w, "  identity:  %s (%s, %s sign-in, %s)\n", id.Name, where, id.Login.Mode, exposure)
	for _, a := range id.Applications {
		fmt.Fprintf(w, "    application %s [%s] brokered by %s\n", a.Name, a.Kind, a.Workload)
	}
	if len(id.Roles) > 0 {
		keys := make([]string, 0, len(id.Roles))
		for _, r := range id.Roles {
			keys = append(keys, r.Key)
		}
		fmt.Fprintf(w, "    roles: %s\n", strings.Join(keys, ", "))
	}
	if len(id.Consumers) > 0 {
		names := make([]string, 0, len(id.Consumers))
		for _, c := range id.Consumers {
			names = append(names, c.Workload)
		}
		fmt.Fprintf(w, "    consumed by: %s\n", strings.Join(names, ", "))
	}
	for _, d := range id.Deliveries {
		fmt.Fprintf(w, "    delivers %s [%s] on %s via %s\n", d.Namespace+"/"+d.Name, d.Kind, d.Cluster, d.ConnectedCluster)
	}
}

// describeShapeProvenance is the one-line "where this came from": the commit,
// the branch, whether the tree was dirty, and the forge that rendered it. A
// render is a function of (forge, KCL, config), so the forge version is part
// of the answer and not a footnote.
func describeShapeProvenance(p release.Provenance) string {
	if p.Commit == "" {
		return "no git source (forge " + emptyAs(p.ForgeVersion, "unknown") + ")"
	}
	parts := []string{shortCommit(p.Commit)}
	if p.Branch != "" {
		parts = append(parts, "on "+p.Branch)
	}
	if p.Dirty {
		parts = append(parts, "DIRTY")
	}
	if p.Unhashed() {
		parts = append(parts, "tree unhashed")
	}
	return strings.Join(parts, " ") + " (forge " + emptyAs(p.ForgeVersion, "unknown") + ")"
}

func shortCommit(commit string) string {
	if len(commit) <= 12 {
		return commit
	}
	return commit[:12]
}

// hostedBundleObjects turns the env's hosted group into rendered objects under
// the hosted cluster tree. It returns placeholder=true when the records had to
// be planned over placeholder digests (no release pins the artifacts).
func hostedBundleObjects(ctx context.Context, projectDir, envName string, entities *KCLEntities) (objs []renderedObject, placeholder bool, err error) {
	if entities == nil || !entities.HasHosted() {
		return nil, false, nil
	}
	group, err := buildHostedGroup(envName, entities)
	if err != nil {
		return nil, false, err
	}
	if group == nil {
		return nil, false, nil
	}
	var pins hostedPins
	if ledger, lerr := ledgerFor(ctx, projectDir, envName); lerr == nil {
		if version := hostedPinReleaseFrom(ctx); version != "" {
			if rel, rerr := ledger.Releases.Get(ctx, version); rerr == nil && rel != nil {
				pins = hostedPins{release: version, digests: rel.SharedDigests(), registries: releaseRegistries(rel)}
			}
		}
		if pins.release == "" {
			pins, _ = hostedPinsFromLedger(ctx, ledger, envName)
		}
	}
	group.Hosted = &deploytarget.HostedTarget{
		Release: pins.release, PromotionID: pins.promotionID,
		Digests: pins.digests, Registries: pins.registries,
		PushBase: platformPushBase(entities),
	}
	records, rerr := deploytarget.HostedRecords(*group)
	if rerr != nil {
		records, err = deploytarget.PreflightHostedRecords(*group)
		if err != nil {
			return nil, false, err
		}
		placeholder = true
	}
	for _, r := range records {
		objs = append(objs, renderedObject{
			Doc: strings.TrimSpace(string(r.YAML)), Kind: r.Kind, Name: r.Name,
			Clusters: []string{release.BundleHostedCluster},
		})
	}
	return objs, placeholder, nil
}
