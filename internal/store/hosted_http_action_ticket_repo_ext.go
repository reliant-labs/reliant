package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// CreateHostedHTTPActionTicket derives authority from the active persisted run,
// never from the caller. No owner fallback: legacy ownerless runs fail closed.
// ON CONFLICT retains expired/redeemed tombstones to refuse activity retries.
func CreateHostedHTTPActionTicket(ctx context.Context, db orm.Context, t core.HostedHTTPActionTicket) (bool, error) {
	result, err := db.Bun().NewRaw(`
 INSERT INTO hosted_http_action_tickets
 (id,workflow_id,temporal_run_id,activity_id,owner_user_id,project_id,workflow_name,node_id,grant_id,request_sha256,created_at,expires_at)
 SELECT ?,w.id,?,?,w.owner_user_id,c.project_id,w.workflow_name,?,?,?,CURRENT_TIMESTAMP,?
 FROM workflows w JOIN chats c ON c.id=w.chat_id
 JOIN projects p ON p.id=c.project_id
 WHERE w.id=? AND w.state=2 AND w.completed_at IS NULL
 AND w.owner_user_id IS NOT NULL AND w.owner_user_id<>''
 AND c.user_id=w.owner_user_id AND p.user_id=w.owner_user_id
 AND w.workflow_name<>''
 AND ? > CURRENT_TIMESTAMP AND ? <= CURRENT_TIMESTAMP + INTERVAL '1 minute'
 ON CONFLICT DO NOTHING`, t.ID, t.TemporalRunID, t.ActivityID, t.NodeID, t.GrantID, t.RequestSHA256, t.ExpiresAt, t.WorkflowID, t.ExpiresAt, t.ExpiresAt).Exec(ctx)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// RedeemHostedHTTPActionTicket atomically consumes only an unexpired exact
// match. A foreign grant, wrong digest or stopped/reowned run cannot consume it.
func RedeemHostedHTTPActionTicket(ctx context.Context, db orm.Context, id, grant, digest string) (*core.HostedHTTPActionTicket, error) {
	var ticket core.HostedHTTPActionTicket
	err := db.Bun().NewRaw(`
 UPDATE hosted_http_action_tickets t SET redeemed_at=CURRENT_TIMESTAMP
 FROM workflows w JOIN chats c ON c.id=w.chat_id JOIN projects p ON p.id=c.project_id
 WHERE t.id=? AND t.grant_id=? AND t.request_sha256=?
 AND t.redeemed_at IS NULL AND t.expires_at>CURRENT_TIMESTAMP
 AND w.id=t.workflow_id AND w.state=2 AND w.completed_at IS NULL
 AND w.owner_user_id=t.owner_user_id AND c.user_id=t.owner_user_id
 AND p.user_id=t.owner_user_id AND c.project_id=t.project_id
 AND w.workflow_name=t.workflow_name
 RETURNING t.id, t.workflow_id, t.temporal_run_id, t.activity_id,
 t.owner_user_id, t.project_id, t.workflow_name, t.node_id, t.grant_id,
 t.request_sha256, t.created_at, t.expires_at, t.redeemed_at`, id, grant, digest).Scan(ctx, &ticket)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ticket, nil
}
