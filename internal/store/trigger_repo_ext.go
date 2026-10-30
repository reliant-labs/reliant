// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for Trigger. The generated trigger_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: triggerRepo (a *crud.Repo[Trigger]) and the
//     package-level GetTriggerByID / ListTrigger / CreateTrigger / UpdateTrigger / DeleteTrigger
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     Trigger struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindTriggerBy(ctx context.Context, db orm.Context, /* args */) ([]*Trigger, error) {
//		var rows []*Trigger
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("user_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveTriggers(ctx context.Context, db orm.Context) ([]*Trigger, error) {
//		return ListTrigger(ctx, db, orm.WhereEq("user_id", true))
//	}
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/crud"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// InsertTrigger stores a new trigger definition. The caller's CreatedAt and
// UpdatedAt are the row's (Create stamps only a timestamp left unset).
func InsertTrigger(ctx context.Context, db orm.Context, t *core.Trigger) error {
	row, err := triggerFromCore(t)
	if err != nil {
		return err
	}
	return CreateTrigger(ctx, db, row)
}

// namedTrigger is a triggers row with the display names its reads join in.
type namedTrigger struct {
	Trigger
	ProjectName  string `bun:"project_name"`
	DaemonName   string `bun:"daemon_name"`
	MachineState string `bun:"machine_state"`
	PoolName     string `bun:"machine_pool_name"`
}

func (n *namedTrigger) toCore() (*core.Trigger, error) {
	t, err := triggerToCore(&n.Trigger)
	if err != nil {
		return nil, err
	}
	t.ProjectName, t.DaemonName, t.MachineState = n.ProjectName, n.DaemonName, n.MachineState
	t.MachinePoolName = n.PoolName
	return t, nil
}

// namedTriggerSQL joins the display names so a trigger can be shown without a
// second lookup. LEFT JOINs: a daemon id is not a foreign key, and an absent
// name must not hide the trigger. GetTrigger and ListTriggersForUser add
// their WHERE and ORDER BY.
var namedTriggerSQL = `SELECT ` + columnsOf[Trigger]("t") + `,
	COALESCE(p.name, '')::text AS project_name,
	COALESCE(NULLIF(d.name, ''), d.hostname, '')::text AS daemon_name,
	COALESCE(mp.name, '')::text AS machine_pool_name,
	CASE WHEN t.no_machine THEN 'none'
	     WHEN t.machine_pool_id IS NOT NULL THEN 'pool'
	     WHEN d.id IS NULL THEN 'removed'
	     WHEN EXISTS (SELECT 1 FROM daemon_attachment a WHERE a.daemon_id = d.id
	       AND a.user_id = t.user_id AND a.last_stream_activity > now() - interval '90 seconds') THEN 'connected'
	     WHEN d.lifecycle_phase IN ('provisioning', 'cloning') THEN 'starting'
	     WHEN d.lifecycle_phase IN ('suspending', 'suspended') THEN 'suspended'
	     WHEN d.lifecycle_phase = 'failed' THEN 'failed'
	     ELSE 'disconnected' END::text AS machine_state
FROM triggers t
LEFT JOIN projects p ON p.id = t.project_id
LEFT JOIN daemons d ON d.id = t.daemon_id AND d.user_id = t.user_id
LEFT JOIN machine_pools mp ON mp.id = t.machine_pool_id
`

// GetTrigger returns the trigger with its project and daemon display names,
// or core.ErrTriggerNotFound.
func GetTrigger(ctx context.Context, db orm.Context, id string) (*core.Trigger, error) {
	var row namedTrigger
	if err := db.Bun().NewRaw(namedTriggerSQL+`WHERE t.id = ?0`, id).Scan(ctx, &row); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, core.ErrTriggerNotFound
		}
		return nil, fmt.Errorf("failed to get trigger: %w", err)
	}
	return row.toCore()
}

