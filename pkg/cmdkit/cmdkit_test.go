package cmdkit

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug":   slog.LevelDebug,
		"DEBUG":   slog.LevelDebug,
		"info":    slog.LevelInfo,
		"warn":    slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
		"":        slog.LevelInfo,
		"bogus":   slog.LevelInfo,
	}
	for in, want := range cases {
		if got := ParseLevel(in); got != want {
			t.Errorf("ParseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestLoggerWritesToConfiguredWriter(t *testing.T) {
	var buf bytes.Buffer
	log := Logger(LoggerOptions{Name: "tool", Format: "json", Out: &buf})
	log.Info("hello")
	out := buf.String()
	if !strings.Contains(out, `"cmd":"tool"`) {
		t.Errorf("expected cmd attribute in output, got %q", out)
	}
	if !strings.Contains(out, "hello") {
		t.Errorf("expected message in output, got %q", out)
	}
}

func TestContextWithTimeoutFallback(t *testing.T) {
	// Non-positive d uses the fallback.
	ctx, cancel := ContextWithTimeout(context.Background(), 0, 50*time.Millisecond)
	defer cancel()
	dl, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected a deadline from fallback")
	}
	if time.Until(dl) > 60*time.Millisecond {
		t.Errorf("deadline too far out: %v", time.Until(dl))
	}

	// Both zero: cancel-only context, no deadline, cancel still valid.
	ctx2, cancel2 := ContextWithTimeout(nil, 0, 0)
	defer cancel2()
	if _, ok := ctx2.Deadline(); ok {
		t.Error("expected no deadline when both d and fallback are zero")
	}
}

func TestPrintJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := PrintJSON(&buf, map[string]int{"n": 3}); err != nil {
		t.Fatal(err)
	}
	var out map[string]int
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("output not valid json: %v", err)
	}
	if out["n"] != 3 {
		t.Errorf("got %v", out)
	}
}

func TestOpenDBRequiresDSN(t *testing.T) {
	if _, err := OpenDB(context.Background(), DBOptions{}); err == nil {
		t.Error("expected error for empty DSN")
	}
}
