package dashspec

import (
	"errors"
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/alerting"
)

// MaxSQLBytes bounds a KindSQL panel's statement: an alert rule's expr bound,
// because the two run under the same guard.
const MaxSQLBytes = alerting.MaxExprBytes

// The query parameters a KindSQL panel's statement may use. They are
// ClickHouse's native parameters, written {name:Type} in the statement and
// bound by the server from the dashboard's scope: nothing a viewer picks is
// ever spliced into the SQL. A statement need not use any of them, and may use
// each any number of times.
//
//	SELECT toStartOfInterval(Timestamp, toIntervalSecond({step:UInt32})) AS t,
//	       count() AS requests
//	FROM otel_traces
//	WHERE Timestamp >= {from:DateTime64(9)} AND Timestamp < {to:DateTime64(9)}
//	  AND (empty({services:Array(String)}) OR has({services:Array(String)}, ServiceName))
//	GROUP BY t ORDER BY t
const (
	// SQLParamFrom is the start of the dashboard's time range, inclusive:
	// {from:DateTime64(9)}.
	SQLParamFrom = "from"
	// SQLParamTo is the end of the dashboard's time range, exclusive:
	// {to:DateTime64(9)}.
	SQLParamTo = "to"
	// SQLParamStep is a bucket width in seconds suited to the range, for a
	// time series: {step:UInt32}.
	SQLParamStep = "step"
	// SQLParamEnvIDs are the reliant.env.id values in scope; empty means every
	// environment: {env_ids:Array(String)}.
	SQLParamEnvIDs = "env_ids"
	// SQLParamEnvs are the deployment.environment.name values in scope, in the
	// order of SQLParamEnvIDs: {envs:Array(String)}.
	SQLParamEnvs = "envs"
	// SQLParamServices are the service.name values in scope; empty means
	// every service: {services:Array(String)}.
	SQLParamServices = "services"
)

// CheckSQL reports whether sql is a statement a KindSQL panel may carry:
// non-empty, at most MaxSQLBytes, and exactly one read-only SELECT by the alert
// rule guard (alerting.ValidateExpr), which refuses writes, DDL, SETTINGS,
// FORMAT, the system database and the table functions that read outside the
// owner's data.
//
// It is the first line of defence, not the containment: a reader must run the
// statement as a read-only database user confined to the viewer's own data,
// under a timeout and a row cap, exactly as it runs an alert rule.
func CheckSQL(sql string) error {
	if len(sql) > MaxSQLBytes {
		return fmt.Errorf("the statement is longer than %d bytes", MaxSQLBytes)
	}
	if err := alerting.ValidateExpr(sql); err != nil {
		// Drop the guard's "alerting: expr refused: " prefix: the problem is
		// reported on a panel's sql, not on an alert's expr.
		return errors.New(strings.TrimPrefix(err.Error(), alerting.ErrBadExpr.Error()+": "))
	}
	return nil
}
