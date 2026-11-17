package dbtrace

import (
	"strings"
	"testing"
)

func TestSanitizeSQL(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"placeholders kept", "SELECT id FROM chats WHERE user_id = $1 AND state <> $2", "SELECT id FROM chats WHERE user_id = $1 AND state <> $2"},
		{"string literal", "SELECT * FROM users WHERE email = 'a@b.com'", "SELECT * FROM users WHERE email = ?"},
		{"escaped quote", "UPDATE t SET name = 'O''Brien' WHERE id = 7", "UPDATE t SET name = ? WHERE id = ?"},
		{"E string with backslash quote", `SELECT E'it\'s secret', 1`, "SELECT ?, ?"},
		{"dollar quoted", "SELECT $$top secret$$, $tag$ also $ secret $tag$", "SELECT ?, ?"},
		{"numbers", "SELECT * FROM t LIMIT 50 OFFSET 1.5e3", "SELECT * FROM t LIMIT ? OFFSET ?"},
		{"digits inside identifiers", "SELECT col1, t2.x FROM t2", "SELECT col1, t2.x FROM t2"},
		{"quoted identifier kept", `SELECT "Weird Col" FROM "Chats"`, `SELECT "Weird Col" FROM "Chats"`},
		{"comments dropped", "SELECT 1 -- password=hunter2\nFROM t /* key 'x' /* nested */ */", "SELECT ? FROM t"},
		{"whitespace collapsed", "SELECT\n\t a,\n   b\nFROM   t", "SELECT a, b FROM t"},
		{"cast", "SELECT 'x'::text, now() - interval '1 day'", "SELECT ?::text, now() - interval ?"},
		{"bun inlined values", "INSERT INTO usage_events (id, tokens, model) VALUES ('0b9c', 1200, 'opus') RETURNING id", "INSERT INTO usage_events (id, tokens, model) VALUES (?, ?, ?) RETURNING id"},
		{"unterminated literal", "SELECT 'oops", "SELECT ?"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SanitizeSQL(tc.in); got != tc.want {
				t.Errorf("SanitizeSQL(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSanitizeSQLCapsLength(t *testing.T) {
	q := "INSERT INTO t (a) VALUES " + strings.Repeat("(1), ", 2000) + "(1)"
	if got := SanitizeSQL(q); len(got) > maxQueryText {
		t.Fatalf("len = %d, want <= %d", len(got), maxQueryText)
	}
}

func TestSummarize(t *testing.T) {
	cases := []struct{ in, op, table string }{
		{"SELECT id FROM chats WHERE id = $1", "SELECT", "chats"},
		{"select count(*) from public.messages m join chats c on c.id = m.chat_id", "SELECT", "public.messages"},
		{`SELECT * FROM "Chats"`, "SELECT", "Chats"},
		{"SELECT * FROM (SELECT 1) sub", "SELECT", ""},
		{"SELECT 1", "SELECT", ""},
		{"INSERT INTO usage_events (a) VALUES ($1)", "INSERT", "usage_events"},
		{"UPDATE reservations SET state = $1", "UPDATE", "reservations"},
		{"UPDATE ONLY reservations SET state = $1", "UPDATE", "reservations"},
		{"DELETE FROM tokens WHERE expires_at < now()", "DELETE", "tokens"},
		{"WITH recent AS (SELECT * FROM chats WHERE x) UPDATE messages SET y = 1 FROM recent", "UPDATE", "messages"},
		{"WITH a AS (SELECT 1), b AS (SELECT 2) SELECT * FROM a, b", "SELECT", "a"},
		{"/* sqlcommenter */ SELECT 1 FROM t", "SELECT", "t"},
		{"BEGIN", "BEGIN", ""},
		{"", "", ""},
		{"  ", "", ""},
	}
	for _, tc := range cases {
		op, table := Summarize(tc.in)
		if op != tc.op || table != tc.table {
			t.Errorf("Summarize(%q) = (%q, %q), want (%q, %q)", tc.in, op, table, tc.op, tc.table)
		}
	}
}

// ClickHouse's literals are read by ClickHouse's rules: a backslash escapes in
// every quoted string, `…` and “…” quote identifiers, ‘…’ is a literal, and
// //, "# " and #! start comments. Read by Postgres's rules, 'a\'secret' ends at
// the backslash-quote and "secret" is recorded as SQL text.
func TestSanitizeClickHouse(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"backslash-escaped quote", `SELECT 'a\'secret' FROM t`, "SELECT ? FROM t"},
		{"escaped backslash ends the literal", `SELECT 'a\\', 'secret'`, "SELECT ?, ?"},
		{"doubled quote", `SELECT 'O''secret'`, "SELECT ?"},
		{"backquoted identifier", "SELECT `it's`, 'secret' FROM `db`.`t`", "SELECT `it's`, ? FROM `db`.`t`"},
		{"double-quoted identifier with a backslash", `SELECT "a\"b", 'secret'`, `SELECT "a\"b", ?`},
		{"unicode quotes", "SELECT ‘secret’, x‘secret’, “Col” FROM t", "SELECT ?, x?, “Col” FROM t"},
		{"comments", "SELECT 1 // secret\n# secret\n#!secret\nFROM t -- secret", "SELECT ? FROM t"},
		{"# without a space is not a comment", "SELECT a #b", "SELECT a #b"},
		{"heredoc", "SELECT $$secret$$, $1$secret$1$, $tag$secret$tag$", "SELECT ?, ?, ?"},
		{"non-decimal and separated numbers", "SELECT 0xDEADBEEF, 0b1010, 1_000_000, 0x1.8p3, 1e-5", "SELECT ?, ?, ?, ?, ?"},
		{"query parameters kept", "SELECT * FROM t WHERE a = {p1:String} AND b IN {ids:Array(UInt64)}", "SELECT * FROM t WHERE a = {p1:String} AND b IN {ids:Array(UInt64)}"},
		{"unterminated literal", `SELECT 'oops\' secret`, "SELECT ?"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClickHouse.Sanitize(tc.in); got != tc.want {
				t.Errorf("ClickHouse.Sanitize(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}
}

// No ClickHouse literal, however it escapes its quotes, leaves a byte of its
// contents in the sanitized text or the summary.
func TestSanitizeClickHouseNeverLeaksALiteral(t *testing.T) {
	escapes := []func(string) string{
		func(s string) string { return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'" },
		func(s string) string { return "'" + strings.NewReplacer(`\`, `\\`, `'`, `''`).Replace(s) + "'" },
		// A ‘…’ literal has no escape: ClickHouse ends it at the first ’
		// (TestSanitizeClickHouseCurlyQuotesHaveNoEscape), so it cannot hold one.
		func(s string) string { return "‘" + strings.ReplaceAll(s, "’", "") + "’" },
		func(s string) string { return "$x$" + strings.ReplaceAll(s, "$x$", "") + "$x$" },
	}
	for _, secret := range []string{
		`SECRET`, `a'SECRET`, `a\'SECRET`, `a\\'SECRET`, `'' SECRET`, `\`, `x' FROM SECRET --`,
		`" SECRET "`, "` SECRET `", `/* SECRET`, `-- SECRET`, `’ SECRET`, `$$ SECRET`,
	} {
		for i, esc := range escapes {
			lit := esc(secret)
			for _, q := range []string{
				"SELECT " + lit + " FROM t",
				"SELECT a FROM t WHERE b = " + lit + " AND c = 'SECRET'",
				"INSERT INTO t VALUES (" + lit + ", " + lit + ")",
			} {
				if got := ClickHouse.Sanitize(q); strings.Contains(got, "SECRET") {
					t.Errorf("escape %d: Sanitize(%q) = %q", i, q, got)
				}
				if op, table := ClickHouse.Summarize(q); strings.Contains(op+table, "SECRET") {
					t.Errorf("escape %d: Summarize(%q) = %q %q", i, q, op, table)
				}
			}
		}
	}
}

func TestSummarizeClickHouse(t *testing.T) {
	cases := []struct{ in, op, table string }{
		{"SELECT * FROM `t_org`.`otel_logs` WHERE x", "SELECT", "t_org.otel_logs"},
		{`SELECT 'x\' FROM leaked' FROM events`, "SELECT", "events"},
		{"# a comment\nINSERT INTO t_org.logs VALUES", "INSERT", "t_org.logs"},
		{`SELECT * FROM "s"."t"`, "SELECT", "s.t"},
	}
	for _, tc := range cases {
		op, table := ClickHouse.Summarize(tc.in)
		if op != tc.op || table != tc.table {
			t.Errorf("ClickHouse.Summarize(%q) = (%q, %q), want (%q, %q)", tc.in, op, table, tc.op, tc.table)
		}
	}
}

// ClickHouse ends a ‘…’ literal at the first ’, backslash or not
// (quotedStringWithUnicodeQuotes in its src/Parsers/Lexer.cpp searches for
// the closing quote alone). The sanitizer must end it there too: honouring a
// backslash would run the literal past the server's end, and the text after
// a ’ inside a following ASCII literal would be read as SQL.
func TestSanitizeClickHouseCurlyQuotesHaveNoEscape(t *testing.T) {
	q := `SELECT ‘a\’, 'x’ SECRET', 'y' FROM t`
	if got, want := ClickHouse.Sanitize(q), "SELECT ?, ?, ? FROM t"; got != want {
		t.Errorf("ClickHouse.Sanitize(%q)\n got %q\nwant %q", q, got, want)
	}
	if _, table := ClickHouse.Summarize(q); table != "t" {
		t.Errorf("ClickHouse.Summarize(%q) table = %q, want t", q, table)
	}
}
