package lint

import "testing"

// sqlCommentValue reads every literal spelling a COMMENT ON value takes.
func TestSQLCommentValue(t *testing.T) {
	for in, want := range map[string]struct {
		value string
		known bool
	}{
		" NULL;":               {"", true},
		"\n\tnull":             {"", true},
		" nullable":            {"", false},
		" 'it''s';":            {"it's", true},
		` E'it\'s\n';`:         {"it's\n", true},
		" $$a 'b' c$$;":        {"a 'b' c", true},
		" $t$has $$ inside$t$": {"has $$ inside", true},
		" $$unterminated":      {"", false},
		" 'unterminated":       {"", false},
		" U&'d\\0061t'":        {"", false},
	} {
		if v, k := sqlCommentValue(in); v != want.value || k != want.known {
			t.Errorf("sqlCommentValue(%q) = %q, %v; want %q, %v", in, v, k, want.value, want.known)
		}
	}
}
