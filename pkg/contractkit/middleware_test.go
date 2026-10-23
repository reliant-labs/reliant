package contractkit

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// TestLogCallErr_FingerprintLocked verifies the slog record shape:
// level=INFO, msg=<method>, attrs include "duration" and "error" keys.
// The attribute keys are part of the locked surface — tests across
// dogfood projects scrape these.
func TestLogCallErr_FingerprintLocked(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	start := time.Now()
	want := errors.New("boom")
	LogCallErr(logger, "Send", start, want)

	out := buf.String()
	if !strings.Contains(out, "msg=Send") {
		t.Errorf("missing msg=Send in %q", out)
	}
	if !strings.Contains(out, "duration=") {
		t.Errorf("missing duration attr in %q", out)
	}
	if !strings.Contains(out, "error=boom") {
		t.Errorf("missing error=boom in %q", out)
	}
	if !strings.Contains(out, "level=INFO") {
		t.Errorf("expected INFO level in %q", out)
	}
}

func TestLogCallErr_NilErrStillEmitsErrorAttr(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	LogCallErr(logger, "Close", time.Now(), nil)
	out := buf.String()
	// Match the previous generated behaviour: the error attribute is
	// always emitted on error-returning methods, even when nil.
	if !strings.Contains(out, "error=") {
		t.Errorf("expected error= attr even when nil, got %q", out)
	}
}

func TestLogCall_VoidNoErrorAttr(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	LogCall(logger, "Tick", time.Now())
	out := buf.String()
	if !strings.Contains(out, "msg=Tick") {
		t.Errorf("missing msg=Tick in %q", out)
	}
	if !strings.Contains(out, "duration=") {
		t.Errorf("missing duration in %q", out)
	}
	if strings.Contains(out, "error=") {
		t.Errorf("void call should not include error attr: %q", out)
	}
}

func TestLogCall_NilLoggerSafe(t *testing.T) {
	// nil logger must not panic — the helper short-circuits.
	LogCall(nil, "M", time.Now())
	LogCallErr(nil, "M", time.Now(), errors.New("x"))
}
