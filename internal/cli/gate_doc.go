package cli

// gateFromDocument: ONE parser that turns a JSON document into the gate it
// attests to (control-plane docs/design/hosted-deploy-primitives.md §3.3).
//
// WHY ONE PARSER AND NOT A GLUE SCRIPT PER VERB. Evidence is only worth
// recording if producing it is cheaper than not bothering. Every forge verb
// that can judge something already emits a `--json` document carrying the
// verdict, so the gate is a PROJECTION of a document the pipeline already
// has — `forge gate record prod --from wait.json` with no jq in between.
// The alternative, a shell snippet per verb mapping fields to --name/--status,
// is where the evidence trail quietly stops being written: each snippet is
// one more thing to get wrong, and a wrong one reports a pass.
//
// So this accepts TWO shapes, and the distinction between them is the whole
// design:
//
//   - A DeployGate-shaped document (it has a `name` AND a `status`), which is
//     what `--gate-json` writes and what another tool can produce directly.
//     Read as the gate it already is.
//   - Any forge `--json` document, which carries a verdict but was not written
//     to be a gate. Its `ok` / `exit_code` are the verdict (§3.A guarantees
//     they are computed from the same error the process exits with), and the
//     per-verb cases below supply the NAME and the one-line summary.
//
// ADDING A VERB IS ADDING ONE CASE. That is the property worth protecting:
// `documentShapes` is a table, each entry naming how to recognise a document
// and what to call the gate it yields. A new forge verb with a --json document
// becomes recordable without touching the parsing logic.
//
// A DOCUMENT THAT STATES NO VERDICT YIELDS NO GATE. Not a pass, not an error
// gate — a refusal, with the reason. An unrecognised document whose fields we
// guessed at would produce evidence about nothing, and "passed" is the guess
// a reader is most likely to trust and least able to check.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/release"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// gateDocument is the union of the fields this parser reads, across every
// shape it accepts. Decoding into one struct rather than a map keeps the
// recognition rules below readable as data; a field a document does not
// carry is simply absent.
//
// EVERY VERDICT FIELD IS A POINTER. `ok` and `exit_code` must be
// distinguishable from "absent": a document with no `ok` at all is not a
// document claiming ok:false, and treating absent-as-false would invent a
// failure verdict for a shape that never reported one (and absent-as-true
// would invent a pass, which is worse).
type gateDocument struct {
	// The DeployGate shape (release.Gate's own JSON).
	Name   string `json:"name,omitempty"`
	Status string `json:"status,omitempty"`
	URL    string `json:"url,omitempty"`
	// Summary is declared `json:"-"` down with the other summary shapes —
	// see the note there. decodeSummaries fills it.
	StartedAt  *time.Time     `json:"started_at,omitempty"`
	FinishedAt *time.Time     `json:"finished_at,omitempty"`
	RunID      string         `json:"run_id,omitempty"`
	Details    map[string]any `json:"details,omitempty"`

	// The forge --json envelope (§3.A): the verdict every forge document
	// carries, computed from the same error the process exits with.
	OK       *bool `json:"ok,omitempty"`
	ExitCode *int  `json:"exit_code,omitempty"`
	Error    string

	// Fields the per-verb cases below recognise a document BY, and
	// summarise FROM. Each is read by at most a couple of cases; they
	// live in one struct because a document is read once.

	// `forge lint --json`
	Findings []json.RawMessage `json:"findings,omitempty"`

	// `forge env smoke --json`
	Env    string            `json:"env,omitempty"`
	Routes []json.RawMessage `json:"routes,omitempty"`

	// THE `summary` KEY IS CLAIMED BY FOUR DIFFERENT SHAPES, so this
	// struct decodes it as NONE of them. A gate's `summary` is a string,
	// lint's is {errors,warnings,infos,total}, smoke's is
	// {pass,warn,fail,ok}, and release verify's is
	// {verified,failed,unverifiable,unreachable}.
	//
	// Two failure modes, both of which this `json:"-"` closes, and both
	// of which were real bugs here rather than hypotheticals:
	//
	//  1. Declaring two fields with the same tag makes encoding/json drop
	//     EVERY field in the conflicting set, so all of them decode as
	//     absent. The only symptom was a parser that refused every
	//     document for "stating no verdict".
	//  2. An UNTAGGED field is still matched case-insensitively, so
	//     `Summary string` without a tag still binds to `summary` — and a
	//     document whose summary is an object then fails the WHOLE
	//     unmarshal with a type error, taking the verdict down with it.
	//
	// So each shape is decoded by its own probe (decodeSummaries), into
	// the fields below, and recognition picks which one applies.
	Summary      string             `json:"-"`
	LintSummary  *lintSummaryProbe  `json:"-"`
	SmokeSummary *smokeSummaryProbe `json:"-"`

	// `forge env status --json` / `forge release verify --json`
	Release   string            `json:"release,omitempty"`
	Bound     *bool             `json:"bound,omitempty"`
	Images    []json.RawMessage `json:"images,omitempty"`
	Artifacts []json.RawMessage `json:"artifacts,omitempty"`
	Detail    string            `json:"detail,omitempty"`
	// Diagnostic is release verify's one-line reason.
	Diagnostic string `json:"diagnostic,omitempty"`

	// `forge env status --wait --json` (§3.2, task F3): the rollout phase is the
	// verdict's reason, and the richest thing a wait has to say.
	Phase  string `json:"phase,omitempty"`
	Reason string `json:"reason,omitempty"`

	// `forge ci verify-test-run --gate-json` and the test shape.
	Totals *struct {
		Tests   int `json:"tests"`
		Passed  int `json:"passed"`
		Failed  int `json:"failed"`
		Skipped int `json:"skipped"`
	} `json:"totals,omitempty"`
}

