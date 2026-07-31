package builtin_test

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	forgecli "github.com/reliant-labs/forge/cli"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestForgeOneShotCharterFacts guards the FACTUAL claims the forge-one-shot
// charter makes about forge's CLI behaviour.
//
// The charter is prose, but an agent executes the prose literally, so a stale
// fact costs a real run real time. This is the counterpart to forge's own
// removalguard: a claim about behaviour that no longer holds is a straggler,
// and prose is the surface stragglers survive on.
//
// The claim that prompted it: four passages told the agent "`forge run`
// migrates but does NOT seed" and sent it to run `forge db seed apply` before
// it could judge any page. `forge run` HAS auto-seeded on first boot against an
// empty dev database for some time (forge internal/cli/run.go — it prints
// "[up] auto-seeded <N> rows across <M> tables (first boot; disable with
// --no-seed)"), so the instruction bought a redundant serial step and, worse,
// primed the orchestrator to treat a correctly-seeded database as a blocker
// under "Never proceed with an empty database".
//
// SCOPE — read before adding an entry. This guard pins claims that are
// AFFIRMATIVELY stated, and only where a match proves a defect. The charter
// deliberately NAMES several dead spellings in order to deny them ("there is no
// `forge project add`", "NOT a `--cors-origins` flag — that does not exist"),
// and those denials are load-bearing: they stop an agent hunting for an older
// spelling that works. A substring entry for one of those fires on the
// correction itself — a guard that cries wolf gets weakened until it guards
// nothing. Add an entry only when you can make it FAIL against the current
// file by restoring the false claim.
//
// PREFER A DERIVED GUARD. Every row here is a literal substring, which is the
// weakest possible form: it passes when the claim is REWORDED and it cannot
// distinguish a false claim from the charter's denial of one. Where the fact is
// about forge's CLI — a command exists, a flag exists, a flag value is legal —
// derive it from forge's own cobra tree instead
// (TestForgeOneShotHandsOutOnlyCommandsForgeActuallyDefines and its two
// siblings). That tree is the emitter: reliant embeds forge, so a verb or flag
// forge renames drops out of it and fails automatically, with no row to
// maintain. `--type interactor` was a row here and is now derived; what is left
// below is the residue that is NOT derivable from either producer — claims
// about forge's runtime BEHAVIOUR (`forge run` seeds on first boot), about
// files forge writes, and about routes it does not emit. Those are properties
// of forge's internals that reliant cannot import and cannot execute here, so
// they stay substrings until something exports them.
func TestForgeOneShotCharterFacts(t *testing.T) {
	data, err := builtin.BuiltinWorkflowsFS.ReadFile("forge-one-shot.yaml")
	require.NoError(t, err)
	charter := string(data)

	for _, tc := range []struct {
		claim string
		why   string
	}{
		{
			claim: "does NOT seed",
			why: "`forge run` auto-seeds on first boot against an empty dev database " +
				"(forge internal/cli/run.go, `--no-seed` opts out). Tell the agent to READ the " +
				"`[up] auto-seeded …` line and seed by hand only when it is absent.",
		},
		{
			claim: "stays alive, like `reliant forge run`",
			why: "`forge run` does NOT stay alive. It is an alias for `forge env up --host-only`, " +
				"whose lifecycle is resolved by a TTY check (forge internal/cli/up.go, " +
				"resolveUpLifecycle(cliutil.StdinIsTTY(), …)); an agent's shell has no TTY, so it " +
				"takes the detach path — start every host service and frontend, Process.Release " +
				"them onto their own log files, print the ports, return 0. Backgrounding it yields " +
				"a pid that has already exited and buries the ephemeral-port line in a buffer.",
		},
		{
			claim: "Only add a field whose implementation already exists",
			why: "this contradicted 'fix the Deps surface NOW' three lines above it, and the " +
				"contradiction is what a measured run's synthesis died on. forge's rule is " +
				"RESOLVABILITY, not existence: a Deps field resolves to another registered " +
				"component (by its Service interface type) or to an assignable field on the owned " +
				"*Infra struct in internal/app/providers.go, and anything else HARD-FAILS " +
				"generate. Say which of the two providers each field takes.",
		},
		{
			claim: "auth/callback",
			why: "forge emits no auth route at all — the callback page, the login and signup " +
				"forms and the auth barrel were deleted when the frontend half was found to be " +
				"posting at server routes forge never shipped (every one of them 404'd). The " +
				"charter named it in the list of pages the scaffold DOES emit, in the same " +
				"sentence that tells the agent to delete only what the product never offers, so " +
				"an agent either hunts for a page that was never born or 'preserves' it. Sign-in " +
				"is owned app logic; say that instead of naming a route.",
		},
		// The "commented FK" claim used to be a row here and is now
		// TestForgeOneShotDoesNotCallBirthsForeignKeysCommented, which
		// derives the fact from forge's own db skill instead of banning a
		// spelling. The row banned "commented-out FK" while the charter
		// said "commented FK", so it passed for as long as the false claim
		// stood — the exact failure mode this file's header warns about.
		{
			claim: "interactor.go",
			why: "the package scaffold writes contract.go + service.go + contract_test.go " +
				"(plus observe_chain.go and a stub mock_gen.go); interactor.go was the deleted " +
				"template tree's file and forge writes it nowhere. An agent told to leave TODO " +
				"bodies in `interactor.go` either invents a file forge does not know about or " +
				"stalls looking for one that was never born.",
		},
	} {
		if strings.Contains(charter, tc.claim) {
			t.Errorf("forge-one-shot.yaml still asserts %q — %s", tc.claim, tc.why)
		}
	}
}

// fkClaimRe matches the vocabulary a sentence uses when it is talking about
// foreign keys at all, in either the charter's idiom or forge's.
var fkClaimRe = regexp.MustCompile(`(?i)\bFKs?\b|foreign keys?|REFERENCES|ADD CONSTRAINT`)

// commentedRe matches any inflection of "comment" — commented, commented-out,
// uncomment, comments. The stem is the point: the row this guard replaces
// banned the exact string "commented-out FK" while the charter said
// "commented FK", so it passed for as long as the false claim stood.
var commentedRe = regexp.MustCompile(`(?i)comment`)

// fkProximityWindow is how many characters either side of a "comment" mention
// still count as the same claim, measured on whitespace-collapsed text. Wide
// enough to span the sentence the false claim actually occupied ("Vet forge's
// commented FK suggestions against actually-born tables"), narrow enough that
// two unrelated sentences do not get joined into one.
const fkProximityWindow = 100

// commentedFKClaims returns every passage in text that mentions foreign keys
// and commenting CLOSE TOGETHER — i.e. every place the prose could be read as
// "forge comments its FKs out".
func commentedFKClaims(text string) []string {
	flat := strings.Join(strings.Fields(text), " ")
	var out []string
	for _, c := range commentedRe.FindAllStringIndex(flat, -1) {
		lo := max(0, c[0]-fkProximityWindow)
		hi := min(len(flat), c[1]+fkProximityWindow)
		if window := flat[lo:hi]; fkClaimRe.MatchString(window) {
			out = append(out, window)
		}
	}
	return out
}

