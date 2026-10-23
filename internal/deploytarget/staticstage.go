package deploytarget

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// This file owns the part of "ship a static frontend" that is the SAME
// whatever the target is: run the JS build with the environment's
// build-time env injected, assemble the build output plus any sibling
// pre-built directories into one staging tree honoring base_path, and
// write the environment's runtime config document into it last.
//
// It exists because FirebaseProvider had all of it inline, and the
// StaticSite provider needs it verbatim. Two copies would drift on the
// details that are easy to get subtly wrong and expensive to debug —
// which NODE_ENV each phase runs under, that the runtime config document
// is written AFTER the copies so a dev config.js baked into the bundle
// cannot ship to prod, and that base_path and bundle dests normalize the
// same way. Both providers now build a StagePlan and hand it to
// runStagePlan; anything target-specific hangs off the plan, not inside
// it.
//
// The seam is deliberately a PLAN plus two functions over it rather than
// an interface: there are two implementations, they substitute for
// nothing, and a plan a dry-run can print without side effects is the
// property both providers actually need.

// BundleDirSpec is one extra pre-built static directory assembled into a
// site alongside the frontend's own build output. Dest empty means the
// site root. Target-neutral: Firebase Hosting and a GCS static site
// assemble bundles identically.
type BundleDirSpec struct {
	Src  string
	Dest string
}

// StageInput is everything the shared build-and-assemble step needs,
// with no target-specific field on it. Both FirebaseFrontend and
// StaticSiteFrontend project onto this.
type StageInput struct {
	// Name is the forge frontend name (logging, error prefixes, and the
	// default staging directory name).
	Name string

	// Path is the frontend source dir — where install and build run.
	// Relative paths resolve against ProjectDir.
	Path string

	// DevRunner is "npm" (default) | "pnpm" | "yarn"; selects the
	// install command. The build command is always `npm run build`.
	DevRunner string

	// BuildEnv is the build-time env injected into the build process
	// (NEXT_PUBLIC_* / VITE_*), layered over NODE_ENV=production.
	BuildEnv map[string]string

	// PublicDir is the build output directory, relative to Path.
	PublicDir string

	// BasePath is the sub-path under the site root to mount PublicDir
	// under ("/admin"). Empty means the site root.
	BasePath string

	// Bundle is the extra pre-built static dirs to assemble into the
	// same tree. Src resolves against ProjectDir.
	Bundle []BundleDirSpec

	// RuntimeConfigJS is this ENVIRONMENT's rendered runtime config
	// document. Written into the assembled tree AFTER every copy, which
	// is what makes promotion real: the built bundle is
	// environment-agnostic and this one file is the only part of the
	// artifact that differs between environments. Empty means the
	// frontend declares no typed config and nothing is written.
	RuntimeConfigJS string

	// StripRuntimeConfig deletes <basePath>/config.js from the assembled
	// tree instead of writing one: the environment-agnostic artifact a
	// hosted release ships. See StaticSiteFrontend.StripRuntimeConfig.
	StripRuntimeConfig bool

	// ProjectDir is the project root that Path and Bundle[].Src resolve
	// against. Empty means the current working directory.
	ProjectDir string

	// StagingRoot overrides where the assembled tree is written. Empty
	// means a temp dir under os.TempDir().
	StagingRoot string

	// Project is the forge project name, recorded in the source map manifest.
	Project string

	// VersionEnvVar is the framework-scoped variable the bundle reads its
	// service.version from (NEXT_PUBLIC_APP_VERSION / VITE_APP_VERSION).
	// forge sets it to the build id unless BuildEnv already sets it. Empty
	// means the frontend has no such variable and no id is injected.
	VersionEnvVar string

	// Sourcemaps is "publish" (default: maps leave the served tree for a
	// sidecar), "public" (maps are served) or "off" (maps are dropped).
	Sourcemaps string
}

