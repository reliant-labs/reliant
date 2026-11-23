package testreport

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Verdict classifies one failure.
type Verdict string

// The verdicts: confirmed|real|raced|inconclusive|unattributed|host|correlated.
// Only VerdictConfirmed scores; host and correlated failures never do, and
// are reported as host health and infra events.
const (
	// VerdictConfirmed: the test failed, then passed on the same tree (a
	// retry within the run, a rerun, or another run of the same tree).
	VerdictConfirmed Verdict = "confirmed"
	// VerdictReal: the test failed again on rerun, or failed with nothing
	// to show it would pass. A break, not a flake.
	VerdictReal Verdict = "real"
	// VerdictRaced: the output reports a data race. A race is a bug and is
	// never counted as a flake.
	VerdictRaced Verdict = "raced"
	// VerdictInconclusive: the rerun could not run (a build failure, a
	// timeout, a host failure on rerun) or there was no rerun.
	VerdictInconclusive Verdict = "inconclusive"
	// VerdictUnattributed: the failure could not be tied to a test (a build
	// failure, a panic in TestMain).
	VerdictUnattributed Verdict = "unattributed"
	// VerdictHost: the failure output matches a host signature (network,
	// rate limit, disk, port, docker, timeout under load). Matched before
	// anything is scored.
	VerdictHost Verdict = "host"
	// VerdictCorrelated: the failure is one of a correlated group, three or
	// more failures in one run sharing a package, a harness or a signature
	// ([GroupCorrelated]). The group is one infra event, named by its
	// infra_group; its tests are not scored for it. It takes a whole run to
	// see, so [DetectFlakeEvents] assigns it, never [Classify].
	VerdictCorrelated Verdict = "correlated"
)

// Signal is what showed a test to be flaky.
type Signal string

// The confirmed-flake signals: an event's kind.
const (
	// SignalRetryPass: the test failed, then passed on retry within one run
	// (`go test -count=N`, a rerunning runner).
	SignalRetryPass Signal = "retry_pass"
	// SignalSameTreeSplit: the test passed in one run and failed in another
	// on the same tree hash (sharded jobs, re-runs, a local run and CI).
	SignalSameTreeSplit Signal = "same_tree"
	// SignalConfirm: the test failed, and a `forge test flakes confirm`
	// rerun (a run in [LaneConfirm]) on the same tree passed it.
	SignalConfirm Signal = "confirm"
)

// HostRule is one host-failure signature: a failure whose output matches
// Regexp is the host's fault, not the test's.
type HostRule struct {
	// Name is the cause reported as host health: "network", "rate_limit",
	// "disk", "port", "docker", "file_descriptors", "memory", "timeout".
	Name   string
	Regexp *regexp.Regexp
}

var defaultHostRules = []HostRule{
	{Name: "network", Regexp: regexp.MustCompile(`(?i)dial tcp|dial udp|connection refused|connection reset by peer|no such host|i/o timeout|tls handshake timeout|temporary failure in name resolution|network is unreachable|server misbehaving|proxyconnect|econnreset|econnrefused|etimedout|eai_again|failed to download|error downloading|npm err! network`)},
	{Name: "rate_limit", Regexp: regexp.MustCompile(`(?i)toomanyrequests|429 too many requests|rate limit exceeded|api rate limit`)},
	{Name: "disk", Regexp: regexp.MustCompile(`(?i)no space left on device|enospc|disk quota exceeded|edquot`)},
	{Name: "port", Regexp: regexp.MustCompile(`(?i)address already in use|eaddrinuse`)},
	{Name: "docker", Regexp: regexp.MustCompile(`(?i)cannot connect to the docker daemon|error response from daemon|docker[^\n]{0,200}(?:timeout|timed out|deadline exceeded)`)},
	{Name: "file_descriptors", Regexp: regexp.MustCompile(`(?i)too many open files|emfile|inotify`)},
	{Name: "memory", Regexp: regexp.MustCompile(`(?i)cannot allocate memory|fatal error: runtime: out of memory|signal: killed`)},
	{Name: "timeout", Regexp: regexp.MustCompile(`panic: test timed out after`)},
}

// DefaultHostRules is the host signature list: network and download errors,
// registry rate limits (`toomanyrequests`), ENOSPC and quota, address already
// in use, docker CLI failures and timeouts, exhausted file descriptors or
// inotify watches, memory, and `panic: test timed out` (on a loaded host the
// common cause; a deadlocked test is still caught when it times out again on
// a quiet one). The slice is a copy.
func DefaultHostRules() []HostRule { return append([]HostRule(nil), defaultHostRules...) }

