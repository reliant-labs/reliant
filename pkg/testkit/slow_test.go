package testkit

import (
	"flag"
	"fmt"
	"strings"
	"testing"
)

// recordingTB stands in for the test a Slow call receives, so the skip and
// fatal paths can be observed without ending the real test.
type recordingTB struct {
	testing.TB
	skipped string
	fatal   string
}

func (r *recordingTB) Helper() {}

func (r *recordingTB) Skip(args ...any) {
	if r.skipped == "" && r.fatal == "" {
		r.skipped = fmt.Sprint(args...)
	}
}

func (r *recordingTB) Fatalf(format string, args ...any) {
	if r.fatal == "" {
		r.fatal = fmt.Sprintf(format, args...)
	}
}

// withShort runs fn with -short set to short, restoring it afterwards.
// Not parallel-safe: it flips a process-wide flag.
func withShort(t *testing.T, short bool, fn func()) {
	t.Helper()
	was := testing.Short()
	set := func(v bool) {
		val := "false"
		if v {
			val = "true"
		}
		if err := flag.Set("test.short", val); err != nil {
			t.Fatalf("set -test.short: %v", err)
		}
	}
	set(short)
	defer set(was)
	fn()
}

func TestSlowSkipsUnderShortAndSaysWhereItRuns(t *testing.T) {
	withShort(t, true, func() {
		rec := &recordingTB{TB: t}
		Slow(rec, "builds the forge binary")
		if rec.fatal != "" {
			t.Fatalf("Slow failed a valid call: %s", rec.fatal)
		}
		for _, want := range []string{"-short", "builds the forge binary", "full tier", "task test"} {
			if !strings.Contains(rec.skipped, want) {
				t.Errorf("skip message %q does not mention %q", rec.skipped, want)
			}
		}
		if !SlowSkipped() {
			t.Error("SlowSkipped() = false under -short")
		}
	})
}

func TestSlowRunsInTheFullTier(t *testing.T) {
	if integrationLane {
		t.Skip("the integration lane skips slow tests by design; TestSlowSkipsInTheIntegrationLane covers it")
	}
	withShort(t, false, func() {
		rec := &recordingTB{TB: t}
		Slow(rec, "builds the forge binary", Inputs("deploy/kcl/**", "db/migrations/*.sql"))
		if rec.skipped != "" || rec.fatal != "" {
			t.Fatalf("Slow did not let the test run without -short: skipped=%q fatal=%q", rec.skipped, rec.fatal)
		}
		if SlowSkipped() {
			t.Error("SlowSkipped() = true without -short")
		}
	})
}

func TestSlowRejectsAnEmptyReason(t *testing.T) {
	withShort(t, true, func() {
		rec := &recordingTB{TB: t}
		Slow(rec, "  ")
		if !strings.Contains(rec.fatal, "why is empty") {
			t.Fatalf("an empty reason was accepted: fatal=%q skipped=%q", rec.fatal, rec.skipped)
		}
	})
}

// A malformed glob must fail in the FAST tier, where it is cheap to see —
// not only in the full tier, where the test would otherwise first run.
func TestSlowValidatesInputsEvenUnderShort(t *testing.T) {
	withShort(t, true, func() {
		rec := &recordingTB{TB: t}
		Slow(rec, "renders prod", Inputs("../outside/**"))
		if !strings.Contains(rec.fatal, "testkit.Inputs") {
			t.Fatalf("a glob escaping the module was accepted under -short: fatal=%q skipped=%q", rec.fatal, rec.skipped)
		}
	})
}

func TestValidateInputGlob(t *testing.T) {
	t.Parallel()
	valid := []string{"deploy/**", "deploy/kcl/**/*.k", "db/migrations/*.sql", "go.mod", "frontends/web/", "**/*.proto", "a/[bc]/d"}
	for _, glob := range valid {
		if err := validateInputGlob(glob); err != nil {
			t.Errorf("validateInputGlob(%q) = %v, want nil", glob, err)
		}
	}
	invalid := map[string]string{
		"":             "empty",
		"/etc/passwd":  "absolute",
		"../x":         "escapes",
		"..":           "escapes",
		"./deploy/**":  "clean",
		"deploy//x":    "clean",
		`deploy\kcl`:   "forward slashes",
		"deploy**":     "whole path segment",
		"deploy/[a/**": "malformed",
	}
	for glob, want := range invalid {
		err := validateInputGlob(glob)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("validateInputGlob(%q) = %v, want an error mentioning %q", glob, err, want)
		}
	}
}