// TestForgeOneShotDoesNotCallBirthsForeignKeysCommented is the derived
// replacement for a substring row that could not fail.
//
// THE FACT. Birth APPLIES every foreign key it resolves. forge writes a live
// `ALTER TABLE … ADD CONSTRAINT … FOREIGN KEY` plus its `CREATE INDEX` for
// each `*_id` whose stem names a table it knows (forge
// internal/scaffold/entityproto.go writeFKStatements, whose call site is
// commented "These are APPLIED, not commented"), and forge's own
// entityproto_test.go fails the build on a `-- ALTER TABLE` or `-- CREATE
// INDEX` line. A commented constraint would be forge stating the correct
// schema and then declining to build it: no referential integrity, and the
// seeder — which reads the FK graph off the LIVE database — fills every child
// column with a synthesized string instead of a real parent id. An agent sent
// hunting for commented-out SQL finds none, and concludes either that forge is
// broken or that it must add constraints that already exist.
//
// WHY IT IS NOT A ROW. The banned string was "commented-out FK"; the charter
// said "commented FK". One word, and the guard was decorative for as long as
// the false claim stood. Any fixed sentence has that failure mode, so this
// guard does not hold a sentence:
//
//  1. The fact is DERIVED from forge's db skill, which reliant can load
//     through the exported forgecli.LoadSkill — the same text forge hands the
//     agent as authority. If forge ever does start commenting FKs, the skill
//     stops saying they are applied and this guard fails on its own
//     precondition rather than silently blessing a charter that agrees with
//     the new behaviour. That is as close to the emitter as reliant can get:
//     the SQL writer itself lives in forge/internal/scaffold, which the Go
//     internal rule puts out of reach, and only forge/cli and forge/pkg are
//     importable here.
//
//  2. The charter side is a PROXIMITY property over stems, not a spelling.
//     Any sentence that puts "comment" (commented, commented-out, uncomment)
//     within fkProximityWindow of FK vocabulary fails, so "commented FK",
//     "commented-out FK", "FKs forge leaves commented" and "uncomment the
//     foreign keys" all trip it. Rewording the TRUE claim cannot trip it,
//     because the true claim has no reason to mention commenting near an FK.
func TestForgeOneShotDoesNotCallBirthsForeignKeysCommented(t *testing.T) {
	// The producer half: forge's own db skill is the authority the charter
	// is paraphrasing, and it says the reference is APPLIED.
	skill, err := forgecli.LoadSkill("", "db")
	require.NoError(t, err, "could not load forge's db skill — this guard derives the fact "+
		"from it, and a guard that cannot reach its producer must fail loudly rather than "+
		"pass over an empty set")

	fkSentences := fkClaimRe.FindAllString(string(skill), -1)
	require.NotEmpty(t, fkSentences, "forge's db skill says nothing about foreign keys at all. "+
		"That is the precondition of this guard, not a pass: either the skill was "+
		"restructured or LoadSkill returned something unexpected, and the charter's FK "+
		"claims are no longer being checked against anything")

	require.Regexp(t, `(?i)applied `+"`?REFERENCES", string(skill),
		"forge's db skill no longer says birth emits an APPLIED `REFERENCES`. Either forge "+
			"changed what birth does — in which case the charter's FK guidance needs to change "+
			"with it, and this guard is the thing that noticed — or the skill was reworded and "+
			"this derivation needs a new anchor. Do NOT delete this assertion to get green.")

	// The consumer half: no phase may describe those applied constraints as
	// commented.
	for id, prompt := range charterPrompts(t) {
		for _, claim := range commentedFKClaims(prompt) {
			t.Errorf("phase %s puts commenting and foreign keys in the same breath:\n\n  …%s…\n\n"+
				"Birth APPLIES the FKs it resolves (forge internal/scaffold/entityproto.go "+
				"writeFKStatements; forge's own entityproto_test.go fails the build on a "+
				"`-- ALTER TABLE` line), and forge's db skill calls the emitted reference "+
				"APPLIED. An agent sent to vet, uncomment or add commented-out constraints "+
				"finds none, and either reports forge broken or re-adds constraints that "+
				"already exist. Say the constraints are live SQL to be vetted, not "+
				"suggestions to be enabled.", id, claim)
		}
	}
}

// TestForgeOneShotNamesThePerRPCScaffoldTestFile pins the scaffold-test layout
// the charter sends units to.
//
// This guard used to assert the OPPOSITE: that the pre-split step named the
// single shared `handlers_scaffold_test.go`, because in a measured run three
// backend units each collided on it and each ended with the package test
// command from its own brief RED. forge then fixed the cause — it births
// `handlers_scaffold_<rpc>_test.go`, one file per RPC with NOTHING at package
// scope (forge internal/codegen/service_stub_gen.go ScaffoldTestFileName, and
// its TestScaffoldTests_SurviveTwoOwners guard). The collision is structurally
// gone, so the pre-split instruction was pure cost and was deleted.
//
// What survives is a per-UNIT fact, not an orchestrator step: the row asserts
// CodeUnimplemented and goes red the moment its RPC is implemented, so
// rewriting or deleting it is part of finishing the RPC. Three units correctly
// reported it as a blocker last time because nothing told them it was theirs.
//
// Both halves are held here: the dead single-file spelling must never come
// back (an agent sent to split a file forge no longer births hunts for
// something that does not exist), and the per-RPC spelling must stay named.
func TestForgeOneShotNamesThePerRPCScaffoldTestFile(t *testing.T) {
	data, err := builtin.BuiltinWorkflowsFS.ReadFile("forge-one-shot.yaml")
	require.NoError(t, err)
	charter := string(data)

	require.NotContains(t, charter, "handlers_scaffold_test.go",
		"forge no longer births a single shared handlers_scaffold_test.go — it writes "+
			"handlers_scaffold_<rpc>_test.go, one per RPC, with nothing at package scope. "+
			"Naming the old file sends the orchestrator to pre-split something that does "+
			"not exist, and a per-RPC layout has no collision to pre-split")

	require.Contains(t, charter, "handlers_scaffold_<rpc>_test.go",
		"the backend unit brief must name the per-RPC scaffold test: its Test<Rpc>_Generated "+
			"row asserts CodeUnimplemented and goes RED the moment the RPC is implemented, so "+
			"a unit that does not know the file is its own reports a red suite as a blocker "+
			"instead of finishing the RPC")
}

// TestForgeOneShotStartsAndStopsTheDevServerTheWayForgeActuallyBehaves pins the
// two halves of `forge run`'s real lifecycle.
//
// The charter told the agent to launch it with `run_in_background: true` "so the
// process survives this turn", then to stop it with
// `pkill -f 'reliant forge run'`. Both follow from the same false premise. With
// no TTY (which is every agent shell) `forge run` takes forge's detach
// lifecycle: it starts the host services and frontends, releases them onto their
// own log files, prints the ephemeral ports, and EXITS 0. So the backgrounded
// pid is dead on arrival and the port line the next four steps depend on is in a
// buffer nobody blocked on — and the pkill matches nothing, because the process
// bearing that command line is exactly the one that already exited. What is
// still running are the DETACHED children, whose pids forge persisted to
// ~/.cache/forge/up/<project>/<env>.pids for `forge env down <env>` to read.
//
// `forge env down` is also the only stop that is safe to write down: a
// pattern-matching pkill in a shared dev environment is how an agent takes out
// processes it does not own.
func TestForgeOneShotStartsAndStopsTheDevServerTheWayForgeActuallyBehaves(t *testing.T) {
	data, err := builtin.BuiltinWorkflowsFS.ReadFile("forge-one-shot.yaml")
	require.NoError(t, err)
	charter := string(data)

	require.NotContains(t, charter, "run_in_background: true",
		"no phase may background `forge run`: it detaches its servers and returns 0, so "+
			"backgrounding hands the agent an already-exited pid and hides the ephemeral-port "+
			"line the following steps read. Run it FOREGROUND.")

	require.NotContains(t, charter, "pkill",
		"the charter must not tell an agent to pkill: `forge run` has already exited, so the "+
			"pattern matches nothing that matters, and a pattern kill in a shared dev "+
			"environment reaches processes the run does not own. Stop the detached servers "+
			"with `reliant forge env down dev`, which reads forge's own pid ledger.")

	require.Contains(t, charter, "reliant forge env down dev",
		"every phase that starts a dev server must name the command that stops it; without a "+
			"stop the next phase inherits a localhost-bound server serving a stale tree, which "+
			"is what produced a stale preview URL in a measured run")
}

// TestForgeOneShotSaysScaffoldRPCDoesNotWriteTheProto pins the hand-authoring
// the command deliberately leaves to the caller.
//
// `forge scaffold rpc` has an explicit non-goal (forge
// internal/cli/scaffold/rpc.go): it does NOT edit the .proto, because proto
// files carry hand-curated section markers and ordering an injector would
// regress. It writes the signed handler stub and PRINTS a snippet whose message
// bodies are literally `// TODO: request fields`. The charter named the snippet
// but not its consequence, so the plan had no line item for it and a measured
// run hand-authored 8 messages it had not budgeted.
//
// The file layout is the same fact's other half, and it decides whether the
// fan-out has anything to pre-split: scaffolded BEFORE the RPC is in the proto,
// the stub lands in its own `rpc_<snake>.go`; declared in the proto first, the
// next `forge generate` APPENDS it to the one shared handlers.go.
func TestForgeOneShotSaysScaffoldRPCDoesNotWriteTheProto(t *testing.T) {
	data, err := builtin.BuiltinWorkflowsFS.ReadFile("forge-one-shot.yaml")
	require.NoError(t, err)
	charter := string(data)

	require.Contains(t, charter, "does NOT edit the .proto",
		"the charter must say `forge scaffold rpc` leaves the proto to the caller — an agent "+
			"that reads 'scaffolds the RPC' plans no time for authoring the request/response "+
			"messages and discovers the work mid-phase")

	require.Contains(t, charter, "// TODO: request fields",
		"quote what the printed snippet actually contains: skeleton messages with no fields. "+
			"'prints the proto snippet' reads as 'the proto is handled' until you have seen one")

	require.Contains(t, charter, "rpc_<snake_name>.go",
		"the charter must name the file `forge scaffold rpc` writes. It is one file per RPC — "+
			"already disjoint, so it needs no pre-split — and that is only true on the "+
			"scaffold-before-proto path")
}

