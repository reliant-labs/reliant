package cli

// `forge env preview name|create|delete` and `forge env gc`: per-PR and
// per-branch preview environments with an expiry.
//
// A PREVIEW INSTANCE IS AN ORDINARY ENV. `create` copies a committed template
// env (deploy/kcl/preview/, by default) to deploy/kcl/<instance>/ verbatim and
// marks the copy (kclrender.PreviewMarkerFile). Every env verb then works on
// the instance unchanged — `forge env deploy <instance>` builds, records and
// promotes it into its own ledger and, hosted, its own control-plane
// environment — and every render of it binds the marker, which becomes
// Bundle.preview: recorded in the shape (so a control plane that advertises
// shape-preview deletes an expired hosted instance itself) and stamped on the
// instance's namespaces (so `forge env gc` finds an expired one on a cluster
// you operate from the cluster alone).
//
// THE NAME IS A FUNCTION OF THE KEY. release.PreviewName turns `--pr 123` or
// `--branch feat/x` into the same name every time, so the job that creates a
// preview on PR open and the job that deletes it on close agree without
// sharing any state.
//
// NAMESPACE PER PREVIEW. A template env that runs on a cluster you operate
// declares its namespace from the env name — `namespace = "<project>-" +
// option("env")` — so each instance renders into its own. `create` renders the
// instance and REFUSES one that would write into a namespace the template env
// itself renders into: a preview that deployed into staging's namespace would
// be the worst outcome this command could have.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/pkg/release"
)

// The namespace metadata a preview instance's render stamps (kcl/render.k
// _with_preview_meta).
const (
	previewOfLabel        = "forge.dev/preview-of"
	previewInstanceLabel  = "forge.dev/preview"
	previewExpiresAnnot   = "forge.dev/expires-at"
	defaultPreviewFrom    = "preview"
	defaultPreviewTTL     = "72h"
	previewHeaderFileName = "README.preview"
)

// Seams, so the commands are testable without a render, a cluster or a
// control plane.
var (
	previewRender = RenderKCL
	previewNow    = time.Now
	// previewKubectl runs kubectl against one context and returns stdout.
	previewKubectl = func(ctx context.Context, kctx string, args ...string) ([]byte, error) {
		cmd := cluster.KubectlCommand(ctx, append(kubeContextArgs(kctx), args...)...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("kubectl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return out, nil
	}
	// previewDeleteHosted deletes a hosted env by name; a never-deployed one
	// is not an error.
	previewDeleteHosted = func(ctx context.Context, name string, out io.Writer) error {
		t, err := resolveLifecycleTarget(ctx, name, "delete", "", "")
		if errors.Is(err, deploytarget.ErrHostedEnvironmentNotFound) {
			fmt.Fprintf(out, "[preview] %s was never deployed to its control plane; nothing hosted to delete\n", name)
			return nil
		}
		if err != nil {
			return err
		}
		return runEnvDelete(ctx, t.Client, t.EnvironmentID, name,
			envDeleteOptions{Yes: true, Project: t.Project, Endpoint: t.Endpoint}, lifecycleStdin, out)
	}
)

type previewKeyFlags struct {
	pr       int
	branch   string
	from     string
	template string
}

func (f *previewKeyFlags) register(cmd *cobra.Command) {
	cmd.Flags().IntVar(&f.pr, "pr", 0, "The pull request the preview is for")
	cmd.Flags().StringVar(&f.branch, "branch", "", "The branch the preview is for (instead of --pr)")
	cmd.Flags().StringVar(&f.from, "from", defaultPreviewFrom, "The committed template env the preview copies (deploy/kcl/<from>/)")
	cmd.Flags().StringVar(&f.template, "name-template", "", "How the instance is named: {env} is --from, {id} is pr-<n> or the branch (default \""+release.DefaultPreviewTemplate+"\")")
}

func (f previewKeyFlags) name() (string, error) {
	name, err := release.PreviewName(f.template, f.from, release.PreviewKey{PR: f.pr, Branch: f.branch})
	if err != nil {
		return "", cliutil.UserErr("forge env preview", err.Error(), "", "pass --pr <number> or --branch <name>")
	}
	return name, nil
}

func newEnvPreviewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "preview",
		Short: "Per-PR / per-branch preview environments with an expiry",
		Long: `A preview is a copy of a committed template env (deploy/kcl/preview/ by
default) named for a pull request or branch, which expires.

  forge env preview create --pr 123 --ttl 72h   # writes deploy/kcl/preview-pr-123/
  forge env deploy preview-pr-123 --yes         # an ordinary env from here on
  forge env preview delete --pr 123 --yes       # on PR close
  forge env gc --yes                            # delete what expired

Hosted: a control plane that advertises shape-preview deletes an expired
instance itself. On a cluster you operate, each instance renders into its own
namespace (declare the template's namespace from option("env")), labelled so
'forge env gc' finds it. Load the deploy/preview skill for the CI recipe.`,
	}
	cmd.AddCommand(newEnvPreviewNameCmd(), newEnvPreviewCreateCmd(), newEnvPreviewDeleteCmd())
	return cmdutil.StrictGroup(cmd)
}

