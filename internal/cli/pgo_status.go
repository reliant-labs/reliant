package cli

// `forge pgo status`: per Go binary, the profile its build would read, where
// that profile came from (the provenance `forge pgo pull` writes into it),
// and the effective PGO config.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/pkg/profilequery"
)

type pgoStatusOpts struct {
	env     string
	jsonOut bool
}

func newPGOStatusCmd() *cobra.Command {
	var o pgoStatusOpts
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show each Go binary's default.pgo: provenance, age and effective PGO config",
		Long: `Show, per Go binary the env builds, the cmd/<bin>/default.pgo its build reads
(Go's -pgo=auto), where it came from — the env, services and window ` + "`forge pgo pull`" + `
recorded in it — how old its data is, and the effective PGO config read from
--env's KCL (default prod). It reads only local files and the env's declaration.

--json prints one document (schema forge.pgo.status/v1).`,
		Example: `  forge pgo status
  forge pgo status --env staging --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d, err := defaultPGODeps(cmd, "pgo status")
			if err != nil {
				return err
			}
			return runPGOStatus(cmd.Context(), d, o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.env, "env", "", "the env whose KCL build config to read (default: prod)")
	f.BoolVar(&o.jsonOut, "json", false, "print the result as a JSON document")
	return cmd
}

const pgoStatusSchema = "forge.pgo.status/v1"

type pgoStatusDoc struct {
	Schema   string            `json:"schema"`
	Env      string            `json:"env"`
	Binaries []pgoStatusBinary `json:"binaries"`
}

type pgoStatusBinary struct {
	Package string `json:"package"`
	Path    string `json:"path"`
	// Config is the effective PGO the env declares for the binary.
	Config pgoStatusConfig `json:"config"`
	// Exists reports whether the profile file is there; Description is the
	// one-line account the build prints ("from prod, data up to 6d old").
	Exists      bool   `json:"exists"`
	Description string `json:"description,omitempty"`
	// ForgeWritten is false for a file `forge pgo pull` did not write.
	ForgeWritten bool `json:"forge_written"`
	// Provenance is what pull recorded in the file; nil when it holds none.
	Provenance *pgoProvenance `json:"provenance,omitempty"`
}

type pgoStatusConfig struct {
	Enabled     bool     `json:"enabled"`
	FromEnv     string   `json:"from_env"`
	Since       string   `json:"since"`
	MinWeight   string   `json:"min_weight"`
	MinWeightNS int64    `json:"min_weight_ns"`
	Services    []string `json:"services"`
	// ServicesDeclared is true when pgo.services names them, false when they
	// are the workloads of the env that run the binary.
	ServicesDeclared bool `json:"services_declared"`
}

type pgoProvenance struct {
	Env           string     `json:"env"`
	Services      []string   `json:"services"`
	From          *time.Time `json:"from,omitempty"`
	To            *time.Time `json:"to,omitempty"`
	Oldest        *time.Time `json:"oldest,omitempty"`
	Newest        *time.Time `json:"newest,omitempty"`
	Edges         int        `json:"edges"`
	TotalWeightNS int64      `json:"total_weight_ns"`
}

func runPGOStatus(ctx context.Context, d pgoDeps, o pgoStatusOpts) error {
	env, err := pgoEnv(d, o.env)
	if err != nil {
		return err
	}
	bins, _, err := pgoBinaries(ctx, d, env, nil)
	if err != nil {
		return err
	}
	now := d.Now()
	doc := pgoStatusDoc{Schema: pgoStatusSchema, Env: env, Binaries: []pgoStatusBinary{}}
	for _, b := range bins {
		doc.Binaries = append(doc.Binaries, pgoBinaryStatus(d.ProjectDir, b, now))
	}
	if o.jsonOut {
		enc := json.NewEncoder(d.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(doc)
	}
	printPGOStatus(d.Stdout, doc)
	return nil
}

func pgoBinaryStatus(projectDir string, b PGOBinary, now time.Time) pgoStatusBinary {
	s := pgoStatusBinary{
		Package: b.Package,
		Path:    b.ProfilePath(),
		Config: pgoStatusConfig{
			Enabled: b.PGO.Enabled, FromEnv: b.PGO.FromEnv,
			Since: pgoDuration(b.PGO.Since), MinWeight: pgoDuration(b.PGO.MinWeight), MinWeightNS: b.PGO.MinWeight.Nanoseconds(),
			Services: append([]string{}, b.Services...), ServicesDeclared: len(b.PGO.Services) > 0,
		},
	}
	path := filepath.Join(projectDir, filepath.FromSlash(s.Path))
	s.Description = describePGOFile(path, now)
	f, err := os.Open(path)
	if err != nil {
		return s
	}
	defer func() { _ = f.Close() }()
	if info, err := f.Stat(); err != nil || info.IsDir() {
		return s
	}
	s.Exists = true
	// describePGOFile already said why a file that is not forge's is not.
	p, err := profilequery.ReadPGO(f)
	if err != nil {
		return s
	}
	s.ForgeWritten = true
	s.Provenance = &pgoProvenance{
		Env: p.Env, Services: append([]string{}, p.Services...), Edges: len(p.Edges), TotalWeightNS: p.TotalWeight(),
		From: pgoTime(p.From), To: pgoTime(p.To), Oldest: pgoTime(p.Oldest), Newest: pgoTime(p.Newest),
	}
	return s
}

// pgoTime is t for a document: nil when unknown.
func pgoTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func printPGOStatus(w io.Writer, doc pgoStatusDoc) {
	fmt.Fprintf(w, "pgo status: config from env %s, %d Go binar%s\n", doc.Env, len(doc.Binaries), plural(len(doc.Binaries), "y", "ies"))
	if len(doc.Binaries) == 0 {
		fmt.Fprintf(w, "  env %s builds no Go binary\n", doc.Env)
		return
	}
	missing := false
	for _, b := range doc.Binaries {
		fmt.Fprintf(w, "  %s\n", b.Package)
		switch {
		case !b.Config.Enabled:
			state := "absent"
			if b.Exists {
				state = "present but unused"
			}
			fmt.Fprintf(w, "      profile: off (pgo.enabled = False; builds pass -pgo=off); %s %s\n", b.Path, state)
			continue
		case !b.Exists:
			missing = true
			fmt.Fprintf(w, "      profile: none — builds run without PGO (forge pgo pull writes %s)\n", b.Path)
		default:
			line := b.Path + " (" + b.Description + ")"
			if p := b.Provenance; p != nil {
				line += fmt.Sprintf(": %d edge%s, %s edge weight", p.Edges, plural(p.Edges, "", "s"), pgoDuration(time.Duration(p.TotalWeightNS)))
				if len(p.Services) > 0 {
					line += ", services " + strings.Join(p.Services, ", ")
				}
			}
			fmt.Fprintf(w, "      profile: %s\n", line)
		}
		services := "the workloads that run it: " + strings.Join(b.Config.Services, ", ")
		if b.Config.ServicesDeclared {
			services = strings.Join(b.Config.Services, ", ") + " (pgo.services)"
		} else if b.Config.FromEnv != doc.Env {
			services = "the workloads of " + b.Config.FromEnv + " that run it"
		} else if len(b.Config.Services) == 0 {
			services = "none (no workload runs it)"
		}
		fmt.Fprintf(w, "      config:  from_env=%s since=%s min_weight=%s services=%s\n", b.Config.FromEnv, b.Config.Since, b.Config.MinWeight, services)
	}
	if missing {
		fmt.Fprintf(w, "\nforge pgo pull --env %s writes the missing profiles; commit them.\n", doc.Env)
	}
}