// TestForgeOneShotResolvesTheDepsSurfaceContradiction holds the resolution of
// the charter's one self-contradiction.
//
// "Fix the Deps surface NOW" and "only add a field whose implementation already
// exists" cannot both be followed for an interactor, and a measured run's
// synthesis failed on exactly that seam with
// `Fulfillment.Deps.Store has no provider`. forge's own generate-time error was
// a complete runbook — the Infra field to add, the OpenInfra line, the compose
// assignment it then does for you — and the charter had no answer, so the agent
// had nothing to reconcile its two instructions against.
//
// The governing rule is "fix it NOW" (that is what keeps units off a shared
// struct). The subordinate rule is resolvability, and it has exactly two legal
// answers, which is what this guard pins.
func TestForgeOneShotResolvesTheDepsSurfaceContradiction(t *testing.T) {
	data, err := builtin.BuiltinWorkflowsFS.ReadFile("forge-one-shot.yaml")
	require.NoError(t, err)
	charter := string(data)

	require.Contains(t, charter, "internal/app/providers.go",
		"the charter must name the *Infra struct as the second provider: it is the only way to "+
			"resolve a dep type with no component behind it, and it is hand-wiring that is due "+
			"in the same pass as the Deps field")

	require.Contains(t, charter, "has no provider",
		"the charter must quote forge's generate-time error, because that error IS the "+
			"remediation (it prints the exact Infra field, OpenInfra line and compose "+
			"assignment). An agent that does not recognise it deletes the Deps field instead, "+
			"and the seam comes back under fan-out contention")
}

// TestForgeOneShotCommandCostsAreMeasuredOnAFinishedApp pins the PHASE the
// command-cost numbers were taken at.
//
// Every cost figure in the charter used to come from the scaffold phase — an
// almost-empty project — and the charter then repeated them, unqualified, in
// build_mvp, where the app is finished. An agent has no
// way to tell a number that generalises from one that does not, so a run that
// found its gate slower than advertised had nothing to reconcile against, and
// one iteration concluded the gate cost ~19 minutes and proposed cutting lanes
// out of it.
//
// It does not cost that. Measured on a FINISHED 4-entity app (60 hand-written
// Go files, 71 tsx, every custom RPC implemented) against the same app at
// scaffold time, same machine, same binaries: ~66s vs ~55s for a full
// generate+lint+build+test cycle — 1.2x across the entire life of the app.
// That is the fact worth protecting, because the wrong conclusion from the
// wrong number is "drop a lane", and review/tests/verify-by-building are work,
// never waste.
//
// The guard therefore holds two things: that the build_mvp cost passage says
// which phase it was measured at, and that the ~19-minute figure is never
// restored as an unqualified claim.
func TestForgeOneShotCommandCostsAreMeasuredOnAFinishedApp(t *testing.T) {
	data, err := builtin.BuiltinWorkflowsFS.ReadFile("forge-one-shot.yaml")
	require.NoError(t, err)
	charter := string(data)

	require.Contains(t, charter, "FINISHED 4-entity app",
		"the build_mvp cost figures must say they were measured on a "+
			"FINISHED app. Unlabelled scaffold-phase numbers repeated into a later phase are "+
			"how an agent concludes the gate has become expensive and starts removing lanes "+
			"from it.")

	require.Contains(t, charter, "It does not reproduce",
		"the charter must record that the ~19-minute gate-cycle measurement does not "+
			"reproduce, and why (the machine was still loaded by the fan-out wave). Without "+
			"that line the next agent to see a slow gate re-derives the wrong cause.")

	// A gate cycle is generate+lint+build+test. If a future edit drops one of
	// the four from the cost table, the table stops describing the gate.
	//
	// The test lane is `task test`, not `reliant forge test`: the project's
	// Taskfile.yml is where the suite is defined, so the gate runs the same
	// command as the local loop and the project's own CI workflow.
	for _, lane := range []string{"reliant forge generate", "reliant forge lint", "reliant forge build", "task test"} {
		require.Contains(t, charter, lane,
			"the cost table must name every lane of the gate; %s is missing", lane)
	}
}

// TestGetItRightRetryPromptNamesTheFailingLane guards the retry prompt against
// pointing the next attempt at evidence it cannot see.
//
// The prompt used to say: "If previous checks failed, the error details and log
// file paths are in the messages above. Read the log files FIRST." They are
// not. The lint/test/build nodes are siblings of `implement` in the loop, so
// their save_message lands on the PARENT thread, while `implement` runs with
// `thread.mode: fork` — the implementer never receives it.
//
// Measured, real run b25c1f1d, scaffold_and_verify: `forge test` failed, the
// "Test FAILED" message went to the root thread, and attempt 2 opened with the
// agent saying "The prompt says the gate failed, but my last run ended green."
// It then spent ~100s and a dozen turns on `forge project audit` — which the
// same prompt told it to run FIRST, and which is not a gate lane — before
// running `reliant forge test` and seeing the real failure in 8.7s.
//
// The fix is to put the verdict in the prompt itself, built from the loop's own
// `*_exit` outputs plus the static log paths. This guard holds both halves: the
// false claim stays dead, and the expressions keep referencing outputs that are
// actually declared (a rename would otherwise make every lane bullet silently
// evaluate to nothing, restoring the original bug without a word changing).
func TestGetItRightRetryPromptNamesTheFailingLane(t *testing.T) {
	data, err := builtin.BuiltinWorkflowsFS.ReadFile("get-it-right.yaml")
	require.NoError(t, err)
	wf := string(data)

	require.NotContains(t, wf, "log file paths are in the messages above",
		"the retry prompt must not claim the failure details are in the conversation: the "+
			"gate's save_message goes to the PARENT thread and `implement` runs in a fork, so "+
			"the implementer cannot see it. Name the lane and its log path in the prompt.")

	for _, out := range []string{"lint_exit", "test_exit", "build_exit"} {
		require.Contains(t, wf, "outputs."+out,
			"the retry prompt must report lane %s from the loop output; without it the next "+
				"attempt has to guess which lane failed", out)
		require.Contains(t, wf, out+": \"{{nodes.",
			"loop output %s must stay declared — the retry prompt reads it through has(), so a "+
				"rename silently blanks the lane verdict instead of failing loudly", out)
	}

	for _, logInput := range []string{"inputs.lint_log", "inputs.test_log", "inputs.build_log"} {
		require.Contains(t, wf, logInput,
			"the retry prompt must hand over %s so the agent can read the actual error output "+
				"instead of re-running the gate to rediscover it", logInput)
	}

	// `forge project audit` is a diagnostic, not a gate lane. It may be
	// suggested, but never as the first move — that is what sent a measured run
	// into a 9-minute detour.
	auditIdx := strings.Index(wf, "reliant forge project audit")
	require.Positive(t, auditIdx, "the forge retry guidance should still mention project audit as a fallback")
	require.Less(t, strings.Index(wf, "READ THE FAILING LANE LOG FIRST"), auditIdx,
		"the retry prompt must send the agent to the failing lane's log BEFORE suggesting "+
			"`forge project audit`; audit is a diagnostic, not one of the gate lanes")
}

