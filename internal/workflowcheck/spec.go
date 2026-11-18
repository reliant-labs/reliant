// Copyright (c) 2025 Reliant Labs
package workflowcheck

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/triggers/triggerspec"
)

// The bounds and default of check.timeout.
const (
	DefaultTimeout = 6 * time.Hour
	MinTimeout     = time.Minute
	MaxTimeout     = 72 * time.Hour
	// MaxNameLength is GitHub's limit on a check run's name.
	MaxNameLength = 255
)

var (
	shaPattern  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	zeroSHA     = strings.Repeat("0", 40)
	repoPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})/[A-Za-z0-9._-]{1,100}$`)
)

// Spec is a workflow's `check:` block, parsed.
type Spec struct {
	Name       string
	Repository string // template over `trigger`; "" is the event's
	HeadSHA    string // template over `trigger`; "" is the event's head
	Output     string // template over the ended run's scope
	Timeout    time.Duration
}

// SpecOf returns wf's check, or nil when it declares none. An invalid
// timeout is an error (validation refuses it first).
func SpecOf(wf *reliantv1.Workflow) (*Spec, error) {
	check := wf.GetCheck()
	if check == nil {
		return nil, nil
	}
	timeout, err := ParseTimeout(check.GetTimeout())
	if err != nil {
		return nil, err
	}
	return &Spec{
		Name:       strings.TrimSpace(check.GetName()),
		Repository: strings.TrimSpace(check.GetRepository()),
		HeadSHA:    strings.TrimSpace(check.GetHeadSha()),
		Output:     strings.TrimSpace(check.GetOutput()),
		Timeout:    timeout,
	}, nil
}

// ParseTimeout reads check.timeout: a Go duration from MinTimeout to
// MaxTimeout, "" for DefaultTimeout.
func ParseTimeout(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return DefaultTimeout, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("timeout %q is not a duration (e.g. 30m, 2h)", s)
	}
	if d < MinTimeout || d > MaxTimeout {
		return 0, fmt.Errorf("timeout %s is outside %s to %s", d, MinTimeout, MaxTimeout)
	}
	return d, nil
}

// ValidateName reports why name cannot be a check's name, or nil.
func ValidateName(name string) error {
	switch trimmed := strings.TrimSpace(name); {
	case trimmed == "":
		return fmt.Errorf("name is required: the check's name on GitHub, e.g. reliant/ci")
	case len(trimmed) > MaxNameLength:
		return fmt.Errorf("name is %d characters; GitHub allows %d", len(trimmed), MaxNameLength)
	case strings.Contains(trimmed, "{{"):
		return fmt.Errorf("name is a template; a ruleset requires a check by a fixed name, so write it literally")
	}
	return nil
}

// ValidateRepository reports why a literal repository is not "owner/name";
// a template is checked by CompileTemplates.
func ValidateRepository(repo string) error {
	if repo == "" || strings.Contains(repo, "{{") || repoPattern.MatchString(repo) {
		return nil
	}
	return fmt.Errorf("repository %q is not owner/name", repo)
}

// ValidateHeadSHA reports why a literal head_sha is not a commit; a template
// is checked by CompileTemplates.
func ValidateHeadSHA(sha string) error {
	if sha == "" || strings.Contains(sha, "{{") || shaPattern.MatchString(sha) {
		return nil
	}
	return fmt.Errorf("head_sha %q is not a 40-character lowercase hex commit sha", sha)
}

// Target is where one firing's check is posted.
type Target struct {
	Owner, Repo, HeadSHA string
}

// ResolveTarget renders spec's repository and head_sha against a firing:
// each the declared template, or the event's own when none is declared. An
// error says why the firing has no check (a template that fails, an event
// that names no repository or commit); the run still launches.
func ResolveTarget(spec *Spec, payload map[string]any, scope triggerspec.Scope) (Target, error) {
	repository := DefaultRepository(payload)
	if spec.Repository != "" {
		rendered, err := triggerspec.RenderCheckTemplate(spec.Repository, scope)
		if err != nil {
			return Target{}, fmt.Errorf("check.repository: %w", err)
		}
		repository = rendered
	}
	sha := DefaultHeadSHA(payload)
	if spec.HeadSHA != "" {
		rendered, err := triggerspec.RenderCheckTemplate(spec.HeadSHA, scope)
		if err != nil {
			return Target{}, fmt.Errorf("check.head_sha: %w", err)
		}
		sha = strings.ToLower(rendered)
	}
	if repository == "" {
		return Target{}, fmt.Errorf("check.repository: the event names no repository; set check.repository")
	}
	if !repoPattern.MatchString(repository) {
		return Target{}, fmt.Errorf("check.repository: %q is not owner/name", repository)
	}
	if sha == "" {
		return Target{}, fmt.Errorf("check.head_sha: the event names no commit; set check.head_sha")
	}
	if !shaPattern.MatchString(sha) || sha == zeroSHA {
		return Target{}, fmt.Errorf("check.head_sha: %q is not a commit sha", sha)
	}
	owner, repo, _ := strings.Cut(repository, "/")
	return Target{Owner: owner, Repo: repo, HeadSHA: sha}, nil
}

// DefaultRepository is the repository an event names
// (trigger.payload.attributes.repository), "" when it names none.
func DefaultRepository(payload map[string]any) string {
	return str(dig(payload, "attributes", "repository"))
}

// DefaultHeadSHA is the head commit a GitHub event names: a pull request's
// head, a check suite's or check run's head_sha, a workflow run's head_sha,
// or the commit a push moved its branch to. "" when it names none (a
// schedule; a push that deleted a branch).
func DefaultHeadSHA(payload map[string]any) string {
	for _, path := range [][]string{
		{"data", "pull_request", "head", "sha"},
		{"data", "check_suite", "head_sha"},
		{"data", "check_run", "head_sha"},
		{"data", "workflow_run", "head_sha"},
		{"data", "after"},
	} {
		if sha := strings.ToLower(str(dig(payload, path...))); sha != "" && sha != zeroSHA {
			return sha
		}
	}
	return ""
}

func dig(v any, path ...string) any {
	for _, key := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[key]
	}
	return v
}

func str(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}