// StagePlan is the resolved, side-effect-free description of one
// frontend's build-and-assemble. It is computed first so --dry-run can
// print it without touching the filesystem or shelling out, and the real
// path executes against the same plan.
type StagePlan struct {
	Name        string
	FrontendDir string // absolute frontend source dir
	InstallCmd  []string
	BuildCmd    []string
	BuildEnv    map[string]string
	StagingDir  string // absolute assembled public root

	// Copies is the ordered list of (absoluteSrc → relativeDestUnderStaging)
	// the assembler performs. The first entry is always the frontend's own
	// public_dir (mounted under base_path); the rest are bundle dirs.
	Copies []stageCopy

	// RuntimeConfigJS is the environment's runtime config document, and
	// RuntimeConfigRel is where it lands relative to the staging root —
	// inside the base-path subtree, because that is where the document
	// head's <script src="<basePath>/config.js"> resolves it. Empty
	// RuntimeConfigJS means no document is written.
	RuntimeConfigJS  string
	RuntimeConfigRel string
	// StripRuntimeConfig removes RuntimeConfigRel from the tree after the
	// copies rather than writing it.
	StripRuntimeConfig bool

	// Project, VersionEnvVar, Sourcemaps and BasePath are the StageInput
	// fields the build-identity and source map steps need. Sourcemaps is
	// already resolved (never empty).
	Project       string
	VersionEnvVar string
	Sourcemaps    string
	BasePath      string

	// BuildID is the build's identity, set by runStagePlan before the build
	// runs. Empty in a plan that has not run (a dry-run) or has no
	// VersionEnvVar.
	BuildID string
}

// StageResult is what a stage run learned that the plan could not know.
type StageResult struct {
	// BuildID is injected into the bundle as service.version and keys the
	// source maps. Empty when the frontend has no version variable.
	BuildID string
	// SourcemapsDir is the sidecar holding the extracted maps and
	// manifest.json; empty when none were extracted.
	SourcemapsDir string
	// Sourcemaps is how many maps were extracted.
	Sourcemaps int
}

type stageCopy struct {
	// Src is the absolute source directory.
	Src string
	// DestRel is the destination path RELATIVE to the staging root
	// (".", "admin", "docs/v2"). "." means the staging root.
	DestRel string
	// Label identifies the source in plan output ("public_dir" / a
	// bundle src).
	Label string
}

// buildStagePlan resolves a StageInput into its StagePlan. Pure aside
// from path resolution (filepath.Abs) — no build, no copy, no network.
func buildStagePlan(in StageInput) (StagePlan, error) {
	projDir := in.ProjectDir
	if projDir == "" {
		projDir = "."
	}
	projDir, err := filepath.Abs(projDir)
	if err != nil {
		return StagePlan{}, fmt.Errorf("%s: resolve project dir: %w", in.Name, err)
	}

	frontendDir := in.Path
	if !filepath.IsAbs(frontendDir) {
		frontendDir = filepath.Join(projDir, in.Path)
	}

	staging := in.StagingRoot
	if staging == "" {
		staging = filepath.Join(os.TempDir(), "forge-static-"+in.Name)
	}
	staging, err = filepath.Abs(staging)
	if err != nil {
		return StagePlan{}, fmt.Errorf("%s: resolve staging dir: %w", in.Name, err)
	}

	// public_dir resolves against the frontend source dir.
	publicSrc := in.PublicDir
	if !filepath.IsAbs(publicSrc) {
		publicSrc = filepath.Join(frontendDir, in.PublicDir)
	}

	copies := []stageCopy{{
		Src:     publicSrc,
		DestRel: basePathToDestRel(in.BasePath),
		Label:   "public_dir",
	}}
	for _, b := range in.Bundle {
		src := b.Src
		if !filepath.IsAbs(src) {
			src = filepath.Join(projDir, b.Src)
		}
		copies = append(copies, stageCopy{
			Src:     src,
			DestRel: cleanDestRel(b.Dest),
			Label:   "bundle:" + b.Src,
		})
	}

	if in.StripRuntimeConfig && in.RuntimeConfigJS != "" {
		return StagePlan{}, fmt.Errorf("%s: a runtime config document cannot be both written and stripped", in.Name)
	}
	sourcemaps, err := resolveSourcemapsMode(in.Sourcemaps)
	if err != nil {
		return StagePlan{}, fmt.Errorf("%s: %w", in.Name, err)
	}
	runtimeConfigRel := ""
	if in.RuntimeConfigJS != "" || in.StripRuntimeConfig {
		runtimeConfigRel = filepath.Join(basePathToDestRel(in.BasePath), FrontendConfigJSName)
	}

	return StagePlan{
		Name:               in.Name,
		FrontendDir:        frontendDir,
		InstallCmd:         frontendInstallCmd(in.DevRunner),
		BuildCmd:           []string{"npm", "run", "build"},
		BuildEnv:           in.BuildEnv,
		StagingDir:         staging,
		Copies:             copies,
		RuntimeConfigJS:    in.RuntimeConfigJS,
		RuntimeConfigRel:   runtimeConfigRel,
		StripRuntimeConfig: in.StripRuntimeConfig,
		Project:            in.Project,
		VersionEnvVar:      in.VersionEnvVar,
		Sourcemaps:         sourcemaps,
		BasePath:           in.BasePath,
	}, nil
}

