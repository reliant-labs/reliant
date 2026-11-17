package config

// check.go — the boot config check: a binary run with FORGE_CONFIG_CHECK=1
// loads its configuration exactly as at boot, reports what the load found,
// and exits before it builds anything.
//
// ── Why ───────────────────────────────────────────────────────────────────
//
// The typed config load is the first thing a forge process does, and every
// way it can fail — a required field the environment never wired, a value
// outside allowed_values, a duration that does not parse, a buf.validate rule
// the values break — is decided by two things: the binary and the
// environment it is handed. Both exist before a release is pushed: the
// binary was just built, and the environment is the env's render. So the
// failure can be found there, instead of as a CrashLoopBackOff in the env.
// `forge env build` runs the check on every workload of the project image
// between the image build and the push (internal/cli/config_check.go).
//
// ── The contract (the variables the runner sets) ─────────────────────────
//
//	FORGE_CONFIG_CHECK=1                 the mode is on.
//	FORGE_CONFIG_CHECK_PRESENT=A,B       variables whose value is a
//	                                     PLACEHOLDER: the env wires them from
//	                                     a Secret or another reference whose
//	                                     value the check cannot read.
//	FORGE_CONFIG_CHECK_MOUNTS=/p,/q      paths a Secret, ConfigMap or volume
//	                                     is mounted at when the workload runs.
//
// A placeholder counts as SET for `required` — the env wires the secret, and
// whether the store holds a value for it is the secrets pre-flight's
// question — and a rule about the field's own value (allowed_values, a field
// rule) is not judged, because the value is not known. A message-level rule
// still runs, over the placeholder: a cross-field rule ("signer_enabled needs
// registry_base") then fails on the side the env left EMPTY, which is exactly
// the deployment mistake. A variable the env does not wire at all is absent,
// as in the pod, so an unmounted secret its config requires is a finding.
//
// A config file whose path is under a mount cannot be read here; its layer
// is skipped and the result carries a note saying so.
//
// ── The result ────────────────────────────────────────────────────────────
//
// One line on stdout, CheckResultPrefix and a JSON CheckResult. A load that
// finds a problem prints it and exits 1. A load that finds none records its
// config; once every config registered on the command's path has loaded
// (the configs a generated root loads in sequence), the process prints ok and
// exits 0. A process that loads fewer — a hand-written command that registers
// a config it never loads — fails after checkSettle with a problem naming
// what never loaded: a config the check did not load is not verified. The runner gives the process no network, so nothing a
// process does between its loads reaches anything.
//
// The mode is read from the process environment only. Root.Validate, which
// loads a rendered env from a map, never enters it.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
)

// The boot config check's variables, its result-line prefix and the value
// the runner gives a variable it cannot read. CheckResultPrefix is also how
// a runner tells a binary that has the mode from one built before it: the
// string is in every binary that links this file's code path.
const (
	CheckEnv          = "FORGE_CONFIG_CHECK"
	CheckPresentEnv   = "FORGE_CONFIG_CHECK_PRESENT"
	CheckMountsEnv    = "FORGE_CONFIG_CHECK_MOUNTS"
	CheckResultPrefix = "forge-config-check/v1"
	CheckPlaceholder  = "forge-config-check-placeholder"
)

// CheckResult is what a boot config check prints: whether the process would
// pass its config load, the configs it loaded, every problem a load found,
// and notes about what the check could not judge.
type CheckResult struct {
	OK       bool     `json:"ok"`
	Configs  []string `json:"configs,omitempty"`
	Problems []string `json:"problems,omitempty"`
	Notes    []string `json:"notes,omitempty"`
}

// ParseCheckResult finds the result line in a checked process's stdout (the
// last one, should a process print several); ok is false when there is none.
func ParseCheckResult(stdout string) (CheckResult, bool) {
	lines := strings.Split(stdout, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		rest, found := strings.CutPrefix(strings.TrimSpace(lines[i]), CheckResultPrefix+" ")
		if !found {
			continue
		}
		var r CheckResult
		if json.Unmarshal([]byte(rest), &r) == nil {
			return r, true
		}
	}
	return CheckResult{}, false
}

// checkMode is the process's check: what the runner said, and what its
// loads have found so far. One per process (activeCheck).
type checkMode struct {
	present map[string]bool
	mounts  []string

	mu      sync.Mutex
	loaded  []string
	notes   []string
	settle  *time.Timer
	emitted bool
}

// The process's exit, its stdout and how long a process that loads fewer
// configs than its command registers is given: variables so a test can run
// the check in-process.
var (
	checkExit             = os.Exit
	checkOut    io.Writer = os.Stdout
	checkSettle           = 3 * time.Second
)

var (
	checkOnce   sync.Once
	checkActive *checkMode
)

