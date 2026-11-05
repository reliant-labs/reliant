package cli

// The declared-environment catalog a released hosted bundle seals
// (release.BundleDoc.DeclaredEnvs, bundle_catalog.go).
//
// WHAT IT IS FOR. A control plane can derive the release/environment topology
// of every env it has records of. What it cannot see is an env the source
// declares that never recorded anything on it: one added to KCL and never
// built, and every env with a file ledger. The catalog names them, from the
// commit the release was cut from, so the console can show "declared, outside
// the control plane" without a daemon reading a checkout.
//
// WHEN IT IS COMPUTED. Once per build invocation, lazily, and only for a
// RELEASED bundle recorded on a control plane that names the feature: the
// control plane takes the catalog from the newest released bundle, so an
// unreleased or file-ledger bundle carrying one would cost every env's render
// for a block nobody reads — and would move local bundle digests for nothing.
//
// IT NEVER FAILS A BUILD. Each env renders in parallel under one budget; an
// env that does not render, or not in time, is listed by name with a
// render_error code (never the error text, which carries machine paths). A
// catalog that still does not validate — a directory name that is not an env
// name, more envs than the bound — is left out with a warning.

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// declaredEnvCatalogBudget bounds the whole catalog's renders. Generous
// because it runs once per release cut and a loaded CI box renders slowly;
// an env that misses it is listed as render_timeout, not dropped.
const declaredEnvCatalogBudget = 2 * time.Minute

// declaredEnvCatalogParallelism is how many env renders run at once.
const declaredEnvCatalogParallelism = 4

// declaredEnvCatalogFn computes the catalog. A seam so a build test states the
// catalog instead of staging and rendering a project per env.
var declaredEnvCatalogFn = computeDeclaredEnvCatalog

// declaredEnvRenderFn renders one env for the catalog. A seam for the
// catalog's own tests (render failure, timeout) without a KCL project.
var declaredEnvRenderFn = RenderKCL

// computeDeclaredEnvCatalog renders every env under deploy/kcl/ and projects
// each to its catalog entry. Nil when the project declares none, or when the
// result would not validate (warned on w).
func computeDeclaredEnvCatalog(ctx context.Context, projectDir string, w io.Writer) []release.DeclaredEnv {
	var names, skipped []string
	for _, name := range declaredEnvNames(projectDir) {
		if release.ValidDeclaredEnvName(name) {
			names = append(names, name)
		} else {
			skipped = append(skipped, name)
		}
	}
	if len(skipped) > 0 {
		fmt.Fprintf(w, "[bundle] Note: the declared-environment catalog leaves out %d env(s) whose directory name is not an env name (lower case, digits, '.', '_', '-'): %s\n",
			len(skipped), strings.Join(skipped, ", "))
	}
	if len(names) == 0 {
		return nil
	}
	if len(names) > release.MaxDeclaredEnvs {
		fmt.Fprintf(w, "[bundle] Note: the declared-environment catalog was not sealed: the project declares %d envs (at most %d).\n",
			len(names), release.MaxDeclaredEnvs)
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, declaredEnvCatalogBudget)
	defer cancel()

	out := make([]release.DeclaredEnv, len(names))
	sem := make(chan struct{}, declaredEnvCatalogParallelism)
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			out[i] = declaredEnvEntry(ctx, sem, projectDir, name)
		}(i, name)
	}
	wg.Wait()

	if err := release.ValidateDeclaredEnvs(out); err != nil {
		fmt.Fprintf(w, "[bundle] Note: the declared-environment catalog was not sealed: %v\n", err)
		return nil
	}
	return out
}

// declaredEnvEntry renders one env and projects it. The render runs in its own
// goroutine so the budget bounds the wait even when the renderer does not
// observe ctx; its result is then discarded.
func declaredEnvEntry(ctx context.Context, sem chan struct{}, projectDir, name string) release.DeclaredEnv {
	entry := release.DeclaredEnv{Name: name}
	select {
	case sem <- struct{}{}:
	case <-ctx.Done():
		entry.RenderError = release.DeclaredEnvRenderTimeout
		return entry
	}
	type result struct {
		e   *KCLEntities
		err error
	}
	done := make(chan result, 1)
	go func() {
		defer func() { <-sem }()
		e, err := declaredEnvRenderFn(ctx, projectDir, name)
		done <- result{e, err}
	}()
	select {
	case r := <-done:
		if r.err != nil || r.e == nil {
			if ctx.Err() != nil {
				entry.RenderError = release.DeclaredEnvRenderTimeout
			} else {
				entry.RenderError = release.DeclaredEnvRenderFailed
			}
			return entry
		}
		return declaredEnvOf(name, r.e)
	case <-ctx.Done():
		entry.RenderError = release.DeclaredEnvRenderTimeout
		return entry
	}
}

// declaredEnvOf projects a rendered env into its catalog entry. Pure. A
// lifecycle outside the closed vocabulary reads as unset rather than failing
// the catalog: the catalog states what it can name.
func declaredEnvOf(name string, e *KCLEntities) release.DeclaredEnv {
	entry := release.DeclaredEnv{
		Name:             name,
		Destination:      destinationOf(e),
		ControlPlaneKind: hostedControlPlaneKindName(hostedEnvKindOf(e)),
	}
	switch e.Lifecycle {
	case release.DeclaredLifecycleLocal, release.DeclaredLifecycleEphemeral:
		entry.Lifecycle = e.Lifecycle
	}
	return entry
}

// lazyDeclaredEnvCatalog returns a function that computes the catalog on its
// first call and returns the same answer after, so one build invocation
// renders every env at most once however many bundles it writes
// (--bundle-envs).
func lazyDeclaredEnvCatalog(ctx context.Context, projectDir string, w io.Writer) func() []release.DeclaredEnv {
	var (
		once    sync.Once
		catalog []release.DeclaredEnv
	)
	return func() []release.DeclaredEnv {
		once.Do(func() { catalog = declaredEnvCatalogFn(ctx, projectDir, w) })
		return catalog
	}
}

// sealsDeclaredEnvs reports whether a bundle carries the catalog: a released
// bundle, recorded on a control plane, whose reader decodes the feature.
func sealsDeclaredEnvs(releaseVersion string, ledger envLedger, reader *release.BundleReader) bool {
	return releaseVersion != "" && ledger.Hosted && reader.Supports(release.BundleFeatureDeclaredEnvs)
}
