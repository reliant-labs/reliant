// Package kclrender is the single seam through which forge evaluates KCL.
// It renders via the embedded kpm package manager + kcl-go runtime — no
// external `kcl` binary required on PATH — and registers forge's KCL
// plugin namespace (kcl_plugin.forge.*) so KCL can pull host-runtime
// values (e.g. resolve_port) during evaluation.
//
// kpm reads the package's kcl.mod and resolves dependencies — git, local
// path, and OCI/registry — exactly like the `kcl` CLI, so projects
// declare the forge module (and any extra packages) in kcl.mod in
// whatever style they like; forge neither parses nor special-cases deps.
package kclrender

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	kcl "kcl-lang.io/kcl-go"
	"kcl-lang.io/kpm/pkg/client"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/devstack"
	"github.com/reliant-labs/forge/internal/forgecompat"
	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/internal/kclvendor"
	"github.com/reliant-labs/forge/internal/kubeconfig"
)

// forgeModuleArg resolves the external-package binding that supplies
// `import forge` from THIS binary's embedded module, after refusing a
// project whose kcl.mod still declares the module itself.
//
// This is the whole of how a project resolves forge's KCL: no kcl.mod
// dependency, no project-local copy, no network. The binary rendering the
// project is the module version it renders against — for a project pinned
// to a released forge, CI's `go install …@vX.Y.Z` is that release's module.
// See internal/kclvendor.
//
// The unmigrated-kcl.mod refusal lives here, at the one seam every render
// passes through, because the failure it prevents is silent: kpm resolves a
// declared `forge` dependency AHEAD of an external package, so an old
// `forge = { path = "../../.forge-kcl" }` line would render against whatever
// stale copy sat on disk — or fail on a missing directory with kpm's own
// `kcl mod add` advice, which is wrong for a forge project.
func forgeModuleArg(workDir string) (string, error) {
	if err := kclvendor.CheckKclMods(workDir); err != nil {
		return "", err
	}
	// Same seam, same reason: a project still calling the retired
	// forge.registry helper would otherwise fail on KCL's "attribute
	// 'registry' not found in module 'forge'", which names no fix.
	if err := kclvendor.CheckRegistryHelper(workDir); err != nil {
		return "", err
	}
	arg, err := kclvendor.ExternalPkgArg()
	if err != nil {
		return "", fmt.Errorf("materialize the forge KCL module: %w", err)
	}
	return arg, nil
}

type releaseKey struct{}

// WithRelease says which release version this render is for. It surfaces in
// KCL as option("release_version") and from there as `service.version` in each
// workload's OTEL_RESOURCE_ATTRIBUTES. Empty leaves the render unbound, which
// omits the attribute: forge reports a version only when it knows one.
func WithRelease(ctx context.Context, version string) context.Context {
	if version == "" {
		return ctx
	}
	return context.WithValue(ctx, releaseKey{}, version)
}

// ReleaseDArgs is the `-D release_version=<quoted>` binding for ctx's release,
// or nil. Quoted so an all-digit version stays a str (see image_tag).
func ReleaseDArgs(ctx context.Context) []string {
	var args []string
	if v := Release(ctx); v != "" {
		args = append(args, "release_version="+strconv.Quote(v))
	}
	if pins, ok := ctx.Value(snapshotKey{}).([]string); ok {
		args = append(args, pins...)
	}
	return args
}

// Release is the release version WithRelease bound to ctx, or "" when the
// render is unbound. The same value the KCL sees as option("release_version"),
// for the Go-side consumers that never pass through a render — a frontend
// build's env, notably.
func Release(ctx context.Context) string {
	v, _ := ctx.Value(releaseKey{}).(string)
	return v
}