// The three `summary` shapes, each decoded by its own probe. See the note on
// gateDocument.Summary for why they cannot be fields of one struct.
type (
	lintSummaryProbe struct {
		Errors   int `json:"errors"`
		Warnings int `json:"warnings"`
		Infos    int `json:"infos"`
		Total    int `json:"total"`
	}
	smokeSummaryProbe struct {
		Pass int  `json:"pass"`
		Warn int  `json:"warn"`
		Fail int  `json:"fail"`
		OK   bool `json:"ok"`
	}
)

// decodeSummaries fills the three summary fields by decoding the document
// once per shape. A shape whose `summary` does not match decodes to a struct
// of zeroes, so each probe also reports whether the key was present AND
// carried that shape's own fields.
func decodeSummaries(data []byte, doc *gateDocument) {
	var asString struct {
		Summary string `json:"summary"`
	}
	if json.Unmarshal(data, &asString) == nil {
		doc.Summary = asString.Summary
	}
	var asLint struct {
		Summary *lintSummaryProbe `json:"summary"`
	}
	if json.Unmarshal(data, &asLint) == nil && asLint.Summary != nil {
		doc.LintSummary = asLint.Summary
	}
	// A smoke summary must be recognised by its OWN keys being present,
	// not merely by decoding without error: release verify's summary is
	// {verified,failed,unverifiable,unreachable}, which decodes into
	// smokeSummaryProbe as all-zeroes-plus-`failed` and would otherwise
	// read as a smoke that probed nothing. Requiring `pass` and `ok` —
	// which only smoke emits — keeps the two apart.
	var asSmoke struct {
		Summary *struct {
			Pass *int  `json:"pass"`
			Warn *int  `json:"warn"`
			Fail *int  `json:"fail"`
			OK   *bool `json:"ok"`
		} `json:"summary"`
	}
	if json.Unmarshal(data, &asSmoke) == nil && asSmoke.Summary != nil &&
		asSmoke.Summary.Pass != nil && asSmoke.Summary.OK != nil {
		s := smokeSummaryProbe{Pass: *asSmoke.Summary.Pass, OK: *asSmoke.Summary.OK}
		if asSmoke.Summary.Warn != nil {
			s.Warn = *asSmoke.Summary.Warn
		}
		if asSmoke.Summary.Fail != nil {
			s.Fail = *asSmoke.Summary.Fail
		}
		doc.SmokeSummary = &s
	}
}

