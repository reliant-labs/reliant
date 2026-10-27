package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/reviewpublisher"
)

// ReviewPublicationIntentExtra is the owned non-column extension seam.
type ReviewPublicationIntentExtra struct{}

// ReviewPublication is a durable private mutation/reconciliation receipt.
type ReviewPublication struct {
	ID, State, CheckURL string
	CheckID             int64
	Intent              reviewpublisher.Intent
}

// Publication reads and verifies an immutable intent against current authority.
func (s ReviewAuthorityStore) Publication(ctx context.Context, id string) (ReviewPublication, ReviewAuthority, error) {
	var row struct {
		ID, AssignmentID, State string
		Payload                 json.RawMessage
		CheckID                 sql.NullInt64
		CheckURL                sql.NullString
	}
	err := s.DB.Bun().NewRaw(`SELECT id,assignment_id,state,payload,check_id,check_url FROM review_publication_intents WHERE id=?`, id).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return ReviewPublication{}, ReviewAuthority{}, reviewpublisher.ErrUnauthorized
	}
	if err != nil {
		return ReviewPublication{}, ReviewAuthority{}, err
	}
	var in reviewpublisher.Intent
	if json.Unmarshal(row.Payload, &in) != nil || in.AssignmentID != row.AssignmentID {
		return ReviewPublication{}, ReviewAuthority{}, reviewpublisher.ErrUnauthorized
	}
	a, err := s.LoadReviewAuthority(ctx, row.AssignmentID)
	if err != nil {
		return ReviewPublication{}, a, err
	}
	if !authorityCurrent(a) || a.Binding != in.Binding || a.Policy.CheckName != in.CheckName || !a.Completion.Final {
		return ReviewPublication{}, a, reviewpublisher.ErrUnauthorized
	}
	expected := "failure"
	if a.Completion.Verdict == reviewpublisher.Approve && a.Completion.GraphSucceeded {
		expected = "success"
	}
	if in.Conclusion != expected {
		return ReviewPublication{}, a, reviewpublisher.ErrUnauthorized
	}
	return ReviewPublication{ID: row.ID, State: row.State, CheckURL: row.CheckURL.String, CheckID: row.CheckID.Int64, Intent: in}, a, nil
}

// ClaimPublication persists before any create RPC. Exactly one caller may attempt
// create. Existing publishing/uncertain states must reconcile, never POST again.
func (s ReviewAuthorityStore) ClaimPublication(ctx context.Context, id string) (bool, error) {
	claimed := false
	err := s.DB.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		p, _, err := s.Publication(ctx, id)
		if err != nil {
			return err
		}
		if p.State != "prepared" {
			return nil
		}
		res, err := s.DB.Bun().NewRaw(`UPDATE review_publication_intents SET state='publishing',updated_at=now() WHERE id=? AND state='prepared'`, id).Exec(ctx)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		claimed = n == 1
		return err
	})
	return claimed, err
}

// UncertainPublication never releases a create claim after errors or absence.
func (s ReviewAuthorityStore) UncertainPublication(ctx context.Context, id string) error {
	_, err := s.DB.Bun().NewRaw(`UPDATE review_publication_intents SET state='uncertain',updated_at=now() WHERE id=? AND state='publishing'`, id).Exec(ctx)
	return err
}

// ConfirmPublication requires still-current authority and a nonzero CP receipt.
// Exact remote binding is verified by the CP fixed-operation bridge, not claimed
// from a model or a generic GitHub webhook.
func (s ReviewAuthorityStore) ConfirmPublication(ctx context.Context, id string, checkID int64, checkURL string) error {
	if checkID <= 0 || checkURL == "" {
		return reviewpublisher.ErrUnauthorized
	}
	return s.DB.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		p, _, err := s.Publication(ctx, id)
		if err != nil {
			return err
		}
		if p.State == "confirmed" {
			if p.CheckID != checkID || p.CheckURL != checkURL {
				return reviewpublisher.ErrUnauthorized
			}
			return nil
		}
		if p.State != "publishing" && p.State != "uncertain" {
			return reviewpublisher.ErrUnauthorized
		}
		_, err = s.DB.Bun().NewRaw(`UPDATE review_publication_intents SET state='confirmed',check_id=?,check_url=?,updated_at=now() WHERE id=?`, checkID, checkURL, id).Exec(ctx)
		return err
	})
}
