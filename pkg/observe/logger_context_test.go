package observe

import (
	"context"
	"log/slog"
	"testing"
)

func TestFromContextDefaultWhenAbsent(t *testing.T) {
	if got := FromContext(context.Background()); got == nil {
		t.Fatal("FromContext returned nil; expected slog.Default()")
	}
	if got := FromContext(nil); got == nil { //nolint:staticcheck // nil ctx is an explicit case
		t.Fatal("FromContext(nil) returned nil; expected slog.Default()")
	}
}

func TestWithLoggerRoundTrip(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	ctx := WithLogger(context.Background(), logger)
	if got := FromContext(ctx); got != logger {
		t.Errorf("FromContext did not return the stored logger")
	}
}

func TestFromContextNilStoredFallsBack(t *testing.T) {
	ctx := WithLogger(context.Background(), nil)
	if got := FromContext(ctx); got == nil {
		t.Fatal("expected default logger when nil was stored")
	}
}