func newEnvPreviewNameCmd() *cobra.Command {
	var f previewKeyFlags
	cmd := &cobra.Command{
		Use:   "name",
		Short: "Print the preview instance name for a PR or branch",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			name, err := f.name()
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), name)
			return nil
		},
	}
	f.register(cmd)
	return cmd
}

// previewCreateDoc is `create --json`.
type previewCreateDoc struct {
	Name      string `json:"name"`
	Dir       string `json:"dir"`
	Of        string `json:"of"`
	ExpiresAt string `json:"expires_at"`
	Source    string `json:"source"`
	Refreshed bool   `json:"refreshed"`
	Hosted    bool   `json:"hosted"`
	// Namespaces are the cluster namespaces the instance renders into, as
	// "<context>/<namespace>".
	Namespaces []string `json:"namespaces,omitempty"`
}

func newEnvPreviewCreateCmd() *cobra.Command {
	var (
		f        previewKeyFlags
		ttl      string
		jsonMode bool
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Write deploy/kcl/<instance>/ from the template env, with an expiry",
		Long: `Copy the template env (--from, default "preview") to deploy/kcl/<instance>/
and mark it a preview expiring --ttl from now (default 72h; at most 30d). The
instance's KCL is the template's, verbatim; the marker becomes Bundle.preview on
every render. Re-running it for the same PR or branch refreshes the copy and
the expiry.

Refused: a template that is itself a preview instance; an existing env
directory of that name that is not a preview of the same template; and an
instance that would render into a namespace the template env renders into.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			projectDir, err := projectRoot()
			if err != nil {
				return err
			}
			d, err := release.ParsePreviewTTL(ttl)
			if err != nil {
				return cliutil.UserErr("forge env preview create", err.Error(), "", "pass --ttl 72h or --ttl 7d")
			}
			doc, err := createPreview(cmd.Context(), projectDir, f, d)
			if err != nil {
				return err
			}
			if jsonMode {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(doc)
			}
			out := cmd.OutOrStdout()
			verb := "Created"
			if doc.Refreshed {
				verb = "Refreshed"
			}
			fmt.Fprintf(out, "%s preview %s (from %s, %s), expires %s\n  %s\n", verb, doc.Name, doc.Of, doc.Source, doc.ExpiresAt, doc.Dir)
			for _, ns := range doc.Namespaces {
				fmt.Fprintf(out, "  namespace %s\n", ns)
			}
			fmt.Fprintf(out, "\nNext: %s env deploy %s --yes\n", Name(), doc.Name)
			return nil
		},
	}
	f.register(cmd)
	cmd.Flags().StringVar(&ttl, "ttl", defaultPreviewTTL, "How long until the preview expires (72h, 7d; at most 30d)")
	cmd.Flags().BoolVar(&jsonMode, "json", false, "Print the result as JSON")
	return cmd
}

// createPreview materializes (or refreshes) the instance.
func createPreview(ctx context.Context, projectDir string, f previewKeyFlags, ttl time.Duration) (previewCreateDoc, error) {
	name, err := f.name()
	if err != nil {
		return previewCreateDoc{}, err
	}
	kclDir := filepath.Join(projectDir, "deploy", "kcl")
	srcDir, dstDir := filepath.Join(kclDir, f.from), filepath.Join(kclDir, name)
	if _, err := os.Stat(filepath.Join(srcDir, "main.k")); err != nil {
		return previewCreateDoc{}, cliutil.UserErr("forge env preview create",
			fmt.Sprintf("template env %q has no deploy/kcl/%s/main.k", f.from, f.from), "",
			fmt.Sprintf("commit a template env for previews (`%s env new %s --from staging`), or name one with --from", Name(), f.from))
	}
	if _, isPreview, err := kclrender.ReadPreviewMarker(srcDir); err != nil || isPreview {
		return previewCreateDoc{}, cliutil.UserErr("forge env preview create",
			fmt.Sprintf("template env %q is itself a preview instance", f.from), "", "name the committed template env with --from")
	}

	refreshed := false
	if _, err := os.Stat(dstDir); err == nil {
		m, isPreview, err := kclrender.ReadPreviewMarker(dstDir)
		if err != nil || !isPreview || m.Of != f.from {
			return previewCreateDoc{}, cliutil.UserErr("forge env preview create",
				fmt.Sprintf("deploy/kcl/%s exists and is not a preview of %q; refusing to overwrite it", name, f.from), "",
				"pick another --name-template, or remove the directory if it is yours to remove")
		}
		refreshed = true
	}

	marker := kclrender.PreviewMarker{
		Of:        f.from,
		ExpiresAt: previewNow().UTC().Add(ttl).Truncate(time.Second).Format(time.RFC3339),
		Source:    release.PreviewKey{PR: f.pr, Branch: f.branch}.Source(),
	}
	// The instance is staged whole beside it, marker and header included, and
	// swapped in: it is then exactly the template, whatever the template
	// renamed, removed or nested since the last create.
	stage, err := stagePreviewCopy(srcDir, kclDir, name)
	if err != nil {
		return previewCreateDoc{}, err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	data, _ := json.MarshalIndent(marker, "", "  ")
	if err := atomicfile.WriteFile(filepath.Join(stage, kclrender.PreviewMarkerFile), append(data, '\n'), 0o644); err != nil {
		return previewCreateDoc{}, err
	}
	header := fmt.Sprintf("This directory is a PREVIEW INSTANCE of deploy/kcl/%s/, written by `%s env preview create`.\n"+
		"Edit the template, not this copy: the next create overwrites it. It expires %s;\n"+
		"`%s env preview delete` or `%s env gc` removes it. Do not commit it.\n", f.from, Name(), marker.ExpiresAt, Name(), Name())
	if err := atomicfile.WriteFile(filepath.Join(stage, previewHeaderFileName), []byte(header), 0o644); err != nil {
		return previewCreateDoc{}, err
	}
	// The instance renders only under its own name, so the new copy goes in
	// place to be checked; until every check passes the previous instance is
	// kept aside, and any failure puts it back (or removes a new instance), so
	// a refused refresh never leaves a deployable instance behind.
	commit, rollback, err := swapPreviewDir(stage, dstDir)
	if err != nil {
		return previewCreateDoc{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = rollback()
		}
	}()

	doc := previewCreateDoc{Name: name, Dir: filepath.Join("deploy", "kcl", name), Of: f.from,
		ExpiresAt: marker.ExpiresAt, Source: marker.Source, Refreshed: refreshed}
	inst, err := previewRender(ctx, projectDir, name)
	if err != nil {
		return previewCreateDoc{}, fmt.Errorf("render preview %s: %w", name, err)
	}
	tmpl, err := previewRender(ctx, projectDir, f.from)
	if err != nil {
		return previewCreateDoc{}, fmt.Errorf("render template env %s: %w", f.from, err)
	}
	if clash := sharedNamespaces(inst, tmpl); len(clash) > 0 {
		return previewCreateDoc{}, cliutil.UserErr("forge env preview create",
			fmt.Sprintf("preview %s would deploy into %s, which template env %s deploys into too", name, strings.Join(clash, ", "), f.from), "",
			fmt.Sprintf("give each preview its own namespace: in deploy/kcl/%s/main.k declare the ClusterTarget's `namespace = \"<project>-\" + option(\"env\")`", f.from))
	}
	committed = true
	if err := commit(); err != nil {
		return previewCreateDoc{}, err
	}
	doc.Hosted = inst.HasHosted() || inst.ControlPlane != nil
	for _, ns := range inst.ManifestNamespaces {
		doc.Namespaces = append(doc.Namespaces, ns.Cluster+"/"+ns.Name)
	}
	return doc, nil
}

