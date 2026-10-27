package reviewpublisher

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	// ErrDisabled means no complete server configuration or enabled policy exists.
	ErrDisabled = errors.New("review publisher is disabled")
	// ErrUnauthorized means completion does not match current trusted authority.
	ErrUnauthorized = errors.New("review publication is not authorized")
	// ErrUnavailable means an adapter failed; its possibly secret error is withheld.
	ErrUnavailable = errors.New("review publication could not be prepared; retry")
)

var (
	shaPattern    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	idPattern     = regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}$`)
)

type service struct {
	deps    Deps
	origin  string
	enabled bool
}

// New constructs an unwired core. Missing adapters disable it rather than
// silently approving; partially configured services fail construction. It must
// never be exposed as an agent tool or a public RPC.
func New(deps Deps) (Service, error) {
	if deps.Reviews == nil && deps.Heads == nil && deps.Intents == nil && deps.RunOrigin == "" {
		return &service{}, nil
	}
	if deps.Reviews == nil || deps.Heads == nil || deps.Intents == nil {
		return nil, ErrDisabled
	}
	u, err := url.Parse(deps.RunOrigin)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Opaque != "" {
		return nil, ErrDisabled
	}
	return &service{deps: deps, origin: strings.TrimSuffix(u.String(), "/"), enabled: true}, nil
}

func (s *service) Prepare(ctx context.Context, in PrepareInput) (Receipt, error) {
	if !s.enabled {
		return Receipt{}, ErrDisabled
	}
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	if !idPattern.MatchString(in.AssignmentID) || !validBinding(in.Binding) {
		return Receipt{}, ErrUnauthorized
	}
	r, err := s.deps.Reviews.LoadReview(ctx, in.AssignmentID)
	if err != nil {
		return Receipt{}, safeError(ctx, err)
	}
	if !r.Policy.Enabled {
		return Receipt{}, ErrDisabled
	}
	if r.AssignmentID != in.AssignmentID || r.Binding != in.Binding || !validBinding(r.Binding) ||
		r.Policy.RepositoryID != r.Binding.RepositoryID || r.Policy.Revision != r.Binding.PolicyRevision ||
		r.Policy.WorkflowDigest != r.Binding.WorkflowDigest || !validName(r.Policy.CheckName) ||
		r.CurrentGeneration != r.Binding.Generation || r.Cancelled || r.Superseded ||
		!validExecution(r.Author) || r.Author.RootID == r.Binding.Reviewer.RootID ||
		!r.Completion.Final {
		return Receipt{}, ErrUnauthorized
	}
	conclusion, summary, ok := result(r.Completion)
	if !ok {
		return Receipt{}, ErrUnauthorized
	}
	head, err := s.deps.Heads.CurrentHead(ctx, r.Binding.RepositoryID, r.Binding.PullNumber)
	if err != nil {
		return Receipt{}, safeError(ctx, err)
	}
	if head.RepositoryID != r.Binding.RepositoryID || head.PullNumber != r.Binding.PullNumber || head.SHA != r.Binding.HeadSHA {
		return Receipt{}, ErrUnauthorized
	}
	// Only fixed copy is published. Model output, diagnostics and adapter errors
	// cannot carry secrets, markup or arbitrary links into a public check.
	intent := Intent{
		AssignmentID: in.AssignmentID, Binding: r.Binding, CheckName: r.Policy.CheckName,
		Conclusion: conclusion, Summary: summary,
		DetailsURL: s.origin + "/workflows/runs/" + url.PathEscape(r.Binding.Reviewer.RootID),
	}
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	receipt, err := s.deps.Intents.PrepareIntent(ctx, intent)
	if err != nil {
		return Receipt{}, safeError(ctx, err)
	}
	if !idPattern.MatchString(receipt.IntentID) {
		return Receipt{}, ErrUnavailable
	}
	return receipt, nil
}

func validExecution(e Execution) bool {
	return idPattern.MatchString(e.RootID) && idPattern.MatchString(e.RunID)
}

func validBinding(b Binding) bool {
	return b.RepositoryID > 0 && b.PullNumber > 0 && shaPattern.MatchString(b.HeadSHA) &&
		validExecution(b.Reviewer) && digestPattern.MatchString(b.WorkflowDigest) &&
		b.PolicyRevision > 0 && b.Generation > 0
}

func validName(name string) bool {
	if name == "" || len(name) > 100 || !utf8.ValidString(name) || strings.TrimSpace(name) != name {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func result(c Completion) (conclusion, summary string, ok bool) {
	switch c.Verdict {
	case Approve:
		if c.GraphSucceeded {
			return "success", "Review approved. See the review run for details.", true
		}
		return "failure", "Review did not complete successfully. See the review run for details.", true
	case Changes:
		return "failure", "Review requested changes. See the review run for details.", true
	case Incomplete:
		return "failure", "Review is incomplete. See the review run for details.", true
	default:
		return "", "", false
	}
}

func safeError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// Known domain denials remain non-retryable without disclosing adapter text.
	switch {
	case errors.Is(err, ErrDisabled):
		return ErrDisabled
	case errors.Is(err, ErrUnauthorized):
		return ErrUnauthorized
	default:
		return ErrUnavailable
	}
}