// withKubeconfigDArg appends `kubeconfig=<quoted path>` — the machine's
// default kubeconfig — to the render's `-D` bindings. It backs
// `forge.default_kubeconfig()`, and through it `Cluster.kubeconfig`, which is
// how a HOST process declares where a cluster's kubeconfig lives.
//
// It is applied HERE, in the one seam every render passes through, rather than
// at each caller. There are a dozen call sites (env up, env deploy, doctor,
// new-env validation, the config probes, the tests), and a binding that only
// some of them passed would make `<cluster>.kubeconfig` resolve under `forge
// env up` and come back empty under `forge env render` — a difference between
// two renders of the same file, which is the failure this field exists to
// remove.
//
// Quoted with strconv.Quote so KCL types it as `str`, for the same reason
// image_tag is quoted (internal/cluster.renderDArgs): an unquoted value is
// type-inferred, and a path is not reliably a string to KCL.
//
// A caller that already supplied its own `kubeconfig=` wins — nothing does
// today, but an explicit binding should not be silently overridden by a
// derived one. An unresolvable path (no KUBECONFIG, no home directory) appends
// nothing, leaving `option("kubeconfig")` as None and the accessor's "" —
// forge does not guess a path it cannot derive.
func withKubeconfigDArg(dArgs []string) []string {
	for _, a := range dArgs {
		if strings.HasPrefix(a, "kubeconfig=") {
			return dArgs
		}
	}
	path := kubeconfig.DefaultPath()
	if path == "" {
		return dArgs
	}
	// Copy rather than append in place: callers hand us slices they build up
	// and reuse across renders, and appending to a shared backing array would
	// let one render's binding leak into another's.
	out := make([]string, 0, len(dArgs)+1)
	out = append(out, dArgs...)
	return append(out, "kubeconfig="+strconv.Quote(path))
}

// withDevStackDArgs appends the ACTIVE parallel-dev-stack git facts —
// `worktree=` and `branch=` — to the render's `-D` bindings, for the same
// reason and at the same seam as withKubeconfigDArg.
//
// These bindings are not cosmetic: a project keys its NAMESPACE on them.
// control-plane's deploy/kcl/dev/main.k computes
// `_namespace = option("namespace") or identity.namespace(option("worktree"))`,
// so a render that omits `worktree=` resolves to the unsuffixed namespace
// while the deploy that applied the objects resolved to the suffixed one.
//
// That is exactly the defect this centralization removes. `forge env deploy`
// (internal/cluster.renderDArgs) and `forge env up`'s entity render
// (internal/cli.renderKCLRaw) both passed these bindings; the doctor's
// cluster-health render (internal/doctor.renderEnvForCluster) did not. So
// `forge env status dev` listed pods in `control-plane-dev`, found none —
// they were all in `control-plane-dev-<worktree>`, running — and reported
// four healthy workloads as "NO PODS", failing the check and the `forge env
// up` exit code with it.
//
// Applying it here rather than at each caller is what makes the omission
// unrepresentable: there is one place a render can be constructed, so a
// render cannot be constructed without the facts that decide where its
// objects live. A caller that already bound a key wins, matching
// withKubeconfigDArg — an explicit binding is never overridden by a derived
// one.
//
// devstack.ActiveDArgs() is the zero value (no args) in any process that
// never called devstack.SetActive — `forge ci`, `forge project audit`, the
// tests — so every render outside the up/deploy path stays byte-identical.
func withDevStackDArgs(dArgs []string) []string {
	add := devstack.ActiveDArgs()
	if len(add) == 0 {
		return dArgs
	}
	// Copy rather than append in place, for the reason withKubeconfigDArg
	// copies: callers build a slice up and reuse it across renders.
	out := make([]string, 0, len(dArgs)+len(add))
	out = append(out, dArgs...)
	for _, a := range add {
		key, _, ok := strings.Cut(a, "=")
		if !ok {
			continue
		}
		bound := false
		for _, existing := range dArgs {
			if strings.HasPrefix(existing, key+"=") {
				bound = true
				break
			}
		}
		if !bound {
			out = append(out, a)
		}
	}
	return out
}