// errNoGateInDocument is the refusal for a document that states no verdict.
// Distinguishable so a caller can say "that file is not a forge --json
// document" rather than reporting a parse failure.
var errNoGateInDocument = fmt.Errorf("%w: the document states no verdict", release.ErrInvalid)

// gateFromDocument parses one JSON document into the gate it attests to.
//
// nameHint names the gate when the document does not name itself and its
// shape is not recognised — `--name` on `forge gate record`. It is a HINT,
// not an override: a document that names itself wins, because the producer
// knows what it ran better than the person recording it.
func gateFromDocument(data []byte, nameHint string) (release.Gate, error) {
	var doc gateDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return release.Gate{}, fmt.Errorf("%w: not a JSON document: %v", release.ErrInvalid, err)
	}
	decodeSummaries(data, &doc)
	// `error` is a string in the envelope. Decoded separately because a
	// verb whose document carries a structured `error` would otherwise
	// fail the whole parse, and the envelope is not the part we need.
	var errProbe struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(data, &errProbe) == nil {
		doc.Error = errProbe.Error
	}

	// A document that already IS a gate is read as one, not re-derived.
	// It carries a status from the closed set, which is strictly more
	// information than `ok` can express: `skipped` and `error` are both
	// ok-shaped verdicts that are not passes.
	if doc.Name != "" && doc.Status != "" {
		return gateFromGateShape(doc)
	}

	gate, ok := gateFromForgeDocument(doc, nameHint)
	if !ok {
		return release.Gate{}, errNoGateInDocument
	}
	if err := gate.Validate(); err != nil {
		return release.Gate{}, err
	}
	return gate, nil
}

// gateFromDocumentFile is gateFromDocument over a path, with the file-level
// failures named as themselves: a missing file and a document forge cannot
// interpret are different problems with different fixes, and both are
// reported here rather than at every call site.
func gateFromDocumentFile(path, nameHint string) (release.Gate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return release.Gate{}, fmt.Errorf("read gate document %s: %w", path, err)
	}
	gate, err := gateFromDocument(data, nameHint)
	switch {
	case err == errNoGateInDocument:
		return release.Gate{}, fmt.Errorf(
			"%s states no verdict forge can read: expected a gate document (name + status) "+
				"or a forge --json document (ok + exit_code).\n"+
				"  fix: produce it with `forge lint --gate-json %s`, "+
				"`forge ci verify-test-run --gate-json %s`, or any forge verb's --json output; "+
				"or state the gate directly with --name and --status",
			path, path, path)
	case err != nil:
		return release.Gate{}, fmt.Errorf("%s: %w", path, err)
	}
	return gate, nil
}

// gateFromGateShape reads a document that already carries a name and a
// status. The status goes through the STRICT path (release.ParseGateStatus),
// because this is a write: a gate about to be recorded must carry a status
// from the closed set, and a document claiming "green" is refused here
// rather than stored for a reader to puzzle over.
func gateFromGateShape(doc gateDocument) (release.Gate, error) {
	status, err := release.ParseGateStatus(doc.Status)
	if err != nil {
		return release.Gate{}, err
	}
	gate := release.Gate{
		Name:       doc.Name,
		Status:     status,
		URL:        doc.URL,
		Summary:    doc.Summary,
		StartedAt:  doc.StartedAt,
		FinishedAt: doc.FinishedAt,
		RunID:      doc.RunID,
		Details:    doc.Details,
	}
	if err := gate.Validate(); err != nil {
		return release.Gate{}, err
	}
	return gate, nil
}