// ListTriggersForUser lists one user's triggers, newest first, with their
// display names; f.ProjectID narrows to one project. It is always scoped to
// one user: the unscoped listing is ListAllTriggers, a separate query so "no
// user" can never be reached by passing an empty string.
func ListTriggersForUser(ctx context.Context, db orm.Context, f core.TriggerFilters) ([]*core.Trigger, error) {
	var rows []namedTrigger
	err := db.Bun().NewRaw(namedTriggerSQL+`WHERE t.user_id = ?0
	AND (?1::text IS NULL OR t.project_id = ?1::text)
ORDER BY t.created_at DESC, t.id`, f.UserID, f.ProjectID).Scan(ctx, &rows)
	if err != nil {
		return nil, fmt.Errorf("failed to list triggers: %w", err)
	}
	out := make([]*core.Trigger, 0, len(rows))
	for i := range rows {
		t, err := rows[i].toCore()
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// ListAllTriggers lists every user's triggers, newest first. Only the
// schedule syncer's reconciliation calls it.
func ListAllTriggers(ctx context.Context, db orm.Context) ([]*core.Trigger, error) {
	rows, err := ListTrigger(ctx, db,
		orm.WithOrderBy(TriggerFieldCreatedAt, orm.Desc),
		orm.WithOrderBy(TriggerFieldID, orm.Asc))
	if err != nil {
		return nil, fmt.Errorf("failed to list all triggers: %w", err)
	}
	return triggersToCore(rows)
}

// LockTrigger row-locks the trigger for the rest of the surrounding
// transaction, serializing the overlap check with the launch it guards across
// concurrent fires. A missing trigger is core.ErrTriggerNotFound.
func LockTrigger(ctx context.Context, db orm.Context, id string) error {
	var locked string
	if err := db.Bun().NewRaw(`SELECT id FROM triggers WHERE id = ?0 FOR UPDATE`, id).Scan(ctx, &locked); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.ErrTriggerNotFound
		}
		return fmt.Errorf("failed to lock trigger: %w", err)
	}
	return nil
}

// triggerDefinitionColumns are what ReplaceTrigger writes; updated_at is
// stamped with them. Identity columns (id, user_id, kind) are not updatable:
// changing the owner would silently re-point which identity the run executes
// as, and changing the kind would leave Config describing a source that no
// longer applies. The webhook credentials have their own write paths, so no
// definition update can touch them.
var triggerDefinitionColumns = []string{
	TriggerFieldProjectID, TriggerFieldWorktreeID, TriggerFieldName, TriggerFieldEnabled,
	TriggerFieldWorkflow, TriggerFieldPresets, TriggerFieldParams, TriggerFieldMessage,
	TriggerFieldConfig, TriggerFieldDaemonID, TriggerFieldNotifyOnComplete,
	TriggerFieldFilter, TriggerFieldConnectionID, TriggerFieldWorkflowTrigger, TriggerFieldNoMachine,
	TriggerFieldMachinePoolID, TriggerFieldActingIdentity,
}

// ReplaceTrigger rewrites a trigger's definition and stamps updated_at, or
// returns core.ErrTriggerNotFound.
func ReplaceTrigger(ctx context.Context, db orm.Context, t *core.Trigger) error {
	row, err := triggerFromCore(t)
	if err != nil {
		return err
	}
	return writeTrigger(ctx, db, row, "definition", triggerDefinitionColumns...)
}

// RemoveTrigger deletes a trigger. Deleting one that does not exist is not an
// error.
func RemoveTrigger(ctx context.Context, db orm.Context, id string) error {
	if err := DeleteTrigger(ctx, db, id); err != nil && !errors.Is(err, svcerr.ErrNotFound) {
		return err
	}
	return nil
}

// SetTriggerEnabled turns a trigger on or off, or returns
// core.ErrTriggerNotFound.
func SetTriggerEnabled(ctx context.Context, db orm.Context, id string, enabled bool) error {
	return writeTrigger(ctx, db, &Trigger{ID: id, Enabled: &enabled}, "enabled", TriggerFieldEnabled)
}