// Run renders the KCL at source — a package directory or a single .k
// file — and returns the raw JSON result.
//
// Both paths mean what any Go path means: relative to the process cwd. A
// caller holding a relative project dir builds source as
// filepath.Join(projectDir, ...), and that is correct here (see absPaths).
//
// workDir is the process cwd KCL resolves relative reads against, and the
// project root whose managed kcl.mod files are checked for a legacy `forge`
// dependency (forgeModuleArg), so it is part of the contract. The `forge`
// module itself is supplied from this binary as an external package.
// dArgs are `-D key=value` top-level option assignments (e.g. "env=dev").
// `kubeconfig` is appended here for every render — see withKubeconfigDArg —
// as are the active dev-stack git facts, see withDevStackDArgs.
// kpm progress/diagnostics go to stderr.
func Run(workDir, source string, dArgs []string) ([]byte, error) {
	return run(workDir, source, dArgs, false)
}

// RunInWorkDir is Run with one addition: the process enters workDir for the
// duration of the evaluation, so the KCL runtime's own `file.read` resolves a
// relative path against it.
//
// # Why this is not just what Run does
//
// kpm's client.WithWorkDir decides where kpm resolves the PACKAGE from. It does
// NOT reach `file.read`, which the KCL runtime resolves against the real
// process cwd — so a project whose KCL reads a file by project-relative path
// (control-plane's deploy/kcl/lib/barman_plugin.k does
// `file.read("deploy/cnpg/plugin-barman-cloud.yaml")`) renders correctly only
// when forge was invoked from the project root, and fails with "No such file or
// directory" naming a path that plainly exists from anywhere else.
//
// # Why it is OPT-IN rather than the default
//
// Because os.Chdir is process-global, and this package is a library. The chdir
// sits inside the KCL evaluation lock, so no two evaluations can race each
// other — but nothing stops UNRELATED code in the same process from resolving
// its own relative path while an evaluation holds that lock. That is not
// hypothetical: making it unconditional turned
// internal/templates.TestBornContractTestSurvivesDepValidation intermittently
// red, because it computes the forge module root as
// filepath.Abs(filepath.Join("..", "..")) in a t.Parallel() subtest and
// resolved it against a render's workDir instead. A flaky suite is a worse
// defect than the one being fixed, and forge's own root.go names this hazard
// as the reason `--project-dir` exists at all.
//
// So a CLI command — one process, one evaluation, no concurrent readers —
// opts in, and the shared render paths stay byte-identical. `forge env render`
// therefore still has the `-C` defect described above; fixing it needs kcl-go
// to accept a read-root for `file.read`, which is an upstream change, or every
// render path to become chdir-safe.
func RunInWorkDir(workDir, source string, dArgs []string) ([]byte, error) {
	return run(workDir, source, dArgs, true)
}

func run(workDir, source string, dArgs []string, enterWorkDir bool) ([]byte, error) {
	// Install the kcl_plugin.forge bridge before kpm or kcl can initialize
	// the native client without it. Idempotent; an error is a runtime that
	// could not load (kclplugin.Ready), reported before kpm can hit it.
	if err := kclplugin.Ready(); err != nil {
		return nil, err
	}

	workDir, source, err := absPaths(workDir, source)
	if err != nil {
		return nil, err
	}

	// The project's pin vs this binary. Read once: it feeds the up-front
	// refusal AND the annotation on a render that fails anyway. Resolved
	// from workDir, which may be a directory beneath the project root —
	// PinnedForgeVersion walks up to find forge.yaml.
	pinnedVersion := forgecompat.PinnedForgeVersion(workDir)
	if err := skewPreflight(pinnedVersion, buildinfo.Version()); err != nil {
		return nil, err
	}

	forgeArg, err := forgeModuleArg(workDir)
	if err != nil {
		return nil, err
	}

	c, err := client.NewKpmClient()
	if err != nil {
		return nil, fmt.Errorf("kpm client: %w", err)
	}
	// kpm's client writes its own progress ("cloning ...", "downloading ...",
	// "waiting for package-cache lock...") to os.Stdout by default;
	// WithLogger below only redirects the KCL runtime. Stdout carries the
	// rendered value, so route kpm's chatter to stderr.
	c.SetLogWriter(os.Stderr)
	// Serialized: concurrent evaluations in one process corrupt each other's
	// refusals (a refused render can come back as success, or carrying
	// another render's message). See kclplugin.Serialized.
	//
	// The chdir — only when the caller opted in, see RunInWorkDir — lives
	// inside that lock, so no two evaluations can be mid-chdir at once and the
	// cwd is restored before the next one starts.
	res, err := kclplugin.Serialized(func() (*kcl.KCLResultList, error) {
		if enterWorkDir {
			restore, err := chdir(workDir)
			if err != nil {
				return nil, err
			}
			defer restore()
		}
		return c.Run(
			client.WithRunSourceUrl(kpmSourceURL(source)),
			client.WithWorkDir(workDir),
			client.WithArguments(withKubeconfigDArg(withDevStackDArgs(dArgs))),
			client.WithExternalPkgs([]string{forgeArg}),
			client.WithLogger(os.Stderr),
		)
	})
	if err != nil {
		// Lead with the skew when there is one — see annotateRenderErr.
		// The preflight above already refused every COMPARABLE skew, so
		// this is the backstop for the pairings it stayed quiet about.
		return nil, annotateRenderErr(fmt.Errorf("kpm run %s: %w", source, err),
			pinnedVersion, buildinfo.Version())
	}
	return []byte(res.GetRawJsonResult()), nil
}

