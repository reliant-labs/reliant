// Copyright (c) 2025 Reliant Labs

package alerting

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// MaxExprBytes bounds a rule's expr.
const MaxExprBytes = 16 << 10

// ErrBadExpr marks an expr the guard refused.
var ErrBadExpr = errors.New("alerting: expr refused")

// deniedWords are statement keywords that can never appear in a read-only
// SELECT. The match is on whole tokens outside strings, quoted identifiers and
// comments, so a string literal containing "drop" is fine.
var deniedWords = map[string]string{
	"insert": "writes are not allowed", "update": "writes are not allowed", "delete": "writes are not allowed",
	"drop": "DDL is not allowed", "alter": "DDL is not allowed", "create": "DDL is not allowed",
	"truncate": "DDL is not allowed", "attach": "DDL is not allowed", "detach": "DDL is not allowed",
	"rename": "DDL is not allowed", "optimize": "DDL is not allowed",
	"grant": "access control is not allowed", "revoke": "access control is not allowed",
	"kill": "KILL is not allowed", "system": "the system database is not readable", "set": "SET is not allowed",
	"use": "USE is not allowed", "execute": "EXECUTE AS is applied by the Querier, not the rule",
	"outfile": "INTO OUTFILE is not allowed", "settings": "SETTINGS are applied by the evaluator, not the rule",
}

// deniedFunctions read from outside the owner's data. The database grants are
// the real boundary; this refuses them before they are sent.
var deniedFunctions = map[string]bool{
	"url": true, "urlcluster": true, "file": true, "filecluster": true, "s3": true, "s3cluster": true,
	"remote": true, "remotesecure": true, "cluster": true, "clusterallreplicas": true,
	"hdfs": true, "hdfscluster": true, "mysql": true, "postgresql": true, "mongodb": true,
	"redis": true, "jdbc": true, "odbc": true, "sqlite": true, "executable": true, "input": true,
	"azureblobstorage": true, "azureblobstoragecluster": true, "gcs": true, "iceberg": true,
	"deltalake": true, "hudi": true,
}

// ValidateExpr accepts exactly one read-only SELECT (optionally with a WITH
// prefix). It is a lexical check, not a parser, and is the FIRST of two lines of
// defence: the Querier must still run the statement with readonly settings and
// the evaluator's resource limits, which is what actually contains a query.
func ValidateExpr(expr string) error {
	if len(expr) > MaxExprBytes {
		return fmt.Errorf("%w: longer than %d bytes", ErrBadExpr, MaxExprBytes)
	}
	toks, err := lex(expr)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBadExpr, err)
	}
	if len(toks) == 0 {
		return fmt.Errorf("%w: empty", ErrBadExpr)
	}
	if first := toks[0].text; toks[0].kind != tokWord || (first != "select" && first != "with") {
		return fmt.Errorf("%w: must start with SELECT", ErrBadExpr)
	}
	sawSelect := false
	for i, t := range toks {
		switch t.kind {
		case tokSemicolon:
			if i != len(toks)-1 {
				return fmt.Errorf("%w: more than one statement", ErrBadExpr)
			}
		case tokWord:
			if t.text == "select" {
				sawSelect = true
			}
			if why, bad := deniedWords[t.text]; bad {
				return fmt.Errorf("%w: %q: %s", ErrBadExpr, t.text, why)
			}
			if t.text == "format" && !t.call {
				return fmt.Errorf("%w: FORMAT is applied by the driver, not the rule", ErrBadExpr)
			}
			if t.call && deniedFunctions[t.text] {
				return fmt.Errorf("%w: table function %s() reads outside the owner", ErrBadExpr, t.text)
			}
		}
	}
	if !sawSelect {
		return fmt.Errorf("%w: no SELECT", ErrBadExpr)
	}
	return nil
}

// Limits are the per-query resource settings: a timeout and a row cap, both
// throwing on overflow rather than truncating.
//
// A Querier that runs as an IMPERSONATED user (ClickHouse `EXECUTE AS`) sends
// only these. It must not send `readonly`: a readonly session refuses the
// EXECUTE AS statement itself, and the target user's settings profile (which
// should say readonly = 2 with MAX constraints) is what contains the query.
func (o QueryOptions) Limits() map[string]any {
	s := map[string]any{
		"result_overflow_mode":  "throw",
		"timeout_overflow_mode": "throw",
	}
	if o.MaxExecutionTime > 0 {
		secs := int(o.MaxExecutionTime / time.Second)
		if secs < 1 {
			secs = 1
		}
		s["max_execution_time"] = secs
	}
	if o.MaxResultRows > 0 {
		s["max_result_rows"] = o.MaxResultRows
	}
	return s
}

// Settings are Limits plus readonly=2 (no writes or DDL, while the per-query
// limits stay settable): what a Querier on its own, directly-authenticated
// connection sends.
func (o QueryOptions) Settings() map[string]any {
	s := o.Limits()
	s["readonly"] = 2
	return s
}

type tokKind int

const (
	tokWord tokKind = iota
	tokSemicolon
	tokOther
)

type token struct {
	kind tokKind
	text string // lowercased word
	call bool   // word immediately followed by '('
}

// lex splits SQL into words, semicolons and other, dropping comments and the
// contents of string literals and quoted identifiers.
func lex(s string) ([]token, error) {
	var out []token
	n := len(s)
	for i := 0; i < n; {
		c := s[i]
		switch {
		case c == 0:
			return nil, errors.New("NUL byte")
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			i++
		case c == '-' && i+1 < n && s[i+1] == '-', c == '#':
			for i < n && s[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && s[i+1] == '*':
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				return nil, errors.New("unterminated /* comment")
			}
			i += 2 + end + 2
		case c == '\'' || c == '"' || c == '`':
			j, err := skipQuoted(s, i)
			if err != nil {
				return nil, err
			}
			out = append(out, token{kind: tokOther})
			i = j
		case c == ';':
			out = append(out, token{kind: tokSemicolon})
			i++
		case isWordStart(c):
			j := i
			for j < n && isWordPart(s[j]) {
				j++
			}
			out = append(out, token{kind: tokWord, text: strings.ToLower(s[i:j]), call: nextIsParen(s, j)})
			i = j
		default:
			out = append(out, token{kind: tokOther})
			i++
		}
	}
	return out, nil
}

// skipQuoted returns the index after the quoted run starting at s[i]. A quote is
// closed by its own character; backslash escapes and doubled quotes both work.
func skipQuoted(s string, i int) (int, error) {
	q := s[i]
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case q:
			if j+1 < len(s) && s[j+1] == q {
				j++
				continue
			}
			return j + 1, nil
		}
	}
	return 0, fmt.Errorf("unterminated %c quote", q)
}

func isWordStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

func isWordPart(c byte) bool { return isWordStart(c) || (c >= '0' && c <= '9') }

// nextIsParen reports whether the next non-space byte at or after i is '('.
// ClickHouse accepts `url ('...')`, so a function call is not only `name(`.
func nextIsParen(s string, i int) bool {
	for ; i < len(s); i++ {
		switch s[i] {
		case ' ', '\t', '\n', '\r', '\f', '\v':
		default:
			return s[i] == '('
		}
	}
	return false
}