// TestForgeOneShotGateBlocksNameEveryLaneTheEngineRuns pins the gate the charter
// shows an agent to the gate the engine actually runs.
//
// get-it-right runs three parallel `run` lanes from `lint_command`,
// `test_command` and `build_command`, and with review off its pass/fail is
// purely `lint_exit || test_exit || build_exit` (get-it-right.yaml:215-224).
// Every phase of this charter sets all three, so the real gate is four forge
// commands: generate, lint, test, build.
//
// The charter's inline gate blocks showed only `generate && lint && build` and
// said "do NOT add a fourth" — a line originally aimed at stopping a redundant
// `npx tsc --noEmit` pass, but which reads as "the gate is three lanes." In run
// b25c1f1d the scaffold implementer ran exactly the three it was shown, returned
// green, and `forge test` then failed the phase — a whole retry paid for the
// disagreement.
//
// The guard holds both directions: every gate command the charter hands an agent
// runs the test lane, and the "fourth" framing stays dead.
func TestForgeOneShotGateBlocksNameEveryLaneTheEngineRuns(t *testing.T) {
	data, err := builtin.BuiltinWorkflowsFS.ReadFile("forge-one-shot.yaml")
	require.NoError(t, err)
	charter := string(data)

	// A gate command handed to an agent chains lint AND build in one shell line.
	// The engine's own `lint_command` stops at lint (test and build are separate
	// run nodes), and the "after a proto change, regenerate and rebuild" line
	// names no lint — so requiring both selects the prompt gate blocks and only
	// those.
	var gateLines []string
	for _, line := range strings.Split(charter, "\n") {
		if !strings.Contains(line, "&&") {
			continue
		}
		if strings.Contains(line, "reliant forge lint") && strings.Contains(line, "reliant forge build") {
			gateLines = append(gateLines, strings.TrimSpace(line))
		}
	}
	require.NotEmpty(t, gateLines, "expected the charter to show an agent a full gate command")

	// `task test`, not `reliant forge test`: the suite is defined in the
	// project's Taskfile.yml so the gate, the local edit loop, and the
	// project's generated CI workflow all run the identical command. The
	// engine's own `test_command` was moved in lockstep — if these two ever
	// disagree again, the agent returns green on a gate that then fails.
	for _, line := range gateLines {
		require.Contains(t, line, "task test",
			"a gate command the charter hands an agent omits the test lane the engine runs, "+
				"so the agent returns green on a gate that then fails the phase: %s", line)
	}

	require.NotContains(t, charter, "do NOT add a fourth",
		"the gate is four commands (generate, lint, test, build). Telling the agent not to "+
			"add a fourth reads as 'the gate is three' — say FIFTH, and say it is about not "+
			"re-checking the frontend, which is what the line was always for.")
}

// TestForgeOneShotBriefsAScopedFrontendTypecheck guards the leaf-level gate.
//
// `npx tsc --noEmit` typechecks the WHOLE frontend. Under fan-out that exit code
// answers "is the tree green", never "is my slice green", so a unit either
// blocks on a sibling's half-written page or learns its own gate is noise. In
// run b25c1f1d five concurrent frontend leaves shared it and none could gate.
//
// A tsconfig that `extends` the project's and narrows only `include` keeps every
// compilerOption and still checks everything the slice imports. Verified on a
// real 4-entity forge frontend: ~2s, green with a deliberate type error planted
// in a sibling page-group, red with the same error inside the slice.
func TestForgeOneShotBriefsAScopedFrontendTypecheck(t *testing.T) {
	data, err := builtin.BuiltinWorkflowsFS.ReadFile("forge-one-shot.yaml")
	require.NoError(t, err)
	charter := string(data)

	require.Contains(t, charter, `"extends": "./tsconfig.json"`,
		"the unit brief must carry the scoped-typecheck recipe verbatim; extending the "+
			"project tsconfig is what keeps paths/jsx/strict identical, so a hand-rolled "+
			"narrower invocation would silently be a WEAKER check, not a scoped one")

	require.Contains(t, charter, "npx tsc --noEmit -p tsconfig.<unit>.json",
		"the brief must name the -p invocation whose exit code belongs to the unit")

	require.Contains(t, charter, "rm tsconfig.<unit>.json",
		"the recipe writes a scratch tsconfig into a shared frontend; without the delete "+
			"step every unit leaves one behind")
}

// TestForgeOneShotDoesNotClaimForgeSkillsArePreloadedBeforeTheProjectExists
// guards the one phase where the preload cannot work.
//
// `skills:` on a node really does seed each skill as a tool-interaction whose
// body is byte-identical to a hand-load (call_llm.go buildSeededSkillMessages),
// and forge-one-shot really does declare six of them on scaffold_and_verify. The
// claim is nonetheless false at the moment the agent reads it: forge's skills
// are surfaced through the PROJECT — catalog/forge.go drops every `emit: forge`
// skill when the directory has no forge.yaml, which is exactly the state of the
// working directory before the agent has run `forge project new` (its own step
// 1, in this same prompt). The daemon's skill index carries a 60s TTL
// (daemonruntime/runtime.go), so the six become preloadable a minute into the
// phase — long after turn one, where the claim is read and believed.
//
// In run b25c1f1d the scaffold agent read "already preloaded", loaded nothing,
// and worked the whole phase without `architecture`, `service-layer` or
// `frontend/design`.
//
// build_mvp keeps its preload claim: by then forge.yaml exists and the seeding
// works, which is why this guard is scoped to the scaffold prompt's wording
// rather than banning the word everywhere.
func TestForgeOneShotDoesNotClaimForgeSkillsArePreloadedBeforeTheProjectExists(t *testing.T) {
	data, err := builtin.BuiltinWorkflowsFS.ReadFile("forge-one-shot.yaml")
	require.NoError(t, err)
	charter := string(data)

	require.NotContains(t, charter,
		"`frontend/design`) are already preloaded into your context",
		"the scaffold phase must not claim its forge skills are preloaded: the working "+
			"directory has no forge.yaml until the agent runs `forge project new`, so the "+
			"forge skill catalog it would preload from does not exist yet")

	require.Contains(t, charter, "no `forge.yaml`",
		"the scaffold prompt must say WHY its forge skills are absent on turn one — "+
			"otherwise the next agent to notice reports it as a broken preload mechanism "+
			"instead of an ordering fact it can work around")
}

// charterDoc is the shape the structural guards below read. They PARSE the
// workflow instead of grepping its bytes because all three prompts in this
// file number their steps from 1 — a raw-text guard would happily match
// launch_preview's "Step 4" while asserting about the scaffold phase's.
type charterDoc struct {
	Nodes []struct {
		ID   string `yaml:"id"`
		Args struct {
			LintCommand  string `yaml:"lint_command"`
			TestCommand  string `yaml:"test_command"`
			BuildCommand string `yaml:"build_command"`
		} `yaml:"args"`
		Thread struct {
			Inject struct {
				Content string `yaml:"content"`
			} `yaml:"inject"`
		} `yaml:"thread"`
	} `yaml:"nodes"`
}

// gateLaneRe pulls the runnable verbs out of whatever the node declares as
// its lane commands. Deriving them is the point: the lanes are renamed from
// time to time (`reliant forge test` -> `task test` most recently), and a
// guard holding its own copy of the list keeps passing while the charter and
// the engine drift apart.
var gateLaneRe = regexp.MustCompile(`(?:reliant forge|task) [a-z][a-z:_-]*`)

// scaffoldStepRe matches a step heading in the phase prompt.
var scaffoldStepRe = regexp.MustCompile(`\*\*Step (\d+):`)

// fencedBashRe matches a fenced command block — the shape an agent copies and
// runs. Prose addressed to the agent is not the same artifact and is not
// interchangeable with it.
var fencedBashRe = regexp.MustCompile("(?s)```bash\n(.*?)```")

// scaffoldPhase returns the scaffold node's agent-facing prompt and the set of
// lane commands the ENGINE runs for that node, derived from the node itself.
func scaffoldPhase(t *testing.T) (prompt string, lanes []string) {
	t.Helper()

	data, err := builtin.BuiltinWorkflowsFS.ReadFile("forge-one-shot.yaml")
	require.NoError(t, err)

	var doc charterDoc
	require.NoError(t, yaml.Unmarshal(data, &doc))
	require.NotEmpty(t, doc.Nodes, "forge-one-shot.yaml declared no nodes — the guards below "+
		"would silently assert about nothing")

	seen := map[string]bool{}
	for _, n := range doc.Nodes {
		if n.ID != "scaffold_and_verify" {
			continue
		}
		prompt = n.Thread.Inject.Content
		for _, cmd := range []string{n.Args.LintCommand, n.Args.TestCommand, n.Args.BuildCommand} {
			for _, lane := range gateLaneRe.FindAllString(cmd, -1) {
				if !seen[lane] {
					seen[lane] = true
					lanes = append(lanes, lane)
				}
			}
		}
	}

	require.NotEmpty(t, prompt, "no scaffold_and_verify node with an injected prompt — either "+
		"the node was renamed or the prompt moved, and every guard reading it just went blind")
	require.NotEmpty(t, lanes, "derived NO gate lanes from scaffold_and_verify's lint/test/build "+
		"commands. That set is what the phase is actually checked against; an empty one means "+
		"the args were renamed and these guards can no longer tell the charter from the engine")
	sort.Strings(lanes)
	return prompt, lanes
}

