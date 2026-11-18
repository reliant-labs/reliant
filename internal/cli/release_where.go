package cli

// `forge release where <version>`: which environments run a release right now
// (hosted-deploy-primitives §3.5, task F7).
//
// "Runs" means "is the env's CURRENT binding", which is the ledger's answer.
// Whether the bytes actually reached the cluster is `forge env status`'s
// question, not this one's; the help says so rather than letting a reader
// assume.
//
// Two backends, one answer shape. A hosted release's environments come from
// the control plane (GetRelease.current_environment_ids, resolved to names);
// a file-ledger release's from each declared env's promotion log. A project
// may have both, so every declared env is consulted through ITS OWN ledger
// and the hosted ones are asked once per control plane.

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cloud"
)

func newReleaseWhereCmd() *cobra.Command {
	var (
		env     string
		token   string
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "where <version>",
		Short: "Show which environments are currently bound to a release",
		Long: `Show which environments are currently bound to a release.

"Bound" is the ledger's answer: the release is the environment's CURRENT
promotion. It does not prove the bytes arrived — ` + "`forge env status <env>`" + ` does.

With --env, the release is looked up on that env's control plane (hosted) or
in this project's files. Without it, every environment this checkout declares
is consulted through its own ledger.

Exit codes: 0 the release is bound somewhere, 1 it exists but no environment
is bound to it (or it was never cut), 2 a ledger could not be read.

Examples:
  forge release where v1.4.0
  forge release where v1.4.0 --env prod --json`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			sources, err := releaseWhereSources(cmd.Context(), projectDirForKCL(), env, token)
			if err != nil {
				return exitCodeError{code: exitUndetermined, msg: err.Error()}
			}
			return runReleaseWhere(cmd.Context(), args[0], sources, jsonOut, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&env, "env", "", "Ask only this env's ledger (default: every env this checkout declares)")
	cmd.Flags().StringVar(&token, "token", "", "Credential for a hosted ledger, ahead of the env var and the credentials file")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit machine-readable JSON (same exit codes as text mode)")
	return cmd
}

// whereSource is one place a release can be bound: either a set of file-ledger
// envs read through their binding stores, or one control plane asked once.
type whereSource struct {
	// Location names the ledger, for display.
	Location string
	// Hosted, when set, asks a control plane for the release's environments.
	Hosted cloudCaller
	// Files maps a file-ledger env name to its store.
	Files map[string]bindingStore
}

// releaseWhereSources resolves where to ask. One --env gives one source;
// without it, every declared env is grouped by its ledger.
func releaseWhereSources(ctx context.Context, projectDir, env, token string) ([]whereSource, error) {
	envs := []string{env}
	if env == "" {
		all, err := ListEnvs(projectDir)
		if err != nil {
			return nil, err
		}
		envs = all
	}
	files := map[string]bindingStore{}
	hosted := map[string]whereSource{}
	for _, e := range envs {
		l, err := ledgerFor(ctx, projectDir, e)
		if err != nil {
			return nil, err
		}
		if !l.Hosted {
			files[e] = l.Bindings
			continue
		}
		loc := l.Bindings.Location()
		if _, seen := hosted[loc]; seen {
			continue
		}
		ep, cred, err := resolveCloudTarget(ctx, e, token)
		if err != nil {
			return nil, err
		}
		hosted[loc] = whereSource{Location: loc, Hosted: cloud.NewClient(ep, cred)}
	}
	var out []whereSource
	if len(files) > 0 {
		// Every non-hosted env of one project shares ONE machine ledger,
		// so any of the stores just collected names it. Taken from the
		// map rather than re-derived, so the location printed is the
		// location actually read.
		location := ""
		for _, store := range files {
			location = store.Location()
			break
		}
		out = append(out, whereSource{Location: location, Files: files})
	}
	locs := make([]string, 0, len(hosted))
	for loc := range hosted {
		locs = append(locs, loc)
	}
	sort.Strings(locs)
	for _, loc := range locs {
		out = append(out, hosted[loc])
	}
	return out, nil
}

// releaseWhereDocument is the --json output.
type releaseWhereDocument struct {
	jsonEnvelope
	Release string `json:"release"`
	// Environments bound to the release now, sorted. Always non-nil.
	Environments []releaseWhereEnv `json:"environments"`
}

type releaseWhereEnv struct {
	Env    string `json:"env"`
	Ledger string `json:"ledger"`
}

func runReleaseWhere(ctx context.Context, version string, sources []whereSource, jsonOut bool, out io.Writer) error {
	doc := releaseWhereDocument{Release: version, Environments: []releaseWhereEnv{}}
	var found bool
	for _, src := range sources {
		envs, cut, err := src.boundTo(ctx, version)
		if err != nil {
			return exitCodeError{code: exitUndetermined, msg: fmt.Sprintf("read %s: %v", src.Location, err)}
		}
		found = found || cut
		for _, e := range envs {
			doc.Environments = append(doc.Environments, releaseWhereEnv{Env: e, Ledger: src.Location})
		}
	}
	sort.Slice(doc.Environments, func(i, j int) bool { return doc.Environments[i].Env < doc.Environments[j].Env })

	var result error
	if len(doc.Environments) == 0 {
		why := "it was cut, but no environment is bound to it"
		if !found {
			why = "no ledger this checkout consults has it — it was never cut there"
		}
		result = exitCodeError{code: exitWrong, msg: fmt.Sprintf("release %s runs nowhere: %s", version, why)}
	}
	doc.stamp(result)
	if jsonOut {
		if err := emitJSONDocument(doc); err != nil {
			return err
		}
		return result
	}
	if len(doc.Environments) == 0 {
		fmt.Fprintf(out, "Release %s is bound to no environment.\n", version)
		return result
	}
	names := make([]string, 0, len(doc.Environments))
	for _, e := range doc.Environments {
		names = append(names, e.Env)
	}
	fmt.Fprintf(out, "Release %s is the current binding of: %s\n", version, strings.Join(names, ", "))
	fmt.Fprintln(out, "  (bound in the ledger — `forge env status <env>` proves the bytes arrived)")
	return nil
}

// boundTo returns the envs this source binds to version, and whether the
// release was found (cut) here at all.
func (s whereSource) boundTo(ctx context.Context, version string) ([]string, bool, error) {
	if s.Hosted != nil {
		return hostedReleaseEnvs(ctx, s.Hosted, version)
	}
	var envs []string
	cut := false
	names := make([]string, 0, len(s.Files))
	for e := range s.Files {
		names = append(names, e)
	}
	sort.Strings(names)
	for _, e := range names {
		p, bound, err := s.Files[e].Current(ctx, e)
		if err != nil {
			return nil, false, err
		}
		if bound && p.Release == version {
			envs = append(envs, e)
			cut = true
		}
	}
	return envs, cut, nil
}

// hostedReleaseEnvs asks GetRelease which environments currently bind the
// release, and names them. NotFound is "never cut here", not an error.
func hostedReleaseEnvs(ctx context.Context, client cloudCaller, version string) ([]string, bool, error) {
	var resp struct {
		CurrentEnvironmentIDs []string `json:"currentEnvironmentIds"`
	}
	if err := client.Call(ctx, procGetRelease, map[string]any{"version": version}, &resp); err != nil {
		if hostedErrorHasCode(err, cloud.CodeNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if len(resp.CurrentEnvironmentIDs) == 0 {
		return nil, true, nil
	}
	var envs cloudEnvironmentsResponse
	if err := client.Call(ctx, "controlplane.v1.DeployService/ListEnvironments", map[string]any{}, &envs); err != nil {
		return nil, true, err
	}
	byID := map[string]string{}
	for _, e := range envs.Environments {
		byID[e.ID] = e.Name
	}
	out := make([]string, 0, len(resp.CurrentEnvironmentIDs))
	for _, id := range resp.CurrentEnvironmentIDs {
		name, ok := byID[id]
		if !ok {
			// Kept rather than dropped: a binding the caller cannot name is
			// still a binding, and hiding it would under-report.
			name = id
		}
		out = append(out, name)
	}
	return out, true, nil
}
