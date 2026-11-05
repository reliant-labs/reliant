// File: internal/cli/env_config.go
//
// `forge env config` — print the resolved per-workload configuration an
// environment's KCL declares.
//
// The question this answers is "what values is this environment actually
// going to hand my workloads", and it comes up constantly: which database am
// I connected to, which broker, which bucket, which upstream API. All of it
// is already computed — `forge env up` renders exactly these values and
// passes them to each process — but reading it back meant opening
// deploy/kcl/<env>/ and evaluating a template string in your head, then
// cross-referencing .forge/ports-<env>.json for any port that was resolved
// at launch.
//
// That gap has a bad failure mode on a shared machine. Someone who wants the
// database they are working on runs `docker ps`, picks the plausible-looking
// container, and gets SOMEONE's postgres — just not necessarily this
// project's, and the mistake reads as correct right up until the schema
// disagrees.
//
// Deliberately NOT a per-technology command. There is no `forge db dsn`
// here, because a project may have two databases, or none, or reach its
// store over something that is not a DSN at all. forge's business is that
// the environment declares its configuration and forge resolves it; naming
// DATABASE_URL in a command signature would pin a convention that is the
// project's to choose. Print what is declared and let the caller select:
//
//	forge env config dev --json | jq -r '.workloads[].env.DATABASE_URL // empty'

package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// envConfigWorkload is one workload's resolved configuration.
type envConfigWorkload struct {
	Name string            `json:"name"`
	Kind string            `json:"kind"` // service | job | frontend | operator | cronjob
	Env  map[string]string `json:"env,omitempty"`
	// MissingSecrets are the secrets this workload declares that the env's
	// secret store cannot supply. Their variables are absent from Env. Only
	// local (OnHost/OnCompose) workloads are resolved, so only they report.
	MissingSecrets []envConfigMissingSecret `json:"missing_secrets,omitempty"`
}

// envConfigMissingSecret is one declared secret the store lacks: the
// variable, and the Secret and key it is declared to come from. Never a value.
type envConfigMissingSecret struct {
	Name   string `json:"name"`
	Secret string `json:"secret"`
	Key    string `json:"key"`
}

// envConfigReport is the whole environment, ordered for stable output.
type envConfigReport struct {
	Env       string              `json:"env"`
	Workloads []envConfigWorkload `json:"workloads"`
	// SecretStore names where missing secrets would be set, when any are.
	SecretStore string `json:"secret_store,omitempty"`
}

