// Copyright (c) 2025 Reliant Labs
package fakegithub

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/mergequeue/ghport"
)

// Method names a [ghport.Client] method, for faults, hooks and the call log.
type Method string

const (
	MethodGetRef           Method = "GetRef"
	MethodCreateRef        Method = "CreateRef"
	MethodUpdateRef        Method = "UpdateRef"
	MethodDeleteRef        Method = "DeleteRef"
	MethodGetPullRequest   Method = "GetPullRequest"
	MethodMergePullRequest Method = "MergePullRequest"
	MethodCreateCheckRun   Method = "CreateCheckRun"
	MethodUpdateCheckRun   Method = "UpdateCheckRun"
	MethodListCheckRuns    Method = "ListCheckRuns"
	MethodGetContents      Method = "GetContents"
	MethodGetCommit        Method = "GetCommit"
	MethodCreateCommit     Method = "CreateCommit"
	MethodCreateComment    Method = "CreateIssueComment"
	MethodListComments     Method = "ListIssueComments"
)

// Call is one client call as the fake received it.
type Call struct {
	Method Method
	Repo   ghport.Repo
	Number int
	Ref    string
	SHA    string
	// Fault is the injected error the call returned, if any.
	Fault error
}

// Calls returns every client call so far, in order.
func (g *GitHub) Calls() []Call {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]Call(nil), g.calls...)
}

// CallsTo returns the calls of one method.
func (g *GitHub) CallsTo(m Method) []Call {
	var out []Call
	for _, c := range g.Calls() {
		if c.Method == m {
			out = append(out, c)
		}
	}
	return out
}

// Fault makes calls fail.
type Fault struct {
	// Method limits the fault to one method; empty matches every call.
	Method Method
	// Times is how many matching calls fail; 0 means 1.
	Times int
	// Err is what they return: [RateLimit], [ServerError], or any error.
	Err error
	// AfterEffect performs the call first and then returns Err, the way a
	// 5xx or a dropped connection can hide a merge that did happen. A
	// retry then meets the effect (for a merge, ErrAlreadyMerged).
	AfterEffect bool
}

// Inject queues a fault. Faults match in the order they were injected.
func (g *GitHub) Inject(f Fault) {
	if f.Times <= 0 {
		f.Times = 1
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.faults = append(g.faults, &f)
}

// RateLimit is a primary rate limit that resets at reset.
func RateLimit(reset time.Time) error { return &ghport.RateLimitError{Reset: reset} }

// ServerError is a 5xx.
func ServerError(status int) error {
	return &ghport.TransientError{Status: status, Message: "injected by fakegithub"}
}

type hook struct {
	method Method
	once   bool
	fn     func(Call)
}

// BeforeCall registers fn to run before every call of method (empty: every
// call), outside the fake's lock, so it may use the [Repo] helpers. The
// returned func unregisters it.
func (g *GitHub) BeforeCall(method Method, fn func(Call)) (remove func()) {
	h := &hook{method: method, fn: fn}
	g.mu.Lock()
	g.hooks = append(g.hooks, h)
	g.mu.Unlock()
	return func() { g.removeHook(h) }
}

func (g *GitHub) removeHook(h *hook) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for i, x := range g.hooks {
		if x == h {
			g.hooks = append(g.hooks[:i], g.hooks[i+1:]...)
			return
		}
	}
}

// ForeignWrite makes another writer commit files onto branch just before the
// next call of method: main moving between two of the lander's merges, which
// its `main^ == previous` check must catch.
func (r *Repo) ForeignWrite(method Method, branch string, files map[string]string) {
	h := &hook{method: method, once: true}
	// The hook may run on whatever goroutine makes the call (a lander under
	// test), so it reports with Errorf, never Fatalf.
	h.fn = func(Call) {
		r.g.mu.Lock()
		defer r.g.mu.Unlock()
		if _, err := r.commitLocked(branch, "foreign write", files); err != nil {
			r.g.t.Errorf("fakegithub: foreign write on %s: %v", branch, err)
		}
	}
	r.g.mu.Lock()
	defer r.g.mu.Unlock()
	r.g.hooks = append(r.g.hooks, h)
}

// SquashCall is a squash merge the fake is about to write.
type SquashCall struct {
	PR      int
	BaseTip string
	Head    string
	// Tree is what git's merge computed.
	Tree string
}

// OverrideSquash makes every later squash merge write fn's tree instead of
// the computed one ("GitHub's merge differs"); nil restores the default.
// fn runs under the fake's lock and must not call client methods or the
// Commit helpers; [Repo.TreeWithFile] is safe.
func (r *Repo) OverrideSquash(fn func(SquashCall) string) {
	r.g.mu.Lock()
	defer r.g.mu.Unlock()
	r.squashOverride = fn
}

type divergence struct {
	path    string
	content string
}

// DivergeNextSquash makes the next squash merge write a tree with one extra
// file, so the lander's tree(main) == T_i check fails exactly once.
func (r *Repo) DivergeNextSquash(path, content string) {
	r.g.mu.Lock()
	defer r.g.mu.Unlock()
	r.diverge = append(r.diverge, divergence{path: path, content: content})
}

// TreeWithFile returns tree with path set to content. It is safe inside an
// [Repo.OverrideSquash] func.
func (r *Repo) TreeWithFile(tree, path, content string) string {
	r.g.t.Helper()
	out, err := r.editTree(tree, map[string]string{path: content})
	if err != nil {
		r.g.t.Fatalf("fakegithub: %v", err)
	}
	return out
}

// applySquashFaults applies an override or a queued divergence. Caller
// holds g.mu.
func (r *Repo) applySquashFaults(c SquashCall) (string, error) {
	tree := c.Tree
	if r.squashOverride != nil {
		tree = r.squashOverride(c)
	}
	if len(r.diverge) > 0 {
		d := r.diverge[0]
		r.diverge = r.diverge[1:]
		var err error
		if tree, err = r.editTree(tree, map[string]string{d.path: d.content}); err != nil {
			return "", err
		}
	}
	return tree, nil
}

// begin runs the hooks, records the call and picks a fault. A fault that is
// not AfterEffect is returned as err; an AfterEffect one as after, for the
// method to return once it has done its work.
func (g *GitHub) begin(ctx context.Context, c Call) (after, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	g.mu.Lock()
	var run []*hook
	kept := g.hooks[:0]
	for _, h := range g.hooks {
		match := h.method == "" || h.method == c.Method
		if match {
			run = append(run, h)
		}
		if !match || !h.once {
			kept = append(kept, h)
		}
	}
	g.hooks = kept
	g.mu.Unlock()
	for _, h := range run {
		h.fn(c)
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	var fault *Fault
	for i, f := range g.faults {
		if f.Method == "" || f.Method == c.Method {
			fault = f
			if f.Times--; f.Times == 0 {
				g.faults = append(g.faults[:i], g.faults[i+1:]...)
			}
			break
		}
	}
	if fault != nil {
		c.Fault = fault.Err
	}
	g.calls = append(g.calls, c)
	switch {
	case fault == nil:
		return nil, nil
	case fault.AfterEffect:
		return fault.Err, nil
	default:
		return nil, fault.Err
	}
}
