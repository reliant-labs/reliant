// Package kcloptions discovers the `-D` render options a project's KCL
// declares, so forge can validate and list them WITHOUT knowing what any of
// them mean.
//
// The division of labour
// ======================
// forge owns a small set of options it DERIVES and binds itself — the env
// name, the namespace, the resolved image tag, the git worktree/branch. Those
// are forge's (see [Reserved]).
//
// Everything else is the project's. A project declares an option by CALLING
// it, which it has to do anyway to read the value:
//
//	_host_runner = option("host_runner", type="str", default="air",
//	                      help="Host launch runner: air (default) or go-run")
//
// There is no forge-side declaration format, no reserved filename, and no
// schema to keep in sync — the call site IS the declaration. forge parses the
// KCL, collects the option calls, subtracts its own, and treats what's left as
// opaque names it will relay. It never interprets a value.
//
// Why dependency resolution is load-bearing
// =========================================
// kcl's ListOptions walks every single-name call expression and unwraps a
// symbol-map lookup (crates/loader/src/option.rs). An identifier it cannot
// resolve is not a soft miss — it panics Rust-side, and the panic surfaces as
// an empty result rather than an error. Every forge project's main.k imports
// the forge KCL module, so pointing ListOptions at one WITHOUT resolving
// kcl.mod returns nothing at all.
//
// So discovery resolves the env's dependencies through kpm first (the same
// resolution the render path performs) and hands them over as external
// packages. With them, a real env package reports its options; without them,
// zero. Because that failure is indistinguishable from "declares nothing" by
// the option list alone, [Discover] returns a separate discoverable flag
// rather than letting an empty list stand in for both — see its docs.
package kcloptions

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	kcl "kcl-lang.io/kcl-go"
	"kcl-lang.io/kcl-go/pkg/spec/gpyrpc"
	"kcl-lang.io/kpm/pkg/client"
	"kcl-lang.io/kpm/pkg/env"
	kclpkg "kcl-lang.io/kpm/pkg/package"

	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/internal/kclvendor"
)

// Reserved are the option names forge derives and binds on every render. A
// project must not declare or `-D` them: forge computes the value, and a
// caller-supplied one would silently disagree with the rest of the pipeline
// (an image tag that doesn't match what was built, a namespace that doesn't
// match what was applied).
//
// This is the whole of forge's option vocabulary. Everything else is opaque.
var Reserved = map[string]string{
	"env":             "the environment name — comes from the `forge env up <env>` argument",
	"namespace":       "the k8s namespace — derived from the env's cluster target",
	"image_tag":       "the resolved image tag — derived from the env and the build",
	"release_version": "the release being built or deployed — bound only when a release names it; it becomes service.version in OTEL_RESOURCE_ATTRIBUTES",
	"image_digests":   "the built images' content digests — captured by `forge env deploy`",
	"target_arch":     "the deploy-target GOARCH — derived from --target-arch, the env's cluster platform, or forge.yaml deploy.target_arch",
	"worktree":        "the git worktree basename — resolved by the parallel-dev-stack primitives",
	"branch":          "the git branch — resolved by the parallel-dev-stack primitives",
	"kubeconfig":      "the default kubeconfig path on this machine — resolved by forge, read via `<cluster>.kubeconfig`",
	// The render refuses a frontend bound to a static runtime whose build
	// is not a static export; this is how it knows. A caller-supplied value
	// would let a server build through the refusal.
	"frontend_outputs": "each frontend's production build shape — read from each frontend's next.config",
	// An immutable release render binds both from the release snapshot
	// (kclrender.WithSnapshot / SnapshotDArgs). A caller-supplied pin map
	// would deploy images the release never recorded.
	"forge_workload_image_digests": "each workload's per-slot image references — bound from the release snapshot",
	"forge_immutable_release":      "refuses any unpinned workload image — bound when rendering a release snapshot",
}

// Option is one render option a project's KCL declares. Type/Default/Help are
// populated only when the `option()` call passes them; a bare
// `option("name")` yields just the name, which is legal but gives the CLI
// nothing to show.
type Option struct {
	Name     string `json:"name"`
	Type     string `json:"type,omitempty"`
	Default  string `json:"default,omitempty"`
	Help     string `json:"help,omitempty"`
	Required bool   `json:"required,omitempty"`
}

