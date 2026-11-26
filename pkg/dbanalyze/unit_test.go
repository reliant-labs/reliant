package dbanalyze

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestConfigDefaultsAndHints(t *testing.T) {
	if got := (Config{}).largeTableRows(); got != DefaultLargeTableRows {
		t.Errorf("zero LargeTableRows = %d, want the default %d", got, DefaultLargeTableRows)
	}
	if got := (Config{LargeTableRows: -5}).largeTableRows(); got != DefaultLargeTableRows {
		t.Errorf("negative LargeTableRows = %d, want the default", got)
	}
	if got := (Config{LargeTableRows: 7}).largeTableRows(); got != 7 {
		t.Errorf("LargeTableRows = %d, want 7", got)
	}
	if got := (Config{}).smallTableRows(); got != DefaultSmallTableRows {
		t.Errorf("zero SmallTableRows = %d, want the default", got)
	}
	if got := (Config{SmallTableRows: -1}).smallTableRows(); got != -1 {
		t.Errorf("negative SmallTableRows keeps every finding, got %d", got)
	}

	cfg := Config{TableRows: map[string]int64{"orders": 5, "audit.log": 9, "public.users": 3}}
	for _, tc := range []struct {
		name string
		want int64
		ok   bool
	}{
		{"orders", 5, true},
		{"audit.log", 9, true},
		{"users", 3, true}, // a bare name also matches its public.-qualified hint
		{"log", 0, false},  // a bare name does not match another schema's table
		{"audit.other", 0, false},
		{"missing", 0, false},
	} {
		if v, ok := cfg.hint(tc.name); v != tc.want || ok != tc.ok {
			t.Errorf("hint(%q) = %d, %v; want %d, %v", tc.name, v, ok, tc.want, tc.ok)
		}
	}
}

func TestTableRowsPrefersStatisticsThenHintsThenUnknown(t *testing.T) {
	cfg := Config{TableRows: map[string]int64{"t": 42}}
	if rc := tableRows(TargetInfo{HasStatistics: true}, cfg, "t", 1000); rc.Source != RowsFromReltuples || rc.Value == nil || *rc.Value != 1000 {
		t.Errorf("with statistics: %+v", rc)
	}
	// reltuples is -1 for a table postgres has never analyzed: not a size.
	if rc := tableRows(TargetInfo{HasStatistics: true}, cfg, "t", -1); rc.Source != RowsFromHints || *rc.Value != 42 {
		t.Errorf("never analyzed falls back to the hint: %+v", rc)
	}
	if rc := tableRows(TargetInfo{}, cfg, "t", 1000); rc.Source != RowsFromHints || *rc.Value != 42 {
		t.Errorf("no statistics: %+v", rc)
	}
	if rc := tableRows(TargetInfo{}, cfg, "other", 1000); rc.Source != RowsUnknown || rc.Value != nil {
		t.Errorf("nothing known: %+v", rc)
	}
}

func TestStatementErrorIsRecognizedThroughWrapping(t *testing.T) {
	cause := errors.New("boom")
	se := &StatementError{File: "m.up.sql", Line: 3, SQLState: "42P01", Err: cause}
	if !errors.Is(se, cause) {
		t.Error("Unwrap must expose the statement's own error")
	}
	if msg := se.Error(); !strings.Contains(msg, "m.up.sql:3") || !strings.Contains(msg, "boom") {
		t.Errorf("message = %q", msg)
	}
	if !IsStatementError(fmt.Errorf("analysis: %w", se)) {
		t.Error("a wrapped StatementError is still one")
	}
	if IsStatementError(cause) || IsStatementError(nil) {
		t.Error("an ordinary error is not a StatementError")
	}
}

