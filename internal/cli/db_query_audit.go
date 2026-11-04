package cli

// The audit record of a query forge runs itself: the direct, port-forward and
// --addr transports. A hosted env's query is recorded by the control plane,
// which executes it (db_query_cloud.go); on every other transport no server
// of forge's sees the query, so forge records it where it runs:
//
//	.forge/audit/<env>.jsonl  one JSON line per attempt, appended, in the
//	                          project the query ran from
//	stderr                    one structured log line naming that record
//	application_name          the record's id on the connection, so the
//	                          database's own logs (pg_stat_activity, pgaudit,
//	                          Cloud SQL data-access logs) name it too
//
// The record is written for every attempt that reaches the database, the ones
// the guard or the read-only transaction refuse included, through dbquery's
// Auditor: a record that cannot be written withholds the rows.
//
// It is a record, not a control. Whoever holds the role's password can connect
// without forge, and --confirm-env / --reason are judged by this process
// alone. What bounds such a session is the role, which is why a real env
// refuses a role that can write (db_query.go, checkDBQueryRole).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"time"

	"github.com/reliant-labs/forge/pkg/dbquery"
)

// dbQueryAuditSchema names the record's shape.
const dbQueryAuditSchema = "forge.db.query.audit/v1"

// dbQueryAuditRecord is one line of .forge/audit/<env>.jsonl.
type dbQueryAuditRecord struct {
	Schema    string       `json:"schema"`
	ID        string       `json:"id"`
	At        time.Time    `json:"at"`
	Env       string       `json:"env"`
	Transport string       `json:"transport"`
	Workload  string       `json:"workload,omitempty"`
	Database  string       `json:"database,omitempty"`
	Policy    string       `json:"policy"`
	Who       dbQueryActor `json:"who"`
	Reason    string       `json:"reason,omitempty"`
	// Role is the Postgres role the statement ran as; RoleWrites what it
	// could do beyond reading, when forge asked (a real env).
	Role       string   `json:"role,omitempty"`
	RoleWrites []string `json:"role_writes,omitempty"`
	// AllowWritableRole is --allow-writable-role.
	AllowWritableRole bool   `json:"allow_writable_role,omitempty"`
	SQL               string `json:"sql"`
	SQLSHA256         string `json:"sql_sha256"`
	// Outcome is "ok" or the dbquery.Kind of the failure.
	Outcome    string `json:"outcome"`
	Error      string `json:"error,omitempty"`
	RowCount   int    `json:"row_count"`
	Bytes      int    `json:"bytes"`
	Truncated  bool   `json:"truncated,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

// dbQueryActor is who ran the query, as this machine knows it. KubeContext
// is the identity a port-forward authenticated with; the cluster's own audit
// log records the same call under it.
type dbQueryActor struct {
	User        string `json:"user,omitempty"`
	Host        string `json:"host,omitempty"`
	KubeContext string `json:"kube_context,omitempty"`
}

// newDBQueryAuditID is a record id: random, so two machines' records of one
// env never collide.
func newDBQueryAuditID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "dbq_" + hex.EncodeToString(b[:])
}

// dbQueryAuditPath is the env's record file in the project.
func dbQueryAuditPath(projectDir, env string) (string, error) {
	if env == "" || filepath.Base(env) != env || env == "." || env == ".." {
		return "", fmt.Errorf("env %q is not a file name", env)
	}
	return filepath.Join(projectDir, ".forge", "audit", env+".jsonl"), nil
}

func currentDBQueryActor(kubeContext string) dbQueryActor {
	a := dbQueryActor{KubeContext: kubeContext}
	if u, err := user.Current(); err == nil {
		a.User = u.Username
	}
	if h, err := os.Hostname(); err == nil {
		a.Host = h
	}
	return a
}

// dbQueryAuditor records each attempt to path and logs it to errOut. base is
// the record without the attempt's own facts, which Audit fills in.
type dbQueryAuditor struct {
	path string
	base dbQueryAuditRecord
	log  *slog.Logger
	// sqlSHA256 is the hash of the statement last recorded.
	sqlSHA256 string
}

func newDBQueryAuditor(path string, base dbQueryAuditRecord, errOut io.Writer) *dbQueryAuditor {
	base.Schema = dbQueryAuditSchema
	return &dbQueryAuditor{path: path, base: base, log: slog.New(slog.NewJSONHandler(errOut, nil))}
}

func (a *dbQueryAuditor) Audit(_ context.Context, e dbquery.Event) error {
	r := a.base
	r.At = e.StartedAt.UTC()
	r.SQL, r.SQLSHA256 = e.SQL, e.SQLSHA256
	r.Outcome = e.Outcome
	if e.Err != nil {
		r.Error = e.Err.Error()
	}
	r.RowCount, r.Bytes, r.Truncated = e.RowCount, e.Bytes, e.Truncated
	r.DurationMS = e.Duration.Milliseconds()
	if err := appendDBQueryAudit(a.path, r); err != nil {
		return err
	}
	a.sqlSHA256 = r.SQLSHA256
	// The statement itself stays in the record: the line points at it.
	a.log.Info("forge db query audited", "audit_id", r.ID, "env", r.Env, "transport", r.Transport,
		"database", r.Database, "role", r.Role, "outcome", r.Outcome, "row_count", r.RowCount,
		"sql_sha256", r.SQLSHA256, "record", a.path)
	return nil
}

// appendDBQueryAudit appends r to path as one line, durably.
func appendDBQueryAudit(path string, r dbQueryAuditRecord) error {
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("audit record: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("audit record: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("audit record %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("audit record %s: %w", path, err)
	}
	return f.Close()
}