// stagePreviewCopy copies the template env's whole tree, nested directories
// included, into a new staging directory beside the instances (so the swap is
// a rename on one filesystem) and returns it. The template's own marker and
// header are not copied: the instance gets its own. A symlink is refused, not
// skipped: a preview is the template verbatim, and a link would either
// change meaning in the copy or be silently missing from it.
func stagePreviewCopy(srcDir, kclDir, name string) (string, error) {
	stage, err := os.MkdirTemp(kclDir, "."+name+".stage-")
	if err != nil {
		return "", err
	}
	err = filepath.WalkDir(srcDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, p)
		if err != nil || rel == "." {
			return err
		}
		if rel == kclrender.PreviewMarkerFile || rel == previewHeaderFileName {
			return nil
		}
		dst := filepath.Join(stage, rel)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			return cliutil.UserErr("forge env preview create",
				fmt.Sprintf("template env holds a symlink, %s", filepath.ToSlash(filepath.Join(filepath.Base(srcDir), rel))), "",
				"replace it with the file it points at: a preview copies its template verbatim and does not follow links")
		case d.IsDir():
			return os.MkdirAll(dst, 0o755)
		case !d.Type().IsRegular():
			return fmt.Errorf("template env holds %s, which is neither a file nor a directory", rel)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return atomicfile.WriteFile(dst, data, 0o644)
	})
	if err != nil {
		_ = os.RemoveAll(stage)
		return "", err
	}
	return stage, nil
}