// TestForgeOneShotScaffoldStepsStayNumberedAndEnumerated pins the two lists
// that have to agree with each other, and cannot be kept in agreement by
// reading either one alone.
//
// The scaffold prompt is a numbered procedure, and its SCOPE BOUNDARY section
// re-states the same procedure as "your job in this phase is ONLY: 1..N" —
// which is the list an agent checks itself against before it stops. Inserting
// a step means renumbering both, plus every cross-reference ("the packages
// Step 5 births"), and nothing about a missed renumber is visible: the prompt
// still reads fine with two Step 4s, and the boundary list still reads fine
// missing one entry, right up until an agent stops a step early because the
// boundary never mentioned it.
//
// Both lists are DERIVED here. A skipped, duplicated or out-of-order number
// fails, and so does a boundary list that stops enumerating the steps.
func TestForgeOneShotScaffoldStepsStayNumberedAndEnumerated(t *testing.T) {
	prompt, _ := scaffoldPhase(t)

	var steps []int
	for _, m := range scaffoldStepRe.FindAllStringSubmatch(prompt, -1) {
		n, err := strconv.Atoi(m[1])
		require.NoError(t, err)
		steps = append(steps, n)
	}
	require.NotEmpty(t, steps, "the scaffold prompt declares no numbered steps — it is a "+
		"numbered procedure and this guard derives everything from those headings")

	for i, n := range steps {
		require.Equal(t, i, n,
			"scaffold step headings must run 0,1,2,… in order with no gaps or repeats; got %v. "+
				"A duplicated or skipped number is invisible in prose and is what a missed "+
				"renumber leaves behind", steps)
	}

	// The SCOPE BOUNDARY list enumerates the numbered steps (Step 0 is
	// "work from the source of truth", not a build step) plus the closing
	// "run the gate" item.
	start := strings.Index(prompt, "## SCOPE BOUNDARY")
	require.Positive(t, start, "the scaffold prompt lost its SCOPE BOUNDARY section — that is "+
		"the list an agent checks itself against before it stops")
	end := strings.Index(prompt[start:], "**STOP once")
	require.Positive(t, end, "the SCOPE BOUNDARY list lost its closing STOP clause, so this "+
		"guard can no longer tell where the enumeration ends")
	boundary := prompt[start : start+end]

	var items []int
	for _, m := range regexp.MustCompile(`(?m)^\s*(\d+)\. `).FindAllStringSubmatch(boundary, -1) {
		n, err := strconv.Atoi(m[1])
		require.NoError(t, err)
		items = append(items, n)
	}
	require.NotEmpty(t, items, "the SCOPE BOUNDARY section enumerates nothing — that list is "+
		"what an agent checks itself against before it stops")
	for i, n := range items {
		require.Equal(t, i+1, n,
			"SCOPE BOUNDARY items must run 1,2,3,… in order; got %v", items)
	}

	require.Len(t, items, len(steps),
		"SCOPE BOUNDARY must enumerate every numbered step from 1 up, plus the closing "+
			"gate item: %d steps (0..%d) means %d boundary items, got %d. A step the "+
			"boundary never lists is a step an agent is entitled to skip",
		len(steps), len(steps)-1, len(steps), len(items))
}

// orderedItemRe matches an ordered-list item at the start of a line, capturing
// its indent and its number. The indent is captured because a nested list
// restarts at 1 under its parent, and folding the two depths together would
// read a legal nesting as a numbering fault.
var orderedItemRe = regexp.MustCompile(`(?m)^([ \t]*)(\d+)\. `)

// numberedRun is one maximal ascending block of ordered-list items at a single
// indent — i.e. one list, as a reader sees it. A number that does not exceed
// its predecessor at the same indent starts a new run, which is how two
// adjacent lists under different headings stay two lists.
type numberedRun struct {
	indent  int
	numbers []int
}

// numberedRuns splits text into the ordered lists it contains.
func numberedRuns(text string) []numberedRun {
	var runs []numberedRun
	// Index into runs of the open run at each indent, so appending to runs
	// never invalidates what the map is pointing at.
	open := map[int]int{}
	for _, m := range orderedItemRe.FindAllStringSubmatch(text, -1) {
		indent := len(m[1])
		n, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		if i, ok := open[indent]; ok {
			if prev := runs[i].numbers; n > prev[len(prev)-1] {
				runs[i].numbers = append(runs[i].numbers, n)
				continue
			}
		}
		runs = append(runs, numberedRun{indent: indent, numbers: []int{n}})
		open[indent] = len(runs) - 1
	}
	return runs
}

// TestForgeOneShotEveryPhaseKeepsItsProceduresContiguous closes the
// step-enumeration gap that only scaffold_and_verify was covered for.
//
// THE GAP THIS FIXES. Deleting a step from launch_preview stayed GREEN:
// TestForgeOneShotScaffoldStepsStayNumberedAndEnumerated reads
// scaffold_and_verify's prompt and nothing else, so launch_preview's seven
// `**Step N:` headings and build_mvp's four ordered procedures were unguarded.
// A phase is one injected prompt an agent works straight down, so a hole in
// its numbering is exactly the thing an agent reads as "there is no such
// step": deleting Step 5 leaves 1,2,3,4,6,7, and an agent that never sees a
// Step 5 never seeds the preview.
//
// WHAT IT DERIVES FROM. Both shapes are derived from the prompt's own
// structure, never from a quoted sentence:
//
//   - `**Step N:` headings — the shape scaffold_and_verify and launch_preview
//     use. Collected in document order and required to run consecutively.
//     Rewording a heading's TEXT changes nothing this reads; deleting a
//     heading, duplicating a number or skipping one fails.
//
//   - Ordered lists — the shape build_mvp uses instead of Step headings (its
//     serial setup, its two focus lists, its visual-verification procedure).
//     Each maximal ascending block at one indent is one list, and each must
//     run 1,2,3,… A nested list restarts at 1 under its parent, which is why
//     the indent is part of the identity.
//
// Every phase is checked, so a phase that GAINS a procedure is covered the day
// it gains it rather than the day someone remembers to add a guard. The set
// each half derives is asserted non-empty before it is used: a phase whose
// headings and lists both vanished is a prompt that stopped being a procedure,
// and this fails rather than passing over nothing.
func TestForgeOneShotEveryPhaseKeepsItsProceduresContiguous(t *testing.T) {
	prompts := charterPrompts(t)

	// Derived, not listed: whichever phases actually spell procedures.
	// Hard-coding the phase names here would reintroduce the very gap this
	// test closes, one rename later.
	var withSteps, withLists []string

	for id, prompt := range prompts {
		var steps []int
		for _, m := range scaffoldStepRe.FindAllStringSubmatch(prompt, -1) {
			n, err := strconv.Atoi(m[1])
			require.NoError(t, err)
			steps = append(steps, n)
		}
		if len(steps) > 0 {
			withSteps = append(withSteps, id)
			// The first heading fixes the base (scaffold_and_verify
			// starts at Step 0, launch_preview at Step 1); only
			// CONTIGUITY is a property of the procedure itself.
			base := steps[0]
			for i, n := range steps {
				require.Equal(t, base+i, n,
					"phase %s: `**Step N:` headings must run %d,%d,%d,… in order with no gaps "+
						"or repeats; got %v. A phase is ONE injected prompt worked straight "+
						"down, so a hole in the numbering is a step an agent never performs — "+
						"and a duplicated number is invisible in prose",
					id, base, base+1, base+2, steps)
			}
		}

		for _, run := range numberedRuns(prompt) {
			if len(run.numbers) < 2 {
				continue // a lone "1." is a paragraph marker, not a procedure
			}
			withLists = append(withLists, id)
			for i, n := range run.numbers {
				require.Equal(t, i+1, n,
					"phase %s: an ordered list at indent %d must run 1,2,3,… with no gaps or "+
						"repeats; got %v. These lists ARE the phase's procedure where it has no "+
						"`**Step N:` headings, and a missing number is a step an agent is "+
						"entitled to believe does not exist",
					id, run.indent, run.numbers)
			}
		}
	}

	// Fail loudly when the derived sets are empty. Both halves are the
	// premise of every assertion above; an empty one means the prompts were
	// restructured and this guard is now checking nothing at all.
	require.NotEmpty(t, withSteps, "NO phase in forge-one-shot.yaml declares `**Step N:` "+
		"headings any more. Either the heading idiom changed — in which case this guard needs "+
		"to learn the new one before it can claim to cover anything — or the prompts stopped "+
		"being numbered procedures. Do not delete this assertion to get green")
	require.NotEmpty(t, withLists, "NO phase declares a multi-item ordered list any more. "+
		"That is the only shape build_mvp's procedures have, so an empty set means this guard "+
		"covers build_mvp vacuously")
}