// MatchHost names the first host rule output matches. rules nil means
// [DefaultHostRules].
func MatchHost(output string, rules []HostRule) (string, bool) {
	if rules == nil {
		rules = defaultHostRules
	}
	for _, r := range rules {
		if r.Regexp != nil && r.Regexp.MatchString(output) {
			return r.Name, true
		}
	}
	return "", false
}

var raceRe = regexp.MustCompile(`WARNING: DATA RACE|race detected during execution of test`)

// IsRace reports whether output reports a data race.
func IsRace(output string) bool { return raceRe.MatchString(output) }

// Classify gives one failure its verdict. rerun is the same test's result
// when it was run again on the same tree (nil when it was not); rules nil
// means [DefaultHostRules]. In order: a race, a host signature, a failure no
// test owns, a retry pass, then the rerun decides.
func Classify(failure Case, rerun *Case, rules []HostRule) Verdict {
	switch {
	case IsRace(failure.Output):
		return VerdictRaced
	case isHost(failure.Output, rules):
		return VerdictHost
	case failure.ID.Name == "":
		return VerdictUnattributed
	case failure.RetryPassed:
		return VerdictConfirmed
	case rerun == nil:
		return VerdictInconclusive
	}
	switch rerun.Status {
	case StatusPass:
		return VerdictConfirmed
	case StatusFail:
		switch {
		case IsRace(rerun.Output):
			return VerdictRaced
		case isHost(rerun.Output, rules):
			return VerdictInconclusive
		}
		return VerdictReal
	}
	return VerdictInconclusive
}

func isHost(output string, rules []HostRule) bool {
	_, ok := MatchHost(output, rules)
	return ok
}

// Harness is a shared test harness whose outage fails many tests at once.
type Harness struct {
	Name   string
	Regexp *regexp.Regexp
}

var defaultHarnesses = []Harness{
	{Name: "testsrv", Regexp: regexp.MustCompile(`(?i)testsrv`)},
	{Name: "clickhouse", Regexp: regexp.MustCompile(`(?i)clickhouse`)},
	{Name: "pgtest", Regexp: regexp.MustCompile(`(?i)pgtest|embedded[- ]postgres`)},
}

// DefaultHarnesses is testsrv, ClickHouse and pgtest. The slice is a copy.
func DefaultHarnesses() []Harness { return append([]Harness(nil), defaultHarnesses...) }

// CorrelateOptions configures [GroupCorrelated].
type CorrelateOptions struct {
	// Min is how many distinct top-level tests make a group. Default 3.
	Min int
	// Harnesses nil means [DefaultHarnesses].
	Harnesses []Harness
}

// CorrelatedGroup is failures of one run that share a package, a harness or
// a signature: one infra event, not several flakes.
type CorrelatedGroup struct {
	// Key is the group's infra_group: it identifies the co-failing set, so
	// the same set of tests failing together on a later commit has the same
	// Key and is the same event.
	Key string `json:"infra_group"`
	// Reason is what the members share: "package:<path>",
	// "harness:<name>" or "signature:<hash>".
	Reason string `json:"reason"`
	// Members are the top-level tests, sorted.
	Members []TestID `json:"members"`
}

// GroupCorrelated groups one run's failures. Failures are counted by
// top-level test. Larger groups are taken first, a failure joins at most one
// group, and what joins none is returned as rest, in input order.
func GroupCorrelated(failures []Case, opts CorrelateOptions) (groups []CorrelatedGroup, rest []Case) {
	minN := opts.Min
	if minN <= 0 {
		minN = 3
	}
	harnesses := opts.Harnesses
	if harnesses == nil {
		harnesses = defaultHarnesses
	}
	// candidate key -> top-level tests sharing it
	cands := map[string]map[TestID]bool{}
	keysOf := make([][]string, len(failures))
	for i, f := range failures {
		top := f.ID.TopLevel()
		ks := []string{"package:" + f.ID.Package}
		for _, h := range harnesses {
			if h.Regexp != nil && h.Regexp.MatchString(f.Output) {
				ks = append(ks, "harness:"+h.Name)
			}
		}
		if f.Signature != "" {
			ks = append(ks, "signature:"+f.Signature)
		}
		keysOf[i] = ks
		for _, k := range ks {
			if cands[k] == nil {
				cands[k] = map[TestID]bool{}
			}
			cands[k][top] = true
		}
	}
	order := make([]string, 0, len(cands))
	for k, m := range cands {
		if len(m) >= minN {
			order = append(order, k)
		}
	}
	sort.Slice(order, func(i, j int) bool {
		if len(cands[order[i]]) != len(cands[order[j]]) {
			return len(cands[order[i]]) > len(cands[order[j]])
		}
		return order[i] < order[j]
	})
	taken := map[TestID]bool{}
	for _, k := range order {
		var members []TestID
		for id := range cands[k] {
			if !taken[id] {
				members = append(members, id)
			}
		}
		if len(members) < minN {
			continue
		}
		sort.Slice(members, func(i, j int) bool { return lessID(members[i], members[j]) })
		for _, id := range members {
			taken[id] = true
		}
		groups = append(groups, CorrelatedGroup{Key: setKey(members), Reason: k, Members: members})
	}
	for _, f := range failures {
		if !taken[f.ID.TopLevel()] {
			rest = append(rest, f)
		}
	}
	return groups, rest
}

