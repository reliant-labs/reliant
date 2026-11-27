// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"context"
	"slices"
	"time"

	"github.com/reliant-labs/reliant/internal/store"
)

// The library's audit and its two-step prune (scope r3.3). A token's
// prune is staged as a request; a person, through a person-class
// credential, confirms or cancels it.

var pruneRequestStatuses = []PruneRequestStatus{PruneRequestPending, PruneRequestConfirmed, PruneRequestCancelled, PruneRequestExpired}

// pruneExpiryCutoff is the creation time at or before which a PENDING prune
// request has expired.
func (s *service) pruneExpiryCutoff() time.Time { return s.now().Add(-LibraryPruneRequestTTL) }

// ListLibraryImports lists the org's import audit, newest first.
func (s *service) ListLibraryImports(ctx context.Context, actor Actor, in ListLibraryImportsInput) (ListLibraryImportsResult, error) {
	if err := s.admit(ctx, actor); err != nil {
		return ListLibraryImportsResult{}, err
	}
	limit, offset, err := page(in.PageSize, in.PageToken, DefaultLibraryImportPageSize, MaxLibraryImportPageSize)
	if err != nil {
		return ListLibraryImportsResult{}, err
	}
	rows, err := s.store.ListLibraryImports(ctx, LibraryImportFilter{OrgID: actor.OrgID, Limit: limit, Offset: offset})
	if err != nil {
		return ListLibraryImportsResult{}, err
	}
	rows, next := nextToken(rows, limit, offset)
	out := make([]LibraryImport, 0, len(rows))
	for _, r := range rows {
		out = append(out, libraryImportFromRow(r))
	}
	return ListLibraryImportsResult{Imports: out, NextPageToken: next}, nil
}

// ListLibraryPruneRequests lists the org's prune requests, newest first,
// each PENDING one past its TTL answered as EXPIRED.
func (s *service) ListLibraryPruneRequests(ctx context.Context, actor Actor, in ListLibraryPruneRequestsInput) (ListLibraryPruneRequestsResult, error) {
	if err := s.admit(ctx, actor); err != nil {
		return ListLibraryPruneRequestsResult{}, err
	}
	if in.Status != "" && !slices.Contains(pruneRequestStatuses, in.Status) {
		return ListLibraryPruneRequestsResult{}, invalid("status", "unknown status")
	}
	limit, offset, err := page(in.PageSize, in.PageToken, DefaultLibraryImportPageSize, MaxLibraryImportPageSize)
	if err != nil {
		return ListLibraryPruneRequestsResult{}, err
	}
	cutoff := s.pruneExpiryCutoff()
	rows, err := s.store.ListPruneRequests(ctx, PruneRequestFilter{OrgID: actor.OrgID, Status: in.Status, ExpiredAtOrBefore: cutoff, Limit: limit, Offset: offset})
	if err != nil {
		return ListLibraryPruneRequestsResult{}, err
	}
	rows, next := nextToken(rows, limit, offset)
	out := make([]LibraryPruneRequest, 0, len(rows))
	for _, r := range rows {
		out = append(out, pruneRequestFromRow(r, cutoff))
	}
	return ListLibraryPruneRequestsResult{Requests: out, NextPageToken: next}, nil
}