// runStagePlan executes a StagePlan: derive the build id, install, build,
// then assemble the staging tree. After it returns nil, plan.StagingDir holds
// the complete artifact any target can publish, with no source map in it; the
// maps are in the result's SourcemapsDir.
func runStagePlan(ctx context.Context, runner commandRunner, plan StagePlan) (StageResult, error) {
	buildEnv, err := withBuildIdentity(&plan)
	if err != nil {
		return StageResult{}, fmt.Errorf("%s: %w", plan.Name, err)
	}
	if err := runFrontendBuild(ctx, runner, plan.Name, plan.FrontendDir, plan.InstallCmd, plan.BuildCmd, buildEnv); err != nil {
		return StageResult{}, err
	}
	if err := assembleStaging(plan); err != nil {
		return StageResult{}, fmt.Errorf("%s: assemble: %w", plan.Name, err)
	}
	res := StageResult{BuildID: plan.BuildID}
	if plan.Sourcemaps != SourcemapsPublic {
		n, err := extractSourcemaps(plan)
		if err != nil {
			return StageResult{}, fmt.Errorf("%s: extract source maps: %w", plan.Name, err)
		}
		res.Sourcemaps = n
		if n > 0 && plan.Sourcemaps == SourcemapsPublish {
			res.SourcemapsDir = SourcemapsDir(plan.StagingDir)
		}
	}
	if err := guardNoSourceMaps(plan.StagingDir, plan.Sourcemaps); err != nil {
		return StageResult{}, fmt.Errorf("%s: %w", plan.Name, err)
	}
	return res, nil
}

// withBuildIdentity records the build id on the plan and returns the env the
// build runs with: the plan's env plus the id (as the frontend's version
// variable, unless the project set one) and the switch that asks the bundler
// for source maps.
func withBuildIdentity(plan *StagePlan) (map[string]string, error) {
	env := make(map[string]string, len(plan.BuildEnv)+2)
	for k, v := range plan.BuildEnv {
		env[k] = v
	}
	if plan.VersionEnvVar != "" {
		id, err := computeBuildID(plan.FrontendDir, plan.VersionEnvVar, env)
		if err != nil {
			return nil, err
		}
		plan.BuildID = id
		env[plan.VersionEnvVar] = id
	}
	if plan.Sourcemaps == SourcemapsOff {
		env[SourcemapsEnvVar] = "0"
	} else {
		env[SourcemapsEnvVar] = "1"
	}
	return env, nil
}

