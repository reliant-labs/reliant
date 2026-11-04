package cli

// `forge release bundle <env> <version|digest>`: pull a recorded bundle and
// show what it is — and, with --live-diff, what applying it would change.
//
// WHY THIS EXISTS. A bundle is the record of what an env runs, and under Flux
// it is exactly what gets applied. Reviewing one used to need a scratch Go test
// calling bundle.Fetch + bundle.Unpack, and a kubectl diff by hand against each
// cluster. Both are this command now.
//
// The live diff is CLIENT-side `kubectl diff` and only that. The server-side
// form is a server-side APPLY request — the operation that takes field
// ownership — and --force-conflicts takes it from every other manager. A
// command documented as writing nothing never issues one against a live
// cluster, dry run or not: that is the owner's standing rule for read-only
// prod diffs since 2026-09-30, and TestLiveDiff_IsAClientSideKubectlDiff pins
// the argv.

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
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/pkg/release"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

func newReleaseBundleCmd() *cobra.Command {
	var (
		dir      string
		liveDiff bool
		asJSON   bool
	)
	cmd := &cobra.Command{
		Use:   "bundle <environment> <version|sha256:digest>",
		Short: "Pull an environment's recorded bundle for a release, and optionally diff it against the live clusters",
		Long: `Pull the bundle recorded for a release (or a bundle digest) and unpack it.

The bundle is the one recorded for the release — the bundle a plan is computed
from and the bundle a deploy applies — so this is how you read exactly what a
deploy would ship:

  <dir>/manifests/<cluster>/…   the rendered objects, one file each
  <dir>/bundle.json             the bundle document: release, pins, provenance, shape

--live-diff runs a client-side kubectl diff of every cluster's manifests
against that cluster:

  kubectl --context <cluster> diff -R -f <dir>/manifests/<cluster>

It writes nothing to any cluster, and it is deliberately NOT the server-side
form: --server-side is a server-side apply request, which takes field
ownership, so a read-only diff never sends one. Each cluster's diff goes to
<dir>/diff/<cluster>.diff and the summary names every object that would
change.

What a client-side diff cannot show: a field the bundle STOPS setting. forge
applies server-side, which removes such a field; a client-side diff has no
record of what forge set before, so it reports added and changed fields only.`,
		Example: `  forge release bundle prod 20261007.102305-f63c36382f4a
  forge release bundle prod 20261007.102305-f63c36382f4a --live-diff
  forge release bundle prod sha256:4645278bf09a… --dir /tmp/prod-bundle --json`,
		Args:         cobra.ExactArgs(2),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReleaseBundle(cmd.Context(), cmd.OutOrStdout(), projectDirForKCL(), args[0], args[1], releaseBundleOptions{
				Dir: dir, LiveDiff: liveDiff, JSON: asJSON,
			})
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "Where to unpack it (default .forge/bundles/<env>/<version or digest>)")
	cmd.Flags().BoolVar(&liveDiff, "live-diff", false, "Also diff every cluster's manifests against that live cluster (client-side kubectl diff; writes nothing; does not show fields the bundle stops setting)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit the result as JSON")
	return cmd
}

type releaseBundleOptions struct {
	Dir      string
	LiveDiff bool
	JSON     bool
}

// releaseBundleDocument is the --json output.
type releaseBundleDocument struct {
	jsonEnvelope
	Env        string                      `json:"env"`
	Release    string                      `json:"release,omitempty"`
	Digest     string                      `json:"digest"`
	Reference  string                      `json:"reference"`
	Dir        string                      `json:"dir"`
	RenderedBy string                      `json:"rendered_by,omitempty"`
	Commit     string                      `json:"commit,omitempty"`
	Clusters   []release.BundleClusterTree `json:"clusters"`
	Objects    int                         `json:"objects"`
	LiveDiff   []clusterLiveDiff           `json:"live_diff,omitempty"`
}

func runReleaseBundle(ctx context.Context, out io.Writer, projectDir, env, versionOrDigest string, o releaseBundleOptions) error {
	ledger, err := bundleLedgerFor(ctx, projectDir, env)
	if err != nil {
		return err
	}
	ref, err := resolveReleaseBundleRef(ctx, projectDir, env, versionOrDigest, ledger)
	if err != nil {
		return err
	}
	fetched, err := fetchBundleRef(ctx, projectDir, env, ref)
	if err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	dir := o.Dir
	if dir == "" {
		label := versionOrDigest
		if strings.HasPrefix(label, "sha256:") {
			label = shortDigest(label)
		}
		dir = filepath.Join(projectDir, ".forge", "bundles", env, strings.ReplaceAll(label, ":", "-"))
	}
	if err := unpackBundleInto(fetched, dir); err != nil {
		return err
	}
	doc := releaseBundleDocument{
		Env: env, Release: fetched.Doc.Release, Digest: fetched.Digest, Reference: ref, Dir: dir,
		RenderedBy: fetched.Doc.Provenance.ForgeVersion, Commit: fetched.Doc.Provenance.Commit,
		Clusters: fetched.Doc.ClusterPaths, Objects: len(fetched.Doc.Shape.Objects),
	}
	if doc.Clusters == nil {
		doc.Clusters = []release.BundleClusterTree{}
	}
	var diffErr error
	if o.LiveDiff {
		doc.LiveDiff, diffErr = liveDiffBundle(ctx, dir, fetched.Doc.ClusterPaths)
	}
	doc.stamp(diffErr)
	if o.JSON {
		if err := emitJSONDocument(doc); err != nil {
			return err
		}
		return diffErr
	}
	renderReleaseBundle(out, doc)
	return diffErr
}

