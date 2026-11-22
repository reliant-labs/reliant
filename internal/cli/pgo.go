package cli

// `forge pgo` — profile-guided optimization from the profiles an env already
// collects (Contract F of the PGO design).
//
// `forge pgo pull` turns the CPU call edges an env's profiles carry
// (default.profile_pgo_edges, pkg/profilequery) into cmd/<bin>/default.pgo,
// one file per Go binary, which the user commits. Go's default -pgo=auto reads
// that file in every build — the Dockerfile, host builds, air — so a build
// never fetches anything and stays reproducible. `forge pgo status` reports
// what each binary's build would read.
//
// Which binaries, their PGO settings and the workloads that run them come
// from the rendered env (ResolvePGOBinaries); where an env's profiles live is
// decided exactly as for every `forge obs` verb (obsbackend.Resolve): the local
// ClickStack, or the control plane's ExportPGOProfile.

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/devtelemetry"
	"github.com/reliant-labs/forge/internal/obsbackend"
)

func newPGOCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pgo",
		Short: "Profile-guided builds: pull an env's CPU profiles into each Go binary's default.pgo",
		Long: `Profile-guided optimization (PGO) from the CPU profiles your envs already
collect. Go applies PGO per main package: ` + "`go build ./cmd/<bin>`" + ` reads
cmd/<bin>/default.pgo when it exists (Go's default -pgo=auto), so the
Dockerfile build, host builds and air all use it with no flag changes.

` + "`forge pgo pull`" + ` writes that file from an env's profiles; you commit it, and every
build after that is optimized for how the binary really runs. Builds never
fetch profiles themselves, so they stay reproducible.

Configure it per build in KCL (` + "`pgo = forge.PGO {...}`" + ` on a GoBuild,
DockerBuild.pgo keyed by binary, or ShellBuild.pgo keyed by binary relative to its
cwd, whose ` + "`forge.ShellPGO {out = ...}`" + ` may name a sibling checkout's profile);
load ` + "`forge skill load observability/profiles`" + `.`,
	}
	cmd.AddCommand(newPGOPullCmd(), newPGOStatusCmd())
	return cmdutil.StrictGroup(cmd)
}

// pgoDeps are the seams `forge pgo` reaches the world through; tests replace
// them, defaultPGODeps is the real thing.
type pgoDeps struct {
	// ProjectDir is the project root.
	ProjectDir string
	// Envs lists the envs the project declares.
	Envs func(projectDir string) []string
	// Render renders an env's declaration.
	Render func(ctx context.Context, projectDir, env string) (*KCLEntities, error)
	// Facts reads where an env's telemetry goes (obsFactsFromKCL).
	Facts func(ctx context.Context, errOut io.Writer, env string) (obsbackend.Facts, error)
	// Local runs a statement against env's local ClickStack.
	Local func(env string) profileRunner
	// Cloud opens env's control plane; HostedDev opens it for a local env
	// whose `forge env up` sent its telemetry to the hosted store.
	Cloud     func(ctx context.Context, env string) (obsbackend.Cloud, error)
	HostedDev func(ctx context.Context, env string, rec devtelemetry.Record) (obsbackend.Cloud, error)
	Now       func() time.Time
	Stdout    io.Writer
	Stderr    io.Writer
}

// pgoDepsOverride replaces defaultPGODeps in tests.
var pgoDepsOverride func(cmd *cobra.Command) (pgoDeps, error)

func defaultPGODeps(cmd *cobra.Command, verb string) (pgoDeps, error) {
	if pgoDepsOverride != nil {
		return pgoDepsOverride(cmd)
	}
	dir, err := requireProjectDir(verb)
	if err != nil {
		return pgoDeps{}, err
	}
	return pgoDeps{
		ProjectDir: dir,
		Envs:       declaredEnvNames,
		Render:     RenderKCL,
		Facts:      obsFactsFromKCL,
		Local:      pgoLocalRunner,
		Cloud:      openObsCloud,
		HostedDev:  openObsHostedDev,
		Now:        time.Now,
		Stdout:     cmd.OutOrStdout(),
		Stderr:     cmd.ErrOrStderr(),
	}, nil
}

// pgoDefaultEnv is the env `forge pgo` reads config from when --env is not
// given: prod's profiles are the ones worth optimizing for.
const pgoDefaultEnv = "prod"

// pgoEnv is the env whose declaration --env names, or prod when the project
// declares one. Anything else is a usage error listing the envs.
func pgoEnv(d pgoDeps, flag string) (string, error) {
	envs := d.Envs(d.ProjectDir)
	want := strings.TrimSpace(flag)
	if want == "" {
		want = pgoDefaultEnv
	}
	if slices.Contains(envs, want) {
		return want, nil
	}
	list := "none (no deploy/kcl/<env>/main.k)"
	if len(envs) > 0 {
		list = strings.Join(envs, ", ")
	}
	if strings.TrimSpace(flag) == "" {
		return "", fmt.Errorf("the project declares no %s env; pick the env whose build config to read with --env (declared: %s)", pgoDefaultEnv, list)
	}
	return "", fmt.Errorf("env %q is not declared; declared envs: %s", want, list)
}

// pgoBinaries renders env and resolves its Go binaries, narrowed to only
// (--binary) when given. A --binary the env does not build is a usage error
// naming the ones it does. project is the env's Bundle.project, which its
// workloads report as service.namespace (kcl/render.k), so it scopes the
// profile read to this project.
func pgoBinaries(ctx context.Context, d pgoDeps, env string, only []string) (bins []PGOBinary, project string, err error) {
	e, err := d.Render(ctx, d.ProjectDir, env)
	if err != nil {
		return nil, "", fmt.Errorf("render env %s: %w", env, err)
	}
	bins, err = ResolvePGOBinaries(e)
	if err != nil {
		return nil, "", fmt.Errorf("env %s: %w", env, err)
	}
	if len(only) == 0 {
		return bins, e.Project, nil
	}
	have := map[string]PGOBinary{}
	var names []string
	for _, b := range bins {
		have[b.Package] = b
		names = append(names, b.Package)
	}
	var out []PGOBinary
	seen := map[string]bool{}
	for _, o := range only {
		pkg := normalizePGOPackage(o)
		b, ok := have[pkg]
		if !ok {
			list := "none"
			if len(names) > 0 {
				list = strings.Join(names, ", ")
			}
			return nil, "", fmt.Errorf("--binary %s: env %s builds no such Go binary (it builds: %s)", o, env, list)
		}
		if !seen[pkg] {
			seen[pkg] = true
			out = append(out, b)
		}
	}
	slices.SortFunc(out, func(a, b PGOBinary) int { return strings.Compare(a.Package, b.Package) })
	return out, e.Project, nil
}

// pgoDuration prints a window or a CPU total the way a person writes one:
// "168h", "1h30m", "3.2s".
func pgoDuration(d time.Duration) string {
	if d >= time.Minute {
		d = d.Round(time.Second)
	} else if d >= time.Second {
		d = d.Round(100 * time.Millisecond)
	}
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}
