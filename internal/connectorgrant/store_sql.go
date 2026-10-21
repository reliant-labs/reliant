// Copyright (c) 2025 Reliant Labs

package connectorgrant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/store"
)

// SQLStore is the Postgres-backed Store, over the forge ORM's generated
// stores and the connector queries in internal/store. It maps those rows onto
// this package's types.
type SQLStore struct {
	db orm.Context
}

// Deps is what the SQL store runs on.
type Deps struct {
	// DB is the forge ORM handle. A call made with a ctx carrying a
	// transaction joins it.
	DB orm.Context
}

// NewSQLStore returns the Postgres-backed Store.
//
//forge:constructor
func NewSQLStore(deps Deps) Store {
	return &SQLStore{db: deps.DB}
}

// CreateGrant inserts a grant.
//
// The DB's CHECK constraints are the backstop, but the same rules are enforced
// here so a caller gets a clear message rather than a constraint violation.
func (s *SQLStore) CreateGrant(ctx context.Context, g *Grant) error {
	if err := validateGrant(g); err != nil {
		return err
	}

	tools, err := json.Marshal(g.AllowedTools)
	if err != nil {
		return fmt.Errorf("encode allowed tools: %w", err)
	}
	allowlist, err := json.Marshal(orEmpty(g.ExecAllowlist))
	if err != nil {
		return fmt.Errorf("encode exec allowlist: %w", err)
	}

	now := time.Now().UTC()
	if g.CreatedAt.IsZero() {
		g.CreatedAt = now
	}
	g.UpdatedAt = now
	row := &store.ConnectorGrant{
		ID: g.ID, UserID: g.UserID, DaemonID: g.DaemonID, Name: g.Name, TokenPrefix: g.TokenPrefix,
		AllowedTools: tools, PathRoot: g.PathRoot, ExecMode: string(g.ExecMode), ExecAllowlist: allowlist,
		ExpiresAt: g.ExpiresAt, CreatedAt: g.CreatedAt, UpdatedAt: g.UpdatedAt,
	}
	if err := store.CreateConnectorGrant(ctx, s.db, row); err != nil {
		return fmt.Errorf("create connector grant: %w", err)
	}
	return nil
}

// validateGrant rejects grants that would be stored as unusable.
func validateGrant(g *Grant) error {
	switch {
	case g == nil:
		return errors.New("grant is required")
	case g.ID == "":
		return errors.New("grant id is required")
	case g.UserID == "":
		return errors.New("grant user id is required")
	case g.DaemonID == "":
		// A grant must name exactly one daemon. There is no "all daemons"
		// form, by design.
		return errors.New("grant must be bound to a daemon")
	case len(g.AllowedTools) == 0:
		return errors.New("grant must allow at least one tool")
	case g.PathRoot == "":
		return errors.New("grant must specify a path root")
	}

	switch g.ExecMode {
	case ExecDeny, ExecUnrestricted:
	case ExecAllowlist:
		if len(g.ExecAllowlist) == 0 {
			return errors.New("exec allowlist mode requires at least one allowed command")
		}
	case "":
		// Default to the safest mode rather than rejecting, so a caller that
		// simply did not think about exec gets no exec.
		g.ExecMode = ExecDeny
	default:
		return fmt.Errorf("unknown exec mode %q", g.ExecMode)
	}

	return nil
}

// GetGrant fetches one of the user's grants, including revoked ones so the UI
// can show history.
func (s *SQLStore) GetGrant(ctx context.Context, userID, id string) (*Grant, error) {
	row, err := store.GetConnectorGrantByID(ctx, s.db, id)
	if errors.Is(err, svcerr.ErrNotFound) || (err == nil && row.UserID != userID) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get connector grant: %w", err)
	}
	return grantFromRow(row)
}

// GetGrantByID returns a live grant by id, with the same liveness filtering as
// credential lookup so a revoked or expired grant cannot be re-resolved.
func (s *SQLStore) GetGrantByID(ctx context.Context, id string) (*Grant, error) {
	row, err := store.GetLiveConnectorGrant(ctx, s.db, id)
	if errors.Is(err, orm.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get connector grant by id: %w", err)
	}
	return grantFromRow(row)
}

// ListGrantsByUser returns a user's grants, newest first.
func (s *SQLStore) ListGrantsByUser(ctx context.Context, userID string) ([]*Grant, error) {
	rows, err := store.ListConnectorGrant(ctx, s.db,
		orm.WhereEq(store.ConnectorGrantFieldUserID, userID),
		orm.WithOrderBy(store.ConnectorGrantFieldCreatedAt, orm.Desc))
	if err != nil {
		return nil, fmt.Errorf("list connector grants: %w", err)
	}
	var grants []*Grant
	for _, row := range rows {
		g, err := grantFromRow(row)
		if err != nil {
			return nil, err
		}
		grants = append(grants, g)
	}
	return grants, nil
}

// RevokeGrant marks a grant revoked. It reports whether a live grant was
// changed, so a repeated revoke is distinguishable from one that took effect.
// It is scoped by user so a caller can never revoke someone else's grant by
// guessing an id.
func (s *SQLStore) RevokeGrant(ctx context.Context, userID, id string) (bool, error) {
	revoked, err := store.RevokeConnectorGrant(ctx, s.db, userID, id)
	if err != nil {
		return false, fmt.Errorf("revoke connector grant: %w", err)
	}
	return revoked, nil
}

// TouchGrant stamps last use. Failures are the caller's to ignore: this is
// telemetry, and it must never fail a tool call.
func (s *SQLStore) TouchGrant(ctx context.Context, id string) error {
	if err := store.TouchConnectorGrant(ctx, s.db, id); err != nil {
		return fmt.Errorf("touch connector grant: %w", err)
	}
	return nil
}