// SetTriggerWebhookTokenHash rotates a webhook trigger's token hash. The hash
// has its own write path: no definition update can touch it, and rotating it
// is one statement. A missing trigger is core.ErrTriggerNotFound.
func SetTriggerWebhookTokenHash(ctx context.Context, db orm.Context, id string, hash []byte) error {
	return writeTrigger(ctx, db, &Trigger{ID: id, WebhookTokenHash: hash}, "webhook token", TriggerFieldWebhookTokenHash)
}

// SetTriggerWebhookSecret stores the HMAC secret, sealed by the caller under
// the owner's tenant; nil clears it. A missing trigger is
// core.ErrTriggerNotFound.
func SetTriggerWebhookSecret(ctx context.Context, db orm.Context, id string, sealed []byte) error {
	return writeTrigger(ctx, db, &Trigger{ID: id, WebhookSecretSealed: sealed}, "webhook secret", TriggerFieldWebhookSecretSealed)
}

// writeTrigger writes the named columns of a trigger and stamps updated_at.
func writeTrigger(ctx context.Context, db orm.Context, row *Trigger, what string, fields ...string) error {
	err := UpdateTriggerMasked(ctx, db, row, fields, crud.NoReadBack())
	if errors.Is(err, svcerr.ErrNotFound) {
		return core.ErrTriggerNotFound
	}
	if err != nil {
		return fmt.Errorf("failed to update trigger %s: %w", what, err)
	}
	return nil
}

// GetTriggerWebhookCredentials returns a webhook trigger's token hash and
// sealed secret, or core.ErrTriggerNotFound. Only the webhook receiver reads
// these; they are never rendered.
func GetTriggerWebhookCredentials(ctx context.Context, db orm.Context, id string) (*core.TriggerWebhookCredentials, error) {
	row, err := GetTriggerByID(ctx, db, id)
	if errors.Is(err, svcerr.ErrNotFound) {
		return nil, core.ErrTriggerNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get webhook credentials: %w", err)
	}
	return &core.TriggerWebhookCredentials{TokenHash: row.WebhookTokenHash, SecretSealed: row.WebhookSecretSealed}, nil
}

// SetTriggerProjection refreshes an activation's projection of its workflow's
// declaration: the config and filter routing and the schedule syncer read.
// The kind is not here: an activation whose declaration changed kind is
// broken (its connection or webhook token belongs to the old kind), never
// re-projected. An id that is not an activation is core.ErrTriggerNotFound.
func SetTriggerProjection(ctx context.Context, db orm.Context, id string, p core.TriggerProjection) error {
	res, err := db.Bun().NewUpdate().Model((*Trigger)(nil)).
		Set("config = ?", string(triggerConfigJSON(p.Config))).
		Set("filter = ?", p.Filter).
		Set("updated_at = NOW()").
		Where("id = ?", id).
		Where("workflow_trigger IS NOT NULL").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to set trigger projection: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return core.ErrTriggerNotFound
	}
	return nil
}

// ListAllWorkflowTriggerActivations lists every activation of a declared
// trigger, for the periodic reconcile.
func ListAllWorkflowTriggerActivations(ctx context.Context, db orm.Context) ([]*core.Trigger, error) {
	rows, err := ListTrigger(ctx, db,
		orm.WhereIsNotNull(TriggerFieldWorkflowTrigger),
		orm.WithOrderBy(TriggerFieldID, orm.Asc))
	if err != nil {
		return nil, fmt.Errorf("failed to list workflow trigger activations: %w", err)
	}
	return triggersToCore(rows)
}

// The two routing reads below spell `t.kind = 'integration' AND t.enabled`
// into the SQL rather than binding it: it is idx_triggers_integration's
// partial-index predicate, which the planner must see at plan time.