// documentShape is one recognised forge --json document: how to tell it
// apart, what the gate it yields is called, and the one line that says what
// it found.
//
// Recognition is by SHAPE, never by a version or a verb field, because no
// forge document carries one — and adding one only to be recognised here
// would put this parser in every one of those files' contracts.
type documentShape struct {
	// name is the gate's name: the check, as a reader of the evidence
	// trail will see it. Deliberately the verb's own vocabulary ("lint",
	// "test", "smoke") rather than the command line that produced it.
	name string
	// recognises reports whether doc is this shape. Must be specific
	// enough that no two shapes both match; the table is ordered most
	// specific first and the first match wins, so a near-miss degrades
	// to the generic envelope rather than to the wrong verb.
	recognises func(doc gateDocument) bool
	// summarise is the gate's one line. It may return "" when the
	// document carries nothing worth saying beyond the status.
	summarise func(doc gateDocument) string
	// details is the small structured payload — counts and verdicts, not
	// logs (the full report is behind URL, and the server caps this at
	// 8 KiB). nil when the shape has no counts.
	details func(doc gateDocument) map[string]any
	// status, when set, overrides the envelope-derived verdict. Only a
	// shape that can distinguish `skipped` from a pass needs this: `ok`
	// alone cannot express "checked nothing", and that is precisely the
	// green-while-blind case §3.3 exists to prevent.
	status func(doc gateDocument) (release.GateStatus, bool)
	// verdict supplies the verdict for a document that carries NO §3.A
	// envelope, because its output contract predates one — smoke states
	// `summary.ok`, nested. Only such a shape needs it; a document with
	// an envelope always uses the envelope, so the two cannot disagree.
	verdict func(doc gateDocument) release.GateStatus
}