// swapPreviewDir puts the staged copy in place of dstDir, keeping the old
// instance renamed aside. commit removes the old instance; rollback removes
// the new copy and renames the old instance back (when there was none, the
// new copy is simply removed). A failed swap leaves the old instance as it was.
func swapPreviewDir(stage, dstDir string) (commit, rollback func() error, err error) {
	old := ""
	if _, err := os.Stat(dstDir); err == nil {
		old = stage + ".old"
		if err := os.Rename(dstDir, old); err != nil {
			return nil, nil, err
		}
	}
	if err := os.Rename(stage, dstDir); err != nil {
		if old != "" {
			_ = os.Rename(old, dstDir)
		}
		return nil, nil, err
	}
	commit = func() error {
		if old == "" {
			return nil
		}
		return os.RemoveAll(old)
	}
	rollback = func() error {
		if err := os.RemoveAll(dstDir); err != nil {
			return err
		}
		if old == "" {
			return nil
		}
		return os.Rename(old, dstDir)
	}
	return commit, rollback, nil
}

// sharedNamespaces are the "<context>/<namespace>" pairs both renders write.
func sharedNamespaces(a, b *KCLEntities) []string {
	if a == nil || b == nil {
		return nil
	}
	seen := map[string]bool{}
	for _, ns := range b.ManifestNamespaces {
		seen[ns.Cluster+"/"+ns.Name] = true
	}
	var out []string
	for _, ns := range a.ManifestNamespaces {
		if key := ns.Cluster + "/" + ns.Name; seen[key] {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

func newEnvPreviewDeleteCmd() *cobra.Command {
	var (
		f    previewKeyFlags
		name string
		yes  bool
	)
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a preview: its hosted env or its namespaces, then its directory",
		Long: `Delete the preview for --pr / --branch (or --name): its hosted environment on
the control plane, data included, or — on a cluster you operate — the
namespaces it renders into, each only if it still carries the preview's label.
Then remove deploy/kcl/<instance>/. A directory that is gone (a fresh CI
checkout) is re-created from the template first, to know what to delete.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !yes && !lifecycleInteractive() {
				return fmt.Errorf("refusing to delete a preview without confirmation: no interactive terminal (or CI is set)\nfix: re-run with `--yes`")
			}
			projectDir, err := projectRoot()
			if err != nil {
				return err
			}
			if name == "" {
				if name, err = f.name(); err != nil {
					return err
				}
			}
			return deletePreview(cmd.Context(), projectDir, f, name, cmd.OutOrStdout())
		},
	}
	f.register(cmd)
	cmd.Flags().StringVar(&name, "name", "", "The instance to delete, by name (instead of --pr / --branch)")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm the deletion (required when non-interactive)")
	return cmd
}

// deletePreview deletes one instance everywhere it runs, then its directory.
func deletePreview(ctx context.Context, projectDir string, f previewKeyFlags, name string, out io.Writer) error {
	dstDir := filepath.Join(projectDir, "deploy", "kcl", name)
	m, isPreview, err := kclrender.ReadPreviewMarker(dstDir)
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(dstDir); statErr == nil && !isPreview {
		return cliutil.UserErr("forge env preview delete",
			fmt.Sprintf("deploy/kcl/%s is not a preview instance; refusing to delete it", name), "",
			"`forge env delete` deletes an ordinary hosted env")
	}
	if !isPreview {
		// A fresh checkout: re-create the instance to learn where it runs.
		if _, err := createPreview(ctx, projectDir, f, time.Hour); err != nil {
			return fmt.Errorf("re-creating preview %s to delete it: %w", name, err)
		}
		if m, _, err = kclrender.ReadPreviewMarker(dstDir); err != nil {
			return err
		}
	}
	inst, err := previewRender(ctx, projectDir, name)
	if err != nil {
		return fmt.Errorf("render preview %s: %w", name, err)
	}
	if inst.HasHosted() || inst.ControlPlane != nil {
		if err := previewDeleteHosted(ctx, name, out); err != nil {
			return err
		}
	}
	for _, ns := range inst.ManifestNamespaces {
		if err := deletePreviewNamespace(ctx, ns.Cluster, ns.Name, m.Of, out); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(dstDir); err != nil {
		return err
	}
	fmt.Fprintf(out, "[preview] deleted %s\n", name)
	return nil
}

// deletePreviewNamespace deletes a namespace only while it carries the
// preview's label: a namespace the label does not mark is not this preview's
// to delete, whatever the render says.
func deletePreviewNamespace(ctx context.Context, kctx, ns, of string, out io.Writer) error {
	raw, err := previewKubectl(ctx, kctx, "get", "namespace", ns, "--ignore-not-found", "-o", "json")
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}
	var obj rawManifest
	if err := json.Unmarshal(raw, &obj); err != nil {
		return fmt.Errorf("reading namespace %s on %s: %w", ns, kctx, err)
	}
	if obj.Metadata.Labels[previewOfLabel] != of {
		fmt.Fprintf(out, "[preview] %s/%s is not labelled %s=%s; left alone\n", kctx, ns, previewOfLabel, of)
		return nil
	}
	if _, err := previewKubectl(ctx, kctx, "delete", "namespace", ns, "--wait=false"); err != nil {
		return err
	}
	fmt.Fprintf(out, "[preview] deleting namespace %s/%s\n", kctx, ns)
	return nil
}

// gcCandidate is one expired preview `env gc` found.
type gcCandidate struct {
	Kind      string `json:"kind"` // "instance" (a local directory) | "namespace"
	Name      string `json:"name"`
	Context   string `json:"context,omitempty"`
	Of        string `json:"of"`
	ExpiresAt string `json:"expires_at"`
}

func newEnvGCCmd() *cobra.Command {
	var (
		from     []string
		contexts []string
		dryRun   bool
		yes      bool
		jsonMode bool
	)
	cmd := &cobra.Command{
		Use:   "gc",
		Short: "Delete expired preview environments",
		Long: `Find and delete the preview instances whose expiry has passed:

  - local instance directories (deploy/kcl/<instance>/ with a preview marker),
    deleted like 'forge env preview delete': hosted env, namespaces, directory;
  - namespaces on a cluster you operate labelled forge.dev/preview-of=<template>
    whose forge.dev/expires-at has passed — found from the cluster alone, so a
    CI runner that created them need not still exist. The clusters are the
    ones each --from template env renders into, plus every --context.

A hosted preview is also deleted by its control plane once it expires, when the
control plane advertises shape-preview. An expiry that cannot be read is never
treated as expired.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !dryRun && !yes && !lifecycleInteractive() {
				return fmt.Errorf("refusing to delete without confirmation: no interactive terminal (or CI is set)\nfix: re-run with `--yes`, or `--dry-run` to list")
			}
			projectDir, err := projectRoot()
			if err != nil {
				return err
			}
			found, err := findExpiredPreviews(cmd.Context(), projectDir, from, contexts)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if !dryRun {
				for _, c := range found {
					switch c.Kind {
					case "instance":
						err = deletePreview(cmd.Context(), projectDir, previewKeyFlags{from: c.Of}, c.Name, out)
					default:
						err = deletePreviewNamespace(cmd.Context(), c.Context, c.Name, c.Of, out)
					}
					if err != nil {
						return err
					}
				}
			}
			if jsonMode {
				return json.NewEncoder(out).Encode(map[string]any{"expired": found, "deleted": !dryRun})
			}
			if len(found) == 0 {
				fmt.Fprintln(out, "No expired previews.")
			}
			for _, c := range found {
				where := c.Name
				if c.Context != "" {
					where = c.Context + "/" + c.Name
				}
				verb := "deleted"
				if dryRun {
					verb = "would delete"
				}
				fmt.Fprintf(out, "%s %s %s (preview of %s, expired %s)\n", verb, c.Kind, where, c.Of, c.ExpiresAt)
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&from, "from", []string{defaultPreviewFrom}, "Template env(s) whose previews to collect on its clusters")
	cmd.Flags().StringSliceVar(&contexts, "context", nil, "Also scan these kubectl contexts for expired preview namespaces")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "List what expired; delete nothing")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm the deletions (required when non-interactive)")
	cmd.Flags().BoolVar(&jsonMode, "json", false, "Print the result as JSON")
	return cmd
}

// findExpiredPreviews lists every expired preview, local instances first.
func findExpiredPreviews(ctx context.Context, projectDir string, from, contexts []string) ([]gcCandidate, error) {
	now := previewNow()
	var out []gcCandidate
	envs, err := ListEnvs(projectDir)
	if err != nil {
		return nil, err
	}
	// local is each local instance's template, by instance name: what a
	// cluster namespace's instance label is checked against below.
	local := map[string]string{}
	for _, env := range envs {
		m, ok, err := kclrender.ReadPreviewMarker(filepath.Join(projectDir, "deploy", "kcl", env))
		if err != nil || !ok {
			continue
		}
		local[env] = m.Of
		if (release.ShapePreview{Of: m.Of, ExpiresAt: m.ExpiresAt}).Expired(now) {
			out = append(out, gcCandidate{Kind: "instance", Name: env, Of: m.Of, ExpiresAt: m.ExpiresAt})
		}
	}

	scan := map[string]bool{}
	for _, c := range contexts {
		scan[c] = true
	}
	ofs := map[string]bool{}
	for _, f := range from {
		ofs[f] = true
		if ok, _ := EnvExists(projectDir, f); !ok {
			continue
		}
		tmpl, err := previewRender(ctx, projectDir, f)
		if err != nil {
			return nil, fmt.Errorf("render template env %s: %w", f, err)
		}
		for _, ns := range tmpl.ManifestNamespaces {
			scan[ns.Cluster] = true
		}
	}
	kctxs := make([]string, 0, len(scan))
	for c := range scan {
		kctxs = append(kctxs, c)
	}
	sort.Strings(kctxs)
	for _, kctx := range kctxs {
		raw, err := previewKubectl(ctx, kctx, "get", "namespaces", "-l", previewOfLabel, "-o", "json")
		if err != nil {
			return nil, err
		}
		var list struct {
			Items []rawManifest `json:"items"`
		}
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, fmt.Errorf("reading namespaces on %s: %w", kctx, err)
		}
		for _, ns := range list.Items {
			of := ns.Metadata.Labels[previewOfLabel]
			at := ns.Metadata.Annotations[previewExpiresAnnot]
			if !ofs[of] {
				continue // another template's
			}
			// The namespace of a local instance of the SAME template is that
			// instance's: deleted with it above when it expired, and kept
			// while it is live (a refreshed instance restamps it on its next
			// deploy). A same-named directory of another template, or none
			// at all, says nothing about it: it is judged on its own expiry.
			if inst := ns.Metadata.Labels[previewInstanceLabel]; inst != "" && local[inst] == of {
				continue
			}
			if (release.ShapePreview{Of: of, ExpiresAt: at}).Expired(now) {
				out = append(out, gcCandidate{Kind: "namespace", Name: ns.Metadata.Name, Context: kctx, Of: of, ExpiresAt: at})
			}
		}
	}
	return out, nil
}
