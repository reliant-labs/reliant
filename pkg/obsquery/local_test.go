package obsquery

import (
	"strings"
	"testing"
	"time"
)

// localScope is a scope over a project's own ClickStack.
func localScope() Scope {
	return Scope{Database: LocalDatabase, Local: true, Start: t0, End: t0.Add(time.Hour)}
}

// A local scope compiles every family against `default`, the database the
// ClickStack exporter writes to.
func TestLocalScopeCompilesAgainstDefault(t *testing.T) {
	stmts := map[string]func() (Stmt, error){
		"logs":       LogQuery{Scope: localScope(), Query: "service:api", Limit: 10}.Compile,
		"traces":     TraceQuery{Scope: localScope(), Limit: 10}.Compile,
		"flamegraph": Flamegraph{Scope: localScope(), Service: "api", SampleType: "cpu"}.Compile,
		"sql":        SQLPanel{Scope: localScope(), SQL: "SELECT count() FROM otel_logs", StepSeconds: 30}.Compile,
	}
	for name, compile := range stmts {
		st, err := compile()
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if name != "sql" && !strings.Contains(st.SQL, LocalDatabase+".") {
			t.Errorf("%s: SQL does not name %s.*:\n%s", name, LocalDatabase, st.SQL)
		}
		if strings.Contains(st.SQL, "FORMAT") {
			t.Errorf("%s: client-side FORMAT left in:\n%s", name, st.SQL)
		}
	}
}

// Only a Local scope may name `default`: in the control plane it is the
// server's own database, never an org's.
func TestDefaultDatabaseRefusedOffALocalScope(t *testing.T) {
	sc := localScope()
	sc.Local = false
	stmts := map[string]func() (Stmt, error){
		"logs":       LogQuery{Scope: sc, Limit: 10}.Compile,
		"traces":     TraceQuery{Scope: sc, Limit: 10}.Compile,
		"flamegraph": Flamegraph{Scope: sc, Service: "api", SampleType: "cpu"}.Compile,
		"sql":        SQLPanel{Scope: sc, SQL: "SELECT 1", StepSeconds: 30}.Compile,
	}
	for name, compile := range stmts {
		if _, err := compile(); !isInvalid(err) {
			t.Errorf("%s: database %q on a non-local scope: err = %v, want invalid", name, LocalDatabase, err)
		}
	}
}