// documentShapes is the table. ONE ENTRY PER FORGE VERB whose --json
// document is gate-shaped evidence.
//
// Ordered most specific first. Each `recognises` names a field combination
// that only that verb emits, so the order is a safety net rather than the
// mechanism.
var documentShapes = []documentShape{
	{
		// `forge lint --json` / `--gate-json`: the only shape with a
		// findings array beside an errors/warnings summary.
		name: "lint",
		recognises: func(doc gateDocument) bool {
			return doc.LintSummary != nil && doc.Findings != nil
		},
		summarise: func(doc gateDocument) string {
			s := doc.LintSummary
			return fmt.Sprintf("%d error(s), %d warning(s), %d info(s) over %d finding(s)",
				s.Errors, s.Warnings, s.Infos, s.Total)
		},
		details: func(doc gateDocument) map[string]any {
			s := doc.LintSummary
			return map[string]any{
				"errors": s.Errors, "warnings": s.Warnings,
				"infos": s.Infos, "findings": s.Total,
			}
		},
	},
	{
		// `forge ci verify-test-run`: a test-run totals block.
		name: "test",
		recognises: func(doc gateDocument) bool {
			return doc.Totals != nil
		},
		summarise: func(doc gateDocument) string {
			t := doc.Totals
			return fmt.Sprintf("%d passed, %d failed, %d skipped of %d test(s)",
				t.Passed, t.Failed, t.Skipped, t.Tests)
		},
		details: func(doc gateDocument) map[string]any {
			t := doc.Totals
			return map[string]any{
				"tests": t.Tests, "passed": t.Passed,
				"failed": t.Failed, "skipped": t.Skipped,
			}
		},
	},
	{
		// `forge env smoke --json`: a routes array with a pass/warn/fail
		// summary.
		name: "smoke",
		recognises: func(doc gateDocument) bool {
			// Recognised by the pass/warn/fail summary ALONE, not by
			// the routes array. A smoke that probed nothing emits
			// `"routes": null`, and that is precisely the run whose
			// verdict matters most — requiring a non-nil array would
			// make the checked-nothing document the one shape this
			// parser could not read, and it would fall through to
			// "states no verdict".
			return doc.SmokeSummary != nil
		},
		summarise: func(doc gateDocument) string {
			s := doc.SmokeSummary
			if s.Pass+s.Warn+s.Fail == 0 {
				return "no probeable route — nothing was checked"
			}
			return fmt.Sprintf("%d passed, %d warned, %d failed of %d probe(s)",
				s.Pass, s.Warn, s.Fail, len(doc.Routes))
		},
		details: func(doc gateDocument) map[string]any {
			s := doc.SmokeSummary
			return map[string]any{"pass": s.Pass, "warn": s.Warn, "fail": s.Fail}
		},
		verdict: func(doc gateDocument) release.GateStatus {
			// smoke's verdict is `summary.ok`, nested — its output
			// contract predates §3.A's envelope.
			if doc.SmokeSummary.OK {
				return release.GateStatusPassed
			}
			return release.GateStatusFailed
		},
		status: func(doc gateDocument) (release.GateStatus, bool) {
			// A SMOKE THAT PROBED NOTHING IS SKIPPED, NOT PASSED.
			// This is the one case `ok` cannot express: a hosted env
			// with no URL-bearing workload exits 0 with ok:true
			// having checked nothing, and recording that as `passed`
			// is a green check in the evidence trail attesting to a
			// probe that never happened.
			if s := doc.SmokeSummary; s != nil && s.Pass+s.Warn+s.Fail == 0 {
				return release.GateStatusSkipped, true
			}
			return "", false
		},
	},
	{
		// `forge env status --json`: an images array, with `bound`
		// distinguishing it from release verify.
		name: "verify",
		recognises: func(doc gateDocument) bool {
			return doc.Bound != nil && doc.Images != nil
		},
		summarise: func(doc gateDocument) string {
			if !*doc.Bound {
				return "environment has never been promoted"
			}
			if doc.Detail != "" {
				return doc.Detail
			}
			return fmt.Sprintf("release %s over %d declared image(s)", doc.Release, len(doc.Images))
		},
		status: func(doc gateDocument) (release.GateStatus, bool) {
			// An unbound env has declared nothing, so there is
			// nothing to be wrong about — and nothing to vouch for
			// either. `forge env status` exits 0 for it; recording a
			// pass would attest to images nobody checked.
			if doc.Bound != nil && !*doc.Bound {
				return release.GateStatusSkipped, true
			}
			return "", false
		},
	},
	{
		// `forge release verify --json`: an artifacts array under a
		// release label. Distinct from env verify, which has `images`
		// and `bound` — a release is verified independently of any
		// environment, so the gate is about the RELEASE.
		name: "release-verify",
		recognises: func(doc gateDocument) bool {
			return doc.Artifacts != nil && doc.Release != ""
		},
		summarise: func(doc gateDocument) string {
			if doc.Diagnostic != "" {
				return doc.Diagnostic
			}
			return fmt.Sprintf("release %s over %d artifact(s)", doc.Release, len(doc.Artifacts))
		},
		details: func(doc gateDocument) map[string]any {
			return map[string]any{"release": doc.Release, "artifacts": len(doc.Artifacts)}
		},
	},
	{
		// `forge env status --wait --json` (§3.2): the rollout phase.
		name: "wait",
		recognises: func(doc gateDocument) bool {
			return doc.Phase != ""
		},
		summarise: func(doc gateDocument) string {
			if doc.Reason != "" {
				return fmt.Sprintf("rollout %s: %s", doc.Phase, doc.Reason)
			}
			return "rollout " + doc.Phase
		},
		details: func(doc gateDocument) map[string]any {
			return map[string]any{"phase": doc.Phase}
		},
	},
}