func TestRuleIDsListEveryRuleOnce(t *testing.T) {
	mig, qry, all := MigrationRuleIDs(), QueryRuleIDs(), RuleIDs()
	if !slices.Equal(all, append(slices.Clone(mig), qry...)) {
		t.Errorf("RuleIDs = %v, want migration rules %v then query rules %v", all, mig, qry)
	}
	seen := map[string]bool{}
	for _, id := range all {
		if !strings.HasPrefix(id, "db-analyze-") || seen[id] {
			t.Errorf("rule id %q is duplicated or not a db-analyze rule", id)
		}
		seen[id] = true
	}
	for _, id := range []string{RuleTableRewrite, RuleAccessExclusiveLock, RuleValidationScan, RuleBlockingIndexBuild, RuleStatementTimeout, RuleQueryNoUsableIndex, RuleQueryLargeSeqScan} {
		if !seen[id] {
			t.Errorf("rule %q is missing from RuleIDs", id)
		}
	}
	// The returned slices are the caller's: appending to RuleIDs must not alias the migration list.
	_ = append(MigrationRuleIDs(), "x")
	if len(MigrationRuleIDs()) != len(mig) {
		t.Error("MigrationRuleIDs is not stable across calls")
	}
}

func TestTransactionControlNamesEveryWayToLeaveTheFilesTransaction(t *testing.T) {
	for sql, want := range map[string]string{
		"BEGIN":                   "BEGIN",
		"start transaction":       "START",
		"COMMIT":                  "COMMIT",
		"end":                     "END",
		"ROLLBACK":                "ROLLBACK",
		"abort":                   "ABORT",
		"SAVEPOINT s":             "SAVEPOINT",
		"RELEASE s":               "RELEASE",
		"PREPARE TRANSACTION 'x'": "PREPARE TRANSACTION",
		"PREPARE p AS SELECT 1":   "",
		"PREPARE":                 "",
		"SELECT 1":                "",
		"":                        "",
	} {
		var st Statement
		if parts := SplitStatements(sql); len(parts) > 0 {
			st = parts[0]
		}
		if got := st.transactionControl(); got != want {
			t.Errorf("transactionControl(%q) = %q, want %q", sql, got, want)
		}
	}
}

func TestServerWideNamesWhatACopyCannotIsolate(t *testing.T) {
	for sql, want := range map[string]string{
		"CREATE ROLE r":                                      "a role",
		"ALTER USER u SET search_path = x":                   "a role",
		"DROP GROUP g":                                       "a role",
		"CREATE USER MAPPING FOR u SERVER s":                 "",
		"CREATE DATABASE d":                                  "a database",
		"drop database d":                                    "a database",
		"ALTER TABLESPACE ts RENAME TO x":                    "a tablespace",
		"CREATE SUBSCRIPTION s CONNECTION 'x' PUBLICATION p": "a subscription",
		"ALTER SYSTEM SET work_mem = '1MB'":                  "the server's configuration (ALTER SYSTEM)",
		"COMMENT ON DATABASE d IS 'x'":                       "a database, role or tablespace's label",
		"COMMENT ON ROLE r IS 'x'":                           "a database, role or tablespace's label",
		"COMMENT ON TABLE t IS 'x'":                          "",
		"SECURITY LABEL ON ROLE r IS 'x'":                    "a database, role or tablespace's label",
		"SECURITY LABEL ON TABLE t IS 'x'":                   "",
		"REASSIGN OWNED BY a TO b":                           "a role's ownership and privileges",
		"DROP OWNED BY a":                                    "a role's ownership and privileges",
		"GRANT admin TO bob":                                 "a role's membership",
		"REVOKE admin FROM bob":                              "a role's membership",
		"GRANT CONNECT ON DATABASE d TO bob":                 "a privilege on a database",
		"GRANT CREATE ON TABLESPACE ts TO bob":               "a privilege on a tablespace",
		"GRANT SET ON PARAMETER work_mem TO bob":             "a privilege on a parameter",
		"GRANT SELECT ON TABLE t TO bob":                     "",
		"ALTER TABLE t ADD COLUMN c int":                     "",
		"CREATE TABLE t (id int)":                            "",
	} {
		parts := SplitStatements(sql)
		if len(parts) != 1 {
			t.Fatalf("%q split into %d statements", sql, len(parts))
		}
		if got := parts[0].serverWide(); got != want {
			t.Errorf("serverWide(%q) = %q, want %q", sql, got, want)
		}
	}
}

func TestStatementHelpersOnAnEmptyStatement(t *testing.T) {
	var st Statement
	if st.first() != "" || st.has("select") || st.transactionControl() != "" || st.serverWide() != "" || st.NonTransactional() != "" {
		t.Errorf("an empty statement matches nothing: %+v", st)
	}
	if !SplitStatements("SELECT 1; SELECT 2")[1].has() {
		t.Error("has() with no words holds vacuously")
	}
}