func setKey(members []TestID) string {
	h := sha256.New()
	for _, m := range members {
		h.Write([]byte(m.String()))
		h.Write([]byte{0})
	}
	return "infra:" + hex.EncodeToString(h.Sum(nil))[:16]
}

// RunRecord is one lane run, as flake detection reads it.
type RunRecord struct {
	ID     string
	Commit string
	// Tree is the tree hash the run tested. Two runs on one tree are
	// comparable: one passing and one failing is a flake.
	Tree    string
	At      time.Time
	Results *Results
}

// FlakeEvent is one classified failure of one test in one run.
type FlakeEvent struct {
	// Test is the top-level test, which flake state is kept for.
	Test   TestID    `json:"test"`
	Lane   Lane      `json:"lane,omitempty"`
	Commit string    `json:"commit"`
	Run    string    `json:"run"`
	At     time.Time `json:"at"`
	// Signal is set for a confirmed event.
	Signal    Signal  `json:"kind,omitempty"`
	Verdict   Verdict `json:"verdict"`
	Signature string  `json:"signature,omitempty"`
	// InfraGroup is set for a VerdictCorrelated event: the
	// [CorrelatedGroup.Key] of the group it belongs to.
	InfraGroup string `json:"infra_group,omitempty"`
}

// HostEvent is one failure attributed to the host.
type HostEvent struct {
	Rule   string    `json:"rule"`
	Test   TestID    `json:"test"`
	Lane   Lane      `json:"lane,omitempty"`
	Commit string    `json:"commit"`
	Run    string    `json:"run"`
	At     time.Time `json:"at"`
}

// InfraEvent is one co-failing set, with every run it occurred in.
type InfraEvent struct {
	CorrelatedGroup
	Occurrences []EventRef `json:"occurrences"`
}

// EventRef names a run an event occurred in.
type EventRef struct {
	Commit string    `json:"commit"`
	Run    string    `json:"run"`
	At     time.Time `json:"at"`
}

// Detection is what [DetectFlakeEvents] found.
type Detection struct {
	// Events are the per-test events, one per (top-level test, run), in run
	// order. A correlated group's members are VerdictCorrelated events
	// carrying its infra_group. Only VerdictConfirmed events feed
	// [FlakeState].
	Events []FlakeEvent `json:"events"`
	Host   []HostEvent  `json:"host,omitempty"`
	Infra  []InfraEvent `json:"infra,omitempty"`
}

// DetectOptions configures [DetectFlakeEvents].
type DetectOptions struct {
	// HostRules nil means [DefaultHostRules].
	HostRules []HostRule
	Correlate CorrelateOptions
}