// activeCheck is the process's check, or nil: the mode is off, or the load
// reads a rendered env (Root.Validate) rather than the process's.
func activeCheck(e environ) *checkMode {
	if e.vars != nil {
		return nil
	}
	checkOnce.Do(func() {
		if os.Getenv(CheckEnv) != "1" {
			return
		}
		c := &checkMode{present: map[string]bool{}}
		for _, n := range splitList(os.Getenv(CheckPresentEnv)) {
			c.present[n] = true
		}
		for _, p := range splitList(os.Getenv(CheckMountsEnv)) {
			c.mounts = append(c.mounts, filepath.Clean(p))
		}
		checkActive = c
	})
	return checkActive
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// placeholder reports whether the variable name carries a placeholder.
func (c *checkMode) placeholder(name string) bool {
	return c != nil && c.present[name]
}

// skipsFile reports whether the config file at path is out of the check's
// reach — under a mount — and notes it if so.
func (c *checkMode) skipsFile(path string) bool {
	if c == nil {
		return false
	}
	clean := filepath.Clean(path)
	for _, m := range c.mounts {
		if clean == m || strings.HasPrefix(clean, m+string(filepath.Separator)) {
			c.mu.Lock()
			c.notes = append(c.notes, fmt.Sprintf("config file %s is mounted at run time: its layer was not checked", path))
			c.mu.Unlock()
			return true
		}
	}
	return false
}

// finish ends one load under the check: a problem prints the result and
// exits 1; a clean load exits 0 once every config on cmd's path has loaded.
// It returns what the load would have, for a test whose checkExit returns.
func (c *checkMode) finish(cmd *cobra.Command, msg proto.Message, collected []error, err error) error {
	name := string(msg.ProtoReflect().Descriptor().FullName())
	problems := collected
	if err != nil && !containsErr(collected, err) {
		problems = append(problems, err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.emitted {
		return errors.Join(problems...)
	}
	c.loaded = append(c.loaded, name)
	if len(problems) > 0 {
		r := c.resultLocked(false)
		for _, p := range problems {
			r.Problems = append(r.Problems, fmt.Sprintf("%s: %v", name, p))
		}
		c.emitLocked(r, 1)
		return errors.Join(problems...)
	}
	missing := c.unloadedLocked(cmd, name)
	if len(missing) == 0 {
		c.emitLocked(c.resultLocked(true), 0)
		return nil
	}
	if c.settle == nil {
		c.settle = time.AfterFunc(checkSettle, func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.emitted {
				return
			}
			// A config the command registers but never loads is one the
			// check did not verify: that is a failure, never an ok.
			still := c.unloadedLocked(cmd, "")
			if len(still) == 0 {
				c.emitLocked(c.resultLocked(true), 0)
				return
			}
			r := c.resultLocked(false)
			for _, n := range still {
				r.Problems = append(r.Problems, fmt.Sprintf("%s: registered on the command but never loaded within %s, so the check could not verify it", n, checkSettle))
			}
			c.emitLocked(r, 1)
		})
	}
	return nil
}

// CheckVariables is the boot config check for a command that reads its
// settings straight from environment variables instead of loading a typed
// config — the scaffolded `db migrate`, whose whole configuration is its
// DSN. In check mode it reports every named variable the process was not
// given (unset or empty) as a problem, prints the result and exits: 0 when
// each one is set, 1 otherwise. A placeholder counts as set, as it does for a
// typed load. Outside check mode it returns at once, so a command calls it
// first, before it acts on anything.
//
// Without it such a command never reaches a config load, so the check can
// only report it unverified — and a migration job then refuses every push
// of an otherwise well-wired env.
func CheckVariables(names ...string) {
	c := activeCheck(environ{})
	if c == nil {
		return
	}
	var missing []string
	for _, n := range names {
		if v, ok := os.LookupEnv(n); !ok || v == "" {
			missing = append(missing, n)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.emitted {
		return
	}
	r := c.resultLocked(len(missing) == 0)
	if len(names) > 0 {
		r.Notes = append(r.Notes, fmt.Sprintf("reads %s directly, with no typed config: checked that each is set", strings.Join(names, ", ")))
	}
	for _, n := range missing {
		r.Problems = append(r.Problems, fmt.Sprintf("%s is not set: the command reads it at start and refuses to run without it", n))
	}
	code := 0
	if len(missing) > 0 {
		code = 1
	}
	c.emitLocked(r, code)
}

// unloadedLocked is the configs registered on cmd's path (and self) that no
// load has finished yet, sorted.
func (c *checkMode) unloadedLocked(cmd *cobra.Command, self string) []string {
	want := map[string]bool{}
	if self != "" {
		want[self] = true
	}
	for p := cmd; p != nil; p = p.Parent() {
		for _, n := range strings.Split(p.Annotations[fileMessagesAnnotation], ",") {
			if n != "" {
				want[n] = true
			}
		}
	}
	for _, n := range c.loaded {
		delete(want, n)
	}
	out := make([]string, 0, len(want))
	for n := range want {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func (c *checkMode) resultLocked(ok bool) CheckResult {
	r := CheckResult{OK: ok, Configs: append([]string(nil), c.loaded...), Notes: append([]string(nil), c.notes...)}
	if n := len(c.present); n > 0 {
		r.Notes = append(r.Notes, fmt.Sprintf("%d variable(s) wired from a secret or reference were checked for presence only", n))
	}
	return r
}

func (c *checkMode) emitLocked(r CheckResult, code int) {
	c.emitted = true
	if c.settle != nil {
		c.settle.Stop()
	}
	b, _ := json.Marshal(r)
	fmt.Fprintf(checkOut, "%s %s\n", CheckResultPrefix, b)
	checkExit(code)
}

func containsErr(errs []error, err error) bool {
	for _, e := range errs {
		if e == err {
			return true
		}
	}
	return false
}