// assembleStaging realises plan.Copies into a fresh staging tree. It
// removes any prior staging dir first so a re-deploy doesn't inherit
// stale files, then copies each source into its destination under the
// staging root.
func assembleStaging(plan StagePlan) error {
	if err := os.RemoveAll(plan.StagingDir); err != nil {
		return fmt.Errorf("clean staging: %w", err)
	}
	if err := os.MkdirAll(plan.StagingDir, 0o755); err != nil {
		return fmt.Errorf("create staging: %w", err)
	}
	for _, c := range plan.Copies {
		dst := plan.StagingDir
		if c.DestRel != "." {
			dst = filepath.Join(plan.StagingDir, c.DestRel)
		}
		if _, err := os.Stat(c.Src); err != nil {
			return fmt.Errorf("source %s (%s): %w", c.Src, c.Label, err)
		}
		if err := copyDir(c.Src, dst); err != nil {
			return fmt.Errorf("copy %s -> %s: %w", c.Src, dst, err)
		}
	}

	// The environment's runtime config document, written LAST — after
	// every copy — so it overwrites any config.js that travelled inside
	// the built bundle (the dev copy `forge generate` checks in, which is
	// under the frontend's static-asset root and therefore gets built into
	// public_dir). Writing it before the copies would let dev's values
	// silently ship to production.
	//
	// This is the step that makes promotion real: the bundle is
	// environment-agnostic, and this one file is the only part of the
	// deployed artifact that differs between environments.
	if plan.StripRuntimeConfig {
		dst := filepath.Join(plan.StagingDir, plan.RuntimeConfigRel)
		if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("strip runtime config %s: %w", dst, err)
		}
	}
	if plan.RuntimeConfigJS != "" {
		dst := filepath.Join(plan.StagingDir, plan.RuntimeConfigRel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("create runtime config dir: %w", err)
		}
		if err := atomicfile.WriteFile(dst, []byte(plan.RuntimeConfigJS), 0o644); err != nil {
			return fmt.Errorf("write runtime config %s: %w", dst, err)
		}
	}
	return nil
}

// printStagePlanBuild renders the build-and-assemble half of a dry-run
// plan. Each provider prints its own target-specific lines after this.
func printStagePlanBuild(w io.Writer, plan StagePlan) {
	_, _ = fmt.Fprintf(w, "    build dir:    %s\n", plan.FrontendDir)
	_, _ = fmt.Fprintf(w, "    [DRY-RUN] would exec: %s\n", strings.Join(plan.InstallCmd, " "))
	if len(plan.BuildEnv) > 0 {
		_, _ = fmt.Fprintf(w, "    build env:    %s\n", formatBuildEnv(plan.BuildEnv))
	}
	_, _ = fmt.Fprintf(w, "    [DRY-RUN] would exec: %s (NODE_ENV=production)\n", strings.Join(plan.BuildCmd, " "))
	_, _ = fmt.Fprintf(w, "    assemble into %s:\n", plan.StagingDir)
	for _, c := range plan.Copies {
		mount := "/"
		if c.DestRel != "." {
			mount = "/" + c.DestRel
		}
		_, _ = fmt.Fprintf(w, "      %-18s -> %s   (%s)\n", c.Src, mount, c.Label)
	}
	switch plan.Sourcemaps {
	case SourcemapsPublic:
		_, _ = fmt.Fprintf(w, "    source maps:  served publicly (sourcemaps = %q)\n", SourcemapsPublic)
	case SourcemapsOff:
		_, _ = fmt.Fprintf(w, "    source maps:  dropped (sourcemaps = %q)\n", SourcemapsOff)
	default:
		_, _ = fmt.Fprintf(w, "    source maps:  moved to %s, never served\n", SourcemapsDir(plan.StagingDir))
	}
	if plan.VersionEnvVar != "" {
		_, _ = fmt.Fprintf(w, "    build id:     injected as %s (content hash of source + build env, unless the env sets it)\n", plan.VersionEnvVar)
	}
	if plan.StripRuntimeConfig {
		_, _ = fmt.Fprintf(w, "      %-18s -> /%s   (removed: the control plane writes it per environment)\n",
			"<none>", filepath.ToSlash(plan.RuntimeConfigRel))
	}
	if plan.RuntimeConfigJS != "" {
		_, _ = fmt.Fprintf(w, "      %-18s -> /%s   (runtime config for this environment)\n",
			"<rendered KCL>", filepath.ToSlash(plan.RuntimeConfigRel))
	}
}