// DetectFlakeEvents classifies every failure across runs. Per run, in order:
// races and host signatures are set aside (host events never score); the
// rest are grouped ([GroupCorrelated]) and each group becomes one infra
// event, its members VerdictCorrelated events naming it; what remains is per
// test: a retry pass, or a failure on a tree some other run passed, is
// confirmed; a failure with no test is unattributed; anything else is real.
func DetectFlakeEvents(runs []RunRecord, opts DetectOptions) Detection {
	passed := passedOnTree(runs)
	var det Detection
	infra := map[string]*InfraEvent{}
	var infraOrder []string
	for _, run := range runs {
		if run.Results == nil {
			continue
		}
		lane := run.Results.Lane
		var pending []Case
		seen := map[TestID]bool{}
		emit := func(f Case, v Verdict, sig Signal, group string) {
			top := f.ID.TopLevel()
			if seen[top] {
				return
			}
			seen[top] = true
			det.Events = append(det.Events, FlakeEvent{Test: top, Lane: lane, Commit: run.Commit, Run: run.ID, At: run.At, Signal: sig, Verdict: v, Signature: f.Signature, InfraGroup: group})
		}
		for _, f := range failures(run.Results) {
			switch {
			case IsRace(f.Output):
				emit(f, VerdictRaced, "", "")
			default:
				if rule, ok := MatchHost(f.Output, opts.HostRules); ok {
					det.Host = append(det.Host, HostEvent{Rule: rule, Test: f.ID.TopLevel(), Lane: lane, Commit: run.Commit, Run: run.ID, At: run.At})
					continue
				}
				pending = append(pending, f)
			}
		}
		groups, rest := GroupCorrelated(pending, opts.Correlate)
		for _, g := range groups {
			ev := infra[g.Key]
			if ev == nil {
				ev = &InfraEvent{CorrelatedGroup: g}
				infra[g.Key] = ev
				infraOrder = append(infraOrder, g.Key)
			}
			ev.Occurrences = append(ev.Occurrences, EventRef{Commit: run.Commit, Run: run.ID, At: run.At})
			members := map[TestID]bool{}
			for _, m := range g.Members {
				members[m] = true
			}
			for _, f := range pending {
				if members[f.ID.TopLevel()] {
					emit(f, VerdictCorrelated, "", g.Key)
				}
			}
		}
		for _, f := range rest {
			p := passed[treeTest{run.Tree, f.ID.TopLevel()}]
			switch {
			case f.ID.Name == "":
				emit(f, VerdictUnattributed, "", "")
			case f.RetryPassed:
				emit(f, VerdictConfirmed, SignalRetryPass, "")
			case run.Tree != "" && p.confirm && lane != LaneConfirm:
				emit(f, VerdictConfirmed, SignalConfirm, "")
			case run.Tree != "" && p.any:
				emit(f, VerdictConfirmed, SignalSameTreeSplit, "")
			default:
				emit(f, VerdictReal, "", "")
			}
		}
	}
	for _, k := range infraOrder {
		det.Infra = append(det.Infra, *infra[k])
	}
	return det
}

type treeTest struct {
	tree string
	test TestID
}

// passes says whether some run passed a test on a tree, and whether a
// confirm-lane run did.
type passes struct{ any, confirm bool }

// passedOnTree marks each (tree, top-level test) that some run passed: every
// leaf of the test it ran passed on its last attempt with no failed attempt,
// and at least one did.
func passedOnTree(runs []RunRecord) map[treeTest]passes {
	out := map[treeTest]passes{}
	for _, run := range runs {
		if run.Results == nil || run.Tree == "" {
			continue
		}
		state := map[TestID]int{} // 1 passed, -1 failed
		for _, c := range run.Results.Leaves() {
			top := c.ID.TopLevel()
			switch {
			case c.Status == StatusFail || c.FailedAttempts > 0:
				state[top] = -1
			case c.Status == StatusPass && state[top] == 0:
				state[top] = 1
			}
		}
		for id, s := range state {
			if s == 1 {
				k := treeTest{run.Tree, id}
				p := out[k]
				p.any = true
				p.confirm = p.confirm || run.Results.Lane == LaneConfirm
				out[k] = p
			}
		}
	}
	return out
}

// failures is a run's failure observations: leaf cases that failed on any
// attempt, and packages that failed with no failing case (as a Case with the
// package's id and output).
func failures(r *Results) []Case {
	var out []Case
	for _, c := range r.Leaves() {
		if c.Status == StatusFail || c.FailedAttempts > 0 {
			out = append(out, c)
		}
	}
	for _, p := range r.Packages {
		if (p.Status == StatusFail || p.BuildFailed) && p.Failed == 0 {
			out = append(out, Case{ID: p.ID(), Status: StatusFail, Output: p.Output, Signature: p.Signature})
		}
	}
	return out
}

// Flakes is the forge.test.flakes/v1 document: the flake state of each test
// that has one, host health, and the correlated (infra) events that were
// kept out of the scores.
type Flakes struct {
	Head
	Tests []FlakyTest `json:"tests"`
	// Host is host health: the host failures per signature ([HostHealthOf]).
	Host  []HostHealth `json:"host"`
	Infra []InfraEvent `json:"infra,omitempty"`
	// Suggested are commands worth running next.
	Suggested []string `json:"suggested,omitempty"`
}