// Discover returns the options the env's KCL declares, minus forge's own.
//
// The bool result is "discoverable": false means the parse produced NOTHING —
// not even the forge-derived options every forge project's KCL necessarily
// calls — which indicates the resolution failed rather than that the project
// declares no options. Callers should degrade permissively on false (accept
// the `-D` and skip validation) rather than reject a name they merely failed
// to see. A project that genuinely declares no options returns (nil, true).
func Discover(projectDir, envName string) ([]Option, bool, error) {
	kclDir := filepath.Join(projectDir, "deploy", "kcl", envName)
	if _, err := os.Stat(kclDir); err != nil {
		return nil, false, fmt.Errorf("kcl dir %s: %w", kclDir, err)
	}

	// `import forge` resolves from THIS binary's embedded module, handed to
	// KCL as an external package — exactly as every render does
	// (internal/kclrender). The project's kcl.mod declares no forge
	// dependency, so without this the walk would find no forge package and
	// report zero options, which callers read as "discovery failed".
	if err := kclvendor.CheckKclMods(projectDir); err != nil {
		return nil, false, err
	}
	forgeDir, err := kclvendor.ModuleDir()
	if err != nil {
		return nil, false, fmt.Errorf("materialize the forge KCL module: %w", err)
	}

	// The kpm client inside externalPkgs can initialize the native KCL
	// client; the plugin bridge must already be installed. See kclplugin.Register.
	if err := kclplugin.Ready(); err != nil {
		return nil, false, err
	}
	extPkgs, err := externalPkgs(kclDir)
	if err != nil {
		return nil, false, err
	}
	extPkgs = append(extPkgs, &gpyrpc.ExternalPkg{PkgName: kclvendor.ModuleName, PkgPath: forgeDir})

	// Serialized with every other native KCL call: see kclplugin.Serialized.
	res, err := kclplugin.Serialized(func() (*kcl.ListOptionsResult, error) {
		return kcl.ListOptions(&kcl.ListOptionsArgs{
			Paths:        []string{kclDir},
			ExternalPkgs: extPkgs,
		})
	})
	if err != nil {
		return nil, false, fmt.Errorf("list kcl options in %s: %w", kclDir, err)
	}
	// Nothing at all — including none of forge's own — means the walk bailed.
	if len(res.Options) == 0 {
		return nil, false, nil
	}
	return project(res.Options), true, nil
}

// project folds the raw ListOptions output into the project's own options:
// drops forge's reserved names, and merges duplicates.
//
// Duplicates are expected, not a defect: ListOptions reports one entry per
// CALL SITE, so an option read from three places appears three times, and
// forge's own accessors contribute several. The merge keeps the richest entry
// — a bare `option("x")` in one file must not erase the `help=`/`default=`
// declared at another.
func project(raw []*gpyrpc.OptionHelp) []Option {
	merged := make(map[string]Option, len(raw))
	for _, o := range raw {
		name := strings.TrimSpace(o.Name)
		if name == "" {
			continue
		}
		if _, reserved := Reserved[name]; reserved {
			continue
		}
		cur := merged[name]
		cur.Name = name
		cur.Type = richer(cur.Type, o.Type)
		cur.Help = richer(cur.Help, o.Help)
		cur.Default = richer(cur.Default, unquote(o.DefaultValue))
		// Required anywhere means required: the strictest call site wins.
		cur.Required = cur.Required || o.Required
		merged[name] = cur
	}

	out := make([]Option, 0, len(merged))
	for _, o := range merged {
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// richer prefers a non-empty value, keeping the existing one on a tie so the
// first declaration with metadata wins over later bare call sites.
func richer(cur, next string) string {
	if strings.TrimSpace(cur) != "" {
		return cur
	}
	return strings.TrimSpace(next)
}

// unquote strips the KCL literal quoting ListOptions returns for defaults
// (`"air"` arrives as the 6-byte string `"air"`), so help output reads as the
// value rather than as source.
func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// externalPkgs resolves the env package's kcl.mod dependencies through kpm and
// returns them in the shape ListOptions wants. Mirrors what kpm's own Run path
// does before evaluating (ResolveDepsIntoMap + absolutising against the kpm
// home), so discovery sees exactly the module set the render does.
//
// A project with no kcl.mod resolves to no external packages, which is fine:
// nothing to import means nothing to fail resolving.
func externalPkgs(kclDir string) ([]*gpyrpc.ExternalPkg, error) {
	modRoot := findModuleRoot(kclDir)
	if modRoot == "" {
		return nil, nil
	}
	kp, err := kclpkg.LoadKclPkgWithOpts(kclpkg.WithPath(modRoot))
	if err != nil {
		return nil, fmt.Errorf("load kcl package %s: %w", modRoot, err)
	}
	c, err := client.NewKpmClient()
	if err != nil {
		return nil, fmt.Errorf("kpm client: %w", err)
	}
	c.SetLogWriter(os.Stderr) // kpm defaults to stdout; stdout carries values
	depMap, err := c.ResolveDepsIntoMap(kp)
	if err != nil {
		return nil, fmt.Errorf("resolve kcl deps for %s: %w", modRoot, err)
	}

	// Registry/git deps come back relative to the kpm home; path deps are
	// already absolute. Resolve the former the way kpm's Run path does.
	var home string
	names := make([]string, 0, len(depMap))
	for name := range depMap {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic order for stable output/tests

	out := make([]*gpyrpc.ExternalPkg, 0, len(depMap))
	for _, name := range names {
		path := depMap[name]
		if !filepath.IsAbs(path) {
			if home == "" {
				if home, err = env.GetAbsPkgPath(); err != nil {
					return nil, fmt.Errorf("resolve kpm home: %w", err)
				}
			}
			path = filepath.Join(home, path)
		}
		out = append(out, &gpyrpc.ExternalPkg{PkgName: name, PkgPath: path})
	}
	return out, nil
}

// findModuleRoot walks up from dir looking for the kcl.mod that owns it. An
// env package (deploy/kcl/<env>/) usually has no kcl.mod of its own — the
// module is declared once at deploy/kcl/. Returns "" when there is none.
func findModuleRoot(dir string) string {
	for cur := dir; ; {
		if _, err := os.Stat(filepath.Join(cur, "kcl.mod")); err == nil {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return ""
		}
		cur = parent
	}
}