// runFrontendBuild runs the install + build phase for a frontend in
// frontendDir. The two phases get DELIBERATELY DIFFERENT env:
//
//   - INSTALL runs under NODE_ENV=development (installEnv) so the package
//     manager pulls the FULL dependency set, devDependencies included.
//     The build toolchain (typescript, bundlers, next's config loader)
//     lives in devDependencies — under NODE_ENV=production, `npm install`
//     SKIPS them and the subsequent build dies with "Cannot find module
//     'typescript'" (Next.js needs typescript to load next.config.ts).
//     The frontend's inline env_vars are NOT injected here: they're
//     build-time values (NEXT_PUBLIC_* / VITE_*), irrelevant to install.
//   - BUILD runs under NODE_ENV=production with the inline env_vars
//     layered on (buildTimeEnv), so the static-export path (Next.js
//     `output: "export"` gated on NODE_ENV) and Vite's production mode
//     both engage and NEXT_PUBLIC_*/VITE_* are baked in.
//
// Shared by every static-frontend path — Firebase deploy, StaticSite
// deploy, and build-only — so they can never drift on install command,
// build command, or env semantics.
func runFrontendBuild(ctx context.Context, runner commandRunner, name, frontendDir string, installCmd, buildCmd []string, extraEnv map[string]string) error {
	if err := runInDir(ctx, runner, frontendDir, installEnv(), installCmd); err != nil {
		return fmt.Errorf("frontend %s: install: %w", name, err)
	}
	if err := runInDir(ctx, runner, frontendDir, buildTimeEnv(extraEnv), buildCmd); err != nil {
		return fmt.Errorf("frontend %s: build: %w", name, err)
	}
	return nil
}

// installEnv is the env overlay for the dependency-install phase. It
// forces NODE_ENV=development so devDependencies are installed even when
// the ambient/inherited NODE_ENV is "production" (the package manager
// skips devDeps under production). Set explicitly rather than left empty:
// runInDir inherits the parent process env when the overlay is empty, so
// an inherited NODE_ENV=production would otherwise leak through and strip
// the build toolchain (typescript, bundlers) the build phase needs.
func installEnv() map[string]string {
	return map[string]string{"NODE_ENV": "development"}
}

// buildTimeEnv layers a frontend's inline env_vars over a forced
// NODE_ENV=production. Extracted so every build path produces
// byte-identical build env.
func buildTimeEnv(extraEnv map[string]string) map[string]string {
	env := map[string]string{"NODE_ENV": "production"}
	for k, v := range extraEnv {
		env[k] = v
	}
	return env
}

// frontendInstallCmd returns the dependency-install command for a dev
// runner. npm uses `npm ci` when a lockfile is present at runtime — but
// to keep the plan deterministic (and not stat the lockfile during
// planning) we use `npm install`, which works with or without a
// lockfile. pnpm/yarn use their `install` verb.
func frontendInstallCmd(devRunner string) []string {
	switch devRunner {
	case "pnpm":
		return []string{"pnpm", "install"}
	case "yarn":
		return []string{"yarn", "install"}
	default:
		return []string{"npm", "install"}
	}
}

