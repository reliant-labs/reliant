package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/bugreport"
	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/generator"
)

// `forge report-bug` files a defect in forge itself with the Reliant
// engineering team. It is the standalone forge's counterpart of reliant's
// report_bug agent tool, for a person or an agent in any harness. forge
// sends nothing unless someone runs it (internal/bugreport,
// docs/report-bug.md).

// reportBugUsage is the guidance every surface gives: when to report, when
// not to, and what never belongs in a report. The agent guide
// (reliant.md.tmpl) and the forge skill carry the same words.
const reportBugUsage = `Report a defect in forge itself to the Reliant engineering team. One call files it; there is nothing to follow up.

Use it when forge misbehaves, not the user's code: a forge command fails, hangs or generates something wrong, or a forge lint, skill or doc is wrong.

Do not use it for bugs in the user's own application, for test or build failures in their code, or for your own mistakes.

File each defect once: a repeat of the same title within 30 minutes returns the first report's id and sends nothing. Put what you observed in evidence — exact commands, error text, file paths — never credentials or the user's file contents. Then carry on with your task, and still tell the user when the defect blocks them.`

func newReportBugCmd() *cobra.Command {
	var (
		r                      bugreport.Report
		fromJSON, dryRun, anon bool
		output                 string
		textFlags              = []string{"severity", "title", "summary", "expected", "actual", "evidence"}
	)
	cmd := &cobra.Command{
		Use:   "report-bug --severity <level> --title <line> [--summary ...] [--expected ...] [--actual ...] [--evidence ...]",
		Short: "Report a defect in forge itself to the Reliant engineering team",
		Long: reportBugUsage + `

What is sent: the report's fields, redacted of credential shapes and home
directories; forge's version, OS and architecture; the command line with
secrets removed; the project's pinned forge_version; the agent harness
forge detected; and a random install id used only for rate limiting. Never
file contents or environment values. --dry-run prints the exact payload
without sending it. forge sends nothing unless someone runs this command.

When you are logged in (forge login), the report carries your user id so
the team can follow up; --anonymous leaves it off.

Exit status is non-zero when the report could not be filed.`,
		Example: `  forge report-bug --severity high \
    --title "forge generate drops optional fields from list filters" \
    --actual "ListTasksRequest.status is missing from the generated filter" \
    --evidence "forge generate on proto/services/tasks/v1/tasks.proto; forge v0.1.48"

  echo '{"severity":"low","title":"typo in forge lint --help","actual":"\"receive\""}' | forge report-bug --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if fromJSON {
				for _, name := range textFlags {
					if cmd.Flags().Changed(name) {
						return fmt.Errorf("--json reads the whole report from stdin; drop --%s", name)
					}
				}
				parsed, err := readReportJSON(cmd.InOrStdin())
				if err != nil {
					return err
				}
				r = parsed
			}
			if output != "text" && output != "json" {
				return fmt.Errorf("--output must be text or json (got %q)", output)
			}
			filer, rctx := forgeBugReportFiler(anon), forgeBugReportContext()
			if dryRun {
				p, err := filer.Preview(r, rctx)
				if err != nil {
					return err
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				fmt.Fprintf(cmd.ErrOrStderr(), "dry run: nothing sent. This is the request body for %s/%s (the report id is minted when it is sent):\n", filer.Endpoint, bugreport.Procedure)
				return enc.Encode(p)
			}
			res, err := filer.File(cmd.Context(), r, rctx)
			if err != nil {
				return err
			}
			return printReportResult(cmd.OutOrStdout(), output, res)
		},
	}
	f := cmd.Flags()
	f.StringVar(&r.Severity, "severity", "", "critical (data loss, or nothing can proceed), high (it blocks your task), medium (there is a workaround) or low (cosmetic)")
	f.StringVar(&r.Title, "title", "", "one line naming the defect; ids and numbers go in --evidence")
	f.StringVar(&r.Summary, "summary", "", "what happened, in a few sentences")
	f.StringVar(&r.Expected, "expected", "", "what should have happened")
	f.StringVar(&r.Actual, "actual", "", "what happened instead, with the exact error text")
	f.StringVar(&r.Evidence, "evidence", "", "how to reproduce it: commands run, their output, file paths")
	f.BoolVar(&fromJSON, "json", false, "read the report from stdin as a JSON object with the fields severity, title, summary, expected, actual, evidence")
	f.BoolVar(&dryRun, "dry-run", false, "print exactly what would be sent, and send nothing")
	f.BoolVar(&anon, "anonymous", false, "do not attach your forge login, even when you are logged in")
	f.StringVarP(&output, "output", "o", "text", "text or json")
	return cmd
}

// readReportJSON reads one report object from r, refusing unknown fields so
// a misspelled field is an error rather than a silently empty one.
func readReportJSON(r io.Reader) (bugreport.Report, error) {
	dec := json.NewDecoder(io.LimitReader(r, 1<<20))
	dec.DisallowUnknownFields()
	var rep bugreport.Report
	if err := dec.Decode(&rep); err != nil {
		return bugreport.Report{}, fmt.Errorf("--json: reading the report from stdin: %w", err)
	}
	return rep, nil
}

func printReportResult(w io.Writer, output string, res bugreport.Result) error {
	if output == "json" {
		return json.NewEncoder(w).Encode(res)
	}
	if res.Duplicate {
		_, err := fmt.Fprintf(w, "Already reported %s ago as %s; nothing sent.\n", time.Since(res.FiledAt).Round(time.Second), res.ReportID)
		return err
	}
	_, err := fmt.Fprintf(w, "Filed forge bug report %s. Thank you.\n", res.ReportID)
	return err
}

// forgeBugReportFiler targets the reporting endpoint with forge's state
// directory, presenting the stored forge login for that endpoint unless
// anonymous. A login that does not resolve is simply not presented.
func forgeBugReportFiler(anonymous bool) *bugreport.Filer {
	endpoint := bugreport.DefaultEndpoint()
	f := &bugreport.Filer{Endpoint: endpoint, State: bugreport.NewState(forgeStateDir())}
	if !anonymous {
		if cred, err := cloud.ResolveCredential("", cloud.Endpoint{URL: endpoint}); err == nil {
			f.Token = cred.Token
		}
	}
	return f
}

// forgeStateDir is the directory forge keeps per-user state in: the one that
// holds credentials.json (FORGE_HOME, else $XDG_CONFIG_HOME/forge, else
// ~/.config/forge).
func forgeStateDir() string {
	if p, err := cloud.CredentialsPath(); err == nil {
		return filepath.Dir(p)
	}
	return filepath.Join(os.TempDir(), "forge")
}

// forgeBugReportContext is what forge knows about where it ran.
func forgeBugReportContext() bugreport.Context {
	c := bugreport.Context{ForgeVersion: buildinfo.Version(), Args: os.Args}
	if h, _ := generator.DetectHarness(os.Getenv); h != "" {
		c.Harness = string(h)
	}
	if root, err := findProjectRoot(); err == nil {
		if pin, ok := readPinFields(root); ok {
			c.ProjectForgeVersion = strings.TrimSpace(pin.ForgeVersion)
		}
	}
	return c
}
