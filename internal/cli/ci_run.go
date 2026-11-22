package cli

// `forge ci run <run-id>`: one CI run's timeline, read from the control plane
// (control-plane docs/design/hosted-deploy-primitives.md §3.7, task F8).
//
// THE SPEC CALLED THIS `forge run show`, and it was renamed for a collision:
// at the time, a top-level `run` was the dev-server runner and took
// positional arguments of its own, so `run show` would have captured a
// dev-server argument named "show" and given one word two unrelated meanings
// ("run my dev server" / "a CI pipeline run"). The `ci` group already holds
// the CI-facing verbs.
//
// That dev-server command has since been deleted (the local lifecycle is
// `forge env up <env>`), so the collision no longer exists — but the name
// does not move back. `ci run` is where this verb shipped, and "run" as a
// bare top-level verb is the ambiguity the `ci` prefix was chosen to avoid
// in the first place.
//
// NOTHING HERE ASSEMBLES A TIMELINE. The control plane's GetRun derives every
// stage from the ledger — the cut, each promotion, each recorded gate carrying
// the run id, each promotion's rollout — and this verb reads that answer and
// collapses it with release.RunTimeline.Verdict, the same function the server
// side is tested against. A second derivation here would be a second opinion
// about whether the run passed.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/release"
)

const procGetRun = "controlplane.v1.DeployService/GetRun"