// listIntegrationTriggersSQL is every ENABLED integration trigger for one
// integration, with its connection's routing identity. The inner join drops
// triggers whose connection is gone, revoked or belongs to someone else: an
// event can only reach a trigger through its own owner's live connection.
// Status is returned rather than filtered so a caller can tell needs_reauth
// apart.
var listIntegrationTriggersSQL = `SELECT ` + columnsOf[Trigger]("t") + `,
	c.external_account_id AS connection_account,
	c.status AS connection_status
FROM triggers t
JOIN connections c
	ON c.id = t.connection_id
	AND c.user_id = t.user_id
	AND c.owner_kind = 'user'
	AND c.deleted_at IS NULL
WHERE t.kind = 'integration'
	AND t.enabled
	AND t.config ->> 'integration' = ?0::text
ORDER BY t.id`

// ListIntegrationTriggers lists the routes an inbound event of one
// integration may take.
func ListIntegrationTriggers(ctx context.Context, db orm.Context, integration string) ([]*core.IntegrationTriggerRoute, error) {
	var rows []struct {
		Trigger
		ConnectionAccount *string `bun:"connection_account"`
		ConnectionStatus  string  `bun:"connection_status"`
	}
	if err := db.Bun().NewRaw(listIntegrationTriggersSQL, integration).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("failed to list integration triggers: %w", err)
	}
	out := make([]*core.IntegrationTriggerRoute, 0, len(rows))
	for i := range rows {
		t, err := triggerToCore(&rows[i].Trigger)
		if err != nil {
			return nil, err
		}
		route := &core.IntegrationTriggerRoute{Trigger: t, ConnectionStatus: rows[i].ConnectionStatus}
		if rows[i].ConnectionAccount != nil {
			route.ConnectionAccount = *rows[i].ConnectionAccount
		}
		out = append(out, route)
	}
	return out, nil
}

// listAccessRoutedTriggersSQL is every enabled integration trigger of one
// integration whose OWNER can see the event's (account, resource) per a
// snapshot refreshed at or after ?3. A stale snapshot routes nothing: access
// is only trusted while recently confirmed. A trigger that names a connection
// must name its own owner's live one; one with no connection (a delegated
// authority, e.g. GitHub via control-plane) is routed on access alone. The
// model is in 20261005061008_integration_event_access.sql.
var listAccessRoutedTriggersSQL = `SELECT ` + columnsOf[Trigger]("t") + `
FROM triggers t
JOIN integration_event_access a
	ON a.user_id = t.user_id
	AND a.integration_id = ?0::text
	AND a.account_key = ?1::text
	AND a.resource_key = ?2::text
	AND a.refreshed_at >= ?3::timestamptz
LEFT JOIN connections c
	ON c.id = t.connection_id
WHERE t.kind = 'integration'
	AND t.enabled
	AND t.config ->> 'integration' = ?0::text
	AND (
		t.connection_id IS NULL
		OR (c.id IS NOT NULL AND c.user_id = t.user_id AND c.owner_kind = 'user'
			AND c.deleted_at IS NULL AND c.status = 'active')
	)
ORDER BY t.id`

// ListAccessRoutedTriggers lists the enabled integration triggers an event
// about (account, resource) reaches: those whose owner holds a grant for it
// refreshed at or after freshAfter, and whose named connection (if any) is
// the owner's and active.
func ListAccessRoutedTriggers(ctx context.Context, db orm.Context, integration, account, resource string, freshAfter time.Time) ([]*core.Trigger, error) {
	var rows []*Trigger
	if err := db.Bun().NewRaw(listAccessRoutedTriggersSQL, integration, account, resource, freshAfter.UTC()).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("list access-routed triggers: %w", err)
	}
	return triggersToCore(rows)
}