func renderReleaseBundle(out io.Writer, doc releaseBundleDocument) {
	fmt.Fprintf(out, "Bundle %s for env %s", shortDigest(doc.Digest), doc.Env)
	if doc.Release != "" {
		fmt.Fprintf(out, ", release %s", doc.Release)
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "  reference  %s\n", doc.Reference)
	fmt.Fprintf(out, "  rendered   commit %s by forge %s\n", emptyAs(shortSHA(doc.Commit), "(unknown)"), emptyAs(doc.RenderedBy, "(unknown)"))
	fmt.Fprintf(out, "  objects    %d\n", doc.Objects)
	for _, c := range doc.Clusters {
		fmt.Fprintf(out, "    %-60s %d document(s)\n", c.Cluster, c.Documents)
	}
	fmt.Fprintf(out, "  unpacked   %s\n", doc.Dir)
	if len(doc.LiveDiff) == 0 {
		return
	}
	fmt.Fprintln(out)
	renderLiveDiff(out, doc.LiveDiff)
}

// renderLiveDiff prints the per-cluster summary of a live diff.
func renderLiveDiff(out io.Writer, diffs []clusterLiveDiff) {
	fmt.Fprintln(out, "Live diff (client-side kubectl diff; nothing was written)")
	fmt.Fprintln(out, "  added and changed fields only: a field the bundle stops setting is not shown, though the apply removes it")
	for _, d := range diffs {
		switch {
		case d.Error != "":
			fmt.Fprintf(out, "  %s  COULD NOT DIFF: %s\n", d.Cluster, d.Error)
		case len(d.Changed) == 0:
			fmt.Fprintf(out, "  %s  no changes\n", d.Cluster)
		default:
			fmt.Fprintf(out, "  %s  %d object(s) would change  (%s)\n", d.Cluster, len(d.Changed), d.DiffFile)
			for _, obj := range d.Changed {
				fmt.Fprintf(out, "    ~ %s\n", obj)
			}
		}
	}
}

// resolveReleaseBundleRef turns <version|digest> into a fetchable reference.
func resolveReleaseBundleRef(ctx context.Context, projectDir, env, versionOrDigest string, ledger envLedger) (string, error) {
	if !strings.HasPrefix(versionOrDigest, "sha256:") {
		prior, found, err := findReleaseBundle(ctx, projectDir, env, versionOrDigest, ledger, bundleBuildInputs{Pushed: true})
		if err != nil {
			return "", fmt.Errorf("look up release %s's bundle for env %s: %w", versionOrDigest, env, err)
		}
		if !found {
			return "", fmt.Errorf("no bundle is recorded for release %s in env %s — `forge env build %s --release %s --push` records one",
				versionOrDigest, env, env, versionOrDigest)
		}
		return prior.Reference, nil
	}
	digest := versionOrDigest
	if ledger.Hosted {
		base := bundlePushBaseFor(ctx, projectDir, env)
		if base == "" {
			return "", fmt.Errorf("env %s declares no bundle registry, so a digest cannot be located", env)
		}
		return bundle.Repository(base, env) + "@" + digest, nil
	}
	store, err := openMachineLedger(projectDir)
	if err != nil {
		return "", err
	}
	rows, err := store.Bundles(env)
	if err != nil {
		return "", err
	}
	for _, r := range rows {
		if r.Digest == digest || strings.HasPrefix(r.Digest, digest) {
			return r.Reference, nil
		}
	}
	return "", fmt.Errorf("no bundle %s is recorded for env %s", digest, env)
}

// fetchBundleRef pulls a bundle by its recorded reference: a registry
// reference (<repo>@<digest>) or this machine's OCI layout
// (oci-layout:<root>@<digest>).
var fetchBundleRef = func(ctx context.Context, projectDir, env, ref string) (bundle.Fetched, error) {
	if rest, ok := strings.CutPrefix(ref, "oci-layout:"); ok {
		root, digest, found := strings.Cut(rest, "@")
		if !found {
			return bundle.Fetched{}, fmt.Errorf("layout reference %q names no digest", ref)
		}
		layout, err := bundle.NewLocalLayout(root)
		if err != nil {
			return bundle.Fetched{}, err
		}
		return bundle.Fetch(ctx, layout, digest)
	}
	repo, digest, found := strings.Cut(ref, "@")
	if !found {
		return bundle.Fetched{}, fmt.Errorf("reference %q names no digest", ref)
	}
	r, err := openBundleRepository(ctx, projectDir, env, repo)
	if err != nil {
		return bundle.Fetched{}, err
	}
	return bundle.Fetch(ctx, r, digest)
}