func newCIRunCmd() *cobra.Command {
	var (
		env     string
		token   string
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "run <run-id>",
		Short: "Show one CI run's timeline — cut, checks, promotes, rollouts — and whether it passed",
		Long: `Read one CI run's timeline from the hosted control plane and say whether it
passed.

A run is everything one pipeline attempt wrote under its run id: the release it
cut, the checks it recorded (` + "`forge gate record`" + `), every promotion it made, and
each promotion's rollout. Nothing is a stored event — the control plane derives
the stages from the ledger on every read, so polling this is safe.

The run id is the one forge stamped on those writes: --run-id, or the CI
default (` + "`github:<owner/repo>/<run>/<attempt>`" + ` under GitHub Actions).

WHICH CONTROL PLANE: --env names an env whose KCL declares it. Without --env,
the project's single declared control plane is used; with several, --env is
required. A run can span envs, so the env only chooses where to ask.

Exit codes (the hosted primitives' shared table):
  0  every stage passed (skipped stages count as passed)
  1  a stage failed or errored, or the request cannot be served (a run too
     large to read as one timeline)
  2  the control plane could not be read — unreachable, auth refused, or it
     does not serve run timelines
  5  a stage is still running: retry this command, the run has not finished

A run id nothing has written under is an EMPTY timeline and exits 5, never 0:
an id nobody used is not a pass. The control plane answers another
organisation's run id the same way, so this cannot be used to probe them.

Examples:
  forge ci run "github:acme/app/12345/1"
  forge ci run "$RUN_ID" --env prod --json | jq -r '.stages[] | select(.status != "passed")'`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			runID := strings.TrimSpace(args[0])
			if runID == "" {
				return errors.New("a run id is required: forge ci run <run-id>")
			}
			client, err := ciRunClient(cmd.Context(), env, token)
			if err != nil {
				return err
			}
			return runCIRun(cmd.Context(), client, runID, jsonOut, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&env, "env", "", "Env whose declared control plane to ask (default: the project's only one)")
	cmd.Flags().StringVar(&token, "token", "", "Credential to use, ahead of the env var and the credentials file")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit the timeline as JSON (same exit codes as text mode)")
	return cmd
}

// ciRunClient resolves the control plane to ask. A run spans envs, so the env
// is only a way to name an endpoint; without one, an unambiguous project
// declaration is enough.
func ciRunClient(ctx context.Context, env, token string) (cloudCaller, error) {
	if env != "" {
		ep, cred, err := resolveCloudTarget(ctx, env, token)
		if err != nil {
			return nil, err
		}
		return cloud.NewClient(ep, cred), nil
	}
	planes, _, err := declaredControlPlanes(ctx)
	if err != nil {
		return nil, err
	}
	switch len(planes) {
	case 0:
		return nil, errors.New("no env in this project declares a control plane (forge.ControlPlane); pass --env <env> for one that does")
	case 1:
		cred, err := cloud.ResolveCredential(token, planes[0].Endpoint)
		if err != nil {
			return nil, err
		}
		return cloud.NewClient(planes[0].Endpoint, cred), nil
	default:
		names := make([]string, 0, len(planes))
		for _, p := range planes {
			names = append(names, fmt.Sprintf("%s (envs: %s)", p.Endpoint.URL, strings.Join(p.Envs, ", ")))
		}
		return nil, fmt.Errorf("this project declares %d control planes, so the run's one is ambiguous; pass --env:\n  %s",
			len(planes), strings.Join(names, "\n  "))
	}
}

// ciRunDocument is the --json output: F0's envelope, the timeline, and its
// verdict.
type ciRunDocument struct {
	jsonEnvelope
	release.RunTimeline
	// Verdict is RunTimeline.Verdict — the value the exit code is derived
	// from, carried so a consumer does not re-implement the ranking.
	Verdict release.StageStatus `json:"verdict"`
}

// runCIRun reads, renders and decides. The verdict is computed once, before
// either renderer runs, so text and --json cannot disagree.
func runCIRun(ctx context.Context, client cloudCaller, runID string, jsonOut bool, out io.Writer) error {
	timeline, err := fetchRunTimeline(ctx, client, runID)
	if err != nil {
		return err
	}
	verdict := timeline.Verdict()
	result := ciRunVerdictError(runID, verdict, len(timeline.Stages))

	if jsonOut {
		doc := ciRunDocument{RunTimeline: timeline, Verdict: verdict}
		if doc.Stages == nil {
			doc.Stages = []release.Stage{}
		}
		doc.stamp(result)
		if err := emitJSONDocument(doc); err != nil {
			return err
		}
		return result
	}
	renderRunTimeline(out, timeline, verdict)
	return result
}

// ciRunVerdictError maps the verdict onto §3.A's table.
//
// ERRORED IS 1, NOT 2. A check that recorded `error` is a recorded fact about
// the run — the run did not pass, and re-reading will say so again. 2 means
// "could not look", which a pipeline treats as retryable; reporting an
// errored check that way would loop a pipeline on a result that cannot change.
func ciRunVerdictError(runID string, verdict release.StageStatus, stages int) error {
	switch verdict {
	case release.StagePassed:
		return nil
	case release.StageFailed, release.StageErrored:
		return exitCodeError{code: exitWrong, msg: fmt.Sprintf("run %s did not pass: a stage %s", runID, verdict)}
	default:
		if stages == 0 {
			return exitCodeError{code: exitTimedOut, msg: fmt.Sprintf(
				"run %s has written nothing on this control plane yet (or the id is wrong): an id nothing has written under is not a pass", runID)}
		}
		return exitCodeError{code: exitTimedOut, msg: fmt.Sprintf("run %s is still running: re-run this command to wait for it", runID)}
	}
}

// fetchRunTimeline calls GetRun and converts the answer.
//
// A FAILED READ IS EXIT 2 — "could not determine" — whatever the cause, with
// two exceptions that are about the request rather than the control plane: a
// run too large to serve as one timeline (resource_exhausted) and a malformed
// id (invalid_argument) are 1, because retrying the identical read cannot
// change them.
func fetchRunTimeline(ctx context.Context, client cloudCaller, runID string) (release.RunTimeline, error) {
	var resp struct {
		Run     *wireRun `json:"run,omitempty"`
		Release *struct {
			Version string `json:"version"`
		} `json:"release,omitempty"`
		Stages []wireRunStage `json:"stages"`
	}
	if err := client.Call(ctx, procGetRun, map[string]any{"runId": runID}, &resp); err != nil {
		code := exitUndetermined
		if hostedErrorHasCode(err, cloud.CodeResourceExhausted) || hostedErrorHasCode(err, cloud.CodeInvalidArgument) {
			code = exitWrong
		}
		msg := fmt.Sprintf("could not read run %s: %v", runID, err)
		if hostedErrorHasCode(err, cloud.CodeUnimplemented) {
			msg = fmt.Sprintf("this control plane does not serve run timelines (it predates GetRun, control-plane task C8): %v", err)
		}
		return release.RunTimeline{}, exitCodeError{code: code, msg: msg}
	}

	out := release.RunTimeline{Run: release.Run{ID: runID}}
	if resp.Run != nil {
		out.Run = runFromWire(resp.Run)
	}
	if resp.Release != nil {
		out.Release = resp.Release.Version
	}
	out.Stages = make([]release.Stage, 0, len(resp.Stages))
	for _, w := range resp.Stages {
		out.Stages = append(out.Stages, stageFromWire(w))
	}
	return out, nil
}

// stageFromWire converts one stage LENIENTLY FOR DISPLAY, CONSERVATIVELY FOR
// THE VERDICT. A newer control plane may send a status this forge does not
// know; refusing the whole timeline would hide a run that is otherwise
// readable, and reading the unknown word as anything that ranks below
// `failed` could turn a failure this binary cannot name into a pass. So it
// becomes `error` — never a pass — and the raw word is kept in the summary.
func stageFromWire(w wireRunStage) release.Stage {
	s := release.Stage{
		Kind:        release.StageKind(w.Kind),
		Name:        w.Name,
		PromotionID: w.PromotionID,
		StartedAt:   w.StartedAt,
		FinishedAt:  w.FinishedAt,
		URL:         w.URL,
		Summary:     w.Summary,
	}
	// A promote or rollout stage is NAMED by its environment (the server
	// sets name = env), and release.Stage carries that as Env too.
	if s.Kind == release.StagePromote || s.Kind == release.StageRollout {
		s.Env = w.Name
	}
	status, err := release.ParseStageStatus(w.Status)
	if err != nil {
		status = release.StageErrored
		note := fmt.Sprintf("unrecognised status %q from the control plane — upgrade forge", w.Status)
		if s.Summary != "" {
			note = s.Summary + "; " + note
		}
		s.Summary = note
	}
	s.Status = status
	return s
}

// renderRunTimeline prints the stage table and the verdict line.
func renderRunTimeline(out io.Writer, t release.RunTimeline, verdict release.StageStatus) {
	fmt.Fprintf(out, "Run %s", t.Run.ID)
	if t.Release != "" {
		fmt.Fprintf(out, " — release %s", t.Release)
	}
	fmt.Fprintln(out)
	if t.Run.URL != "" {
		fmt.Fprintf(out, "  %s\n", t.Run.URL)
	}
	fmt.Fprintln(out)

	if len(t.Stages) == 0 {
		fmt.Fprintln(out, "  no stage carries this run id on this control plane — the run has written nothing yet, or the id is wrong")
	} else {
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  STATUS\tSTAGE\tNAME\tSTARTED\tDURATION\tSUMMARY")
		for _, s := range t.Stages {
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%s\n",
				strings.ToUpper(string(s.Status)), s.Kind, s.Name, stageStarted(s), stageDuration(s), s.Summary)
		}
		_ = tw.Flush()
	}
	fmt.Fprintf(out, "\nVerdict: %s\n", strings.ToUpper(string(verdict)))
}

func stageStarted(s release.Stage) string {
	if s.StartedAt == nil {
		return "-"
	}
	return s.StartedAt.UTC().Format(time.RFC3339)
}

// stageDuration is finished − started, "-" when either is unknown, and
// "running" for an unfinished stage — never a duration measured to now,
// which would read as a recorded fact.
func stageDuration(s release.Stage) string {
	switch {
	case s.StartedAt == nil:
		return "-"
	case s.FinishedAt == nil && s.Status == release.StageRunning:
		return "running"
	case s.FinishedAt == nil:
		return "-"
	default:
		return s.FinishedAt.Sub(*s.StartedAt).Round(time.Second).String()
	}
}