// TestForgeOneShotReviewsTheBirthSchemaBeforeBuildingOnIt pins the schema
// review to the place and the SHAPE that make it happen.
//
// Proto-to-migration is a convenience, not a source of truth — forge's own
// architecture says so (field-level schema options were retired under "SQL is
// the schema", and the ORM projects the APPLIED schema rather than what birth
// emitted). Birth therefore produces a plausible first DRAFT of a schema, and
// nothing in the pipeline used to tell anyone to read it: it was emitted and
// immediately built upon. Every defect left in it then costs a second
// migration plus a cascade through handlers, pages, hooks and tests, where at
// birth it was one edit to a `.up.sql`.
//
// Two properties keep that step real, and both are derived:
//
//  1. It is FENCED and it names the engine's own lanes. Guidance that arrives
//     as a fenced, copy-pasteable command with a stated done-condition gets
//     run; the same instruction as prose does not — measured, in the same run,
//     5 of 5 agents ran the fenced command and 0 of 7 ran the verb mentioned
//     only in prose. The lanes come from the node's OWN lint/test/build args,
//     so a lane rename cannot leave the charter handing out a stale gate.
//
//  2. It lands BEFORE the last step. A schema review that happens after the
//     app logic is written is not a review, it is a migration.
func TestForgeOneShotReviewsTheBirthSchemaBeforeBuildingOnIt(t *testing.T) {
	r := schemaReviewStep(t)

	require.Less(t, r.migrations, r.gate,
		"the agent must be shown the born migrations BEFORE it is handed a gate to make "+
			"green; the other order is how a schema gets built on instead of read")

	require.Less(t, r.gate, r.lastStep,
		"the schema review's gate must land BEFORE the last step of the phase. A schema "+
			"corrected after the app logic is written is not a review — it is the cascade "+
			"this step exists to avoid")

	// The scope fence. "Correct the schema as needed" is one wording away from
	// a failure this pipeline has already paid for: a scaffold banner saying a
	// route is owned and disposable was read as a licence, and three fan-out
	// units deleted working machinery to hand-roll replacements. A review step
	// with no stated ceiling invites exactly that on the schema.
	require.Contains(t, r.read, "Do NOT redesign",
		"the schema-review step must state its ceiling between the read and the gate: "+
			"correct what is wrong for this domain, and leave what already fits. Without a "+
			"ceiling, 'review the schema' reads as 'redesign the schema'")

	// Birth APPLIES the foreign keys it resolves — the constraint and its index
	// are live SQL in the `.up.sql`, not a suggestion in a comment. A review
	// step that sends the agent looking for something to uncomment sends it
	// looking for nothing, and the two conclusions available at the end of that
	// search are "forge is broken" and "I should add these myself", which
	// re-adds constraints the migration already carries.
	for _, dead := range []string{"uncomment", "commented-out", "commented out"} {
		require.NotContains(t, r.review, dead,
			"the schema-review step describes the born foreign keys as %q. They are applied, "+
				"not suggested: read the constraints that ARE there, then write the ones birth "+
				"left out for a stem it could not resolve", dead)
	}

	// A repeated message lands in a JSONB column, and the document-vs-child-table
	// question is only answerable once you know what JSONB does NOT cost. These
	// are forge/pkg/orm symbols; reliant cannot import that package (its bun
	// dependency is outside this module's graph), so this asserts the SPELLING
	// the charter hands the agent rather than deriving it. It still fails loudly
	// when the fact is dropped, which is the failure that was measured.
	for _, helper := range []string{"orm.MarshalJSONBList", "orm.UnmarshalJSONBList"} {
		require.Contains(t, r.read, helper,
			"the type bullet must name %s. Without it, 'keep the JSONB column' reads as "+
				"'lose the field on the CRUD path' and the trade-off looks one-sided; with it "+
				"the real cost is only that no foreign key or index can reach inside the "+
				"document", helper)
	}
}

// schemaReview is the schema-review step, derived from the scaffold prompt's
// own structure rather than from any wording in it.
type schemaReview struct {
	prompt string
	lanes  []string
	blocks [][]int

	migrations int // start of the fenced block that puts the born .up.sql files in front of the agent
	gate       int // start of the fenced block running every lane the engine runs
	lastStep   int // start of the phase's last numbered step

	read   string // the read → gate span: the questions and the ceiling
	review string // the whole step: read → the last step heading
}

// schemaReviewStep derives the step's boundaries. Every landmark is found by
// what it DOES — the block that lists the born migrations, the block that runs
// the node's own lint/test/build lanes, the last numbered heading — so a
// reworded paragraph cannot move a boundary without moving the thing itself,
// and every derivation fails loudly rather than yielding an empty span.
func schemaReviewStep(t *testing.T) schemaReview {
	t.Helper()

	prompt, lanes := scaffoldPhase(t)

	blocks := fencedBashRe.FindAllStringIndex(prompt, -1)
	require.NotEmpty(t, blocks, "the scaffold prompt hands the agent no fenced command block "+
		"at all — every instruction in it is prose, which is the form that measurably does "+
		"not get run")

	steps := scaffoldStepRe.FindAllStringIndex(prompt, -1)
	require.NotEmpty(t, steps, "the scaffold prompt declares no numbered steps")
	lastStep := steps[len(steps)-1][0]

	// The migration-reading block: the one that puts the born schema in front
	// of the agent. Derived by what it operates on, not by its wording.
	migrations := -1
	for _, b := range blocks {
		body := prompt[b[0]:b[1]]
		if strings.Contains(body, "db/migrations") && strings.Contains(body, ".up.sql") {
			migrations = b[0]
			break
		}
	}
	require.NotEqual(t, -1, migrations,
		"no fenced block puts the born `db/migrations/*.up.sql` files in front of the agent. "+
			"Birth emits a first draft of the schema; unread, every defect in it is paid for "+
			"as a migration plus a cascade instead of one edit")

	// The done-condition: a fenced block running every lane the engine runs.
	gate := -1
	for _, b := range blocks {
		body := prompt[b[0]:b[1]]
		full := true
		for _, lane := range lanes {
			if !strings.Contains(body, lane) {
				full = false
				break
			}
		}
		if full {
			gate = b[0]
			break
		}
	}
	require.NotEqual(t, -1, gate,
		"no fenced block runs every lane the engine runs for this phase (%v). The schema "+
			"review needs a stated done-condition an agent can execute, and it has to be the "+
			"same gate the phase is judged by", lanes)

	require.Less(t, migrations, lastStep, "the born-migrations block is at or past the phase's "+
		"last step heading, so the schema-review span this guard derives would be empty")

	return schemaReview{
		prompt:     prompt,
		lanes:      lanes,
		blocks:     blocks,
		migrations: migrations,
		gate:       gate,
		lastStep:   lastStep,
		read:       prompt[migrations:gate],
		review:     prompt[migrations:lastStep],
	}
}

// TestForgeOneShotSchemaReviewRebuildsTheBornCRUDFixtures pins the consequence
// that makes the step's own done-condition reachable.
//
// `internal/handlers/<svc>/handlers_crud_test.go` is scaffold-once and
// user-owned from line one, and its fixtures are derived from the schema AS
// BORN — CHECK-satisfying literals and a seeded FK parent closure, introspected
// on the first-scaffold path only (forge internal/codegen/crud_gen.go
// GenerateCRUDTests, which returns early the moment the file exists). Correcting
// the schema therefore invalidates fixtures that `forge generate` will never
// re-derive, and the step's own gate goes red in `TestCRUD_<Entity>_Lifecycle`
// on the constraints the agent just added, with nothing anywhere saying why.
//
// The remedy is to delete the file and let generate re-emit it against the
// corrected schema. Hand-editing the literals instead re-does by hand what the
// generator does from the schema, and is wrong again the next time the schema
// moves — so the step has to name the delete, not merely warn about the red.
func TestForgeOneShotSchemaReviewRebuildsTheBornCRUDFixtures(t *testing.T) {
	r := schemaReviewStep(t)

	// The regenerate verb comes from the node's OWN lane commands, so a lane
	// rename cannot leave the remedy pointing at a command nothing runs.
	generate := ""
	for _, lane := range r.lanes {
		if strings.HasSuffix(lane, " generate") {
			generate = lane
			break
		}
	}
	require.NotEmpty(t, generate,
		"derived no generate lane from scaffold_and_verify's own commands (%v) — the remedy "+
			"below has no verb to name and this guard would assert nothing", r.lanes)

	deletes := regexp.MustCompile(`rm\s+\S*internal/handlers/\S+_test\.go`).FindAllString(r.review, -1)
	require.NotEmpty(t, deletes,
		"the schema-review step never tells the agent to delete the born CRUD lifecycle test. "+
			"Its fixtures were derived from the schema at birth and `forge generate` will not "+
			"re-derive them while the file exists, so the step's own gate fails on the "+
			"constraints the agent just added and the failure explains nothing")

	require.Contains(t, r.review, generate,
		"the step must name `%s` as what re-emits the deleted CRUD test — a delete with no "+
			"regenerate leaves the phase with no lifecycle test at all", generate)
}