// gateFromForgeDocument derives a gate from a forge --json document: the
// envelope supplies the verdict, the matching shape supplies the name,
// summary and counts.
//
// Returns ok=false for a document carrying no envelope — see the file
// header: a document that states no verdict yields no gate.
func gateFromForgeDocument(doc gateDocument, nameHint string) (release.Gate, bool) {
	gate := release.Gate{URL: doc.URL, RunID: doc.RunID, Details: doc.Details}

	var matched *documentShape
	for i := range documentShapes {
		if documentShapes[i].recognises(doc) {
			matched = &documentShapes[i]
			break
		}
	}

	// THE VERDICT COMES FROM THE ENVELOPE, OR FROM THE SHAPE. §3.A's
	// envelope is the general case, but it post-dates several of these
	// documents: `forge env smoke --json` states its verdict as
	// `summary.ok`, nested, with no top-level `ok` at all. Refusing that
	// document would mean smoke — the primitive §3.3 names explicitly —
	// could not be recorded without first rewriting its output contract
	// and breaking every existing consumer.
	//
	// So a shape may supply the verdict itself, and only a document that
	// states one NEITHER WAY is refused.
	switch {
	case doc.OK != nil || doc.ExitCode != nil:
		gate.Status = statusFromEnvelope(doc)
	case matched != nil && matched.verdict != nil:
		gate.Status = matched.verdict(doc)
	default:
		return release.Gate{}, false
	}

	if matched != nil {
		shape := *matched
		gate.Name = shape.name
		if shape.summarise != nil {
			gate.Summary = shape.summarise(doc)
		}
		if shape.details != nil && gate.Details == nil {
			gate.Details = shape.details(doc)
		}
		if shape.status != nil {
			if status, override := shape.status(doc); override {
				gate.Status = status
			}
		}
	}
	if gate.Name == "" {
		// Recognised as a forge document by its envelope, but not as
		// any particular verb. The verdict is still trustworthy — the
		// envelope's contract is universal — so the caller's --name
		// makes it recordable. Without one there is nothing to call
		// the check, and an unnamed gate is refused by Validate.
		gate.Name = strings.TrimSpace(nameHint)
		if gate.Name == "" {
			return release.Gate{}, false
		}
	}
	if gate.Summary == "" && doc.Error != "" {
		// A failing document's error line is the most useful summary
		// available, and the only one for an unrecognised shape.
		gate.Summary = firstLine(doc.Error)
	}
	return gate, true
}

// statusFromEnvelope maps §3.A's envelope onto the closed status set.
//
// ONLY exitWrong IS `failed`. Every other non-zero code in the shared table
// means something that is NOT "we looked at the release and it is wrong", and
// the gate vocabulary has to preserve that or the evidence lies — permanently,
// because the ledger is append-only and there is no later row that can retract
// a verdict.
//
// This is not a refinement; it is the table's own stated rule. exitcodes.go:
// "5 and 6 are deliberately NOT 1. A timeout and an overtaken wait are both
// 'we never saw this finish', and reporting either as 'the release is bad'
// would fail builds for releases that were fine."
//
// The stakes are concrete. F6's composite runs
// `gate record … --from wait.json` under `if: always()`, so a mapping that
// folded these into `failed` would append a false "the release failed" gate to
// every slow rollout and every overtaken promote, for good.
//
//	0 ok        → passed
//	1 wrong     → failed    the only real verdict against the release
//	2 undetermined → error  could not look (unreachable, auth, unobservable)
//	3 conflict  → error     someone else moved the env; the check never ran
//	4 refused   → error     the write was declined; nothing was judged
//	5 timed out → error     still progressing — we never saw it finish
//	6 superseded → skipped  the subject is gone; a newer promotion replaced it
//
// 6 is `skipped` rather than `error` because nothing went wrong at all: the
// promotion being waited on was legitimately replaced, so the check did not
// apply — which is what `skipped` means. C8 maps superseded the same way, so
// the server's run view and forge's gate agree.
func statusFromEnvelope(doc gateDocument) release.GateStatus {
	if doc.ExitCode != nil {
		switch *doc.ExitCode {
		case exitOK:
			// A zero exit still defers to `ok` below, so a verb that
			// exits 0 while reporting ok:false is not laundered into
			// a pass by this switch.
		case exitWrong:
			return release.GateStatusFailed
		case exitSuperseded:
			return release.GateStatusSkipped
		case exitUndetermined, exitConflict, exitRefused, exitTimedOut:
			return release.GateStatusError
		default:
			// A code this build does not know cannot be classified,
			// and an unclassifiable outcome is `error` — never
			// `failed`, which would assert something about the
			// release, and never `passed`.
			return release.GateStatusError
		}
	}
	if doc.OK != nil {
		if *doc.OK {
			return release.GateStatusPassed
		}
		return release.GateStatusFailed
	}
	return release.GateStatusPassed
}

