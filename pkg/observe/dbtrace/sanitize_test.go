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
