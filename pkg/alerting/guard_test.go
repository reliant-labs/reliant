// Copyright (c) 2025 Reliant Labs

package alerting

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestValidateExprAccepts(t *testing.T) {
	for name, q := range map[string]string{
		"plain":            "SELECT service, count() AS value FROM otel_traces GROUP BY service HAVING value > 3",
		"lowercase":        "select 1 as value",
		"with":             "WITH x AS (SELECT 1 AS v) SELECT v AS value FROM x",
		"trailing ;":       "SELECT 1 AS value;",
		"comment":          "SELECT 1 AS value -- drop table x\n",
		"block comment":    "SELECT /* ; DROP TABLE x */ 1 AS value",
		"string keyword":   "SELECT count() AS value FROM t WHERE Body = 'drop table; insert into x'",
		"escaped quote":    `SELECT 1 AS value WHERE a = 'it\'s; DROP TABLE x'`,
		"doubled quote":    "SELECT 1 AS value WHERE a = 'it''s; DROP'",
		"quoted ident":     "SELECT `update` AS value FROM t",
		"column named set": "SELECT 1 AS value FROM t WHERE x IN (SELECT 2)",
		"subquery":         "SELECT count() AS value FROM (SELECT 1) HAVING value > 0",
		"format function":  "SELECT formatReadableSize(1) AS s, 1 AS value",
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateExpr(q); err != nil {
				t.Fatalf("refused %q: %v", q, err)
			}
		})
	}
}

func TestValidateExprRefuses(t *testing.T) {
	for name, q := range map[string]string{
		"empty":             "",
		"whitespace":        "  \n ",
		"only comment":      "-- nothing",
		"insert":            "INSERT INTO t VALUES (1)",
		"drop":              "DROP TABLE t",
		"two statements":    "SELECT 1 AS value; DROP TABLE t",
		"two selects":       "SELECT 1; SELECT 2",
		"smuggled insert":   "SELECT 1 AS value; INSERT INTO t VALUES (1)",
		"execute as":        "EXECUTE AS r_other SELECT 1",
		"embedded execute":  "SELECT 1 AS value WHERE 1 = (EXECUTE AS x SELECT 1)",
		"system database":   "SELECT name AS value FROM system.users",
		"system tables":     "SELECT 1 AS value FROM system . tables",
		"set":               "SET max_execution_time = 0",
		"settings override": "SELECT 1 AS value SETTINGS readonly = 0",
		"settings lower":    "SELECT 1 AS value settings max_execution_time=0",
		"into outfile":      "SELECT 1 AS value INTO OUTFILE '/tmp/x'",
		"format":            "SELECT 1 AS value FORMAT Native",
		"url":               "SELECT * FROM url('http://169.254.169.254/', 'CSV')",
		"url spaced":        "SELECT * FROM URL ('http://x', 'CSV')",
		"s3":                "SELECT * FROM s3('http://x/y', 'CSV')",
		"remote":            "SELECT * FROM remote('other', db.t)",
		"file":              "SELECT * FROM file('/etc/passwd')",
		"cluster":           "SELECT * FROM cluster('c', db.t)",
		"leading comment":   "/* hi */ DROP TABLE t",
		"unterminated str":  "SELECT 'oops",
		"unterminated cmt":  "SELECT 1 /* oops",
		"nul":               "SELECT 1\x00; DROP TABLE t",
		"too long":          "SELECT '" + strings.Repeat("a", MaxExprBytes) + "'",
		"show":              "SHOW TABLES",
		"describe":          "DESCRIBE t",
	} {
		t.Run(name, func(t *testing.T) {
			err := ValidateExpr(q)
			if err == nil {
				t.Fatalf("accepted %q", q)
			}
			if !errors.Is(err, ErrBadExpr) {
				t.Fatalf("error %v does not wrap ErrBadExpr", err)
			}
		})
	}
}

func TestQueryOptionsSettings(t *testing.T) {
	s := QueryOptions{MaxExecutionTime: 1500 * time.Millisecond, MaxResultRows: 201}.Settings()
	if s["readonly"] != 2 {
		t.Fatalf("readonly = %v, want 2", s["readonly"])
	}
	if s["max_execution_time"] != 1 || s["max_result_rows"] != 201 {
		t.Fatalf("settings = %v", s)
	}
	if s["result_overflow_mode"] != "throw" || s["timeout_overflow_mode"] != "throw" {
		t.Fatalf("overflow modes must throw, got %v", s)
	}
	if _, ok := (QueryOptions{}).Settings()["max_execution_time"]; ok {
		t.Fatal("a zero timeout must not emit max_execution_time=0 (unlimited)")
	}
}

func TestLimitsOmitReadonly(t *testing.T) {
	l := QueryOptions{MaxExecutionTime: 3 * time.Second, MaxResultRows: 9}.Limits()
	if _, bad := l["readonly"]; bad {
		t.Fatal("Limits must not send readonly: a readonly session refuses EXECUTE AS")
	}
	if l["max_execution_time"] != 3 || l["max_result_rows"] != 9 || l["result_overflow_mode"] != "throw" {
		t.Fatalf("limits = %v", l)
	}
}