// RecordAudit appends an audit row.
func (s *SQLStore) RecordAudit(ctx context.Context, rec *AuditRecord) error {
	if rec == nil || rec.ID == "" {
		return errors.New("audit record id is required")
	}

	// The column is NOT NULL, so an absent payload is stored as an empty
	// object rather than as SQL NULL.
	args := rec.Arguments
	if len(args) == 0 {
		args = []byte(`{}`)
	}

	createdAt := rec.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}

	status := rec.Status
	if status == "" {
		status = AuditCompleted
	}

	duration := int32(rec.DurationMS) //nolint:gosec // a tool call's duration in ms
	if err := store.CreateConnectorAuditLog(ctx, s.db, &store.ConnectorAuditLog{
		ID: rec.ID, GrantID: rec.GrantID, UserID: rec.UserID, DaemonID: rec.DaemonID,
		ToolName: rec.ToolName, CommandType: rec.CommandType, Arguments: args, Denied: rec.Denied,
		ErrorMessage: nullString(rec.ErrorMsg), DurationMs: &duration, Status: status, CreatedAt: createdAt,
	}); err != nil {
		return fmt.Errorf("record connector audit: %w", err)
	}
	return nil
}

// CompleteAudit resolves an intent row.
//
// Scoped to rows still in AuditStarted so a late or duplicated completion
// cannot overwrite an outcome already recorded.
func (s *SQLStore) CompleteAudit(ctx context.Context, id, status, errMsg string, durationMS int) error {
	if id == "" {
		return errors.New("audit record id is required")
	}

	duration := int32(durationMS) //nolint:gosec // a tool call's duration in ms
	if err := store.CompleteConnectorAudit(ctx, s.db, id, status, nullString(errMsg), duration); err != nil {
		return fmt.Errorf("complete connector audit: %w", err)
	}
	return nil
}

// ListAuditByUser returns a user's recent connector activity.
func (s *SQLStore) ListAuditByUser(ctx context.Context, userID string, limit int) ([]*AuditRecord, error) {
	return s.listAudit(ctx, limit, orm.WhereEq(store.ConnectorAuditLogFieldUserID, userID))
}

// ListAuditByGrant returns activity for one connector. It is scoped by user so
// a grant id alone does not expose another user's log.
func (s *SQLStore) ListAuditByGrant(ctx context.Context, userID, grantID string, limit int) ([]*AuditRecord, error) {
	return s.listAudit(ctx, limit,
		orm.WhereEq(store.ConnectorAuditLogFieldUserID, userID),
		orm.WhereEq(store.ConnectorAuditLogFieldGrantID, grantID))
}

func (s *SQLStore) listAudit(ctx context.Context, limit int, filters ...orm.QueryOption) ([]*AuditRecord, error) {
	opts := append(filters,
		orm.WithOrderBy(store.ConnectorAuditLogFieldCreatedAt, orm.Desc),
		orm.WithLimit(clampLimit(limit)))
	rows, err := store.ListConnectorAuditLog(ctx, s.db, opts...)
	if err != nil {
		return nil, fmt.Errorf("list connector audit: %w", err)
	}
	var out []*AuditRecord
	for _, row := range rows {
		rec := &AuditRecord{
			ID: row.ID, GrantID: row.GrantID, UserID: row.UserID, DaemonID: row.DaemonID,
			ToolName: row.ToolName, CommandType: row.CommandType, Arguments: row.Arguments,
			Denied: row.Denied, Status: row.Status, CreatedAt: row.CreatedAt,
		}
		if row.ErrorMessage != nil {
			rec.ErrorMsg = *row.ErrorMessage
		}
		if row.DurationMs != nil {
			rec.DurationMS = int(*row.DurationMs)
		}
		out = append(out, rec)
	}
	return out, nil
}

func grantFromRow(row *store.ConnectorGrant) (*Grant, error) {
	g := &Grant{
		ID: row.ID, UserID: row.UserID, DaemonID: row.DaemonID, Name: row.Name, TokenPrefix: row.TokenPrefix,
		PathRoot: row.PathRoot, ExecMode: ExecMode(row.ExecMode),
		ExpiresAt: row.ExpiresAt, LastUsedAt: row.LastUsedAt, RevokedAt: row.RevokedAt,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if err := json.Unmarshal(row.AllowedTools, &g.AllowedTools); err != nil {
		return nil, fmt.Errorf("decode allowed tools for grant %s: %w", g.ID, err)
	}
	if len(row.ExecAllowlist) > 0 {
		if err := json.Unmarshal(row.ExecAllowlist, &g.ExecAllowlist); err != nil {
			return nil, fmt.Errorf("decode exec allowlist for grant %s: %w", g.ID, err)
		}
	}
	return g, nil
}

func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func orEmpty(items []string) []string {
	if items == nil {
		return []string{}
	}
	return items
}

// clampLimit keeps an unbounded or hostile limit from reading the whole table.
func clampLimit(limit int) int {
	const (
		defaultLimit = 100
		maxLimit     = 1000
	)
	if limit <= 0 {
		return defaultLimit
	}
	if limit > maxLimit {
		return maxLimit
	}
	return limit
}

// SetTokenPrefix records the display prefix of a grant's credential once it
// is minted (the grant row must exist first: the credential is bound to it).
func (s *SQLStore) SetTokenPrefix(ctx context.Context, id, prefix string) error {
	if err := store.SetConnectorGrantTokenPrefix(ctx, s.db, id, prefix); err != nil {
		return fmt.Errorf("record connector credential prefix: %w", err)
	}
	return nil
}
