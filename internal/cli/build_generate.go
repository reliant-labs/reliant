package cli

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/mod/modfile"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
)

// ensureGeneratedCode runs the generate pipeline when the project's
// generated code is missing or stale, so a downstream `go build` doesn't
// fail with the cryptic go.work error a fresh checkout hits:
//
//	go: cannot load module gen listed in go.work file: open gen/go.mod: no such file or directory
//
// It is wired into runBuild (the shared build choke point for `forge
// build` and `forge env up`'s build phase) and into runUp's --no-build path,
// where the host runners (air / go-run) compile against gen/ too even
// though the docker build phase is skipped.
//
// No-op when skip is true (--no-generate), when the project declares no
// codegen surface (no go.work module dirs and no proto/), or when the
// generated tree is already up to date. The staleness gate matters: a
// full `forge generate` is heavy and noisy, so we only pay it when we'd
// otherwise hand the user a confusing build failure.
func ensureGeneratedCode(projectDir string, skip bool) error {
	if skip {
		return nil
	}
	reason, needs := generatedCodeNeedsRefresh(projectDir)
	if !needs {
		return nil
	}
	if err := refuseGenerateUnderPinSkew(projectDir); err != nil {
		return fmt.Errorf("generated code %s, and refreshing it would rewrite files under the wrong forge (or pass --no-generate): %w", reason, err)
	}
	fmt.Printf("[build] generated code %s — running `forge generate` first (pass --no-generate to skip)\n", reason)
	err := withGenerateLock(projectDir, func() error {
		return runGeneratePipelineFlags(projectDir, pipelineFlags{})
	})
	if err != nil {
		return fmt.Errorf("auto-generate before build: %w", err)
	}
	return nil
}

// generatedCodeNeedsRefresh reports whether forge-generated code is
// missing or stale relative to its proto sources, returning a short
// human reason (for the build log) alongside the boolean.
//
// Two triggers, in priority order:
//
//  1. Missing module — a go.work-listed directory (other than the main
//     module) has no go.mod. This is the hard-fail case: `go build`
//     aborts immediately with "cannot load module … listed in go.work".
//     Hits every fresh checkout when gen/ is gitignored, and any tree
//     after `forge generate`'s clean sweep removed gen/.
//
//  2. Stale output — the protos are not the ones gen/ was generated from.
//     When gen/forge_descriptor.json carries sources, that is the answer:
//     a digest of every .proto's CONTENT, stamped by the generate that
//     wrote gen/, so it is exact (see staleByDescriptor).
//     Only an unstamped tree falls back to comparing modification times.
func generatedCodeNeedsRefresh(projectDir string) (string, bool) {
	if missing := missingGoWorkModule(projectDir); missing != "" {
		return fmt.Sprintf("module %q is missing (listed in go.work, no go.mod)", missing), true
	}
	// A missing dev runtime document (public/config.js) is deliberately NOT
	// a trigger. It used to be, and it made every fresh checkout run the
	// WHOLE generate pipeline before any build — including
	// `forge env build prod --target <a Go workload>`, which never reads a
	// frontend's dev config — and the pipeline refuses outright when the
	// forge on PATH is not the one the project pins. One gitignored file
	// that a single KCL evaluation renders is not a reason to regenerate a
	// tree that is committed and current: ensureDevRuntimeConfigs renders
	// just that file, where a build actually consumes it.
	if stale, decided := staleByDescriptor(projectDir); decided {
		if stale {
			return "is stale (the .proto files differ from the ones gen/forge_descriptor.json was generated from)", true
		}
		return "", false
	}
	protoNewest, okProto := newestModTime(filepath.Join(projectDir, "proto"))
	genNewest, okGen := newestModTime(filepath.Join(projectDir, "gen"))
	if okProto && okGen && protoNewest.After(genNewest) {
		return "is stale (proto sources newer than gen/)", true
	}
	return "", false
}

// staleByDescriptor answers staleness from content when it can: decided is
// true when gen/forge_descriptor.json carries sources, and stale is whether
// the protos on disk differ from them.
//
// Modification times cannot answer this on a fresh checkout. git writes
// files in index order, and gen/ sorts before proto/, so every clone stamps
// the protos a few milliseconds AFTER the generated tree. Every CI job and
// every new worktree therefore looked stale and ran the whole generate
// pipeline before building. That needs the codegen toolchain, refuses a
// forge that is not the project's pin, and fetches embedded postgres, so
// control-plane's k3d smoke test died on a Maven Central 429 while
// regenerating a tree that was committed and current. The check is the same
// one loadDescriptor uses to refuse a stale descriptor
// (codegen.DescriptorDrift), so the two cannot disagree.
func staleByDescriptor(projectDir string) (stale, decided bool) {
	data, err := os.ReadFile(filepath.Join(projectDir, "gen", "forge_descriptor.json"))
	if err != nil {
		return false, false
	}
	var stamp struct {
		Sources map[string]string `json:"sources"`
	}
	if err := json.Unmarshal(data, &stamp); err != nil || stamp.Sources == nil {
		return false, false
	}
	return codegen.DescriptorDrift(projectDir, stamp.Sources) != "", true
}