// basePathToDestRel maps a frontend base_path ("/admin", "") to the
// staging-relative destination ("admin", "."). A root mount (empty
// base_path) lands at the staging root.
func basePathToDestRel(basePath string) string {
	return cleanDestRel(basePath)
}

// cleanDestRel normalises a dest path (base_path or bundle.dest) into a
// staging-relative directory: leading/trailing slashes stripped, empty
// becomes ".". Defends against absolute / "./" / trailing-slash inputs
// so the assembled layout is predictable.
func cleanDestRel(dest string) string {
	d := strings.Trim(strings.TrimSpace(dest), "/")
	d = filepath.Clean(d)
	if d == "" || d == "." || d == "/" {
		return "."
	}
	return d
}

// stagingDigest is the content-addressed identity of an assembled
// staging tree: sha256 over every file's staging-relative slash-path and
// bytes, in sorted path order. Returned as a 12-hex-char short digest —
// long enough that a collision across one project's releases is not a
// practical concern, short enough to read in a bucket listing.
//
// This is the static-site analogue of an image digest, and it is what
// makes promotion mean the same thing on both halves of a project:
// promoting re-points an environment at an EXISTING digest's archived
// bytes rather than rebuilding and hoping the output matches. Two builds
// of the same source with the same build env produce the same digest, so
// a redeploy of unchanged content is recognisable as a no-op rather than
// churning a new archive.
//
// The environment's runtime config document participates in the digest
// like any other file, which is correct and deliberate: two environments
// running the same bundle with different configuration are genuinely
// different artifacts, and conflating them would let a promotion ship
// staging's API URL to prod.
func stagingDigest(stagingDir string) (string, error) {
	type entry struct {
		rel  string
		path string
	}
	var entries []entry
	err := filepath.Walk(stagingDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(stagingDir, path)
		if rerr != nil {
			return rerr
		}
		entries = append(entries, entry{rel: filepath.ToSlash(rel), path: path})
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("digest staging tree: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })

	h := sha256.New()
	for _, e := range entries {
		// Length-prefix the path so ("ab","c") and ("a","bc") cannot
		// hash the same.
		_, _ = fmt.Fprintf(h, "%d:%s\n", len(e.rel), e.rel)
		f, oerr := os.Open(e.path)
		if oerr != nil {
			return "", fmt.Errorf("digest %s: %w", e.rel, oerr)
		}
		if _, cerr := io.Copy(h, f); cerr != nil {
			_ = f.Close()
			return "", fmt.Errorf("digest %s: %w", e.rel, cerr)
		}
		_ = f.Close()
	}
	return hex.EncodeToString(h.Sum(nil))[:12], nil
}

// runInDir runs argv directly (no shell) in dir with an optional env overlay.
func runInDir(ctx context.Context, runner commandRunner, dir string, env map[string]string, argv []string) error {
	if len(argv) == 0 {
		return nil
	}
	return runner.RunInDir(ctx, dir, env, argv[0], argv[1:]...)
}

// copyDir recursively copies src into dst, creating dst (and parents).
// Plain file copy — symlinks are dereferenced (static export output is
// regular files). Sufficient for assembling a static hosting tree.
func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, path)
		if rerr != nil {
			return rerr
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target, info.Mode())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// formatBuildEnv renders the build-time env map as a stable, sorted
// KEY=VALUE list for the dry-run plan.
func formatBuildEnv(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+env[k])
	}
	return strings.Join(parts, " ")
}

// reportStageResult prints the one line a deploy owes about build identity and
// source maps, so "where did my maps go" has an answer in the log.
func reportStageResult(kind, name string, res StageResult) {
	if res.BuildID != "" {
		fmt.Printf("  [%s] %s: build id %s\n", kind, name, res.BuildID)
	}
	if res.SourcemapsDir != "" {
		fmt.Printf("  [%s] %s: %d source map(s) kept out of the served tree: %s\n", kind, name, res.Sourcemaps, res.SourcemapsDir)
	}
}
