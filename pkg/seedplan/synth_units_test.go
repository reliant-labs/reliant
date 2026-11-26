package seedplan

import (
	"regexp"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// patternSample's contract is that whatever it returns MATCHES every pattern
// it was given, and that what it cannot build it declines rather than guess.
func TestPatternSample_BuildsOnlyValuesTheDeclarationAdmits(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pats    []string
		row     int
		minLen  int
		want    string // "" when only matching is asserted
		wantOK  bool
		wantLen int
	}{
		{"a literal with a counted digit field varies per row", []string{`^SKU-[0-9]{4}$`}, 6, 0, "SKU-0007", true, 0},
		{"a counted field keeps the low-order digits", []string{`^[0-9]{2}$`}, 122, 0, "23", true, 0},
		{"an optional part is left out", []string{`^ab?c$`}, 0, 0, "ac", true, 0},
		{"a star is filled when the class is buildable", []string{`^a[0-9]*$`}, 4, 0, "a5", true, 0},
		{"a star of something unbuildable is skipped", []string{`^a(?:[^\x00-\x{10FFFF}])*$`}, 0, 0, "a", true, 0},
		{"a plus grows to reach the minimum length", []string{`^[a-z]+$`}, 0, 6, "", true, 6},
		{"a star grows to reach the minimum length", []string{`^[a-z]*$`}, 0, 5, "", true, 5},
		{"an alternation takes the first buildable branch", []string{`^(?:x|y)z$`}, 0, 0, "xz", true, 0},
		{"a repeated group repeats its sub-expression", []string{`^(?:ab){3}$`}, 0, 0, "ababab", true, 0},
		{"a zero-count repeat emits nothing", []string{`^a(?:b){0}c$`}, 0, 0, "ac", true, 0},
		{"a capture group is transparent", []string{`^(a)(b)$`}, 0, 0, "ab", true, 0},
		{"any character takes the row filler", []string{`^.$`}, 8, 0, "9", true, 0},
		{"a word boundary and anchors are free", []string{`\bfoo\b`}, 0, 0, "foo", true, 0},
		{"an unparseable pattern is declined", []string{`([a-z`}, 0, 0, "", false, 0},
		{"a pattern nothing can match is declined", []string{`[^\x00-\x{10FFFF}]`}, 0, 0, "", false, 0},
		{"a second pattern the first-built value fails is declined", []string{`^a[0-9]*$`, `^a$`}, 4, 0, "", false, 0},
		{"a class with only non-printables is declined", []string{`^[\x00-\x20]$`}, 0, 0, "", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := patternSample(tc.pats, tc.row, tc.minLen)
			if ok != tc.wantOK {
				t.Fatalf("patternSample(%q) = %q, %v; want ok=%v", tc.pats, got, ok, tc.wantOK)
			}
			if !ok {
				return
			}
			for _, p := range tc.pats {
				if !regexp.MustCompile(p).MatchString(got) {
					t.Errorf("%q does not match %q", got, p)
				}
			}
			if tc.want != "" && got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
			if tc.wantLen > 0 && len([]rune(got)) < tc.wantLen {
				t.Errorf("got %q (%d chars), want at least %d", got, len([]rune(got)), tc.wantLen)
			}
		})
	}
}

func TestEmailSample(t *testing.T) {
	if got, ok := emailSample([]string{EmailCheckPattern}, 2, 0, 0); !ok || got != "user3@example.com" {
		t.Errorf("= %q, %v", got, ok)
	}
	if _, ok := emailSample([]string{`^x$`}, 2, 0, 0); ok {
		t.Error("only forge's own email CHECK is recognised")
	}
	if _, ok := emailSample([]string{EmailCheckPattern}, 2, 40, 0); ok {
		t.Error("an address shorter than the column's minimum is declined")
	}
	if _, ok := emailSample([]string{EmailCheckPattern}, 2, 0, 10); ok {
		t.Error("an address longer than the column's cap is declined")
	}
	if _, ok := emailSample([]string{EmailCheckPattern, `^never@`}, 2, 0, 0); ok {
		t.Error("an address another declared pattern rejects is declined")
	}
	if _, ok := emailSample([]string{EmailCheckPattern, `([`}, 2, 0, 0); ok {
		t.Error("an unparseable companion pattern is declined, not assumed satisfied")
	}
}

func TestClassHelpers(t *testing.T) {
	digits := []rune{'0', '9'}
	if !inClass(digits, '5') || inClass(digits, 'a') || inClass(nil, '5') {
		t.Error("inClass reads [lo, hi] pairs")
	}
	if !classAdmits(digits, "123") || classAdmits(digits, "12a") {
		t.Error("classAdmits requires every rune inside")
	}
	if r, ok := classMember([]rune{'0', '9'}); !ok || r != '0' {
		t.Errorf("a digit class contributes 0, got %q %v", r, ok)
	}
	if r, ok := classMember([]rune{'B', 'D'}); !ok || r != 'B' {
		t.Errorf("a class without the preferred members takes its first printable, got %q %v", r, ok)
	}
	// A negated class starts at NUL: the control characters are skipped.
	if r, ok := classMember([]rune{0, 0x20, 0x7f, 0x10FFFF}); ok && r < 0x21 {
		t.Errorf("a control character must never be a member, got %q", r)
	}
	if _, ok := classMember([]rune{0, 0x20}); ok {
		t.Error("a class of only controls and space has no member")
	}
	if _, ok := classMember(nil); ok {
		t.Error("an empty class has no member")
	}
}

