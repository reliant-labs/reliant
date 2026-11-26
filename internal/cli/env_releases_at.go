package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/obsout"
	"github.com/reliant-labs/forge/internal/releasetimeline"
)

// releasesAtDoc is the `at --json` document.
type releasesAtDoc struct {
	obsout.Head
	Env     string `json:"env"`
	Backend string `json:"backend"`
	Ledger  string `json:"ledger"`
	// Query is what the user asked about, as typed; ResolvedFrom says how it
	// became a time ("timestamp", or "trace" when the trace's first span set it).
	Query        string `json:"query"`
	ResolvedFrom string `json:"resolved_from"`
	// AtResult carries env, at, live, previous, next and the flags. Its Env
	// duplicates the field above, so Env is the one set here and AtResult's
	// is shadowed by the shallower field.
	releasetimeline.AtResult
	Warnings  []string `json:"warnings,omitempty"`
	Suggested []string `json:"suggested,omitempty"`
}

var releasesAtSchema = obsout.Schema{
	Name: schemaReleasesAt,
	Fields: append(append([]string{
		"env", "backend", "ledger", "query", "resolved_from", "at",
		"live", "previous", "next", "before_history", "history_incomplete", "unsettled",
		"warnings", "suggested",
	}, prefixed("live.", entryFields)...), append(prefixed("previous.", entryFields), prefixed("next.", entryFields)...)...),
}

// traceIDRe is a hex trace id. A timestamp always contains a '-' or ':' and is
// refused here, so the two forms never overlap ("20261021" is a date only
// because it is 8 digits; trace ids are 16 or 32).
var traceIDRe = regexp.MustCompile(`^[0-9a-fA-F]{16}$|^[0-9a-fA-F]{32}$`)

func newEnvReleasesAtCmd(deps releasesDeps) *cobra.Command {
	var flags obsout.Flags
	cmd := &cobra.Command{
		Use:   "at <env> <timestamp|trace-id>",
		Short: "Which release was live at a moment, and the releases either side",
		Args:  cobra.ExactArgs(2),
		Long: `Answer "which release was live then": the promotion in force at a timestamp, the
one before it and the one after. A timestamp is RFC 3339 or a date; a duration
means that long ago ("90m", "2d"); "now" is now. A 16- or 32-digit hex string is
a trace id: its first span's start time is used (control-plane environments only).

A promotion made exactly at the time is the live one. A time before the first
promotion on record says so (before_history) rather than naming a release, and a
history the source did not fully read says that instead (history_incomplete).
unsettled means the release's rollout had not finished at that moment, so the
environment may still have been serving the one before it.

Exit 0 the question was answered (including "nothing was live yet"), 2 it could
not be asked.

` + releasesAtSchema.Help(),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			fields, err := flags.Parsed(releasesAtSchema)
			if err != nil {
				return usagef("%v", err)
			}
			doc, err := runReleasesAt(cmd.Context(), deps, args[0], args[1])
			if err != nil {
				return failJSON(cmd, flags, releasesAtSchema, err,
					releasesCmd("env", "releases", "list", args[0]))
			}
			writeWarnings(cmd.ErrOrStderr(), doc.Warnings)
			if ok, err := flags.Emit(cmd.OutOrStdout(), releasesAtSchema, doc, fields); ok || err != nil {
				return err
			}
			renderReleasesAt(cmd.OutOrStdout(), doc)
			return nil
		},
	}
	// Only the output flags that mean something for a three-row answer.
	flags.Register(cmd.Flags(), 0)
	_ = cmd.Flags().MarkHidden("limit")
	return cmd
}

