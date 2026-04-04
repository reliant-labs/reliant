package oauthcallback

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRunCancelsWhenContextDone(t *testing.T) {
	originalOpenBrowser := openBrowser
	openBrowser = func(string) error { return nil }
	defer func() { openBrowser = originalOpenBrowser }()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	result, err := Run(ctx, "https://auth.openai.com/oauth/authorize?redirect_uri={redirect_uri}", 120)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if result != nil {
		t.Fatalf("expected nil result, got %#v", result)
	}
	if !strings.Contains(err.Error(), "OAuth callback cancelled") {
		t.Fatalf("expected cancellation message, got %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
}