func TestPkLiteral_FollowsTheColumnType(t *testing.T) {
	cfg := Config{Salt: 1}
	for _, tc := range []struct {
		typ  schemadef.CanonicalType
		row  int
		want string
	}{
		{schemadef.TypeInt, 4, "5"},
		{schemadef.TypeFloat, 4, "5.00"},
		{schemadef.TypeBool, 0, "true"},
		{schemadef.TypeBool, 1, "false"},
		{schemadef.TypeJSON, 3, `'{"row": 3}'`},
	} {
		col := schemadef.Column{Name: "k", Type: tc.typ}
		if got := pkLiteral(cfg, "t", col, tc.row, false); got != tc.want {
			t.Errorf("%s row %d = %s, want %s", tc.typ, tc.row, got, tc.want)
		}
	}
	bytea := pkLiteral(cfg, "t", schemadef.Column{Name: "k", Type: schemadef.TypeBytes}, 2, false)
	if bytea == pkLiteral(cfg, "t", schemadef.Column{Name: "k", Type: schemadef.TypeBytes}, 3, false) || bytea[len(bytea)-8:] != "'::bytea" {
		t.Errorf("bytea keys are distinct per row, got %s", bytea)
	}
	str := schemadef.Column{Name: "k", Type: schemadef.TypeString}
	if pkLiteral(cfg, "t", str, 1, false) == pkLiteral(cfg, "t", str, 1, true) {
		t.Error("a composite member keys its UUID on its own name too")
	}
}

func TestSynthScalar_StaysInsideItsBoundsAndIsDeterministic(t *testing.T) {
	p := &Plan{cfg: Config{Salt: 1, Now: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)}, conv: map[string]schemadef.Conventions{}}
	tbl := schemadef.Table{Name: "t"}
	float := schemadef.Column{Name: "f", Type: schemadef.TypeFloat}
	for _, tc := range []struct {
		name  string
		row   int
		bound NumBound
		want  string
	}{
		{"unbounded", 1, NumBound{}, "21.00"},
		{"below the min is raised to it", 0, NumBound{Min: ptr(50)}, "50.00"},
		{"above the max is lowered to it", 9, NumBound{Max: ptr(30)}, "30.00"},
	} {
		if got := p.synthScalar(tbl, float, tc.row, tc.bound); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
	if got := p.synthScalar(tbl, schemadef.Column{Name: "b", Type: schemadef.TypeBytes}, 1, NumBound{}); got[len(got)-8:] != "'::bytea" {
		t.Errorf("bytes = %s", got)
	}
	if got := p.synthScalar(tbl, schemadef.Column{Name: "n", Type: schemadef.TypeInt}, 4, NumBound{Max: ptr(3)}); got != "3" {
		t.Errorf("an int is clamped to its bound, got %s", got)
	}
	flags := map[string]bool{}
	for i := 0; i < 16; i++ {
		flags[p.synthScalar(tbl, schemadef.Column{Name: "ok", Type: schemadef.TypeBool}, i, NumBound{})] = true
	}
	if !flags["true"] || !flags["false"] || len(flags) != 2 {
		t.Errorf("a boolean column draws both values across rows, got %v", flags)
	}
	if again := p.synthScalar(tbl, float, 1, NumBound{}); again != "21.00" {
		t.Errorf("synthesis is deterministic, got %s", again)
	}
	if pick(1, "t", "c", 3, 0) != 0 || pick(1, "t", "c", 3, -2) != 0 {
		t.Error("pick over an empty range is 0")
	}
}

func TestManagedTimestampLiteral(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	if got := managedTimestampLiteral(now, managedDeletedAt, 0); got != "NULL" {
		t.Errorf("a soft-delete marker seeds live rows: %s", got)
	}
	created, updated := managedTimestampLiteral(now, managedCreatedAt, 3), managedTimestampLiteral(now, managedUpdatedAt, 3)
	if created == updated || created == "NULL" {
		t.Errorf("created %s updated %s", created, updated)
	}
	cs, us := mustTimeLiteral(t, created), mustTimeLiteral(t, updated)
	if !us.After(cs) {
		t.Errorf("updated (%v) must sit after created (%v)", us, cs)
	}
	if us.After(now) {
		t.Errorf("seeded timestamps are never in the future: %v", us)
	}
}

func mustTimeLiteral(t *testing.T, lit string) time.Time {
	t.Helper()
	got, err := time.Parse(timeLiteralLayout, lit[1:len(lit)-1])
	if err != nil {
		t.Fatalf("%s: %v", lit, err)
	}
	return got
}