func TestNonConcurrentlyDropsOnlyCreateIndexConcurrently(t *testing.T) {
	for sql, want := range map[string]string{
		"CREATE INDEX CONCURRENTLY i ON t (a)":        "CREATE INDEX i ON t (a)",
		"create unique index concurrently i on t (a)": "create unique index i on t (a)",
	} {
		got, ok := SplitStatements(sql)[0].NonConcurrently()
		if !ok || got != want {
			t.Errorf("NonConcurrently(%q) = %q, %v; want %q", sql, got, ok, want)
		}
	}
	if got, ok := SplitStatements("CREATE INDEX i ON t (a)")[0].NonConcurrently(); ok || got != "" {
		t.Errorf("a plain index is not rewritten: %q, %v", got, ok)
	}
	if _, ok := SplitStatements("DROP INDEX CONCURRENTLY i")[0].NonConcurrently(); ok {
		t.Error("only index builds are rewritten")
	}
}

func TestValidationFixPicksTheFixForTheKindOfCheck(t *testing.T) {
	for sql, want := range map[string]string{
		"ALTER TABLE t ALTER COLUMN a SET NOT NULL":           fixValidateNotNull,
		"ALTER TABLE t ADD FOREIGN KEY (a) REFERENCES p (id)": fixValidateFK,
		"ALTER TABLE t ADD CHECK (a > 0)":                     fixValidateCheck,
		"ALTER TABLE t ADD CONSTRAINT u UNIQUE (a)":           fixValidateCheck,
		"ALTER TABLE t VALIDATE CONSTRAINT":                   fixValidateCheck,
		"ALTER TABLE t ATTACH PARTITION q FOR VALUES IN (1)":  fixValidateGeneric,
	} {
		if got := validationFix(SplitStatements(sql)[0]); got != want {
			t.Errorf("validationFix(%q) picked the wrong fix:\n got %q\nwant %q", sql, got, want)
		}
	}
}

func TestMaintenanceFixAndStrongest(t *testing.T) {
	for sql, want := range map[string]string{
		"VACUUM FULL t":                  fixVacuumFull,
		"CLUSTER t":                      fixCluster,
		"REINDEX TABLE t":                fixReindex,
		"ALTER TABLE t ADD COLUMN c int": "",
	} {
		if got := maintenanceFix(SplitStatements(sql)[0]); got != want {
			t.Errorf("maintenanceFix(%q) = %q", sql, got)
		}
	}
	if got := strongest([]string{"AccessShareLock", "AccessExclusiveLock", "ShareLock"}); got != "AccessExclusiveLock" {
		t.Errorf("strongest = %q", got)
	}
	if got := strongest(nil); got != "" {
		t.Errorf("strongest(nil) = %q", got)
	}
}

func TestQueryStatementReadsPastCommentsAndParentheses(t *testing.T) {
	for _, tc := range []struct {
		sql  string
		kw   string
		want bool
	}{
		{"SELECT 1", "SELECT", true},
		{"  \n\t(select 1)", "SELECT", true},
		{"-- a comment\nWITH x AS (SELECT 1) SELECT * FROM x", "WITH", true},
		{"-- only a comment", "empty", false},
		{"/* block */ DELETE FROM t", "DELETE", true},
		{"/* never closed", "empty", false},
		{"EXPLAIN SELECT 1", "EXPLAIN", false},
		{"CREATE TABLE t (a int)", "CREATE", false},
		{"", "empty", false},
		{"   ", "empty", false},
		{"; SELECT 1", `";"`, false},
		{"'a'", `"'"`, false},
		{"VALUES (1)", "VALUES", true},
		{"table t", "TABLE", true},
		{"merge into t using s on true when matched then delete", "MERGE", true},
	} {
		kw, ok := queryStatement(tc.sql)
		if kw != tc.kw || ok != tc.want {
			t.Errorf("queryStatement(%q) = %q, %v; want %q, %v", tc.sql, kw, ok, tc.kw, tc.want)
		}
	}
}