// TestForgeOneShotSchemaReviewProvesARowAgainstARealDatabase pins the step's
// last question to a command that can answer it.
//
// The question is "would a row built from this schema ALONE be a row your own
// RPCs accept?", and READING a schema cannot answer it — the answer depends on
// what a generator produces from the types, constraints and DEFAULTs, which is
// strictly less than the domain knows. Seeding answers it by construction: the
// rows either come out legal or they come out as a total with no line items.
// The same command is also the only thing that reports an undeclared reference
// diamond, where one parent is reachable by two routes and independent
// synthesis makes the two disagree.
//
// It needs a live, MIGRATED database, and the phase's own gate does not leave
// one behind: the born CRUD tests boot a throwaway postgres inside the test
// process and generate introspects a scratch shadow database. `forge run` is
// what creates the dev database, applies the migrations on boot and seeds it,
// and it detaches its servers rather than staying alive. So the block has to
// bring the app up, seed, and stop it again — all three, or the check is
// unrunnable where it is written.
//
// Every verb is checked against forge's OWN cobra tree rather than against a
// remembered spelling. That tree is the emitter: reliant embeds forge, so the
// set of commands that exist is knowable exactly, and a verb forge renames or
// retires drops out of it and fails here — which is the failure mode every
// entry in TestForgeOneShotCharterFacts was paid for the hard way.
func TestForgeOneShotSchemaReviewProvesARowAgainstARealDatabase(t *testing.T) {
	r := schemaReviewStep(t)

	paths := forgeCommandPaths(t)
	seedVerbs := forgePathsUnder(paths, "db seed")
	require.NotEmpty(t, seedVerbs,
		"forge no longer ships a `db seed` command tree, so the schema-review step cannot be "+
			"asked to prove a row with one — this guard and the step both need rewriting "+
			"against whatever replaced it")

	// Every `reliant forge …` this step hands the agent must name a real command.
	invocations := forgeInvocations(r.review)
	require.NotEmpty(t, invocations,
		"the schema-review step hands the agent no `reliant forge` command at all")
	seededIn := -1
	for _, inv := range invocations {
		path := forgeLongestValidPath(paths, inv.args)
		require.NotEmpty(t, path,
			"the schema-review step runs `reliant forge %s`, which resolves to no command in "+
				"forge's own tree", strings.Join(inv.args, " "))
		if seedVerbs[path] && seededIn == -1 {
			seededIn = inv.at
		}
	}
	require.NotEqual(t, -1, seededIn,
		"the schema-review step never seeds. Its own last question — would a row built from "+
			"this schema ALONE be a row your RPCs accept — cannot be answered by reading a "+
			"schema, and the seeder answers it by construction; it is also the only report of "+
			"an undeclared reference diamond, which no amount of reading surfaces")
	seededIn += r.migrations // forgeInvocations indexed into r.review; compare in prompt coordinates

	require.Less(t, r.gate, seededIn,
		"the seed must run AFTER the gate is green: it needs the corrected schema applied and "+
			"the app buildable, and a red generate/lint/test/build is a different failure "+
			"wearing the same red")
	require.Less(t, seededIn, r.lastStep,
		"the seed must run inside the schema-review step. Deferred past it, the finding "+
			"arrives after the fan-out has built on the schema, which is the cascade this "+
			"step exists to prevent")

	// The seeding block has to be RUNNABLE: the database it needs does not exist
	// until the app is brought up, and the servers that brings up do not stop
	// themselves.
	block := ""
	for _, b := range r.blocks {
		if b[0] <= seededIn && seededIn < b[1] {
			block = r.prompt[b[0]:b[1]]
			break
		}
	}
	require.NotEmpty(t, block, "the seed command is not inside a fenced block — prose "+
		"measurably does not get run, and this one carries a precondition and a teardown "+
		"that have to travel with it")

	var ran []string
	for _, inv := range forgeInvocations(block) {
		ran = append(ran, forgeLongestValidPath(paths, inv.args))
	}
	require.Contains(t, ran, "run",
		"the seeding block never brings the app up. `forge run` is what creates the dev "+
			"database, applies the migrations on boot and seeds it; nothing earlier in the "+
			"phase leaves a migrated database behind, so without it the seed exits on a "+
			"connection string it cannot resolve or a database that was never created "+
			"(the block runs: %v)", ran)
	require.Contains(t, ran, "env down",
		"the seeding block never stops what it started. `forge run` detaches its servers and "+
			"returns rather than staying alive, so a block without the stop leaves a dev stack "+
			"up for every later phase to collide with (the block runs: %v)", ran)
}

// charterPrompts returns every phase prompt the charter injects, keyed by node
// id, derived from the parsed node graph.
//
// Deriving the set (rather than naming the three nodes known today) is what
// makes a NEW phase covered by the guards below the moment it is added: a phase
// whose prompt hands out a retired forge verb fails here without anyone
// remembering to extend a list.
func charterPrompts(t *testing.T) map[string]string {
	t.Helper()

	data, err := builtin.BuiltinWorkflowsFS.ReadFile("forge-one-shot.yaml")
	require.NoError(t, err)

	var doc charterDoc
	require.NoError(t, yaml.Unmarshal(data, &doc))
	require.NotEmpty(t, doc.Nodes, "forge-one-shot.yaml declared no nodes")

	prompts := map[string]string{}
	for _, n := range doc.Nodes {
		if p := n.Thread.Inject.Content; p != "" {
			prompts[n.ID] = p
		}
	}
	require.NotEmpty(t, prompts, "no node in forge-one-shot.yaml injects a prompt — every "+
		"guard that reads a phase prompt just went blind, and a guard over an empty set "+
		"passes vacuously")
	return prompts
}

// TestForgeOneShotHandsOutOnlyCommandsForgeActuallyDefines checks every
// `reliant forge …` the charter hands an agent against forge's OWN cobra tree.
//
// This replaces the substring rows that used to pin dead spellings one at a
// time ("--type interactor", "forge project add"). A row per dead spelling can
// only ever catch the spelling someone already paid for, and it cannot tell a
// FALSE CLAIM from the charter's deliberate DENIAL of one — the charter names
// several retired spellings in order to deny them ("there is no `forge project
// add`"), and a substring row fires on the correction itself.
//
// Deriving from the emitter fixes both halves. reliant embeds forge, so the set
// of commands that exist is knowable exactly: a verb forge retires drops out of
// the tree and fails here, and a denial in prose is not an invocation so it
// never trips. The check is scoped to FENCED blocks — the artifact an agent
// copies and runs — because that is where a non-existent verb costs a run,
// while prose is where the denials live.
func TestForgeOneShotHandsOutOnlyCommandsForgeActuallyDefines(t *testing.T) {
	paths := forgeCommandPaths(t)

	root := forgecli.NewRootCmd()

	checked := 0
	for id, prompt := range charterPrompts(t) {
		blocks := fencedBashRe.FindAllString(prompt, -1)
		for _, block := range blocks {
			for _, inv := range forgeInvocations(block) {
				checked++
				spelled := strings.Join(inv.args, " ")

				require.NotEmpty(t, forgeLongestValidPath(paths, inv.args),
					"phase %s hands the agent a fenced `reliant forge %s`, which resolves to "+
						"no command in forge's own cobra tree. A retired or renamed verb exits "+
						"non-zero on `unknown command` mid-phase, which is the failure every "+
						"hand-written row in TestForgeOneShotCharterFacts was paid for",
					id, spelled)

				// Resolving to a valid PREFIX is not enough. `db seed run`
				// resolves to the real `db seed` with `run` left over, and a
				// prefix check would call that fine — but cobra hands a GROUP
				// command an unknown subcommand and forge's StrictGroup exits
				// non-zero. That is exactly how a renamed leaf (`db seed apply`
				// -> `db seed run`) would slip past this guard, so the leftover
				// word has to be rejected when the command it lands on is a
				// group.
				cmd, rest, err := root.Find(inv.args)
				require.NoError(t, err)
				if cmd.HasSubCommands() && len(rest) > 0 {
					t.Errorf("phase %s hands the agent a fenced `reliant forge %s`, but forge's "+
						"`%s` is a command GROUP with no %q subcommand. cobra rejects an unknown "+
						"subcommand and forge's StrictGroup exits non-zero rather than printing "+
						"help and returning success, so the step dies where the charter promised "+
						"work", id, spelled, strings.TrimPrefix(cmd.CommandPath(), "forge "), rest[0])
				}
			}
		}
	}

	require.Positive(t, checked, "derived NO `reliant forge` invocations from any phase's "+
		"fenced blocks. The charter drives the whole run through that command, so an empty "+
		"set means the extraction broke — and every assertion above it then passes "+
		"vacuously")
}

