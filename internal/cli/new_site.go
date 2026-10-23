package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
)

// `forge project new --in-place --kind site` — adopt an existing static
// frontend (a Vite SPA, a Next.js static export) as a forge project, where it
// stands.
//
// The service scaffold is the wrong shape for that directory twice over. It
// writes a Go server, cmd/, proto/, a Dockerfile and docker-compose beside an
// app that has no backend, and it never sees the app: a frontend at the root
// is not under frontends/<name>, so the scaffold reported nothing and the
// next steps said to add a Go service. A site is its frontend and its deploy
// envs, so that is all this writes: forge.yaml, deploy/kcl/kcl.mod and the
// dev / staging / prod envs declaring the root app (`path = "."`), bound to
// its dev server in dev and to hosted static hosting in staging and prod.
// The app's own files are never touched.

// projectKindSite is the `--kind site` value. It is a SCAFFOLD choice only:
// like every kind, the project's kind is derived from its tree afterwards,
// and a site's tree (deploy/kcl, no go.mod) needs no kind of its own —
// config.ProjectConfig.HasGoModule is what every command consults.
const projectKindSite = "site"

// viteOutDirPattern reads a literal `outDir: '<dir>'` from a vite config. A
// computed outDir is not followed; the KCL `public_dir` is the one place to
// correct it.
var viteOutDirPattern = regexp.MustCompile(`outDir\s*:\s*['"]([^'"]+)['"]`)

// siteFrontendAt reports the static frontend at the root of dir, or why
// there is none.
func siteFrontendAt(dir string) (generator.SiteFrontend, error) {
	apps := detectExistingFrontendAppsIn(dir, []string{"."})
	if len(apps) == 0 {
		return generator.SiteFrontend{}, cliutil.UserErr("forge project new --kind site",
			fmt.Sprintf("%s has no frontend at its root (a package.json that depends on vite or next)", dir),
			"",
			"run it with --in-place inside the app's own directory. For a new site with nothing to adopt yet: "+
				"forge project new <name> --frontend web --output static --routes none")
	}
	fe := generator.SiteFrontend{Name: config.RootFrontendName}
	switch apps[0].typ {
	case "nextjs":
		fe.Type, fe.PublicDir = "nextjs", "out"
	default:
		fe.Type, fe.PublicDir = "vite", "dist"
		for _, name := range []string{"vite.config.ts", "vite.config.js", "vite.config.mjs", "vite.config.mts"} {
			if data, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
				if m := viteOutDirPattern.FindSubmatch(data); m != nil {
					fe.PublicDir = string(m[1])
				}
				break
			}
		}
	}
	return fe, nil
}

func runNewSite(targetPath, projectName string, inPlace, force bool, harness string) error {
	if !inPlace {
		return cliutil.UserErr("forge project new --kind site",
			"--kind site adopts the frontend already in a directory, so it runs --in-place",
			"",
			"cd into the app's directory and run: forge project new --in-place --kind site --name <name>. "+
				"For a new site with nothing to adopt yet: forge project new <name> --frontend web --output static --routes none")
	}
	if _, err := os.Stat(filepath.Join(targetPath, "go.mod")); err == nil {
		return cliutil.UserErr("forge project new --kind site",
			fmt.Sprintf("%s is a Go module (go.mod); a site has no Go code", targetPath),
			"",
			"for an app with a Go backend use --kind service and declare the frontend in deploy/kcl/<env>/main.k")
	}
	h, err := generator.ParseHarness(harness)
	if err != nil {
		return err
	}
	fe, err := siteFrontendAt(targetPath)
	if err != nil {
		return err
	}

	staging, err := os.MkdirTemp("", "forge-site-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging) //nolint:errcheck // best-effort cleanup of a temp dir
	if err := generator.WriteSiteScaffold(staging, projectName, h, fe); err != nil {
		return err
	}
	merged, err := mergeScaffoldInto(staging, targetPath, force)
	if err != nil {
		return err
	}

	fmt.Printf("✅ Static site '%s' adopted at %s\n", projectName, targetPath)
	fmt.Printf("   frontend %q: the %s app at the root (path \".\", builds into %s/)\n", fe.Name, fe.Type, fe.PublicDir)
	fmt.Println("   wrote forge.yaml, deploy/kcl/kcl.mod, deploy/kcl/{dev,staging,prod}/main.k — no Go, no server")
	for _, line := range inPlaceMergeSummary(merged) {
		fmt.Println(line)
	}
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  build-time config (VITE_* / NEXT_PUBLIC_*): `env_vars` on the frontend in each env's main.k")
	fmt.Println("  forge env up dev                         the dev server")
	fmt.Println("  forge lint --static-export && forge env render prod")
	fmt.Println("  forge env build prod --release v0.1.0    build once, push, record the release")
	fmt.Println("  forge env deploy prod v0.1.0             publish to hosted static hosting")
	fmt.Println("  forge domain add <host> --env prod       your own hostname")
	fmt.Println("  forge skill load deploy/static-site      the whole flow")
	return nil
}
