package cli

// `forge explain`: the CLI twin of the MCP explain_error tool. It reads the
// same table (explain_error.go), and --json prints the document the tool
// returns as its structured content, so a person at a terminal and an agent
// over MCP get one answer.

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cliutil"
)

// explainListing is `forge explain --json` with nothing to explain: every
// error_reason and exit code it knows, each with its explanation.
type explainListing struct {
	ErrorReasons []explainedReason   `json:"error_reasons"`
	ExitCodes    []explainedExitCode `json:"exit_codes"`
}

type explainedReason struct {
	ErrorReason string `json:"error_reason"`
	forgeErrorExplanation
}

type explainedExitCode struct {
	ExitCode int `json:"exit_code"`
	forgeErrorExplanation
}

func newExplainCmd() *cobra.Command {
	var (
		jsonOut bool
		env     string
		errText string
	)
	cmd := &cobra.Command{
		Use:   "explain [error_reason|exit_code] [exit_code]",
		Short: "Explain a forge failure: what it means, the command to run next, and whether retrying helps",
		Long: `Explain a forge failure from its error_reason (the field a --json document
carries), its exit code, or its error text: what happened, the exact command to
run next, and whether re-running the same command can succeed ("now", "later",
or "no"). It is the MCP explain_error tool on the command line, and reads the
same table.

An error_reason decides when this forge knows it; otherwise the error text
(--error); otherwise the exit code. Pass both a reason and a code when you have
them, so a reason newer than this forge still gets the code's answer. Works
anywhere: it reads nothing from the directory.

  forge explain                          every error_reason and exit code it knows
  forge explain claimable_throttled      one reason
  forge explain 7                        one exit code
  forge explain claimable_throttled 4    a reason, falling back to its exit code
  forge explain --error "<text>"         the error text forge printed
  forge explain 8 --env staging          next commands name the env (default prod)
  forge explain 4 --json                 the explain_error tool's document

Something it does not know exits 1 and lists what it does.`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if len(args) == 0 && errText == "" {
				return writeExplainListing(out, env, jsonOut)
			}
			code, reason, err := parseExplainArgs(args)
			if err != nil {
				return err
			}
			ex, ok := explainForgeErrorMatch(code, reason, errText, env)
			if !ok {
				return explainUnknownErr(args, errText)
			}
			if jsonOut {
				return writeExplainJSON(out, ex)
			}
			_, err = fmt.Fprintln(out, ex.text())
			return err
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print the explain_error tool's document (meaning, next, retry); with no argument, every known reason and code")
	cmd.Flags().StringVar(&env, "env", claimableDefaultEnv, "The env the failing command acted on, so the next commands name it")
	cmd.Flags().StringVar(&errText, "error", "", "The error text forge printed, for a failure with no error_reason")
	return cmd
}

// parseExplainArgs splits the arguments into an exit code (-1 when none) and
// an error_reason: an integer is the code, anything else the reason, at most
// one of each.
func parseExplainArgs(args []string) (code int, reason string, err error) {
	code = -1
	haveCode := false
	for _, a := range args {
		if n, convErr := strconv.Atoi(a); convErr == nil {
			if haveCode {
				return 0, "", explainUsageErr(fmt.Sprintf("two exit codes (%d and %d)", code, n))
			}
			if n < 0 {
				return 0, "", explainUsageErr(fmt.Sprintf("exit code %d is negative", n))
			}
			code, haveCode = n, true
			continue
		}
		if reason != "" {
			return 0, "", explainUsageErr(fmt.Sprintf("two error reasons (%q and %q)", reason, a))
		}
		reason = a
	}
	return code, reason, nil
}

func explainUsageErr(what string) error {
	return exitCodeError{code: exitWrong, msg: cliutil.UserErr(Name()+" explain", what, "",
		"pass at most one error_reason and one exit code").Error()}
}

// explainUnknownErr says nothing given is known, and lists what is.
func explainUnknownErr(args []string, errText string) error {
	given := make([]string, 0, len(args)+1)
	for _, a := range args {
		given = append(given, strconv.Quote(a))
	}
	if errText != "" {
		given = append(given, "the error text")
	}
	codes := make([]string, 0, len(exitCodeExplanations))
	for _, c := range knownExitCodes() {
		codes = append(codes, strconv.Itoa(c))
	}
	return exitCodeError{code: exitWrong, msg: cliutil.UserErr(Name()+" explain",
		fmt.Sprintf("this forge has no explanation for %s", strings.Join(given, " or ")), "",
		fmt.Sprintf("pass one of the error reasons %s, or an exit code %s; '%s explain' lists each with its meaning. A failure it cannot name is explained by its own error text",
			strings.Join(knownErrorReasons(), ", "), strings.Join(codes, ", "), Name())).Error()}
}

func writeExplainJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// writeExplainListing prints every reason and code the table explains.
func writeExplainListing(w io.Writer, env string, jsonOut bool) error {
	c := newExplainCtx(env)
	var l explainListing
	for _, r := range reasonExplanations {
		for _, reason := range r.reasons {
			l.ErrorReasons = append(l.ErrorReasons, explainedReason{reason, r.explain(c)})
		}
	}
	for _, e := range exitCodeExplanations {
		l.ExitCodes = append(l.ExitCodes, explainedExitCode{e.code, e.explain(c)})
	}
	if jsonOut {
		return writeExplainJSON(w, l)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "Error reasons (error_reason in a --json document):")
	for _, r := range l.ErrorReasons {
		fmt.Fprintf(tw, "  %s\t%s\n", r.ErrorReason, firstSentence(r.Meaning))
	}
	fmt.Fprintln(tw, "\nExit codes:")
	for _, e := range l.ExitCodes {
		fmt.Fprintf(tw, "  %d\t%s\n", e.ExitCode, firstSentence(e.Meaning))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "\nOne in full, with the command to run next: %s explain <error_reason|exit_code>\n", Name())
	return err
}