// HostHealth is one host signature's failures: how many, and in which runs.
type HostHealth struct {
	// Signature is the host rule's name ("network", "disk", …).
	Signature string `json:"signature"`
	Events    int    `json:"events"`
	// Runs are the run ids, in first-seen order.
	Runs []string `json:"runs"`
	// Tests are the top-level tests that failed this way, sorted.
	Tests []TestID `json:"tests,omitempty"`
}

// HostHealthOf rolls host events up per signature, sorted by signature.
func HostHealthOf(events []HostEvent) []HostHealth {
	by := map[string]*HostHealth{}
	runs := map[string]map[string]bool{}
	tests := map[string]map[TestID]bool{}
	for _, e := range events {
		h := by[e.Rule]
		if h == nil {
			h = &HostHealth{Signature: e.Rule, Runs: []string{}}
			by[e.Rule] = h
			runs[e.Rule] = map[string]bool{}
			tests[e.Rule] = map[TestID]bool{}
		}
		h.Events++
		if !runs[e.Rule][e.Run] {
			runs[e.Rule][e.Run] = true
			h.Runs = append(h.Runs, e.Run)
		}
		if !tests[e.Rule][e.Test] {
			tests[e.Rule][e.Test] = true
			h.Tests = append(h.Tests, e.Test)
		}
	}
	out := make([]HostHealth, 0, len(by))
	for _, h := range by {
		sort.Slice(h.Tests, func(i, j int) bool { return lessID(h.Tests[i], h.Tests[j]) })
		out = append(out, *h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Signature < out[j].Signature })
	return out
}

// FlakyTest is one test's flake record.
type FlakyTest struct {
	ID    TestID     `json:"id"`
	State State      `json:"state"`
	Since *time.Time `json:"since,omitempty"`
	// Score is the flake rate shown beside N: the Wilson 95% lower bound of
	// Events30d over N ([WilsonLowerBound]). A ranking, never a trigger.
	Score float64 `json:"score"`
	// N is the denominator: completed default-branch runs of the test's lane
	// in which it ran (not skipped), cancelled runs excluded.
	N int `json:"n"`
	// Events30d counts distinct commits with a confirmed event in the last
	// 30 days.
	Events30d  int              `json:"events_30d"`
	Events     []FlakeEvent     `json:"events"`
	Quarantine *QuarantineEntry `json:"quarantine,omitempty"`
	// Issue is the URL of the test's flake issue, when there is one.
	Issue string `json:"issue,omitempty"`
}

// QuarantineEntry is a test's quarantine file
// (ci/quarantine/<lang>/<package dir>/<Test>.yaml).
type QuarantineEntry struct {
	Path      string     `json:"path"`
	Issue     string     `json:"issue,omitempty"`
	Reason    string     `json:"reason,omitempty"`
	Signature string     `json:"signature,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// Overdue says ExpiresAt has passed. An expired entry stays applied.
	Overdue bool `json:"overdue,omitempty"`
}

// signatureNoise are the parts of failure output that differ between two
// occurrences of the same failure.
var signatureNoise = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`0x[0-9a-fA-F]+`), "0x?"},
	{regexp.MustCompile(`(?:/tmp|/var/folders|\$TMPDIR|[A-Za-z]:\\Temp)[^\s:"']*`), "<tmp>"},
	{regexp.MustCompile(`\b[0-9a-f]{7,64}\b`), "<hex>"},
	{regexp.MustCompile(`\d+(?:\.\d+)?(?:ns|µs|us|ms|s|m|h)\b`), "<dur>"},
	{regexp.MustCompile(`\d+`), "N"},
	{regexp.MustCompile(`[ \t]+`), " "},
}

// signatureLines is how many meaningful output lines a signature reads.
const signatureLines = 8

// FailureSignature hashes the first meaningful lines of failure output after
// removing what varies between occurrences of one failure (addresses,
// numbers, durations, hex ids, temp paths), so the same failure on two
// commits has the same signature. "" for empty output.
func FailureSignature(output string) string {
	var kept []string
	for _, line := range strings.Split(output, "\n") {
		l := strings.TrimSpace(line)
		if l == "" || strings.HasPrefix(l, "=== ") || strings.HasPrefix(l, "--- ") || strings.HasPrefix(l, "goroutine ") {
			continue
		}
		for _, n := range signatureNoise {
			l = n.re.ReplaceAllString(l, n.repl)
		}
		kept = append(kept, l)
		if len(kept) == signatureLines {
			break
		}
	}
	if len(kept) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(kept, "\n")))
	return hex.EncodeToString(sum[:8])
}
