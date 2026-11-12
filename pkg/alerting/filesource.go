// Copyright (c) 2025 Reliant Labs

package alerting

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"time"
)

// FileRuleSource is a single-owner RuleSource that reads a rules document
// (RulesDocument, JSON) from a path, typically a mounted ConfigMap rendered by
// forge from the env's declared alerts. It is how a self-hosted, in-cluster
// evaluator gets its rules through the normal release path.
//
// The file is re-stat'ed at most every Recheck and re-read only when its
// identity, size or mtime changed (a ConfigMap update swaps a symlink, which
// os.SameFile sees). A file that is missing, unreadable or invalid KEEPS the
// last good rules and warns, rate-limited: a broken edit must not silently
// delete every alert. Before the first good read there are no rules.
type FileRuleSource struct {
	// Path of the rules document.
	Path string
	// Owner and Env key every rule. Owner may be any non-empty opaque
	// string; it only has to match what the Querier expects.
	Owner, Env string
	// Identity is stamped on every rule (see Rule.Identity).
	Identity map[string]string
	// Recheck is how often to stat the file. Default 5s.
	Recheck time.Duration
	Logger  *slog.Logger

	mu       sync.Mutex
	checked  time.Time
	info     os.FileInfo
	rules    []Rule
	lastWarn time.Time
	now      func() time.Time
}

var _ RuleSource = (*FileRuleSource)(nil)

const fileWarnEvery = time.Minute

// Owners implements RuleSource.
func (f *FileRuleSource) Owners(context.Context) ([]string, error) {
	if len(f.current()) == 0 {
		return nil, nil
	}
	return []string{f.Owner}, nil
}

// Rules implements RuleSource. An owner other than f.Owner has none.
func (f *FileRuleSource) Rules(_ context.Context, owner string) ([]Rule, error) {
	if owner != f.Owner {
		return nil, nil
	}
	return f.current(), nil
}

func (f *FileRuleSource) current() []Rule {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.now == nil {
		f.now = time.Now
	}
	recheck := f.Recheck
	if recheck <= 0 {
		recheck = 5 * time.Second
	}
	now := f.now()
	if !f.checked.IsZero() && now.Sub(f.checked) < recheck {
		return f.rules
	}
	f.checked = now
	f.refreshLocked(now)
	return f.rules
}

func (f *FileRuleSource) refreshLocked(now time.Time) {
	info, err := os.Stat(f.Path)
	if err != nil {
		f.warnLocked(now, err)
		return
	}
	if f.info != nil && os.SameFile(f.info, info) && f.info.ModTime().Equal(info.ModTime()) && f.info.Size() == info.Size() {
		return
	}
	raw, err := os.ReadFile(f.Path)
	if err != nil {
		f.warnLocked(now, err)
		return
	}
	set, err := ParseRulesDocument(raw)
	if err != nil {
		f.info = info // do not re-parse the same bad bytes every recheck
		f.warnLocked(now, err)
		return
	}
	rules := make([]Rule, len(set))
	for i, s := range set {
		rules[i] = s.Rule(f.Owner, f.Env, f.Identity)
	}
	if f.info != nil || len(rules) > 0 {
		f.logger().Info("alert rules loaded", "path", f.Path, "rules", len(rules))
	}
	f.info, f.rules = info, rules
}

func (f *FileRuleSource) warnLocked(now time.Time, err error) {
	if f.lastWarn.IsZero() || now.Sub(f.lastWarn) >= fileWarnEvery {
		f.lastWarn = now
		f.logger().Warn("alert rules file unusable; keeping the last good rules", "path", f.Path, "rules", len(f.rules), "error", err)
	}
}

func (f *FileRuleSource) logger() *slog.Logger {
	if f.Logger != nil {
		return f.Logger
	}
	return slog.Default().With("component", "alert-rules")
}