// missingSecrets is every workload's gap, deduplicated by variable name.
func (r envConfigReport) missingSecrets() []envConfigMissingSecret {
	seen := map[string]bool{}
	var out []envConfigMissingSecret
	for _, w := range r.Workloads {
		for _, m := range w.MissingSecrets {
			if !seen[m.Name] {
				seen[m.Name] = true
				out = append(out, m)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func newEnvConfigCmd() *cobra.Command {
	var jsonOut, strict bool
	var nameFilter string

	cmd := &cobra.Command{
		Use:   "config <env>",
		Short: "Print the resolved configuration deploy/kcl/<env>/ hands each workload",
		Args:  cobra.ExactArgs(1),
		Long: `Print the environment variables ` + "`" + `deploy/kcl/<env>/` + "`" + ` resolves for every
workload — the same values ` + Name() + ` env up passes to each process.

This is the readback for "what is this environment actually configured with":
which database, which broker, which upstream. Ports that were resolved at
launch are reported as launched, so the values match the running stack rather
than a fresh render's guess.

It prints whatever the environment declares. ` + Name() + ` has no opinion about
which variables a project uses, so pick the one you want:

  ` + Name() + ` env config dev
  ` + Name() + ` env config dev --workload api
  ` + Name() + ` env config dev --json | jq -r '.workloads[].env.DATABASE_URL // empty'

  # connect to whatever this project calls its database
  psql "$(` + Name() + ` env config dev --json | jq -r '.workloads[].env.DATABASE_URL // empty' | head -1)"

A declared secret the env's secret store cannot supply is reported, not
fatal: its variable is left out, it is listed on stderr (and under
"missing_secrets" in --json), and the command still exits 0. Running a process
without it is the error, so ` + Name() + ` env exec and env up refuse. Pass --strict to
fail here too.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			envName := args[0]
			projectDir := projectDirForKCL()

			entities, err := readEnvConfigEntities(cmd.Context(), projectDir, envName)
			if err != nil {
				return err
			}
			mode := secretsReported
			if strict {
				mode = secretsRequired
			}
			report, err := resolvedEnvConfigReport(cmd.Context(), projectDir, envName, entities, nameFilter, mode)
			if err != nil {
				return err
			}
			reportMissingSecrets(cmd.ErrOrStderr(), envName, report)

			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(report)
			}
			return renderEnvConfigTable(cmd, report)
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit machine-readable JSON")
	cmd.Flags().StringVar(&nameFilter, "workload", "", "Only this workload (service, job, frontend, operator or cronjob)")
	cmd.Flags().BoolVar(&strict, "strict", false, "Fail (exit 1) when a declared secret is missing from the secret store, as env exec does, instead of reporting it")
	return cmd
}

// reportMissingSecrets warns, on stderr, about declared secrets the store
// lacks. stderr so a --json document on stdout stays exactly the document.
func reportMissingSecrets(w io.Writer, env string, report envConfigReport) {
	missing := report.missingSecrets()
	if len(missing) == 0 {
		return
	}
	store := report.SecretStore
	if store == "" {
		store = "the secret store"
	}
	fmt.Fprintf(w, "warning: %d declared secret(s) are missing from %s and are left out of the output below:\n", len(missing), store)
	width := 0
	for _, m := range missing {
		width = max(width, len(m.Name))
	}
	for _, m := range missing {
		fmt.Fprintf(w, "    %-*s   (Secret %s/%s)\n", width, m.Name, m.Secret, m.Key)
	}
	fmt.Fprintf(w, "  fix: %s secret set --env %s <KEY>. %s env exec and env up refuse until they are set.\n", Name(), env, Name())
}

// collectEnvConfigWorkloads flattens every workload kind into one ordered
// list. Every kind that carries env vars is included: a value a workload is
// launched with is worth reading back regardless of which lowering runs it.
func collectEnvConfigWorkloads(e *KCLEntities) []envConfigWorkload {
	var out []envConfigWorkload
	add := func(name, kind string, vars []KCLEnvVar) {
		out = append(out, envConfigWorkload{Name: name, Kind: kind, Env: envVarMap(vars)})
	}
	for _, w := range e.Workloads {
		// A workload declares ONE env for every runtime (spec.env).
		add(w.Name, w.Kind, w.EnvVars())
	}
	for _, f := range e.Frontends {
		add(f.Name, "frontend", f.EffectiveEnvVars())
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// envVarMap converts the rendered slice to a map. Later entries win, which
// matches the name-keyed LAST-wins merge the KCL layer performs.
func envVarMap(vars []KCLEnvVar) map[string]string {
	if len(vars) == 0 {
		return nil
	}
	m := make(map[string]string, len(vars))
	for _, v := range vars {
		m[v.Name] = v.Value
	}
	return m
}

// overlayLaunchedEnv replaces rendered values with the ones the running
// stack was actually launched with, where a persist exists.
//
// Only variables the launch recorded are overlaid; everything else keeps its
// rendered value, so a stopped stack still reports a complete environment.
func overlayLaunchedEnv(report *envConfigReport, projectDir, envName string) {
	st := loadResolvedEnv(projectIDForDir(projectDir), envName)
	if st == nil || st.DatabaseURL == "" {
		return
	}
	// The persist records the DSN the stack dialed. Overlay it only where
	// the render already declared the same variable — this reports what
	// launched, it does not invent configuration a workload never had.
	for i := range report.Workloads {
		if _, declared := report.Workloads[i].Env["DATABASE_URL"]; declared {
			report.Workloads[i].Env["DATABASE_URL"] = st.DatabaseURL
		}
	}
}

// filterEnvConfigWorkloads narrows to one workload by name.
func filterEnvConfigWorkloads(in []envConfigWorkload, name string) []envConfigWorkload {
	var out []envConfigWorkload
	for _, w := range in {
		if strings.EqualFold(w.Name, name) {
			out = append(out, w)
		}
	}
	return out
}

// renderEnvConfigTable writes the human view: one block per workload, its
// variables sorted by name.
func renderEnvConfigTable(cmd *cobra.Command, report envConfigReport) error {
	out := cmd.OutOrStdout()
	if len(report.Workloads) == 0 {
		fmt.Fprintf(out, "deploy/kcl/%s/ declares no workloads.\n", report.Env)
		return nil
	}
	for i, w := range report.Workloads {
		if i > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintf(out, "%s (%s)\n", w.Name, w.Kind)
		if len(w.Env) == 0 {
			fmt.Fprintln(out, "  (no configuration declared)")
			continue
		}
		names := make([]string, 0, len(w.Env))
		for k := range w.Env {
			names = append(names, k)
		}
		sort.Strings(names)

		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		for _, n := range names {
			fmt.Fprintf(tw, "  %s\t%s\n", n, w.Env[n])
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	return nil
}
