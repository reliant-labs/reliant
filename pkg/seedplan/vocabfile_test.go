package seedplan

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A malformed vocabulary file must fail loudly, naming the line, rather than
// quietly seed generic data the author believes they overrode.
func TestLoadVocab_RefusesMalformedEntriesWithTheirLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
		want string
	}{
		{"pool and type together", "columns:\n  a.b: {pool: p, type: email}\npools:\n  p: [x]\n", "sets pool or type, not both"},
		{"a time range beside a pool", "columns:\n  a.b: {from: -1d, to: now, pool: p}\npools:\n  p: [x]\n", "not several"},
		{"a numeric range beside a type", "columns:\n  a.b: {min: 1, max: 3, type: email}\n", "not several"},
		{"a range with only a min", "columns:\n  a.b: {min: 1}\n", "needs both min and max"},
		{"a range with max below min", "columns:\n  a.b: {min: 5, max: 2}\n", "max (2) is below min (5)"},
		{"decimals out of range", "columns:\n  a.b: {min: 1, max: 3, decimals: 9}\n", "decimals must be between 0 and 6"},
		{"a step that is not a number", "columns:\n  a.b: {min: 1, max: 3, step: often}\n", `step "often" is not a number`},
		{"a step of zero", "columns:\n  a.b: {min: 1, max: 3, step: 0}\n", "step must be positive"},
		{"an unknown semantic type", "columns:\n  a.b: {type: nonsense}\n", `unknown type "nonsense"`},
		{"an empty mapping", "columns:\n  a.b: {}\n", "a mapping entry must be"},
		{"a scalar entry", "columns:\n  a.b: 7\n", "a column entry must be a value list"},
		{"a time range without a to", "columns:\n  a.b: {from: -1d}\n", "needs both from and to"},
		{"a time range from nowhere", "columns:\n  a.b: {from: yesterday-ish, to: now}\n", "time range from"},
		{"a time range to nowhere", "columns:\n  a.b: {from: -1d, to: eventually}\n", "time range to"},
		{"a time range with a bad step", "columns:\n  a.b: {from: -3d, to: now, step: -1d}\n", "time range step"},
		{"a key that is not table.column", "columns:\n  plain: [x]\n", "must be table.column"},
		{"an undefined pool", "columns:\n  a.b: {pool: nope}\n", `undefined pool "nope"`},
		{"an empty list", "columns:\n  a.b: []\n", "has no values"},
		{"an unknown top-level key", "column:\n  a.b: [x]\n", "parse seed vocab"},
		{"a value list holding a mapping", "columns:\n  a.b: [{x: 1}]\n", "plain values"},
		{"a value list holding a list", "columns:\n  a.b: [[1]]\n", "plain values"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadVocab(writeVocab(t, tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

func TestLoadVocab_ReadsWhatItCanAndReportsWhatItCannot(t *testing.T) {
	if v, err := LoadVocab(filepath.Join(t.TempDir(), "absent.yaml")); v != nil || err != nil {
		t.Errorf("a missing file is the built-in behaviour: %v, %v", v, err)
	}
	dir := t.TempDir()
	if _, err := LoadVocab(dir); err == nil || !strings.Contains(err.Error(), "read seed vocab") {
		t.Errorf("a path that is a directory: %v", err)
	}
}

func TestLoadVocab_AliasedValuesAndAFloatRangeExpand(t *testing.T) {
	v, err := LoadVocab(writeVocab(t, `
pools:
  base: &names [Ada, Grace]
columns:
  people.name: *names
  prices.amount: {min: 1, max: 2, step: 0.5, decimals: 2}
  prices.tier: {type: email}
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := v.Columns["people.name"]; len(got) != 2 || got[0] != "Ada" {
		t.Errorf("an aliased list: %v", got)
	}
	if got := strings.Join(v.Columns["prices.amount"], ","); got != "1.00,1.50,2.00" {
		t.Errorf("a decimal range expands at its step: %s", got)
	}
	if len(v.Columns["prices.tier"]) == 0 {
		t.Error("a semantic type expands to a pool")
	}
}

func TestLoadVocab_WarnsWhenADeclaredStepCannotHoldTheRange(t *testing.T) {
	v, err := LoadVocab(writeVocab(t, "columns:\n  t.n: {min: 0, max: 100000, step: 1}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Warnings) != 1 || !strings.Contains(v.Warnings[0], "t.n declares step") || !strings.Contains(v.Warnings[0], "instead") {
		t.Errorf("warnings = %v", v.Warnings)
	}
	if n := len(v.Columns["t.n"]); n == 0 || n > numericRangeMaxValues {
		t.Errorf("the widened range holds %d values, want 1..%d", n, numericRangeMaxValues)
	}
}

func TestFormatOffset_UsesTheLargestUnitThatDividesEvenly(t *testing.T) {
	for _, tc := range []struct {
		d      time.Duration
		signed bool
		want   string
	}{
		{0, true, "now"},
		{14 * 24 * time.Hour, true, "+2w"},
		{-14 * 24 * time.Hour, true, "-2w"},
		{3 * 24 * time.Hour, false, "3d"},
		{90 * time.Minute, false, "90m"},
		{5 * time.Hour, true, "+5h"},
		{36*time.Hour + 30*time.Minute, false, "2190m"},
		{1500 * time.Millisecond, false, "1.5s"},
		{-1500 * time.Millisecond, true, "-1.5s"},
	} {
		if got := formatOffset(tc.d, tc.signed); got != tc.want {
			t.Errorf("formatOffset(%v, %v) = %q, want %q", tc.d, tc.signed, got, tc.want)
		}
	}
}

func TestParseRelOffset_RoundTripsFormatOffset(t *testing.T) {
	for _, s := range []string{"now", "-2w", "+3d", "-90m", "12h", "+1w"} {
		d, ok := parseRelOffset(s)
		if !ok {
			t.Errorf("%q should parse", s)
			continue
		}
		if s != "now" && s[0] != '+' && s[0] != '-' {
			continue
		}
		if back := formatOffset(d, true); back != s && !(s == "+3d" && back == "+3d") {
			t.Errorf("%q → %v → %q", s, d, back)
		}
	}
	for _, s := range []string{"", "yesterday", "3", "d3", "1y", "1.5d"} {
		if _, ok := parseRelOffset(s); ok {
			t.Errorf("%q must not parse", s)
		}
	}
}

func TestFingerprint_CoversMigrationsAndVocabAndRefusesUnreadableInput(t *testing.T) {
	root := t.TempDir()
	mig := filepath.Join(root, "migrations")
	if err := os.MkdirAll(mig, 0o755); err != nil {
		t.Fatal(err)
	}
	if fp, err := FingerprintInputs(filepath.Join(root, "absent")); err != nil || !fp.Empty() {
		t.Errorf("no migrations directory: %+v, %v", fp, err)
	}
	if fp, err := FingerprintWithConfig(filepath.Join(root, "absent"), Config{Rows: 5}); err != nil || !fp.Empty() {
		t.Errorf("no migrations directory, with config: %+v, %v", fp, err)
	}
	if err := os.WriteFile(filepath.Join(mig, "00001_a.up.sql"), []byte("CREATE TABLE a (id int);\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	crlf, err := FingerprintInputs(mig)
	if err != nil || crlf.Empty() || crlf.Files != 1 {
		t.Fatalf("%+v, %v", crlf, err)
	}
	if err := os.WriteFile(filepath.Join(mig, "00001_a.up.sql"), []byte("CREATE TABLE a (id int);\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if lf, _ := FingerprintInputs(mig); lf != crlf {
		t.Errorf("line endings are not a schema change: %+v vs %+v", lf, crlf)
	}
	if err := os.WriteFile(filepath.Join(mig, "00001_a.down.sql"), []byte("DROP TABLE a;"), 0o644); err != nil {
		t.Fatal(err)
	}
	if again, _ := FingerprintInputs(mig); again != crlf {
		t.Error("a down migration is not an input")
	}

	if err := os.MkdirAll(filepath.Join(root, "seeds"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(VocabPath(mig), []byte("columns: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withVocab, err := FingerprintInputs(mig)
	if err != nil || withVocab.Files != 2 || withVocab == crlf {
		t.Errorf("a vocab overlay joins the inputs: %+v, %v", withVocab, err)
	}
	a, _ := FingerprintWithConfig(mig, Config{Rows: 5, Salt: 1, RowsPerTable: map[string]int{"a": 2, "b": 3}})
	b, _ := FingerprintWithConfig(mig, Config{Rows: 5, Salt: 1, RowsPerTable: map[string]int{"b": 3, "a": 2}})
	c, _ := FingerprintWithConfig(mig, Config{Rows: 6, Salt: 1, RowsPerTable: map[string]int{"a": 2, "b": 3}})
	if a != b || a == c || a == withVocab {
		t.Errorf("the config is folded in, order-independently: %v %v %v", a, b, c)
	}

	// An input that exists but cannot be read is an error, never "absent".
	if err := os.Remove(filepath.Join(mig, "00001_a.up.sql")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(mig, "00002_dir.up.sql"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mig, "00003_x.up.sql"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// A directory at the vocab path passes the existence check but cannot be
	// read as a file, even as root or on Windows where modes are not enforced.
	if err := os.Remove(VocabPath(mig)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(VocabPath(mig), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := FingerprintInputs(mig); err == nil || !strings.Contains(err.Error(), "read seed fingerprint input") {
		t.Errorf("an unreadable input: %v", err)
	}
	if _, err := FingerprintWithConfig(mig, Config{Rows: 1}); err == nil {
		t.Error("an unreadable input fails the config-folded fingerprint too")
	}
	// POSIX only: on Windows os.ReadDir of a regular file succeeds (empty), so
	// there is no "not a directory" error to report.
	if runtime.GOOS != "windows" {
		if _, err := FingerprintInputs(filepath.Join(mig, "00003_x.up.sql")); err == nil || !strings.Contains(err.Error(), "read migrations dir") {
			t.Errorf("a migrations path that is a file: %v", err)
		}
	}
}