// unpackBundleInto writes the manifests and the bundle document under dir.
func unpackBundleInto(f bundle.Fetched, dir string) error {
	if _, err := bundle.Unpack(bytes.NewReader(f.Manifests), dir); err != nil {
		return fmt.Errorf("unpack into %s: %w", dir, err)
	}
	docJSON, err := json.MarshalIndent(f.Doc, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(filepath.Join(dir, "bundle.json"), append(docJSON, '\n'), 0o644)
}

// clusterLiveDiff is one cluster's client-side diff of a bundle.
type clusterLiveDiff struct {
	Cluster  string   `json:"cluster"`
	Changed  []string `json:"changed"`
	DiffFile string   `json:"diff_file,omitempty"`
	Error    string   `json:"error,omitempty"`
}

// kubectlLiveDiff runs one cluster's client-side diff: no --server-side, no
// --force-conflicts, no --field-manager (see the file comment). kubectl diff
// exits 0 for no differences, 1 for differences, and >1 for an error. A seam
// so a test states what a cluster answers; the package's tests never reach
// kubectl.
var kubectlLiveDiff = func(ctx context.Context, kubeContext, dir string) (string, int, error) {
	cmd := exec.CommandContext(ctx, "kubectl", "--context", kubeContext, "diff", "-R", "-f", dir)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return stdout.String(), 0, nil
	case errors.As(err, &exitErr) && exitErr.ExitCode() == 1:
		return stdout.String(), 1, nil
	default:
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout.String(), -1, errors.New(msg)
	}
}

// diffHeader matches the "+++ <tmp>/MERGED-…/<group>.<version>.<Kind>.<ns>.<name>"
// line kubectl diff prints once per changed object.
var diffHeader = regexp.MustCompile(`(?m)^\+\+\+ \S*/MERGED-[^/]+/(\S+?)(\s|$)`)

// liveDiffBundle diffs each cluster's unpacked manifests against that
// cluster. A cluster that cannot be diffed is reported, and the others still
// are; the returned error names every cluster that could not be.
func liveDiffBundle(ctx context.Context, dir string, clusters []release.BundleClusterTree) ([]clusterLiveDiff, error) {
	var out []clusterLiveDiff
	var failed []string
	for _, c := range clusters {
		if c.Cluster == "" {
			continue // the unclustered tree: nothing applies it
		}
		d := clusterLiveDiff{Cluster: c.Cluster, Changed: []string{}}
		text, _, err := kubectlLiveDiff(ctx, c.Cluster, filepath.Join(dir, filepath.FromSlash(c.Path)))
		if err != nil {
			d.Error = oneLine(err.Error(), 300)
			failed = append(failed, c.Cluster)
			out = append(out, d)
			continue
		}
		for _, m := range diffHeader.FindAllStringSubmatch(text, -1) {
			d.Changed = append(d.Changed, m[1])
		}
		sort.Strings(d.Changed)
		if text != "" {
			d.DiffFile = filepath.Join(dir, "diff", sanitizeFileName(c.Cluster)+".diff")
			if err := os.MkdirAll(filepath.Dir(d.DiffFile), 0o755); err == nil {
				_ = atomicfile.WriteFile(d.DiffFile, []byte(text), 0o644)
			}
		}
		out = append(out, d)
	}
	if len(failed) > 0 {
		return out, fmt.Errorf("the live diff could not run against %s", strings.Join(failed, ", "))
	}
	return out, nil
}

// printPlanLiveDiff is `forge env deploy <env> <v> --live-diff`: the release's
// bundle, diffed client-side against each live cluster, printed with the plan
// so a reviewer sees what applying it changes BEFORE approving it. It is
// information, not a gate: a diff that cannot run is said, and the deploy's
// own gates decide.
func printPlanLiveDiff(ctx context.Context, w io.Writer, projectDir, env, version string, ledger envLedger) {
	ref, err := resolveReleaseBundleRef(ctx, projectDir, env, version, ledger)
	if err != nil {
		fmt.Fprintf(w, "\nLive diff: not computed — %v\n", err)
		return
	}
	fetched, err := fetchBundleRef(ctx, projectDir, env, ref)
	if err != nil {
		fmt.Fprintf(w, "\nLive diff: not computed — pull %s: %v\n", ref, err)
		return
	}
	dir, err := os.MkdirTemp("", "forge-live-diff-")
	if err != nil {
		fmt.Fprintf(w, "\nLive diff: not computed — %v\n", err)
		return
	}
	if err := unpackBundleInto(fetched, dir); err != nil {
		fmt.Fprintf(w, "\nLive diff: not computed — %v\n", err)
		return
	}
	diffs, _ := liveDiffBundle(ctx, dir, fetched.Doc.ClusterPaths)
	fmt.Fprintf(w, "\nBundle %s (%s)\n", shortDigest(fetched.Digest), ref)
	renderLiveDiff(w, diffs)
}

func sanitizeFileName(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		}
		return '_'
	}, s)
}