func triggerFromCore(t *core.Trigger) (*Trigger, error) {
	presets, err := triggerJSONObject(t.Presets)
	if err != nil {
		return nil, fmt.Errorf("marshal trigger presets: %w", err)
	}
	params, err := triggerJSONObject(t.Params)
	if err != nil {
		return nil, fmt.Errorf("marshal trigger params: %w", err)
	}
	enabled := t.Enabled
	row := &Trigger{
		ID:               t.ID,
		UserID:           t.UserID,
		ProjectID:        t.ProjectID,
		WorktreeID:       t.WorktreeID,
		Name:             t.Name,
		Kind:             t.Kind,
		Enabled:          &enabled,
		Workflow:         t.Workflow,
		Presets:          presets,
		Params:           params,
		Message:          t.Message,
		Config:           triggerConfigJSON(t.Config),
		CreatedAt:        t.CreatedAt,
		UpdatedAt:        t.UpdatedAt,
		NotifyOnComplete: t.NotifyOnComplete,
		Filter:           t.Filter,
		ConnectionID:     t.ConnectionID,
		WorkflowTrigger:  t.WorkflowTrigger,
		NoMachine:        t.NoMachine,
		ActingIdentity:   t.ActingIdentity.OrOwner(),
	}
	// A no-machine trigger's empty daemon is NULL, which is what
	// triggers_no_machine_names_no_daemon_check pairs with no_machine.
	if t.DaemonID != "" {
		daemonID := t.DaemonID
		row.DaemonID = &daemonID
	}
	if t.MachinePoolID != "" {
		poolID := t.MachinePoolID
		row.MachinePoolID = &poolID
	}
	return row, nil
}

func triggerToCore(row *Trigger) (*core.Trigger, error) {
	presets := map[string]string{}
	if err := triggerJSONInto(row.Presets, &presets); err != nil {
		return nil, fmt.Errorf("unmarshal trigger presets (trigger %s): %w", row.ID, err)
	}
	params := map[string]any{}
	if err := triggerJSONInto(row.Params, &params); err != nil {
		return nil, fmt.Errorf("unmarshal trigger params (trigger %s): %w", row.ID, err)
	}
	var config json.RawMessage
	if len(row.Config) > 0 {
		config = append(json.RawMessage(nil), row.Config...)
	}
	t := &core.Trigger{
		ID:               row.ID,
		UserID:           row.UserID,
		ProjectID:        row.ProjectID,
		WorktreeID:       row.WorktreeID,
		Name:             row.Name,
		Kind:             row.Kind,
		Enabled:          row.Enabled != nil && *row.Enabled,
		Workflow:         row.Workflow,
		Presets:          presets,
		Params:           params,
		Message:          row.Message,
		Config:           config,
		Filter:           row.Filter,
		ConnectionID:     row.ConnectionID,
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
		NoMachine:        row.NoMachine,
		ActingIdentity:   row.ActingIdentity,
		NotifyOnComplete: row.NotifyOnComplete,
		WorkflowTrigger:  row.WorkflowTrigger,
	}
	if row.DaemonID != nil {
		t.DaemonID = *row.DaemonID
	}
	if row.MachinePoolID != nil {
		t.MachinePoolID = *row.MachinePoolID
	}
	return t, nil
}

func triggersToCore(rows []*Trigger) ([]*core.Trigger, error) {
	out := make([]*core.Trigger, 0, len(rows))
	for _, row := range rows {
		t, err := triggerToCore(row)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// triggerJSONObject always produces a jsonb OBJECT, never a JSON null: a nil
// Go map marshals to `null`, which reads back as a nil map and would force
// every consumer to nil-check a field the schema declares NOT NULL with a
// '{}' default.
func triggerJSONObject[V any](m map[string]V) (json.RawMessage, error) {
	if m == nil {
		return json.RawMessage(`{}`), nil
	}
	return json.Marshal(m)
}

// triggerConfigJSON passes a stored config through, substituting an empty
// object for an absent one so the NOT NULL column is always satisfied.
func triggerConfigJSON(config json.RawMessage) json.RawMessage {
	if len(config) == 0 {
		return json.RawMessage(`{}`)
	}
	return config
}

func triggerJSONInto(raw []byte, out any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// TriggerExtra carries fields on Trigger that are NOT columns — computed
// values, transient state, anything derived rather than stored. Trigger is
// embedded on TriggerExtra (see trigger_orm.go), so a field added here is a field on
// every Trigger value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type TriggerExtra struct {
}