// decidePrune is ConfirmLibraryPrune's and CancelLibraryPrune's common
// path: a person through a person-class credential (a token, even one that
// may import, is refused) decides a PENDING request that has not expired.
// apply runs in the decision's transaction, before the request is recorded
// with status. A request found expired is recorded EXPIRED, in a
// transaction of its own, and refused.
func (s *service) decidePrune(ctx context.Context, actor Actor, id, action string, status PruneRequestStatus, apply func(ctx context.Context, req *store.GtmLibraryPruneRequest) error) (*store.GtmLibraryPruneRequest, error) {
	if err := s.admit(ctx, actor); err != nil {
		return nil, err
	}
	if !actor.Person() {
		return nil, personOnly(action)
	}
	if err := checkID("request_id", id); err != nil {
		return nil, err
	}
	var expired bool
	var out *store.GtmLibraryPruneRequest
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		expired, out = false, nil
		req, err := s.store.GetPruneRequest(ctx, actor.OrgID, id)
		if err != nil {
			return s.notFound(ctx, err, "prune request", id)
		}
		if PruneRequestStatus(req.Status) == PruneRequestExpired {
			return ErrPruneRequestExpired
		}
		if PruneRequestStatus(req.Status) != PruneRequestPending {
			return ErrPruneRequestNotPending
		}
		now := s.now()
		if !req.CreatedAt.After(s.pruneExpiryCutoff()) {
			// Recorded, then refused: this transaction commits the EXPIRED.
			expired = true
			return s.store.DecidePruneRequest(ctx, actor.OrgID, id, PruneRequestExpired, "", now)
		}
		if err := apply(ctx, req); err != nil {
			return err
		}
		if err := s.store.DecidePruneRequest(ctx, actor.OrgID, id, status, actor.UserID, now); err != nil {
			return err
		}
		out, err = s.store.GetPruneRequest(ctx, actor.OrgID, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	if expired {
		return nil, ErrPruneRequestExpired
	}
	return out, nil
}

// ConfirmLibraryPrune carries out a PENDING prune request. It removes only
// the request's keys that are still in the library and were not imported
// again since the request was made: an entry whose imported_at is after
// the request's created_at was named by a later import, and stays. Both
// stamps come from the service clock.
func (s *service) ConfirmLibraryPrune(ctx context.Context, actor Actor, in ConfirmLibraryPruneInput) (ConfirmLibraryPruneResult, error) {
	var removed int64
	row, err := s.decidePrune(ctx, actor, in.RequestID, "confirm a library prune", PruneRequestConfirmed, func(ctx context.Context, req *store.GtmLibraryPruneRequest) error {
		n, err := s.store.PruneLibraryKeys(ctx, actor.OrgID, req.RemoveKeys, req.CreatedAt)
		removed = n
		return err
	})
	if err != nil {
		return ConfirmLibraryPruneResult{}, err
	}
	return ConfirmLibraryPruneResult{Removed: int(removed), Request: pruneRequestFromRow(row, s.pruneExpiryCutoff())}, nil
}

// CancelLibraryPrune records a PENDING prune request CANCELLED; nothing is
// removed.
func (s *service) CancelLibraryPrune(ctx context.Context, actor Actor, in CancelLibraryPruneInput) (LibraryPruneRequest, error) {
	row, err := s.decidePrune(ctx, actor, in.RequestID, "cancel a library prune", PruneRequestCancelled, func(context.Context, *store.GtmLibraryPruneRequest) error { return nil })
	if err != nil {
		return LibraryPruneRequest{}, err
	}
	return pruneRequestFromRow(row, s.pruneExpiryCutoff()), nil
}

func libraryImportFromRow(r *store.GtmLibraryImport) LibraryImport {
	return LibraryImport{
		ID:             r.ID,
		ActorUserID:    r.ActorUserID,
		CredentialKind: CredentialKind(r.CredentialKind),
		SourceRevision: r.SourceRevision,
		Created:        int(r.Created),
		Updated:        int(r.Updated),
		Unchanged:      int(r.Unchanged),
		Pruned:         int(r.Pruned),
		PruneRequestID: deref(r.PruneRequestID),
		CreatedAt:      r.CreatedAt,
	}
}

// pruneRequestFromRow is r as answered: a PENDING row created at or before
// cutoff is EXPIRED.
func pruneRequestFromRow(r *store.GtmLibraryPruneRequest, cutoff time.Time) LibraryPruneRequest {
	status := PruneRequestStatus(r.Status)
	if status == PruneRequestPending && !r.CreatedAt.After(cutoff) {
		status = PruneRequestExpired
	}
	out := LibraryPruneRequest{
		ID:             r.ID,
		RequestedBy:    r.RequestedBy,
		CredentialKind: CredentialKind(r.CredentialKind),
		SourceRevision: r.SourceRevision,
		RemoveKeys:     slices.Clone(r.RemoveKeys),
		Status:         status,
		DecidedBy:      deref(r.DecidedBy),
		CreatedAt:      r.CreatedAt,
		ExpiresAt:      r.CreatedAt.Add(LibraryPruneRequestTTL),
	}
	if r.DecidedAt != nil {
		out.DecidedAt = *r.DecidedAt
	}
	return out
}
