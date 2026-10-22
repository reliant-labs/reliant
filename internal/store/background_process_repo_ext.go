// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for BackgroundProcess. The generated background_process_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: backgroundProcessRepo (a *crud.Repo[BackgroundProcess]) and the
//     package-level GetBackgroundProcessByID / ListBackgroundProcess / CreateBackgroundProcess / UpdateBackgroundProcess / DeleteBackgroundProcess
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     BackgroundProcess struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindBackgroundProcessBy(ctx context.Context, db orm.Context, /* args */) ([]*BackgroundProcess, error) {
//		var rows []*BackgroundProcess
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("pid = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveBackgroundProcesses(ctx context.Context, db orm.Context) ([]*BackgroundProcess, error) {
//		return ListBackgroundProcess(ctx, db, orm.WhereEq("pid", true))
//	}
package store

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/uptrace/bun"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// InsertBackgroundProcess stores a process record with the timestamps the
// caller set.
func InsertBackgroundProcess(ctx context.Context, db orm.Context, p *core.BackgroundProcess) error {
	return CreateBackgroundProcess(ctx, db, backgroundProcessFromCore(p))
}

// GetBackgroundProcess returns the process record, or "process not found".
func GetBackgroundProcess(ctx context.Context, db orm.Context, id string) (*core.BackgroundProcess, error) {
	row, err := GetBackgroundProcessByID(ctx, db, id)
	if errors.Is(err, orm.ErrNoRows) {
		return nil, svcerr.NotFound("process")
	}
	if err != nil {
		return nil, err
	}
	return backgroundProcessToCore(row), nil
}

// ListBackgroundProcesses returns the records the filters match, most
// recently started first. A zero limit is unbounded.
func ListBackgroundProcesses(ctx context.Context, db orm.Context, f core.BackgroundProcessFilters) ([]*core.BackgroundProcess, error) {
	var rows []*BackgroundProcess
	q := db.Bun().NewSelect().Model(&rows)
	if f.UserID != "" {
		q.Where("user_id = ?", f.UserID)
	}
	if f.WorktreeID != nil {
		q.Where("worktree_id = ?", *f.WorktreeID)
	}
	if f.ProjectID != nil {
		q.Where("project_id = ?", *f.ProjectID)
	}
	if f.ChatID != nil {
		q.Where("chat_id = ?", *f.ChatID)
	}
	if f.Status != nil {
		q.Where("status = ?", *f.Status)
	}
	if f.SourceType != nil {
		q.Where("source_type = ?", *f.SourceType)
	}
	q.OrderExpr("started_at DESC")
	if f.Limit > 0 {
		q.Limit(int64(f.Limit))
	}
	if f.Offset > 0 {
		q.Offset(int64(f.Offset))
	}
	if err := q.Scan(ctx); err != nil {
		return nil, err
	}
	return backgroundProcessesToCore(rows), nil
}

// ListRunningBackgroundProcesses returns every record still RUNNING.
// Unbounded.
func ListRunningBackgroundProcesses(ctx context.Context, db orm.Context) ([]*core.BackgroundProcess, error) {
	var rows []*BackgroundProcess
	err := db.Bun().NewSelect().Model(&rows).
		Where("status = ?", reliantv1.BackgroundProcessStatus_BACKGROUND_PROCESS_STATUS_RUNNING).
		Scan(ctx)
	if err != nil {
		return nil, err
	}
	return backgroundProcessesToCore(rows), nil
}

// SetBackgroundProcessStatus records a process's status, exit code and end
// time. A missing record is not an error.
func SetBackgroundProcessStatus(ctx context.Context, db orm.Context, id string, status reliantv1.BackgroundProcessStatus, exitCode *int, endedAt *time.Time) error {
	_, err := db.Bun().NewUpdate().Model((*BackgroundProcess)(nil)).
		Set("status = ?", status).
		Set("exit_code = ?", exitCode).
		Set("ended_at = ?", endedAt).
		Set("updated_at = ?", time.Now()).
		Where("id = ?", id).
		Exec(ctx)
	return err
}

// SetBackgroundProcessPID records the OS pid and process signature of a
// started process. A missing record is not an error.
func SetBackgroundProcessPID(ctx context.Context, db orm.Context, id string, pid int, signature string) error {
	_, err := db.Bun().NewUpdate().Model((*BackgroundProcess)(nil)).
		Set("pid = ?", pid).
		Set("signature = ?", signature).
		Set("updated_at = ?", time.Now()).
		Where("id = ?", id).
		Exec(ctx)
	return err
}

// MarkStaleBackgroundProcesses marks the processes STALE, ended now.
func MarkStaleBackgroundProcesses(ctx context.Context, db orm.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	now := time.Now()
	_, err := db.Bun().NewUpdate().Model((*BackgroundProcess)(nil)).
		Set("status = ?", reliantv1.BackgroundProcessStatus_BACKGROUND_PROCESS_STATUS_STALE).
		Set("ended_at = ?", now).
		Set("updated_at = ?", now).
		Where("id IN (?)", bun.List(ids)).
		Exec(ctx)
	return err
}

// DeleteEndedBackgroundProcesses deletes the records of processes that ended
// (completed, failed, killed, killed externally or stale) before olderThan,
// and returns how many.
func DeleteEndedBackgroundProcesses(ctx context.Context, db orm.Context, olderThan time.Time) (int64, error) {
	res, err := db.Bun().NewDelete().Model((*BackgroundProcess)(nil)).
		Where("status IN (?)", bun.List([]reliantv1.BackgroundProcessStatus{
			reliantv1.BackgroundProcessStatus_BACKGROUND_PROCESS_STATUS_COMPLETED,
			reliantv1.BackgroundProcessStatus_BACKGROUND_PROCESS_STATUS_FAILED,
			reliantv1.BackgroundProcessStatus_BACKGROUND_PROCESS_STATUS_KILLED,
			reliantv1.BackgroundProcessStatus_BACKGROUND_PROCESS_STATUS_KILLED_EXTERNALLY,
			reliantv1.BackgroundProcessStatus_BACKGROUND_PROCESS_STATUS_STALE,
		})).
		Where("ended_at < ?", olderThan).
		Exec(ctx)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func backgroundProcessFromCore(p *core.BackgroundProcess) *BackgroundProcess {
	status := p.Status
	row := &BackgroundProcess{
		ID:          p.ID,
		Command:     p.Command,
		WorkingDir:  p.WorkingDir,
		WorktreeID:  p.WorktreeID,
		ProjectID:   p.ProjectID,
		ChatID:      p.ChatID,
		UserID:      p.UserID,
		Status:      &status,
		StartedAt:   p.StartedAt,
		EndedAt:     p.EndedAt,
		Signature:   p.Signature,
		SourceType:  p.SourceType,
		CommandName: p.CommandName,
		CreatedAt:   p.CreatedAt,
		UpdatedAt:   p.UpdatedAt,
	}
	if p.PID != nil {
		v := int64(*p.PID)
		row.Pid = &v
	}
	if p.ExitCode != nil {
		v := int64(*p.ExitCode)
		row.ExitCode = &v
	}
	// core carries package_type (an integer column) as its decimal text.
	if p.PackageType != nil {
		if v, err := strconv.ParseInt(*p.PackageType, 10, 32); err == nil {
			pt := int32(v)
			row.PackageType = &pt
		}
	}
	return row
}

func backgroundProcessToCore(row *BackgroundProcess) *core.BackgroundProcess {
	p := &core.BackgroundProcess{
		ID:          row.ID,
		Command:     row.Command,
		WorkingDir:  row.WorkingDir,
		WorktreeID:  row.WorktreeID,
		ProjectID:   row.ProjectID,
		ChatID:      row.ChatID,
		UserID:      row.UserID,
		StartedAt:   row.StartedAt,
		EndedAt:     row.EndedAt,
		Signature:   row.Signature,
		SourceType:  row.SourceType,
		CommandName: row.CommandName,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
	if row.Status != nil {
		p.Status = *row.Status
	}
	if row.Pid != nil {
		v := int(*row.Pid)
		p.PID = &v
	}
	if row.ExitCode != nil {
		v := int(*row.ExitCode)
		p.ExitCode = &v
	}
	if row.PackageType != nil {
		s := strconv.Itoa(int(*row.PackageType))
		p.PackageType = &s
	}
	return p
}

func backgroundProcessesToCore(rows []*BackgroundProcess) []*core.BackgroundProcess {
	out := make([]*core.BackgroundProcess, len(rows))
	for i, row := range rows {
		out[i] = backgroundProcessToCore(row)
	}
	return out
}

// BackgroundProcessExtra carries fields on BackgroundProcess that are NOT columns — computed
// values, transient state, anything derived rather than stored. BackgroundProcess is
// embedded on BackgroundProcessExtra (see background_process_orm.go), so a field added here is a field on
// every BackgroundProcess value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type BackgroundProcessExtra struct {
}