// missingGoWorkModule returns the first go.work-listed module directory
// (other than the main module ".") whose go.mod is absent, or "" when
// the project has no go.work or every listed module is present. A use'd
// directory without a go.mod is exactly what `go build` rejects with
// "cannot load module <dir> listed in go.work file".
//
// A `use` path may be ABSOLUTE — forge's own dev bridge writes one
// (`use /path/to/forge/pkg`) so a scaffold compiles against a working-copy
// forge/pkg. Joining that onto projectDir yields a path that cannot exist,
// which reported the module missing on every invocation and made `forge env up`
// pay a full regenerate (≈9s and ~60 lines of output) before every single
// boot. Resolve absolute paths as given; only relative ones hang off the
// project root.
func missingGoWorkModule(projectDir string) string {
	path := filepath.Join(projectDir, "go.work")
	data, err := os.ReadFile(path)
	if err != nil {
		return "" // no go.work → nothing to verify
	}
	wf, err := modfile.ParseWork(path, data, nil)
	if err != nil {
		return "" // malformed go.work — let `go build` surface the real error
	}
	for _, use := range wf.Use {
		dir := filepath.Clean(use.Path)
		if dir == "." || dir == "" {
			continue
		}
		modDir := dir
		if !filepath.IsAbs(modDir) {
			modDir = filepath.Join(projectDir, modDir)
		}
		if _, err := os.Stat(filepath.Join(modDir, "go.mod")); err != nil {
			return dir
		}
	}
	return ""
}

// missingDevRuntimeConfig returns the project-relative path of the first web
// frontend's dev runtime document (public/config.js) that is absent, or "".
//
// That document is machine-local — `forge generate` renders it from
// deploy/kcl/dev/config.k, whose ports resolve per machine — so it is
// gitignored, and every fresh clone lacks it. A frontend whose committed
// src/lib/config_gen.ts reads window.__FORGE_CONFIG__ then boots with no
// config at all: the bundle's loadConfig() throws "invalid runtime
// configuration" from inside the provider that wraps the whole app. So the
// dev loop (`forge env up`) and `forge build` regenerate first.
//
// Only frontends that actually consume the document are considered: one with
// the generated config module AND a served static root. React Native has
// neither a public/ directory nor a config.js.
func missingDevRuntimeConfig(projectDir string) string {
	entries, err := os.ReadDir(filepath.Join(projectDir, "frontends"))
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		feRel := filepath.Join("frontends", e.Name())
		feDir := filepath.Join(projectDir, feRel)
		if _, err := os.Stat(filepath.Join(feDir, filepath.FromSlash(codegen.FrontendConfigTSFile))); err != nil {
			continue
		}
		if info, err := os.Stat(filepath.Join(feDir, "public")); err != nil || !info.IsDir() {
			continue
		}
		docRel := filepath.Join(feRel, "public", codegen.FrontendConfigJSFile)
		if _, err := os.Stat(filepath.Join(projectDir, docRel)); err != nil {
			return filepath.ToSlash(docRel)
		}
	}
	return ""
}

// ensureDevRuntimeConfigs renders each web frontend's dev runtime document
// (public/config.js) when it is missing, and nothing else — one evaluation of
// deploy/kcl/dev/config.k through the same renderer `forge generate` and
// `forge env up` use. Returns the number of documents written.
//
// It replaces a full `forge generate` that ran for the same reason (see
// generatedCodeNeedsRefresh): rendering the document does not touch the
// generated tree, so it needs no forge-version gate and works with whatever
// forge is running. A no-op when every document is present.
func ensureDevRuntimeConfigs(cfg *config.ProjectConfig, projectDir string) (int, error) {
	missing := missingDevRuntimeConfig(projectDir)
	if missing == "" {
		return 0, nil
	}
	n, err := refreshFrontendRuntimeConfigs(cfg, projectDir, codegen.DevEnvName, nil)
	if err != nil {
		return n, fmt.Errorf("render %s: %w", missing, err)
	}
	return n, nil
}

// newestModTime walks dir and returns the most recent file modtime. The
// bool is false when dir doesn't exist or contains no files. Symlinks and
// unreadable entries are skipped rather than treated as errors — a best-
// effort staleness signal, not a build-correctness gate.
func newestModTime(dir string) (time.Time, bool) {
	var newest time.Time
	found := false
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		if mt := info.ModTime(); mt.After(newest) {
			newest = mt
			found = true
		}
		return nil
	})
	return newest, found
}