func TestUnquoteNameFoldsLikePostgres(t *testing.T) {
	for in, want := range map[string]string{
		"Orders":        "orders",
		`"Orders"`:      "Orders",
		"public.t":      "t",
		`public."My T"`: "My T",
		"s.T":           "s.t",
		`"public"."t"`:  "t",
	} {
		if got := unquoteName(in); got != want {
			t.Errorf("unquoteName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStaticLocksNeedAnIndexTable(t *testing.T) {
	if got := SplitStatements("CREATE INDEX CONCURRENTLY")[0].staticLocks(); got != nil {
		t.Errorf("no table named: %+v, want none", got)
	}
}

func TestParsePlanRejectsWhatIsNotOnePlan(t *testing.T) {
	for _, tc := range []struct{ name, doc, want string }{
		{"not json", `{`, "read the plan"},
		{"two plans", `[{"Plan":{"Node Type":"Result"}},{"Plan":{"Node Type":"Result"}}]`, "2 plans, not one"},
		{"no plan", `[{}]`, "1 plans, not one"},
		{"empty", `[]`, "0 plans, not one"},
	} {
		if _, err := parsePlan([]byte(tc.doc)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want one mentioning %q", tc.name, err, tc.want)
		}
	}
}

func TestPlanNodeNaming(t *testing.T) {
	root, err := parsePlan([]byte(`[{"Plan":{"Node Type":"Seq Scan","Relation Name":"t","Schema":"audit","Alias":"t"}}]`))
	if err != nil {
		t.Fatal(err)
	}
	if got := root.relName(); got != "audit.t" {
		t.Errorf("a non-public schema qualifies the name, got %q", got)
	}
	root.Schema = "public"
	if got := root.relName(); got != "t" {
		t.Errorf("public is dropped, got %q", got)
	}
	root.Schema = ""
	if got := root.relName(); got != "t" {
		t.Errorf("no schema, got %q", got)
	}
}

func TestFeedsLimitInOrder(t *testing.T) {
	scanOf := func(t *testing.T, doc string) *rawPlan {
		t.Helper()
		root, err := parsePlan([]byte(doc))
		if err != nil {
			t.Fatal(err)
		}
		var found *rawPlan
		root.walk(func(n *rawPlan) {
			if n.Index != "" {
				found = n
			}
		})
		if found == nil {
			t.Fatal("no index scan in the plan")
		}
		return found
	}
	for _, tc := range []struct {
		name string
		doc  string
		want bool
	}{
		{"straight to a limit", `[{"Plan":{"Node Type":"Limit","Plans":[{"Node Type":"Index Scan","Index Name":"i"}]}}]`, true},
		{"through order-preserving nodes", `[{"Plan":{"Node Type":"Limit","Plans":[{"Node Type":"Unique","Plans":[{"Node Type":"Result","Plans":[{"Node Type":"Index Scan","Index Name":"i"}]}]}]}}]`, true},
		{"as the outer side of a nested loop", `[{"Plan":{"Node Type":"Limit","Plans":[{"Node Type":"Nested Loop","Plans":[{"Node Type":"Index Scan","Index Name":"i"},{"Node Type":"Seq Scan","Relation Name":"o"}]}]}}]`, true},
		{"as the inner side of a nested loop", `[{"Plan":{"Node Type":"Limit","Plans":[{"Node Type":"Nested Loop","Plans":[{"Node Type":"Seq Scan","Relation Name":"o"},{"Node Type":"Index Scan","Index Name":"i"}]}]}}]`, false},
		{"re-sorted before the limit", `[{"Plan":{"Node Type":"Limit","Plans":[{"Node Type":"Sort","Plans":[{"Node Type":"Index Scan","Index Name":"i"}]}]}}]`, false},
		{"hashed before the limit", `[{"Plan":{"Node Type":"Limit","Plans":[{"Node Type":"Hash Join","Plans":[{"Node Type":"Index Scan","Index Name":"i"}]}]}}]`, false},
		{"no limit above", `[{"Plan":{"Node Type":"Result","Plans":[{"Node Type":"Index Scan","Index Name":"i"}]}}]`, false},
		{"alone", `[{"Plan":{"Node Type":"Index Scan","Index Name":"i"}}]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := scanOf(t, tc.doc).feedsLimitInOrder(); got != tc.want {
				t.Errorf("feedsLimitInOrder = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestClassifyReportsACatalogFailure(t *testing.T) {
	root, err := parsePlan([]byte(`[{"Plan":{"Node Type":"Seq Scan","Relation Name":"t","Schema":"public","Alias":"t"}}]`))
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("catalog gone")
	_, err = classify(root, root, func(string, string) (*relInfo, error) { return nil, boom })
	if !errors.Is(err, boom) {
		t.Errorf("a lookup failure on the no-seqscan plan: %v", err)
	}

	// The failure on the ordinary plan's Seq Scans is reported too.
	noSeq, err := parsePlan([]byte(`[{"Plan":{"Node Type":"Result"}}]`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = classify(root, noSeq, func(string, string) (*relInfo, error) { return nil, boom })
	if !errors.Is(err, boom) {
		t.Errorf("a lookup failure on the ordinary plan: %v", err)
	}
}

func TestClassifyKeepsTheWorstVerdictPerTable(t *testing.T) {
	// One table scanned twice: once served by an index condition, once read through an unindexed filter.
	// The report carries the unserved read, because that is the one a reviewer must see.
	plan := []byte(`[{"Plan":{"Node Type":"Nested Loop","Plans":[
	  {"Node Type":"Index Scan","Relation Name":"t","Schema":"public","Alias":"a","Index Name":"t_pkey","Index Cond":"(a.id = 1)"},
	  {"Node Type":"Seq Scan","Relation Name":"t","Schema":"public","Alias":"b","Filter":"(b.x = 2)","Disabled":true}]}}]`)
	root, err := parsePlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	rels := map[string]*relInfo{"t": {table: "t", reltuples: 10, indexes: map[string]indexInfo{"t_pkey": {}}}}
	got, err := classify(root, root, func(_, name string) (*relInfo, error) { return rels[name], nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Verdict != AccessUnserved || got[0].How != UnservedFilter || got[0].reltuples != 10 {
		t.Fatalf("classified = %+v", got)
	}
}

func TestClassifyServesTIDScansAndPartialIndexes(t *testing.T) {
	tid := []byte(`[{"Plan":{"Node Type":"Tid Scan","Relation Name":"t","Schema":"public","Alias":"t","Filter":"(ctid = '(0,1)'::tid)"}}]`)
	root, err := parsePlan(tid)
	if err != nil {
		t.Fatal(err)
	}
	got, err := classify(root, root, func(_, name string) (*relInfo, error) {
		return &relInfo{table: name, indexes: map[string]indexInfo{}}, nil
	})
	if err != nil || len(got) != 1 || got[0].How != ServedByTID || got[0].Detail != "(ctid = '(0,1)'::tid)" {
		t.Errorf("tid scan: %+v, %v", got, err)
	}
	if rank(AccessUnfiltered) != 0 || rank(AccessServed) != 1 || rank(AccessUnserved) != 2 {
		t.Error("an unserved read must outrank a served one, which outranks an unfiltered one")
	}
}

func TestJoinColumnsAndPredicateHelpers(t *testing.T) {
	root, err := parsePlan([]byte(`[{"Plan":{"Node Type":"Hash Join","Hash Cond":"(o.customer_id = \"C\".\"Id\")","Plans":[
	  {"Node Type":"Seq Scan","Relation Name":"orders","Alias":"o"},
	  {"Node Type":"Seq Scan","Relation Name":"customers","Alias":"C"}]}}]`))
	if err != nil {
		t.Fatal(err)
	}
	aliases := map[string]bool{"o": true, "C": true}
	if got := joinColumns("o", root, aliases); !slices.Equal(got, []string{"customer_id"}) {
		t.Errorf("joinColumns(o) = %v", got)
	}
	if got := joinColumns("C", root, aliases); !slices.Equal(got, []string{"Id"}) {
		t.Errorf("joinColumns(C) = %v (a quoted column is unquoted)", got)
	}
	if got := joinColumns("", root, aliases); got != nil {
		t.Errorf("no alias, no join columns: %v", got)
	}
	if got := joinColumns("zzz", root, aliases); len(got) != 0 {
		t.Errorf("an alias the predicates never name: %v", got)
	}
	for in, want := range map[string]string{`"a""b"`: `a"b`, `plain`: `plain`, `"`: `"`, ``: ``} {
		if got := unquoteIdent(in); got != want {
			t.Errorf("unquoteIdent(%q) = %q, want %q", in, got, want)
		}
	}
	if firstNonEmpty("", "", "x", "y") != "x" || firstNonEmpty() != "" || firstNonEmpty("", "") != "" {
		t.Error("firstNonEmpty must return the first non-empty string")
	}
}

func TestConjunctsSplitsOnlyTopLevelAnds(t *testing.T) {
	for in, want := range map[string][]string{
		"(a = 1)":                     {"(a = 1)"},
		"((a = 1) AND (b = 2))":       {"(a = 1)", "(b = 2)"},
		"(a = 1) AND (b = (c AND d))": {"(a = 1)", "(b = (c AND d))"},
		"(s = 'x AND y') AND (t = 2)": {"(s = 'x AND y')", "(t = 2)"},
		`("a AND b" = 1) AND (t = 2)`: {`("a AND b" = 1)`, "(t = 2)"},
		"a = 1":                       {"a = 1"},
	} {
		if got := conjuncts(in); !slices.Equal(got, want) {
			t.Errorf("conjuncts(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]bool{"(a)": true, "(a) AND (b)": false, "a": false, "": false, "()": true, "((a)": false, "('(' )": true} {
		if got := enclosed(in); got != want {
			t.Errorf("enclosed(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestTimeoutSettingRendersPostgresLiterals(t *testing.T) {
	const def = 5_000_000_000 // 5s
	for _, tc := range []struct {
		d    int64
		want string
	}{
		{-1, "0"},
		{0, "'5000ms'"},
		{2_500_000_000, "'2500ms'"},
	} {
		if got := timeoutSetting(timeDuration(tc.d), timeDuration(def)); got != tc.want {
			t.Errorf("timeoutSetting(%d) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestWithDatabaseRewritesBothDSNForms(t *testing.T) {
	for _, tc := range []struct{ name, dsn string }{
		{"url", "postgres://u:p@h:5432/app?sslmode=disable"},
		{"url with the database as a parameter", "postgresql://u@h/app?dbname=app&sslmode=disable"},
		{"url with a database parameter", "postgres://u@h/app?database=app"},
		{"keyword/value", "host=h user=u dbname=app sslmode=disable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := withDatabase(tc.dsn, "forge_analyze_x")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(got, "forge_analyze_x") {
				t.Errorf("dsn %q does not name the copy", got)
			}
			if strings.HasPrefix(tc.dsn, "postgres") {
				u, err := url.Parse(got)
				if err != nil || u.Path != "/forge_analyze_x" || u.User.String() != map[bool]string{true: "u:p", false: "u"}[strings.Contains(tc.dsn, ":p@")] {
					t.Errorf("url = %q (path %q, user %q): the copy must replace the database and keep the user", got, u.Path, u.User)
				}
			}
		})
	}
	// A DSN pgx cannot parse is an error that does not echo the password.
	if _, err := withDatabase("postgres://u:hunter2@h:badport/app", "x"); err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("unparseable url: %v", err)
	}
	if _, err := withDatabase("host=h dbname=app port=notanumber", "x"); err == nil {
		t.Error("an unparseable keyword/value dsn must fail the check that pgx reads the copy's name")
	}
}

func TestParseCauseHidesTheConnectionString(t *testing.T) {
	_, err := url.Parse("postgres://u:hunter2@h:badport/app")
	if err == nil {
		t.Fatal("setup: want a url error")
	}
	if got := parseCause(err); strings.Contains(got.Error(), "hunter2") {
		t.Errorf("a *url.Error must be reduced to its cause: %v", got)
	}
	plain := errors.New("plain")
	if got := parseCause(plain); got != plain {
		t.Errorf("other errors pass through, got %v", got)
	}
}

func TestCloneNameIsUniqueAndPrefixed(t *testing.T) {
	a, err := cloneName()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := cloneName()
	if !strings.HasPrefix(a, "forge_analyze_") || len(a) != len("forge_analyze_")+16 || a == b {
		t.Errorf("names %q, %q", a, b)
	}
}

func timeDuration(ns int64) time.Duration { return time.Duration(ns) }