// TestForgeOneShotHandsOutOnlyFlagsForgeActuallyDefines is the flag half of the
// same property.
//
// A flag is where the retired `--type interactor` row lived: `forge scaffold
// package` has exactly two --type values and `interactor` was removed with the
// template tree behind it, so the charter was sending the agent to a command
// that exits non-zero at the exact step where it births every orchestration
// package. Deriving from the tree catches the NEXT such rename too, and both
// the flag's existence and its legal values are properties forge computes.
func TestForgeOneShotHandsOutOnlyFlagsForgeActuallyDefines(t *testing.T) {
	root := forgecli.NewRootCmd()
	paths := forgeCommandPaths(t)

	checked := 0
	for id, prompt := range charterPrompts(t) {
		for _, block := range fencedBashRe.FindAllString(prompt, -1) {
			for _, inv := range forgeFlagUses(block) {
				path := forgeLongestValidPath(paths, inv.args)
				if path == "" {
					continue // the command itself is reported by the guard above
				}
				cmd, _, err := root.Find(strings.Fields(path))
				require.NoError(t, err)
				for _, flag := range inv.flags {
					checked++
					require.NotNil(t, cmd.Flags().Lookup(flag),
						"phase %s hands the agent `reliant forge %s --%s`, and forge's own "+
							"`%s` command defines no such flag. cobra rejects an unknown flag "+
							"before the command runs, so the step fails with a parse error at "+
							"the point the charter promised work would happen",
						id, path, flag, path)
				}
			}
		}
	}

	require.Positive(t, checked, "derived NO flags from any fenced `reliant forge` "+
		"invocation. The charter passes flags on project new, project libraries and env "+
		"status at minimum, so an empty set means the extraction broke and this guard "+
		"asserts nothing")
}

// TestForgeOneShotScaffoldPackageTypeIsAValueForgeAccepts pins the --type
// values against the set forge validates against.
//
// `interactor` was a --type value the charter named in four passages after
// forge had removed it, and `forge scaffold package --type interactor` exits
// non-zero with `invalid package type`. The legal set is a property forge
// computes and states in the flag's own usage string, so the charter can be
// checked against it rather than against a remembered spelling.
func TestForgeOneShotScaffoldPackageTypeIsAValueForgeAccepts(t *testing.T) {
	root := forgecli.NewRootCmd()

	cmd, _, err := root.Find([]string{"scaffold", "package"})
	require.NoError(t, err)
	flag := cmd.Flags().Lookup("type")
	require.NotNil(t, flag, "`forge scaffold package` no longer defines --type; the charter "+
		"passages that pass it need rewriting against whatever replaced it")

	// forge states the legal set in the flag's usage ("package shape:
	// service|adapter …"). Reading it there keeps this guard tied to the
	// producer instead of to a list maintained here.
	legal := map[string]bool{}
	if m := regexp.MustCompile(`([a-z|]*\|[a-z|]+)`).FindStringSubmatch(flag.Usage); m != nil {
		for _, v := range strings.Split(m[1], "|") {
			legal[v] = true
		}
	}
	require.NotEmpty(t, legal, "could not derive the legal --type values from forge's own "+
		"flag usage (%q) — this guard would otherwise accept anything", flag.Usage)

	typeRe := regexp.MustCompile(`scaffold package[^\n]*?--type[= ]([a-z]+)`)
	for id, prompt := range charterPrompts(t) {
		for _, m := range typeRe.FindAllStringSubmatch(prompt, -1) {
			require.True(t, legal[m[1]],
				"phase %s tells the agent to run `forge scaffold package --type %s`, but "+
					"forge accepts only %v and exits non-zero with `invalid package type` "+
					"otherwise. The default shape IS the orchestrator shape — an orchestrator "+
					"is a service whose Deps are other services' interfaces — so the fix is "+
					"to drop the flag, not to rename it",
				id, m[1], sortedKeys(legal))
		}
	}
}

// forgeFlagUse is one fenced `reliant forge …` occurrence together with the
// long flags passed to it.
type forgeFlagUse struct {
	args  []string
	flags []string
}

// forgeFlagRe matches a long flag as written on a command line, stopping at the
// `=` of a `--flag=value` form.
var forgeFlagRe = regexp.MustCompile(`^--([a-z][a-z0-9-]*)`)

// forgeFlagUses pulls every `reliant forge …` out of a span along with the long
// flags that belong to it. A shell operator, a newline or a `--` passthrough
// ends the invocation: everything after `--` belongs to the dev server, not to
// forge, which is exactly the distinction the launch_preview step depends on.
func forgeFlagUses(text string) []forgeFlagUse {
	const prefix = "reliant forge "
	var out []forgeFlagUse
	for offset := 0; ; {
		i := strings.Index(text[offset:], prefix)
		if i < 0 {
			return out
		}
		at := offset + i
		rest := text[at+len(prefix):]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[:nl]
		}

		use := forgeFlagUse{}
		inArgs := true
		for _, field := range strings.Fields(rest) {
			if field == "&&" || field == "||" || field == ";" || field == "|" || field == "--" {
				break
			}
			if inArgs && forgeArgRe.MatchString(field) {
				use.args = append(use.args, field)
				continue
			}
			inArgs = false
			if m := forgeFlagRe.FindStringSubmatch(field); m != nil {
				use.flags = append(use.flags, m[1])
			}
		}
		if len(use.args) > 0 {
			out = append(out, use)
		}
		offset = at + len(prefix)
	}
}

// forgeCommandPaths walks forge's OWN cobra tree and returns every command path
// it defines ("db seed apply", "env down", …).
//
// This is the emitter. The charter hands agents `reliant forge <verb>` strings
// and reliant embeds forge, so the set of verbs that exist is knowable exactly
// rather than by memory — and a verb forge renames or retires drops out of this
// set, failing every guard that reads it, instead of the charter quietly
// shipping a command that exits on `unknown command`.
func forgeCommandPaths(t *testing.T) map[string]bool {
	t.Helper()

	paths := map[string]bool{}
	var walk func(cmd *cobra.Command, prefix string)
	walk = func(cmd *cobra.Command, prefix string) {
		for _, sub := range cmd.Commands() {
			path := sub.Name()
			if prefix != "" {
				path = prefix + " " + sub.Name()
			}
			paths[path] = true
			walk(sub, path)
		}
	}
	walk(forgecli.NewRootCmd(), "")

	require.NotEmpty(t, paths, "forge's root command declares no subcommands — the tree these "+
		"guards check the charter against is empty, so every check over it would pass "+
		"vacuously")
	return paths
}

// forgePathsUnder returns the subset of paths inside one subtree, including the
// subtree root itself.
func forgePathsUnder(paths map[string]bool, root string) map[string]bool {
	out := map[string]bool{}
	for p := range paths {
		if p == root || strings.HasPrefix(p, root+" ") {
			out[p] = true
		}
	}
	return out
}

// forgeInvocation is one `reliant forge …` occurrence: where it starts, and the
// command words that follow it.
type forgeInvocation struct {
	at   int
	args []string
}

// forgeArgRe matches a command WORD — the shape a cobra command name has.
// Anything else (a flag, a `<placeholder>`, a shell operator, a template
// expression) ends the invocation and everything after it is an argument.
var forgeArgRe = regexp.MustCompile(`^[a-z][a-z0-9:_-]*$`)

// forgeInvocations pulls every `reliant forge …` out of a span of the prompt.
func forgeInvocations(text string) []forgeInvocation {
	const prefix = "reliant forge "
	var out []forgeInvocation
	for offset := 0; ; {
		i := strings.Index(text[offset:], prefix)
		if i < 0 {
			return out
		}
		at := offset + i
		var args []string
		for _, field := range strings.Fields(text[at+len(prefix):]) {
			if !forgeArgRe.MatchString(field) {
				break
			}
			args = append(args, field)
		}
		if len(args) > 0 {
			out = append(out, forgeInvocation{at: at, args: args})
		}
		offset = at + len(prefix)
	}
}

// forgeLongestValidPath returns the longest leading run of args naming a real
// forge command, or "" when not even the first word does. The words after it
// are the command's own arguments (`env down dev` -> "env down").
func forgeLongestValidPath(paths map[string]bool, args []string) string {
	best := ""
	for n := 1; n <= len(args); n++ {
		if candidate := strings.Join(args[:n], " "); paths[candidate] {
			best = candidate
		}
	}
	return best
}