func runReleasesAt(ctx context.Context, deps releasesDeps, env, when string) (releasesAtDoc, error) {
	now := deps.now()
	b, err := deps.open(ctx, env)
	if err != nil {
		return releasesAtDoc{}, err
	}

	var at time.Time
	resolved := "timestamp"
	if traceIDRe.MatchString(when) {
		if b.Cloud == nil {
			return releasesAtDoc{}, usagef("%q looks like a trace id, but %s keeps its ledger on this machine and has no traces: "+
				"pass a timestamp instead", when, env)
		}
		if at, err = releasetimeline.TraceStart(ctx, *b.Cloud, env, when, now); err != nil {
			if errors.Is(err, releasetimeline.ErrTraceNotFound) {
				return releasesAtDoc{}, usagef("%v (traces are kept 7 days; pass the time the request happened instead)", err)
			}
			return releasesAtDoc{}, releasesError("resolve the trace "+when, err)
		}
		resolved = "trace"
	} else {
		if at, err = parseWhen(when, now, true); err != nil {
			return releasesAtDoc{}, usagef("%v", err)
		}
		if at.IsZero() {
			return releasesAtDoc{}, usagef("name a time or a trace id")
		}
	}
	if at.After(now) {
		return releasesAtDoc{}, usagef("%s is in the future (now is %s)", at.Format(time.RFC3339), now.UTC().Format(time.RFC3339))
	}

	// Everything from before the moment is needed to find the live release;
	// the source bounds itself by --since only, so give it the moment.
	data, err := b.Source.Load(ctx, env, at)
	if err != nil {
		return releasesAtDoc{}, releasesError("read the release timeline of "+env, err)
	}
	tl := releasetimeline.Assemble(data, time.Time{}, 0)
	res := tl.At(at)

	doc := releasesAtDoc{
		Env: env, Backend: b.Name, Ledger: b.Ledger, Query: when, ResolvedFrom: resolved,
		AtResult: res, Warnings: data.Warnings,
	}
	doc.Suggested = suggestFromAt(env, res)
	doc.Stamp(schemaReleasesAt, nil)
	return doc, nil
}

func suggestFromAt(env string, r releasetimeline.AtResult) []string {
	ref := func(e *releasetimeline.Entry) string {
		if e == nil {
			return ""
		}
		id := e.PromotionID
		if id == "" {
			id = e.Version
		}
		return releasesCmd("env", "releases", "show", env, id)
	}
	return obsout.Suggest(ref(r.Live), ref(r.Next), ref(r.Previous),
		releasesCmd("env", "releases", "list", env))
}

func renderReleasesAt(w io.Writer, doc releasesAtDoc) {
	fmt.Fprintf(w, "Release live in %s at %s", doc.Env, doc.At.UTC().Format(time.RFC3339))
	if doc.ResolvedFrom == "trace" {
		fmt.Fprintf(w, "  (start of trace %s)", doc.Query)
	}
	fmt.Fprintf(w, "\n\n")
	switch {
	case doc.Live != nil:
		fmt.Fprintf(w, "  live      %s\n", describeEntry(*doc.Live))
	case doc.HistoryIncomplete:
		fmt.Fprintf(w, "  live      unknown: the history read starts after this moment (widen it, or ask the ledger directly)\n")
	default:
		fmt.Fprintf(w, "  live      nothing: %s had not been promoted yet\n", doc.Env)
	}
	if doc.Unsettled {
		fmt.Fprintf(w, "  note      its rollout had not finished at this moment; the previous release may still have been serving\n")
	}
	if doc.Previous != nil {
		fmt.Fprintf(w, "  before    %s\n", describeEntry(*doc.Previous))
	}
	if doc.Next != nil {
		fmt.Fprintf(w, "  after     %s\n", describeEntry(*doc.Next))
	}
	if len(doc.Suggested) > 0 {
		fmt.Fprintf(w, "\nnext:\n")
		for _, s := range doc.Suggested {
			fmt.Fprintf(w, "  %s\n", s)
		}
	}
}

func describeEntry(e releasetimeline.Entry) string {
	return fmt.Sprintf("%s  %s  %s  by %s", e.Version, e.At.UTC().Format("2006-01-02 15:04:05Z"), entryWhat(e), whoVia(e))
}
