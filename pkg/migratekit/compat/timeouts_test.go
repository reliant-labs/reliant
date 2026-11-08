package compat

import (
	"errors"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func dur(d time.Duration) *time.Duration { return &d }

func TestParseTimeouts(t *testing.T) {
	cases := []struct {
		name      string
		sql       string
		lock, stm *time.Duration
	}{
		{"none", "ALTER TABLE t ADD COLUMN c TEXT;", nil, nil},
		{"lock", LockTimeoutDirective + " 30s\nSELECT 1;", dur(30 * time.Second), nil},
		{"statement", StatementTimeoutDirective + " 2h\nSELECT 1;", nil, dur(2 * time.Hour)},
		{"both, compound duration", "-- forge:lock-timeout 1m30s\n-- forge:statement-timeout 500ms\nSELECT 1;", dur(90 * time.Second), dur(500 * time.Millisecond)},
		{"off", "-- forge:lock-timeout off\n-- forge:statement-timeout 0\n", dur(0), dur(0)},
		{"rationale after the value", "-- forge:statement-timeout 2h — backfills 40M rows\nSELECT 1;", nil, dur(2 * time.Hour)},
		{"case and spacing", "  --   FORGE:Lock-Timeout   10S\n", dur(10 * time.Second), nil},
		{"after statements", "SELECT 1;\n-- forge:lock-timeout 1s\n", dur(time.Second), nil},
		{"inside a block comment", "/*\n forge:lock-timeout bogus\n*/\nSELECT 1;", nil, nil},
		{"inside a string", "SELECT '-- forge:lock-timeout bogus';", nil, nil},
		{"trailing on a statement", "SELECT 1; -- forge:lock-timeout bogus", nil, nil},
		{"other forge directives are not ours", "-- forge:backward-compatible\n-- forge:allow-destructive\nSELECT 1;", nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseTimeouts(c.sql)
			if err != nil {
				t.Fatalf("ParseTimeouts: %v", err)
			}
			if !samePtr(got.Lock, c.lock) || !samePtr(got.Statement, c.stm) {
				t.Errorf("got lock=%v statement=%v, want lock=%v statement=%v", show(got.Lock), show(got.Statement), show(c.lock), show(c.stm))
			}
		})
	}
}

func TestParseTimeoutsRefusesWhatItCannotRead(t *testing.T) {
	cases := []struct {
		name, sql, want string
		line            int
	}{
		{"misspelt", "SELECT 1;\n-- forge:lock-timout 5s\n", "unknown directive", 2},
		{"no value", "-- forge:lock-timeout\n", "names no value", 1},
		{"not a duration", "-- forge:statement-timeout soon\n", "not a duration", 1},
		{"bare number", "-- forge:lock-timeout 30\n", "not a duration", 1},
		{"negative", "-- forge:lock-timeout -5s\n", "below the 1ms", 1},
		{"sub-millisecond", "-- forge:lock-timeout 10us\n", "below the 1ms", 1},
		{"beyond postgres", "-- forge:statement-timeout 9999h\n", "above the largest", 1},
		{"twice", "-- forge:lock-timeout 5s\n\n-- forge:lock-timeout 6s\n", "declared twice", 3},
		{"statement is not a lock", "-- forge:statement 5s\n", "unknown directive", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseTimeouts(c.sql)
			var syn *TimeoutSyntaxError
			if !errors.As(err, &syn) {
				t.Fatalf("got %v, want a *TimeoutSyntaxError", err)
			}
			if syn.Line != c.line || !strings.Contains(syn.Reason, c.want) {
				t.Errorf("got line %d %q, want line %d containing %q", syn.Line, syn.Reason, c.line, c.want)
			}
		})
	}
}

func TestScanCarriesTimeoutsAndRefusesABadDirective(t *testing.T) {
	fsys := fstest.MapFS{
		"m/1_a.up.sql": {Data: []byte("CREATE TABLE a (id INT);")},
		"m/2_b.up.sql": {Data: []byte("-- forge:lock-timeout 45s\nALTER TABLE a ADD COLUMN b INT;")},
	}
	ms, err := Scan(fsys, "m")
	if err != nil {
		t.Fatal(err)
	}
	if ms[0].Timeouts.Lock != nil || ms[1].Timeouts.Lock == nil || *ms[1].Timeouts.Lock != 45*time.Second {
		t.Errorf("timeouts not carried onto the migration: %+v", ms)
	}

	fsys["m/3_c.up.sql"] = &fstest.MapFile{Data: []byte("-- forge:lock-timeout forever\nSELECT 1;")}
	if _, err := Scan(fsys, "m"); err == nil || !strings.Contains(err.Error(), "3_c.up.sql") {
		t.Errorf("Scan accepted a malformed directive, or did not name the file: %v", err)
	}
}

func samePtr(a, b *time.Duration) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func show(d *time.Duration) string {
	if d == nil {
		return "<unset>"
	}
	return d.String()
}
