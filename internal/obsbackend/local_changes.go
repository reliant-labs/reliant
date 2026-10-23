package obsbackend

import (
	"context"
	"fmt"

	"github.com/reliant-labs/forge/internal/releasetimeline"
	"github.com/reliant-labs/forge/pkg/release"
)

// LedgerReader is the part of the machine ledger the change feed reads: the
// timeline's readers plus the local stacks (`forge env up` sessions).
type LedgerReader interface {
	releasetimeline.LedgerReader
	SessionsFor(env string) ([]release.LocalSession, error)
}

// Changes implements ChangesReader over this machine's ledger: the env's
// promotions and failed applies, and the times its local stack started. There
// is no activity feed locally.
func (l Local) Changes(ctx context.Context, req ChangesRequest) (ChangesResult, error) {
	var res ChangesResult
	if l.Ledger == nil {
		return res, Undetermined("no release ledger is available for %s", l.Env)
	}
	ledger, err := l.Ledger()
	if err != nil {
		return res, Undetermined("cannot open this machine's release ledger").Cause(err)
	}
	data, err := releasetimeline.Local{Ledger: ledger}.Load(ctx, l.Env, req.Range.From)
	if err != nil {
		return res, Undetermined("cannot read %s's release ledger", l.Env).Cause(err)
	}
	res.Warnings = append(res.Warnings, data.Warnings...)
	tl := releasetimeline.Assemble(data, req.Range.From, 0)
	res.current(tl, req.Range.To)
	for _, e := range tl.Entries {
		if !req.Range.To.IsZero() && e.At.After(req.Range.To) {
			continue
		}
		res.Changes = append(res.Changes, changeOfEntry(e))
	}
	sessions, err := ledger.SessionsFor(l.Env)
	if err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("local stacks unreadable: %v", err))
	}
	for _, s := range sessions {
		if s.StartedAt.Before(req.Range.From) || (!req.Range.To.IsZero() && s.StartedAt.After(req.Range.To)) {
			continue
		}
		label := firstNonEmpty(s.Worktree.Label, s.Worktree.Key, "primary checkout")
		res.Changes = append(res.Changes, Change{
			Kind: ChangeSession, At: s.StartedAt.UTC(), Via: "local",
			Summary: fmt.Sprintf("forge env up %s started (%s)", l.Env, label),
		})
	}
	sortChanges(&res, req.Limit)
	return res, nil
}