// ─── Writing a gate document (the --gate-json producers) ─────────────────────

// writeGateDocument writes one gate to path as a DeployGate-shaped JSON
// document — what `--gate-json` produces on lint, `ci verify-test-run` and
// build.
//
// A FILE, NOT A STDOUT MODE. That is the design constraint, not an
// implementation detail: these verbs' human output and exit codes must be
// untouched, because CI keeps branching on the exit code and a human keeps
// reading the table. A `--json`-style stdout mode would make the evidence and
// the report mutually exclusive, and the pipeline would have to run the check
// twice to have both — at which point the second run is a different run and
// the evidence is about neither.
//
// It validates before writing: a producer that emitted an unrecordable gate
// would fail later, at `gate record`, in a different job, with the check's
// own output long gone.
func writeGateDocument(path string, gate release.Gate) error {
	if err := gate.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(gate, "", "  ")
	if err != nil {
		return fmt.Errorf("encode gate document: %w", err)
	}
	data = append(data, '\n')
	if err := atomicfile.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write gate document %s: %w", path, err)
	}
	return nil
}

// gateWindow stamps a check's own window onto a gate. The window is what
// makes a gate a run stage with a duration (release.Gate.StartedAt), and it
// is the check's time, not the recording's.
func gateWindow(gate release.Gate, startedAt, finishedAt time.Time) release.Gate {
	if !startedAt.IsZero() {
		started := startedAt.UTC()
		gate.StartedAt = &started
	}
	if !finishedAt.IsZero() {
		finished := finishedAt.UTC()
		gate.FinishedAt = &finished
	}
	return gate
}

// statusForFindings is the verdict a producer reports from its own counts:
// anything gating is `failed`, nothing checked is `skipped`, otherwise
// `passed`.
//
// `checked == 0` IS NOT A PASS. It is the same rule as the smoke shape above
// and it is here because every producer can hit it — a lint run whose every
// lane was unavailable, a test stream with no packages. A gate claiming
// "passed" over zero checks is indistinguishable, to every later reader, from
// one that actually verified something.
func statusForFindings(gating, checked int) release.GateStatus {
	switch {
	case gating > 0:
		return release.GateStatusFailed
	case checked == 0:
		return release.GateStatusSkipped
	default:
		return release.GateStatusPassed
	}
}

// renderGateTable renders gates as the human table `forge gate list` prints.
// Shared with `gate record`'s text output so one gate and a list of them are
// rendered by the same code.
func renderGateTable(gates []release.Gate) string {
	if len(gates) == 0 {
		return "  (no gates recorded)\n"
	}
	nameWidth, statusWidth := len("CHECK"), len("STATUS")
	for _, g := range gates {
		nameWidth = maxInt(nameWidth, len(g.Name))
		statusWidth = maxInt(statusWidth, len(string(g.Status)))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "  %-*s  %-*s  %s\n", nameWidth, "CHECK", statusWidth, "STATUS", "SUMMARY")
	for _, g := range gates {
		summary := g.Summary
		if g.RawStatus != "" {
			// A status recorded before the set closed. Say so, rather
			// than letting it render as a plain `error` that looks
			// like the check itself errored.
			summary = strings.TrimSpace(fmt.Sprintf("%s (recorded as %q)", summary, g.RawStatus))
		}
		fmt.Fprintf(&b, "  %-*s  %-*s  %s\n", nameWidth, g.Name, statusWidth, string(g.Status), summary)
		if g.RecordedBy != "" {
			fmt.Fprintf(&b, "  %-*s  recorded by %s\n", nameWidth+statusWidth+2, "", g.RecordedBy)
		}
	}
	return b.String()
}