// absPaths resolves workDir and source against the process cwd, before kpm
// sees either.
//
// kpm resolves a RELATIVE source against workDir, not against the cwd. So a
// caller with a relative project dir — `forge project new shop` scaffolds
// into `<--path>/shop`, and its generate pipeline renders from there — handed
// kpm `shop/deploy/kcl/dev/...` with workDir `shop`, and kpm looked for
// `shop/shop/deploy/kcl/dev/...`. The frontend-config probe failed that way on
// every fresh scaffold, and its fallback wrote the dev config.js from proto
// defaults. RunInWorkDir was worse off still: it entered workDir and then
// handed kpm the same relative workDir from inside it.
//
// Resolving both here, at the seam every render passes through, makes the
// two paths mean one thing for every caller instead of auditing each one.
func absPaths(workDir, source string) (string, string, error) {
	absWork, err := filepath.Abs(workDir)
	if err != nil {
		return "", "", fmt.Errorf("resolve render work dir %q: %w", workDir, err)
	}
	absSource, err := filepath.Abs(source)
	if err != nil {
		return "", "", fmt.Errorf("resolve render source %q: %w", source, err)
	}
	return absWork, absSource, nil
}

// kpmSourceURL is source in the form kpm's WithRunSourceUrl parses.
//
// kpm does not take a filesystem path; it takes a URL (it also accepts git://,
// oci:// and registry sources) and re-serializes it with url.URL.String before
// using the path. A backslash is not a URL path character, so a native Windows
// path does not survive that round trip: a relative `deploy\kcl\prod` came
// back `deploy%5Ckcl%5Cprod`, which names no file, and every render failed
// with "Cannot find the kcl file". Since absPaths the source is absolute, and
// forward slashes keep it a plain path through that round trip too (`C:/a/b`
// parses as scheme `c`, path `/a/b`, and prints back as `c:/a/b`). Windows
// accepts forward slashes in every file API. A no-op on POSIX.
func kpmSourceURL(source string) string {
	return filepath.ToSlash(source)
}

// chdir moves the process to dir and returns a func restoring the previous
// cwd. Called only with the KCL evaluation lock held — see RunInWorkDir.
//
// A failure to restore is deliberately silent: there is nothing the caller can
// do about it, and the alternative (overwriting a successful render's result
// with a cwd error) would discard the answer the user asked for. The next
// evaluation sets the cwd it needs regardless, so a missed restore cannot make
// one render read another's directory.
func chdir(dir string) (restore func(), err error) {
	prev, err := os.Getwd()
	if err != nil {
		// No cwd to return to — a deleted working directory. Still chdir,
		// since the render needs the right one; just don't promise a restore.
		if cerr := os.Chdir(dir); cerr != nil {
			return nil, fmt.Errorf("enter %s to render: %w", dir, cerr)
		}
		return func() {}, nil
	}
	if err := os.Chdir(dir); err != nil {
		return nil, fmt.Errorf("enter %s to render: %w", dir, err)
	}
	return func() { _ = os.Chdir(prev) }, nil
}
