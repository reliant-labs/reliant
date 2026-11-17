package bundle

// THE BUNDLE AS FLUX SEES IT.
//
// Every other test in this package reads the layer through this package's own
// helpers, which is the wrong instrument for the one property that matters
// here: the layout is a contract with a reconciler that shares no code with
// forge. So these tests do what source-controller does, in its order, with
// nothing of forge's in the path —
//
//  1. pick the ONE layer whose mediaType matches the OCIRepository's
//     layerSelector;
//  2. `operation: extract`, which is gunzip + untar into the artifact dir;
//  3. read the files under Kustomization.spec.path, which kustomize-controller
//     applies (generating a kustomization.yaml when the path has none).
//
// What this is really guarding is a failure that is invisible AND green. A
// Kustomization whose path does not exist in the artifact has no resources to
// apply, so it reconciles successfully and reports Ready — the env is reported
// converged while it goes on running the previous release indefinitely. There
// is no error anywhere to find. The only defence is pinning that the paths
// forge WRITES are the paths the document PUBLISHES, from outside both.

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"regexp"
	"sort"
	"strings"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/reliant-labs/forge/pkg/release"
)

// fluxExtract is source-controller's `operation: extract`: select the layer by
// media type off the OCI manifest, gunzip it, untar it. It returns regular
// files only, keyed by their path inside the artifact — which is what
// Kustomization.spec.path indexes into.
//
// It deliberately goes through the OCI MANIFEST rather than Bundle.Layer, and
// refuses when the selector matches anything other than exactly one layer: a
// layerSelector picks one, and "two layers matched" is not a case Flux has an
// answer for.
func fluxExtract(t *testing.T, built Bundle, mediaType string) map[string]string {
	t.Helper()

	var manifest ocispec.Manifest
	if err := jsonUnmarshal(built.Manifest, &manifest); err != nil {
		t.Fatalf("parse the OCI manifest Flux would resolve: %v", err)
	}
	var selected []byte
	matches := 0
	for _, layer := range manifest.Layers {
		if layer.MediaType != mediaType {
			continue
		}
		matches++
		data, ok := built.Layer(layer.MediaType)
		if !ok {
			t.Fatalf("the manifest lists a %s layer the bundle does not carry", layer.MediaType)
		}
		selected = data
	}
	if matches != 1 {
		t.Fatalf("layerSelector mediaType=%s matched %d layers; Flux selects exactly one", mediaType, matches)
	}

	gz, err := gzip.NewReader(strings.NewReader(string(selected)))
	if err != nil {
		t.Fatalf("the selected layer is not gzip, so `operation: extract` would fail: %v", err)
	}
	defer gz.Close()

	out := map[string]string{}
	tr := tar.NewReader(gz)
	for {
		hdr, nerr := tr.Next()
		if nerr == io.EOF {
			break
		}
		if nerr != nil {
			t.Fatalf("the selected layer is not a well-formed tar: %v", nerr)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		body, rerr := io.ReadAll(tr)
		if rerr != nil {
			t.Fatal(rerr)
		}
		out[hdr.Name] = string(body)
	}
	return out
}

// filesUnderCluster is every file below a cluster path, at any depth, keyed by
// basename: the documents a cluster receives across all its trees.
func filesUnderCluster(extracted map[string]string, path string) (names []string, bodies map[string]string) {
	bodies = map[string]string{}
	for name, body := range extracted {
		rest, ok := strings.CutPrefix(name, path+"/")
		if !ok {
			continue
		}
		base := rest[strings.LastIndex(rest, "/")+1:]
		names = append(names, base)
		bodies[base] = body
	}
	sort.Strings(names)
	return names, bodies
}

// filesUnder is kustomize-controller reading Kustomization.spec.path: the
// files directly under one path, by basename, in the lexicographic order a
// generated kustomization.yaml lists them.
func filesUnder(extracted map[string]string, path string) (names []string, bodies map[string]string) {
	prefix := path + "/"
	bodies = map[string]string{}
	for name, body := range extracted {
		rest, ok := strings.CutPrefix(name, prefix)
		if !ok || strings.Contains(rest, "/") {
			continue
		}
		names = append(names, rest)
		bodies[rest] = body
	}
	sort.Strings(names)
	return names, bodies
}

// TestFluxExtractRoutesEachClusterToItsOwnPath is the headline property: a
// two-cluster render extracts to one path per cluster, each holding exactly
// its own documents.
//
// The fixture's Namespace is routed to BOTH clusters (`# cluster: gke-prod,
// gke-daemon`), which is the case that makes a per-cluster layout necessary
// rather than merely tidy: each cluster's Kustomization applies its own path
// with prune, so a shared object has to be present under both or whichever
// cluster's path omits it would PRUNE it from that cluster on the next
// reconcile.
func TestFluxExtractRoutesEachClusterToItsOwnPath(t *testing.T) {
	built := mustBuild(t, buildFixture())
	extracted := fluxExtract(t, built, release.BundleManifestsLayer)

	prodPath := release.BundleClusterPath("gke-prod")
	daemonPath := release.BundleClusterPath("gke-daemon")
	if prodPath == daemonPath {
		t.Fatalf("two clusters share the path %s; each would apply the other's objects", prodPath)
	}

	prodNames, prodBodies := filesUnderCluster(extracted, prodPath)
	daemonNames, daemonBodies := filesUnderCluster(extracted, daemonPath)

	// gke-prod gets the five objects addressed to it, plus the shared
	// Namespace. gke-daemon gets the shared Namespace and nothing else.
	wantProd := []string{
		"000-deployment-api.yaml",
		"001-service-api.yaml",
		"002-persistentvolumeclaim-orders-data.yaml",
		"003-cluster-orders.yaml",
		"004-namespace-shop-prod.yaml",
	}
	if !equalStrings(prodNames, wantProd) {
		t.Errorf("%s holds\n  %v\nwant\n  %v", prodPath, prodNames, wantProd)
	}
	wantDaemon := []string{"004-namespace-shop-prod.yaml"}
	if !equalStrings(daemonNames, wantDaemon) {
		t.Errorf("%s holds\n  %v\nwant\n  %v", daemonPath, daemonNames, wantDaemon)
	}

	// The shared document is the SAME bytes under both paths. Two
	// renderings of one object that differed would make the object's
	// content depend on which cluster's Kustomization reconciled last.
	const shared = "004-namespace-shop-prod.yaml"
	if prodBodies[shared] != daemonBodies[shared] {
		t.Errorf("the Namespace differs between the two cluster paths:\n--- %s\n%s\n--- %s\n%s",
			prodPath, prodBodies[shared], daemonPath, daemonBodies[shared])
	}
	if !strings.Contains(prodBodies[shared], "kind: Namespace") {
		t.Errorf("%s/%s is not the Namespace document:\n%s", prodPath, shared, prodBodies[shared])
	}

	// Nothing escapes a cluster path. An entry directly under the prefix
	// would belong to no Kustomization and so would never be applied,
	// while still being recorded as shipped.
	for name := range extracted {
		rest, ok := strings.CutPrefix(name, release.BundleManifestsPrefix+"/")
		if !ok {
			t.Errorf("entry %s is outside %s/", name, release.BundleManifestsPrefix)
			continue
		}
		if !strings.Contains(rest, "/") {
			t.Errorf("entry %s sits directly under the prefix, so no cluster's path covers it", name)
		}
	}
}

// TestBundleDocPublishesThePathsTheLayerHolds closes the loop: the control
// plane builds its Kustomizations from BundleDoc.ClusterPaths without opening
// the layer, so those paths must be exactly the paths the layer has — no
// more (a Kustomization over a path that is not there reconciles green over
// an env nothing applied) and no fewer (a cluster with no Kustomization is
// never deployed at all, just as silently).
func TestBundleDocPublishesThePathsTheLayerHolds(t *testing.T) {
	built := mustBuild(t, buildFixture())
	extracted := fluxExtract(t, built, release.BundleManifestsLayer)

	// What the layer actually holds, counted from the extracted entries.
	held := map[string]int{}
	for name := range extracted {
		if at := strings.LastIndex(name, "/"); at > 0 {
			held[name[:at]]++
		}
	}

	// A flat cluster is applied from its cluster path; every other cluster is
	// applied from its trees, which are what the document must publish.
	claimed := map[string]int{}
	for _, tree := range built.Doc.ClusterPaths {
		if tree.Path != release.BundleClusterPath(tree.Cluster) {
			t.Errorf("cluster %q claims path %q, but the layout puts it at %q",
				tree.Cluster, tree.Path, release.BundleClusterPath(tree.Cluster))
		}
		if release.FlatCluster(tree.Cluster) {
			claimed[tree.Path] = tree.Documents
		}
	}
	for _, tree := range built.Doc.Trees {
		claimed[tree.Path] = tree.Documents
	}

	for path, count := range held {
		switch got, ok := claimed[path]; {
		case !ok:
			t.Errorf("the layer holds %s, which the document does not publish: "+
				"that cluster would get no Kustomization and would never be deployed", path)
		case got != count:
			t.Errorf("the document says %s holds %d document(s); the layer holds %d", path, got, count)
		}
	}
	for path := range claimed {
		if _, ok := held[path]; !ok {
			t.Errorf("the document publishes %s, which the layer does not hold: "+
				"a Kustomization there applies nothing and still reports Ready", path)
		}
	}
}

// An UNCLUSTERED document — a host-only env's objects, which no deploy routes
// anywhere — still lands in the layer, under its own path, so it is recorded
// as part of the render. It must not land directly under the prefix: every
// entry at one depth is what lets a consumer walk the tree with one case.
//
// Its path gets no Kustomization, which is correct and is why it is kept
// separate from the real clusters rather than folded into one of them.
func TestFluxExtractKeepsUnclusteredDocumentsInTheirOwnPath(t *testing.T) {
	in := buildFixture()
	in.Shape.Manifests += "---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: host-only\n"
	built := mustBuild(t, in)

	extracted := fluxExtract(t, built, release.BundleManifestsLayer)
	names, _ := filesUnder(extracted, release.BundleClusterPath(""))
	want := []string{"005-configmap-host-only.yaml"}
	if !equalStrings(names, want) {
		t.Errorf("the unclustered path %s holds %v, want %v", release.BundleClusterPath(""), names, want)
	}

	// It is published like any other tree, so a reader can see the env
	// renders objects nothing applies rather than having them vanish.
	var found bool
	for _, tree := range built.Doc.ClusterPaths {
		if tree.Cluster == "" {
			found = true
			if tree.Documents != 1 {
				t.Errorf("the unclustered tree holds 1 document, document says %d", tree.Documents)
			}
		}
	}
	if !found {
		t.Error("the unclustered tree is not published in ClusterPaths")
	}
}

// TestBundleDigestIsTheRevisionFluxWillReport pins the comparand the whole
// convergence read rests on.
//
// The control plane writes this digest to the hub OCIRepository's
// `spec.ref.digest`, and a convergence read compares
// `Kustomization.status.lastAppliedRevision` against it with plain string
// equality (control-plane's fluxobserve.judge: `applied == promoted`). So the
// two have to be the same STRING, not merely the same digest — a tag prefix,
// an `oci://` scheme, a bare hex without the algorithm, or an uppercase hex
// would each compare unequal forever, and every env would read as permanently
// progressing while being perfectly converged. That failure is silent: nothing
// errors, the deploy just never reports success.
//
// Measured read-only on the dev hub, Flux keeps one unchanged string across
// the chain — spec.ref.digest == status.artifact.revision ==
// lastAppliedRevision, all `sha256:<64 hex>` — so this asserts the bundle
// produces exactly that lexical form.
//
// It is the MANIFEST digest, not the manifests LAYER's: a bundle's identity,
// what a promotion names, and what Flux resolves ref.digest to are all this
// one value. Selecting a layer changes which bytes reach the artifact, not
// which revision the source reports.
func TestBundleDigestIsTheRevisionFluxWillReport(t *testing.T) {
	built := mustBuild(t, buildFixture())

	if !release.ValidDigest(built.Digest) {
		t.Fatalf("the bundle digest %q is not a canonical sha256 digest, so it cannot be compared "+
			"against lastAppliedRevision as a string", built.Digest)
	}
	// The exact lexical form Flux reports, spelled out rather than deferred
	// to ValidDigest, because THIS is the shape the comparison needs.
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(built.Digest) {
		t.Errorf("bundle digest %q is not `sha256:<64 lowercase hex>` — the form Flux reports as "+
			"lastAppliedRevision, compared by string equality", built.Digest)
	}

	// And it is the digest OF THE MANIFEST BYTES, which is what a registry
	// resolves `@<digest>` to. Recomputed here rather than trusted, so a
	// change that set Digest from anything else — a layer, the config blob,
	// the shape — fails instead of silently making every env look
	// unconverged.
	sum := sha256.Sum256(built.Manifest)
	want := "sha256:" + hex.EncodeToString(sum[:])
	if built.Digest != want {
		t.Errorf("bundle digest %q is not the digest of the manifest bytes %q; a promotion would name "+
			"a revision no OCIRepository can resolve", built.Digest, want)
	}

	// It must NOT be the manifests layer's digest. Distinct values, and
	// confusing them is the one mistake that would survive every other test
	// in this file: the layer extracts correctly, the paths are right, and
	// convergence never reports.
	layer, ok := built.Layer(release.BundleManifestsLayer)
	if !ok {
		t.Fatal("no manifests layer")
	}
	layerSum := sha256.Sum256(layer)
	if built.Digest == "sha256:"+hex.EncodeToString(layerSum[:]) {
		t.Error("the bundle digest is the LAYER's digest; the revision Flux reports is the manifest's")
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
